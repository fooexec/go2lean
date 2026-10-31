package poc

import (
	"encoding/json"
	"fmt"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/andreasfoo/go2lean/internal/builtin"
	"github.com/andreasfoo/go2lean/internal/contract"
	"github.com/andreasfoo/go2lean/internal/replay"
	"github.com/andreasfoo/go2lean/internal/sandbox"
)

// result is what the harness prints.
type result struct {
	Panicked bool   `json:"panicked"`
	Panic    string `json:"panic"`
	Ret      string `json:"ret"`
	Pre      *bool  `json:"pre"`
	Spec     *bool  `json:"spec"`
	// Stack is the goroutine's stack where the panic was recovered.
	Stack string `json:"stack"`
	// Blocked: the call did not return (a concurrency harness); Leaked: the
	// stack of a goroutine of the package blocked after it returned; Runs:
	// how many times the call ran.
	Blocked bool   `json:"blocked"`
	Leaked  string `json:"leaked"`
	Runs    int    `json:"runs"`
	// Replay: the steps of the model's schedule the last run followed ("k
	// of n"), ReplayDiverged why it left the schedule (a replay harness).
	Replay         string `json:"replay"`
	ReplayDiverged string `json:"replay_diverged"`
	// probes are the GO2LEAN_PROBE lines the test printed.
	probes []contract.Probe
}

// Verify realises and runs one counterexample and writes the verdict and the
// PoC deliverables to the workspace.
// goCrash matches Go's report of an uncaught panic or a fatal error (a
// mutex unlocked twice, a deadlock Go detects) on the test binary's stderr.
var goCrash = regexp.MustCompile(`(?m)^(panic|fatal error): (.*)$`)

// compileErr matches a Go compile error line of the harness file.
// The column is missing when the compiler reports a long type mismatch.
var compileErr = regexp.MustCompile(`zz_go2lean_poc_\d+_test\.go:\d+(:\d+)?: .*`)

// buildErr matches a compiler error in any file: the package (or its own
// test files) did not build with the harness.
var buildErr = regexp.MustCompile(`(?m)^\S+\.go:\d+(:\d+)?: .*$`)

func Verify(ws contract.Workspace, root, cxID string) (*contract.Verdict, error) {
	vs, err := VerifyAll(ws, root, []string{cxID}, nil)
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

// prepared is one counterexample with its records and rendered test.
type prepared struct {
	cx     *contract.Counterexample
	prop   *contract.Property
	target *contract.Target
	fn     string
	imps   []string
	v      *contract.Verdict // set early when the harness cannot be built
	// state lists the package-level variables the model read as havoc on
	// the way to the counterexample (sites from the model reports).
	state []string
	// guessed lists the calls the model does not model whose results
	// the counterexample's run read (havoc it chose).
	guessed []string
	// test: the target is a test function; its t is the harness's own.
	test bool
	abi  *contract.ABI
	// replayed: the package files of a confirmed replay (base name ->
	// rewritten source), which the PoC is built with.
	replayed map[string][]byte
	// race: build and run the harness with -race (a data race).
	race bool
}

// VerifyAll realises the counterexamples, runs those of each package in a
// single `go test` invocation (compiling the package's tests once), and
// writes a verdict and PoC deliverables for each. If a batch fails to
// compile, its members are run one by one so a single bad harness only
// affects itself.
//
// overlay holds files the packages are built with but do not contain
// (generated specs), by absolute path.
func VerifyAll(ws contract.Workspace, root string, cxIDs []string, overlay map[string][]byte) ([]*contract.Verdict, error) {
	byPkg := map[string][]*prepared{}
	var order []string
	all := map[string]*prepared{}
	snapshots := map[string]*builtin.Snapshot{}
	effective := maps.Clone(overlay)
	if effective == nil {
		effective = map[string][]byte{}
	}
	for _, id := range cxIDs {
		p, err := prepare(ws, id)
		if err != nil {
			return nil, err
		}
		if builtin.IsGeneratedProperty(p.prop, p.target) {
			s, err := builtin.ValidateProperty(ws, p.prop)
			if err != nil {
				return nil, err
			}
			key := p.prop.Builtin.SnapshotSHA256
			if _, seen := snapshots[key]; !seen {
				restored, err := s.RestoreOverlay(root)
				if err != nil {
					return nil, err
				}
				for path, source := range restored {
					if old, exists := effective[path]; exists && string(old) != string(source) {
						return nil, fmt.Errorf("builtin: replay overlay conflicts with its canonical snapshot")
					}
					effective[path] = source
				}
				snapshots[key] = s
			}
		}
		all[id] = p
		if p.v == nil {
			if _, seen := byPkg[p.target.Package]; !seen {
				order = append(order, p.target.Package)
			}
			byPkg[p.target.Package] = append(byPkg[p.target.Package], p)
		}
	}
	overlay = effective
	for _, pkg := range order {
		batch := byPkg[pkg]
		pkgDir, pkgName, err := goList(root, pkg)
		if err != nil {
			return nil, err
		}
		results, cmd, exit, out, errOut, err := runBatch(root, pkgDir, pkgName, pkg, batch, overlay)
		if err != nil {
			return nil, err
		}
		// A test without a result in the batch (the batch did not compile,
		// or an earlier test ended the process with log.Fatal) is run on
		// its own, so one target's exit does not hide the others.
		for _, p := range batch {
			if _, ok := results[p.cx.ID]; ok || len(batch) == 1 {
				p.v = verdictOf(p, results, cmd, exit, out, errOut, root)
				continue
			}
			r, c, e, o, eo, err := runBatch(root, pkgDir, pkgName, pkg, []*prepared{p}, overlay)
			if err != nil {
				return nil, err
			}
			p.v = verdictOf(p, r, c, e, o, eo, root)
		}
		// A data race: under Go's race detector, then with the model's
		// schedule replayed.
		for _, p := range batch {
			if p.v != nil && modelPanic(p.cx) == contract.PanicDataRace {
				if err := raceOne(root, pkgDir, pkgName, pkg, p, overlay); err != nil {
					return nil, err
				}
			}
		}
		// A deadlock or leak Go's scheduler did not reproduce: replay the
		// model's schedule in the package rewritten to follow it.
		for _, p := range batch {
			if !replayable(p) {
				continue
			}
			if err := replayOne(root, pkgDir, pkgName, pkg, p, overlay); err != nil {
				return nil, err
			}
		}
	}
	for _, snapshot := range snapshots {
		if _, err := snapshot.RestoreOverlay(root); err != nil {
			return nil, fmt.Errorf("builtin: source graph changed during replay: %w", err)
		}
	}
	var vs []*contract.Verdict
	callers := newCallerIndex(root)
	for _, id := range cxIDs {
		p := all[id]
		if p.v == nil {
			return nil, fmt.Errorf("%s: no verdict produced", id)
		}
		if p.v.Status == contract.VerdictConfirmed && p.prop.Builtin == nil {
			p.v.Rank = rankOf(p, callers)
		}
		if p.prop.Builtin != nil {
			stamp, err := builtin.ReplayRef(p.prop, p.cx)
			if err != nil {
				return nil, err
			}
			p.v.Builtin = stamp
		}
		if err := deliver(ws, root, p, overlay); err != nil {
			return nil, err
		}
		vs = append(vs, p.v)
	}
	return vs, nil
}

// replayable: an inconclusive deadlock or leak, or a panic or false result
// under a concurrent schedule, with a schedule to replay.
func replayable(p *prepared) bool {
	if p.v == nil || p.v.Status != contract.VerdictInconclusive || p.v.Diagnosis == nil || len(p.cx.Schedule) == 0 {
		return false
	}
	switch p.v.Diagnosis.Reason {
	case "schedule_not_reproduced", "panic_kind_mismatch":
	default:
		return false
	}
	m := modelPanic(p.cx)
	return m == contract.PanicDeadlock || m == contract.PanicGoroutineLeak || (m != contract.PanicDataRace && scheduleDependent(p.cx))
}

// replayOne runs p's replay harness: the package rewritten by
// internal/replay, the run following p's schedule. A confirmed replay
// replaces p's verdict and harness; otherwise the first verdict stands,
// with a note.
func replayOne(root, pkgDir, pkgName, pkg string, p *prepared, overlay map[string][]byte) error {
	note := func(s string) error {
		p.v.Diagnosis.Notes = append(p.v.Diagnosis.Notes, "replay of the model's schedule: "+s)
		return nil
	}
	files, err := replay.Rewrite(root, pkg)
	if err != nil {
		return note("the package could not be rewritten: " + err.Error())
	}
	ov := maps.Clone(overlay)
	if ov == nil {
		ov = map[string][]byte{}
	}
	for name, src := range files {
		ov[filepath.Join(pkgDir, name)] = src
	}
	fn, imps, err := ReplayHarnessFunc(p.target, p.prop, p.cx, p.abi)
	if err != nil {
		return note(err.Error())
	}
	q := *p
	q.fn, q.imps, q.v = fn, imps, nil
	r, c, e, o, eo, err := runBatch(root, pkgDir, pkgName, pkg, []*prepared{&q}, ov)
	if err != nil {
		return err
	}
	v := verdictOf(&q, r, c, e, o, eo, root)
	res, ok := r[p.cx.ID]
	switch {
	case !ok && v.Status == contract.VerdictConfirmed:
		// The model's panic ended the run before it reported how far it
		// followed the schedule.
		if v.Observed == nil {
			v.Observed = map[string]string{}
		}
		v.Observed["replay"] = "the model's schedule replayed; the run ended in the model's panic; Go's own scheduler did not take it in " + strconv.Itoa(concRuns) + " runs"
		p.v, p.fn, p.imps, p.replayed = v, fn, imps, files
		return nil
	case !ok:
		return note(fmt.Sprintf("%s (%s)", v.Status, reasonOf(v)))
	case res.ReplayDiverged != "":
		return note(fmt.Sprintf("the real run left the schedule after %s steps: %s", res.Replay, res.ReplayDiverged))
	case v.Status != contract.VerdictConfirmed:
		if m := modelPanic(p.cx); m != contract.PanicDeadlock && m != contract.PanicGoroutineLeak && reasonOf(v) == "schedule_not_reproduced" {
			// The run took the model's interleaving and the property held:
			// the model and Go disagree, as for a sequential spurious one.
			p.v.Status = contract.VerdictSpurious
			p.v.Diagnosis.Reason = "holds_on_model_schedule"
		}
		return note(fmt.Sprintf("followed (%s steps), still %s", res.Replay, reasonOf(v)))
	}
	v.Observed["replay"] = "the model's schedule replayed (" + res.Replay + " steps); Go's own scheduler did not take it in " + strconv.Itoa(concRuns) + " runs"
	p.v, p.fn, p.imps, p.replayed = v, fn, imps, files
	return nil
}

// raceOne judges a data race the model found by Go's race detector: the
// harness under -race, then, when it reports none, with the model's
// schedule replayed (the controller's own synchronisation hidden from the
// detector).
func raceOne(root, pkgDir, pkgName, pkg string, p *prepared, overlay map[string][]byte) error {
	q := *p
	q.race, q.v = true, nil
	r, c, e, o, eo, err := runBatch(root, pkgDir, pkgName, pkg, []*prepared{&q}, overlay)
	if err != nil {
		return err
	}
	if rep := raceReport(eo); rep != "" {
		v := verdictOf(&q, r, c, e, o, eo, root)
		v.Status, v.Diagnosis = contract.VerdictConfirmed, nil
		v.Observed["race"] = rep
		p.v, p.race = v, true
		return nil
	}
	if len(p.cx.Schedule) == 0 {
		return nil
	}
	note := func(s string) error {
		p.v.Diagnosis.Notes = append(p.v.Diagnosis.Notes, "replay of the model's schedule under -race: "+s)
		return nil
	}
	files, err := replay.Rewrite(root, pkg)
	if err != nil {
		return note("the package could not be rewritten: " + err.Error())
	}
	ov := maps.Clone(overlay)
	if ov == nil {
		ov = map[string][]byte{}
	}
	for name, src := range files {
		ov[filepath.Join(pkgDir, name)] = src
	}
	fn, imps, err := ReplayHarnessFunc(p.target, p.prop, p.cx, p.abi)
	if err != nil {
		return note(err.Error())
	}
	q.fn, q.imps = fn, imps
	r, c, e, o, eo, err = runBatch(root, pkgDir, pkgName, pkg, []*prepared{&q}, ov)
	if err != nil {
		return err
	}
	res := r[p.cx.ID]
	rep := raceReport(eo)
	switch {
	case rep == "" && res.ReplayDiverged != "":
		return note(fmt.Sprintf("the real run left the schedule after %s steps: %s", res.Replay, res.ReplayDiverged))
	case rep == "":
		return note(fmt.Sprintf("followed (%s steps), no race reported", res.Replay))
	}
	v := verdictOf(&q, r, c, e, o, eo, root)
	v.Status, v.Diagnosis = contract.VerdictConfirmed, nil
	v.Observed["race"] = rep
	v.Observed["replay"] = "the model's schedule replayed (" + res.Replay + " steps); -race reported nothing in " + strconv.Itoa(raceRuns) + " plain runs"
	p.v, p.fn, p.imps, p.replayed, p.race = v, fn, imps, files, true
	return nil
}

// raceReport is the first data race report in a -race run's output whose
// accesses are both in the program: not in the replay controller
// (go2lean*) or the harness. An access is placed by the first frame of its
// stack, from the top, outside the standard library (a map access starts
// in the runtime's map code, a context's cancel in package context): a
// go2lean helper there (go2leanGate, go2leanDeadline running a cancel, the
// PoC test) makes it the harness's, since the controller orders it and the
// detector does not see that; any other frame makes it the program's. The
// helpers below that frame (go2leanGoAny and go2leanInvoke, which start
// every goroutine the controller runs) do not count.
func raceReport(out string) string {
	for _, block := range strings.Split(out, "==================") {
		if !strings.Contains(block, "WARNING: DATA RACE") {
			continue
		}
		lines := strings.Split(block, "\n")
		ours, accesses := false, 0
		for i, l := range lines {
			if !(strings.HasPrefix(l, "Read at") || strings.HasPrefix(l, "Write at") ||
				strings.HasPrefix(l, "Previous read at") || strings.HasPrefix(l, "Previous write at")) {
				continue
			}
			accesses++
			for _, f := range lines[i+1:] {
				f = strings.TrimSpace(f)
				if f == "" {
					break
				}
				if strings.HasPrefix(f, "/") || stdFrame(f) {
					continue // a file:line line, or a frame of the standard library
				}
				if strings.Contains(f, ".go2lean") || strings.Contains(f, "TestGo2LeanPoC_") {
					ours = true
				}
				break
			}
		}
		if !ours && accesses >= 2 {
			// The accesses come first; the goroutines' creation sites after.
			rep := strings.TrimSpace(block)
			if len(rep) > 2000 {
				rep = rep[:2000] + "..."
			}
			return rep
		}
	}
	return ""
}

// stdFrame says whether stack frame f (a function, as a race report
// prints it) is in the standard library: the first element of its package
// path has no dot (runtime, internal/..., sync/atomic, context), as a
// module path's has (github.com/..., example.com/...). Package main is
// the program's.
func stdFrame(f string) bool {
	if strings.HasPrefix(f, "main.") {
		return false
	}
	first := f
	if i := strings.Index(first, "/"); i >= 0 {
		first = first[:i]
	} else if i := strings.Index(first, "."); i >= 0 {
		first = first[:i]
	}
	return !strings.Contains(first, ".")
}

func reasonOf(v *contract.Verdict) string {
	if v.Diagnosis != nil {
		return v.Diagnosis.Reason
	}
	return string(v.Status)
}

func prepare(ws contract.Workspace, cxID string) (*prepared, error) {
	cx, err := ws.Counterexample(cxID)
	if err != nil {
		return nil, err
	}
	prop, err := ws.Property(cx.Property)
	if err != nil {
		return nil, err
	}
	target, err := ws.Target(prop.Target)
	if err != nil {
		return nil, err
	}
	abi, err := ws.ABI(target, cx.Frontend)
	if err != nil {
		return nil, err
	}
	p := &prepared{cx: cx, prop: prop, target: target, state: unknownState(ws, cx), guessed: guessedCalls(ws, cx), abi: abi}
	for _, prm := range abi.Params {
		if prm.GoType == "*testing.T" && prm.Kind == contract.ABIOpaque {
			// Not an input: the model's opaque t is the harness's own t.
			p.test = true
			cx2 := *cx
			cx2.Objects = maps.Clone(cx.Objects)
			delete(cx2.Objects, prm.Go)
			p.cx = &cx2
		}
	}
	p.fn, p.imps, err = HarnessFunc(target, prop, cx, abi)
	if err != nil {
		p.v = &contract.Verdict{Counterexample: cx.ID, Status: contract.VerdictInconclusive,
			Diagnosis: &contract.Diagnosis{Reason: "cannot_materialise", Notes: []string{err.Error()}}}
	}
	return p, nil
}

// runBatch builds one test file holding every test of the batch into the
// package's test binary, runs it in the sandbox, and returns the parsed results keyed by counterexample
// id (nil when the file did not compile).
func runBatch(root, pkgDir, pkgName, pkg string, batch []*prepared, overlay map[string][]byte) (map[string]result, string, int, string, string, error) {
	imports := map[string]bool{}
	var funcs []string
	for _, p := range batch {
		funcs = append(funcs, p.fn)
		for _, ip := range p.imps {
			imports[ip] = true
		}
	}
	var imps []string
	for ip := range imports {
		imps = append(imps, ip)
	}
	sort.Strings(imps)
	src := File(pkgName, imps, funcs)
	// Copied/legacy harnesses may redeclare this test. Never delete source
	// files to make a build pass, even files carrying our naming prefix.
	if stale, _ := filepath.Glob(filepath.Join(pkgDir, "zz_go2lean_poc_*_test.go")); len(stale) > 0 {
		return nil, "", 0, "", "", fmt.Errorf("replay refused: package contains copied/generated observation tests; move them out of the scratch clone before retrying (no files were removed): %s", strings.Join(stale, ", "))
	}
	// The harness joins the package through an overlay and runs in the
	// sandbox: nothing is written to the package, and a target that writes
	// files, dials out or eats memory meets the sandbox's limits.
	run := "^TestGo2LeanPoC_"
	if len(batch) == 1 {
		run += batch[0].cx.ID + "$"
	}
	tmp, err := os.MkdirTemp("", "go2lean-poc-")
	if err != nil {
		return nil, "", 0, "", "", err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "poc.test")
	name := fmt.Sprintf("zz_go2lean_poc_%d_test.go", os.Getpid())
	files := map[string][]byte{name: []byte(src)}
	helpers, err := packageOverlayFiles(pkgDir, overlay)
	if err != nil {
		return nil, "", 0, "", "", err
	}
	for helper, b := range helpers {
		if helper == name {
			return nil, "", 0, "", "", fmt.Errorf("package overlay replaces the generated test harness: %s", name)
		}
		files[helper] = b
	}
	var flags []string
	for _, p := range batch {
		if p.race {
			flags = []string{"-race"}
		}
	}
	buildOut, exit, err := sandbox.BuildTest(root, testPath(pkg), pkgDir, files, bin, flags...)
	if err != nil {
		return nil, "", 0, "", "", err
	}
	var stdout, stderr string
	if exit != 0 {
		stderr = buildOut
	} else {
		stdout, stderr, exit, err = sandbox.Run(bin, pkgDir, []string{"-test.count=1", "-test.v", "-test.run", run}, nil, sandbox.Default)
		if err != nil {
			return nil, "", 0, "", "", err
		}
	}
	cmdLine := "go test -count=1 -v -run '" + run + "' " + testPath(pkg)
	if len(flags) > 0 {
		cmdLine = "go test -race -count=1 -v -run '" + run + "' " + testPath(pkg)
	}
	results := parseResults(stdout)
	if len(results) == 0 {
		return nil, cmdLine, exit, stdout, stderr, nil
	}
	return results, cmdLine, exit, stdout, stderr, nil
}

func verdictOf(p *prepared, results map[string]result, cmd string, exit int, stdout, stderr, root string) *contract.Verdict {
	v := &contract.Verdict{
		Counterexample: p.cx.ID,
		Evidence:       contract.Evidence{Cmd: cmd, Exit: exit, StdoutTail: tail(stdout), StderrTail: tail(stderr)},
	}
	res, ok := results[p.cx.ID]
	if m := goCrash.FindStringSubmatch(stderr); !ok && exit != 0 && m != nil && compileErr.FindString(stderr) == "" {
		// No result line, and Go's own report of an uncaught panic or a
		// fatal error: a goroutine the call started panicked (the harness
		// recovers only the call's own) and ended the process. This run had
		// the test binary to itself (VerifyAll reruns such tests alone).
		res, ok = result{Panicked: true, Panic: strings.TrimSuffix(m[2], " [recovered]"), Stack: stderr}, true
	}
	if !ok {
		v.Status = contract.VerdictInconclusive
		// Tell a harness that did not compile (ours to fix) from a target
		// that ended the process (log.Fatal, os.Exit): the latter is a
		// finding of its own kind that M3 cannot judge.
		reason, note := "harness_failed", "no GO2LEAN_RESULT line; see stdout/stderr"
		if m := compileErr.FindString(stderr); m != "" {
			reason, note = "harness_compile_error", m
		} else if m := buildErr.FindString(stderr); m != "" && stdout == "" {
			reason, note = "package_build_error", "the package's tests did not build: "+m
		} else if exit != 0 {
			reason, note = "target_exited", "the test binary ended without reporting (log.Fatal or os.Exit in the target?)"
		}
		v.Diagnosis = &contract.Diagnosis{Reason: reason, Notes: []string{note}}
		return v
	}
	judge(v, p.prop, p.cx, res)
	v.Probes = res.probes
	if res.Panicked {
		v.Evidence.PanicSite = panicSite(res.Stack, root)
	}
	panicMsg := v.Observed["panic"]
	nilCaused := strings.Contains(panicMsg, "nil pointer dereference") || (panicMsg != "" && !strings.HasPrefix(panicMsg, "runtime error"))
	if nils := nilArguments(p.cx); v.Status == contract.VerdictConfirmed && len(nils) > 0 && nilCaused && p.prop.Kind == contract.KindNoPanic {
		// Confirmed with a nil argument and a panic one can cause (a nil
		// dereference, an explicit panic): often a precondition of the API
		// (a nil error to report, a nil stream), so say so.
		v.Diagnosis = &contract.Diagnosis{Reason: "nil_argument", Notes: []string{
			"the call passed nil for " + strings.Join(nils, ", ") + ": check that some caller can"}}
	}
	if v.Status == contract.VerdictConfirmed && len(p.state) > 0 && p.prop.Kind == contract.KindNoPanic {
		// Confirmed, but on package state the model does not know: the
		// program may set it up before any call (an Init function), and
		// the harness does not.
		v.Diagnosis = &contract.Diagnosis{Reason: "reads_unknown_state", Notes: []string{
			"the run that panicked read package-level state the model does not know (" + strings.Join(p.state, "; ") +
				"): check that the program does not initialise it before calls like this one"}}
	}
	if (v.Status == contract.VerdictSpurious || v.Status == contract.VerdictInconclusive) && v.Diagnosis != nil && len(p.guessed) > 0 {
		// The likeliest cause of a model that disagrees with Go: it chose
		// the results of calls it does not model. Name them.
		names := p.guessed
		more := ""
		if len(names) > 5 {
			names, more = names[:5], fmt.Sprintf(" and %d more", len(p.guessed)-5)
		}
		note := "the model chose the results of calls it does not model: " +
			strings.Join(names, ", ") + more + "; a choice Go does not make gives a spurious counterexample"
		if slices.ContainsFunc(p.guessed, func(c string) bool { return !stdCallee(c) }) {
			// -deps compiles dependency packages, not the standard
			// library (whose unmodelled functions stay unmodelled).
			note += ". Compiling with -deps models the functions of the dependency packages called, if it was off"
		}
		v.Diagnosis.Notes = append(v.Diagnosis.Notes, note)
	}
	return v
}

// guessedCalls names the callees of the unmodelled calls whose results a
// counterexample's run read (havoc sites of reason unmodeled-call), in
// the order the run read them.
func guessedCalls(ws contract.Workspace, cx *contract.Counterexample) []string {
	var out []string
	for _, site := range havocSites(ws, cx) {
		if site.Reason != "unmodeled-call" {
			continue
		}
		if c := calleeOf(site.Site); c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// RefinableCalls lists the dependency functions the refinement loop can
// translate to remove a counterexample (#85): the callees of its
// unmodelled-call havoc reads outside the standard library. Only the reads
// the violation depends on (relevant_havoc) count, unless relevance was not
// computed.
func RefinableCalls(ws contract.Workspace, cx *contract.Counterexample) []string {
	relevant := map[[2]int]bool{}
	for _, r := range cx.RelevantHavoc {
		relevant[[2]int{r.Idx, r.Iter}] = true
	}
	all := cx.RelevanceSkipped || cx.HavocPerturbed == 0
	var reads contract.Counterexample
	reads.Frontend = cx.Frontend
	for _, h := range cx.Havoc {
		if all || relevant[[2]int{h.Idx, h.Iter}] {
			reads.Havoc = append(reads.Havoc, h)
		}
	}
	var out []string
	for _, site := range havocSites(ws, &reads) {
		if site.Reason != "unmodeled-call" {
			continue
		}
		if c := calleeOf(site.Site); c != "" && !stdCallee(c) && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// stdCallee says whether a callee named by calleeOf is in the standard
// library (no dot in the first element of its package path).
func stdCallee(c string) bool {
	c = strings.TrimPrefix(strings.TrimPrefix(c, "("), "*")
	first, _, hasSlash := strings.Cut(c, "/")
	return !hasSlash || !strings.Contains(first, ".")
}

// calleeOf extracts the callee from a havoc site text "fn: call
// <callee>[.Field...]": a function pkg/path.Name or a method
// (pkg/path.T).Name, without the result fields after it.
func calleeOf(site string) string {
	_, rest, ok := strings.Cut(site, ": call ")
	if !ok {
		return ""
	}
	if strings.HasPrefix(rest, "(") {
		i := strings.Index(rest, ")")
		if i < 0 {
			return ""
		}
		name, _, _ := strings.Cut(rest[i+1:], " ")
		if !strings.HasPrefix(name, ".") {
			return ""
		}
		method, _, _ := strings.Cut(name[1:], ".")
		return rest[:i+1] + "." + method
	}
	slash := strings.LastIndex(rest, "/") + 1
	dot := strings.Index(rest[slash:], ".")
	if dot < 0 || strings.ContainsAny(rest[:slash+dot], " (") {
		return ""
	}
	name, _, _ := strings.Cut(rest[slash+dot+1:], ".")
	name, _, _ = strings.Cut(name, " ")
	return rest[:slash+dot+1] + name
}

// nilArguments names the top-level arguments a counterexample sets to nil.
func nilArguments(cx *contract.Counterexample) []string {
	var out []string
	for name, v := range cx.Inputs {
		if v == "nil" && !strings.Contains(name, "_") {
			out = append(out, name)
		}
	}
	for name, raw := range cx.Objects {
		if s := strings.TrimSpace(string(raw)); s == "null" || s == `"nil"` {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// unknownState names the package-level variables a counterexample's model
// run read as havoc (unmodeled loads), resolved in their packages' reports.
func unknownState(ws contract.Workspace, cx *contract.Counterexample) []string {
	var out []string
	for _, site := range havocSites(ws, cx) {
		if site.Reason == "unmodeled-load" && !slices.Contains(out, site.Site) {
			out = append(out, site.Site)
		}
	}
	return out
}

// havocSites are the model-report sites of the havoc values a
// counterexample's run read.
func havocSites(ws contract.Workspace, cx *contract.Counterexample) []contract.ABIHavoc {
	var out []contract.ABIHavoc
	reports := map[string]*contract.ModelReport{}
	for _, h := range cx.Havoc {
		key, idx, ok := strings.Cut(h.Origin, "#")
		if !ok {
			continue
		}
		key = strings.TrimPrefix(key, cx.Frontend+".")
		r, seen := reports[key]
		if !seen {
			r, _ = ws.ModelReportByKey(cx.Frontend, key)
			reports[key] = r
		}
		i, err := strconv.Atoi(idx)
		if r == nil || err != nil || i >= len(r.Havoc) {
			continue
		}
		out = append(out, r.Havoc[i])
	}
	return out
}

// deliver writes the harness, its effective generated/replayed support
// sources, reproduction instructions, and the verdict.
func deliver(ws contract.Workspace, root string, p *prepared, overlay map[string][]byte) error {
	v := p.v
	if p.fn != "" {
		if p.cx.ID == "" || !token.IsIdentifier("TestGo2LeanPoC_"+p.cx.ID) {
			return fmt.Errorf("invalid reproduction test identifier for %q", p.cx.ID)
		}
		pocDir := ws.PocDir(p.cx.ID)
		if err := os.MkdirAll(pocDir, 0o755); err != nil {
			return err
		}
		if info, err := os.Lstat(pocDir); err != nil || !info.IsDir() {
			return fmt.Errorf("PoC artifact root is not a regular directory: %s", pocDir)
		}
		// A failed update must not leave an old manifest apparently ready.
		if err := os.Remove(filepath.Join(pocDir, "bundle.json")); err != nil && !os.IsNotExist(err) {
			return err
		}
		pkgDir, pkgName, err := goList(root, p.target.Package)
		if err != nil {
			return err
		}
		testName := "zz_go2lean_poc_" + p.cx.ID + "_test.go"
		harness := []byte(File(pkgName, p.imps, []string{p.fn}))
		if err := writeArtifactFile(pocDir, testName, harness); err != nil {
			return err
		}
		cmd := "go test -count=1 -v -run '^TestGo2LeanPoC_" + p.cx.ID + "$' " + testPath(p.target.Package)
		if p.race {
			cmd = strings.Replace(cmd, "go test ", "go test -race ", 1)
		}
		if p.replayed != nil {
			// The package rewritten to follow the model's schedule: built
			// through an overlay, the package itself unchanged.
			odir := filepath.Join(pocDir, "replay")
			if err := os.MkdirAll(odir, 0o755); err != nil {
				return err
			}
			ov := map[string]map[string]string{"Replace": {}}
			for name, src := range p.replayed {
				if err := writeArtifactFile(pocDir, "replay/"+name, src); err != nil {
					return err
				}
				ov["Replace"][filepath.Join(pkgDir, name)] = filepath.Join(odir, name)
			}
			b, _ := json.MarshalIndent(ov, "", "  ")
			if err := writeArtifactFile(pocDir, "replay/overlay.json", b); err != nil {
				return err
			}
			cmd = strings.Replace(cmd, "-count=1 -v", "-count=1 -v -overlay "+filepath.Join(odir, "overlay.json"), 1)
		}
		readme := fmt.Sprintf("# PoC %s\n\nProperty: %s (%s)\n\n%s\n\nVerdict: **%s**\n\nReproduce: copy `%s` into `%s` and run\n\n    %s\n",
			p.cx.ID, p.prop.ID, p.prop.Kind, p.prop.NL, v.Status, testName, pkgDir, cmd)
		if p.replayed != nil {
			readme += "\nThe run replays the model's schedule: `replay/` holds the package's files rewritten so that every synchronisation operation waits for its turn (go2lean's replay controller), built through the overlay; the package itself is not changed.\n"
		}
		helpers, err := packageOverlayFiles(pkgDir, overlay)
		if err != nil {
			return err
		}
		if len(helpers) > 0 || len(p.replayed) > 0 {
			if err := deliverBundle(pocDir, root, pkgDir, testName, harness, p, helpers); err != nil {
				return err
			}
			race := ""
			if p.race {
				race = "-race "
			}
			absoluteRoot, err := filepath.Abs(root)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(absoluteRoot, pkgDir)
			if err != nil {
				return err
			}
			localPkg := "./" + filepath.ToSlash(rel)
			readme = fmt.Sprintf("# PoC %s\n\nProperty: %s (%s)\n\n%s\n\nVerdict: **%s**\n\n"+
				"This bundle includes the harness and the package-local generated/support sources actually used by replay. "+
				"It does not include target source or dependencies. Use a disposable clone of the same target revision and build configuration.\n\n"+
				"From this bundle directory, prepare a NEW overlay for the clone (no target code is built or executed by this preparation):\n\n"+
				"    GOWORK=off go run prepare.go -bundle . -module /absolute/scratch-clone -output /absolute/new-replay-overlay.json\n\n"+
				"Then, from the scratch clone's module directory:\n\n"+
				"    go test %s-count=1 -v -overlay /absolute/new-replay-overlay.json -run '^TestGo2LeanPoC_%s$' %s\n\n"+
				"Preparation checks source SHA-256 hashes and rejects unsafe paths, symlinks, missing files and overwriting an existing output. "+
				"Hashes detect inconsistent artifacts; they do not authenticate an untrusted producer. "+
				"The manual go test command is NOT sandboxed: trust the sources and dependencies, and use an isolated disposable environment. "+
				"These observation tests print GO2LEAN_RESULT; go test exiting zero alone does not confirm a finding.\n",
				p.cx.ID, p.prop.ID, p.prop.Kind, p.prop.NL, v.Status, race, p.cx.ID, "'"+strings.ReplaceAll(localPkg, "'", "'\\''")+"'")
			if len(p.replayed) > 0 {
				readme += "\nThe bundle includes schedule-rewritten package sources; these take precedence over general overlays, matching the confirmed replay. This is an instrumented schedule reproduction, not a claim that the unmodified Go scheduler must reproduce it.\n"
			}
		}
		if err := writeArtifactFile(pocDir, "README.md", []byte(readme)); err != nil {
			return err
		}
		v.PocDir = pocDir
	}
	return ws.WriteVerdict(v)
}

// judge fills Status, Observed and Diagnosis from the harness result.
// hasNilInput says whether any object the test built held a nil opaque
// value (rendered as JSON null by the model).
func hasNilInput(cx *contract.Counterexample) bool {
	for _, raw := range cx.Objects {
		if nullRe.Match(raw) {
			return true
		}
	}
	return false
}

// hasOpaqueHandle says whether an input was a non-nil pointer to an
// object the model left opaque (rendered "opaque"; the test builds it as
// new(T), the zero value).
func hasOpaqueHandle(cx *contract.Counterexample) bool {
	for _, raw := range cx.Objects {
		if strings.Contains(string(raw), `"opaque"`) {
			return true
		}
	}
	return false
}

var nullRe = regexp.MustCompile(`(^|[\[,:\s])null([\]},\s]|$)`)

func judge(v *contract.Verdict, prop *contract.Property, cx *contract.Counterexample, r result) {
	v.Observed = map[string]string{}
	if r.Ret != "" {
		v.Observed["ret"] = r.Ret
	}
	if r.Panicked {
		v.Observed["panic"] = r.Panic
		v.Evidence.PanicMsg = r.Panic
	}
	diag := &contract.Diagnosis{IsCustomProp: prop.Kind == contract.KindCustom}
	for _, h := range cx.Havoc {
		diag.HavocUsed = append(diag.HavocUsed, h.Idx)
	}
	if cx.ModelOutput != nil {
		diag.ModelVsReal = fmt.Sprintf("model: %s %s%s; real: %s", cx.ModelOutput.Res, cx.ModelOutput.Value,
			panicSuffix(string(cx.ModelOutput.Panic)), describe(r))
	}
	if r.Pre != nil && !*r.Pre {
		v.Status = contract.VerdictSpurious
		diag.Reason = "precondition_false"
		v.Diagnosis = diag
		return
	}
	if model := modelPanic(cx); model == contract.PanicDeadlock || model == contract.PanicGoroutineLeak {
		judgeConc(v, prop, model, r, diag)
		return
	}
	if modelPanic(cx) == contract.PanicDataRace {
		// Only Go's race detector can say: judged from -race's report
		// (raceOne); a run without it proves nothing.
		v.Status = contract.VerdictInconclusive
		diag.Reason = "race_not_observed"
		diag.Notes = []string{"go test -race reported no data race between the program's accesses"}
		v.Diagnosis = diag
		return
	}
	switch prop.Kind {
	case contract.KindNoPanic:
		switch {
		case !r.Panicked:
			v.Status = contract.VerdictSpurious
			diag.Reason = "no_panic_in_real_run"
		case prop.ExpectedPanic == "" || prop.ExpectedPanic == ClassifyPanic(r.Panic):
			v.Status = contract.VerdictConfirmed
			diag = nil
			if strings.Contains(r.Panic, "nil pointer dereference") && hasOpaqueHandle(cx) {
				// The test passed a pointer to the zero value of another
				// package's struct it cannot fill in (a *gin.Context with no
				// writer): callers never do, so the panic says nothing
				// about the code.
				v.Status = contract.VerdictInconclusive
				diag = &contract.Diagnosis{Reason: "zero_handle_input", Notes: []string{
					"an input was a pointer to the zero value of another package's struct the test cannot fill in; the nil dereference is likely inside it"}}
				break
			}
			if (strings.Contains(r.Panic, "nil pointer dereference") || strings.Contains(r.Panic, "assignment to entry in nil map")) && hasNilInput(cx) {
				// Confirmed, but worth a reader's second look: the test could
				// only pass nil for an interface, map, func or foreign private
				// field, and the panic may be the dereference of that nil.
				diag = &contract.Diagnosis{Reason: "nil_opaque_input", Notes: []string{
					"an opaque input (interface, map, func, or an unexported field of another package's struct) was nil in the test; the dereference or map write may be of that value"}}
			}
		default:
			v.Status = contract.VerdictInconclusive
			diag.Reason = "panic_kind_mismatch"
			diag.Notes = []string{fmt.Sprintf("expected %s, got %s", prop.ExpectedPanic, ClassifyPanic(r.Panic))}
		}
	case contract.KindNoHang:
		// The model's run came back to a state: the real call should not
		// return. One that does refutes the loop (an under-approximation
		// the run took, or a model gap).
		switch {
		case r.Blocked:
			v.Observed["blocked"] = "the call did not return within 2s"
			v.Status, diag = contract.VerdictConfirmed, nil
		case r.Panicked:
			v.Status = contract.VerdictInconclusive
			diag.Reason = "unexpected_panic"
			diag.Notes = []string{"the model's run loops forever; the real run panicked: " + r.Panic}
		default:
			v.Status = contract.VerdictSpurious
			diag.Reason = "returned_in_real_run"
		}
	case contract.KindSequence:
		// The sequence spec is the target: a panic while it drives the
		// code, or a false verdict, is the violation.
		switch {
		case r.Panicked:
			v.Status = contract.VerdictConfirmed
			diag = nil
		case r.Ret == "false":
			v.Status = contract.VerdictConfirmed
			diag = nil
		default:
			v.Status = contract.VerdictSpurious
			diag.Reason = "oracle_holds_in_real_run"
		}
	case contract.KindPostcondition, contract.KindEquivalence, contract.KindCustom:
		switch {
		case r.Panicked:
			v.Status = contract.VerdictInconclusive
			diag.Reason = "unexpected_panic"
			diag.Notes = []string{"the real run panicked before the oracle could be evaluated; this may be a different bug"}
		case r.Spec == nil:
			v.Status = contract.VerdictInconclusive
			diag.Reason = "no_oracle_result"
		case !*r.Spec:
			v.Status = contract.VerdictConfirmed
			diag = nil
		default:
			v.Status = contract.VerdictSpurious
			diag.Reason = "oracle_holds_in_real_run"
		}
	default:
		v.Status = contract.VerdictInconclusive
		diag.Reason = "unsupported_property_kind"
	}
	if v.Status == contract.VerdictSpurious && scheduleDependent(cx) && (diag.Reason == "oracle_holds_in_real_run" || diag.Reason == "no_panic_in_real_run") {
		// Runs under Go's scheduler that never took the model's
		// interleaving do not refute it: the schedule is replayed next
		// (replayOne), and only a run that follows it decides.
		v.Status = contract.VerdictInconclusive
		diag.Notes = []string{fmt.Sprintf("the model's run was concurrent; no violation in %d real runs: Go's scheduler may not have taken its interleaving", r.Runs)}
		if r.Blocked {
			diag.Reason = "panic_kind_mismatch"
			diag.Notes = []string{"the model's run was concurrent; the real call blocked"}
		} else {
			diag.Reason = "schedule_not_reproduced"
		}
	}
	v.Diagnosis = diag
}

func panicSuffix(k string) string {
	if k == "" {
		return ""
	}
	return " (" + k + ")"
}

func describe(r result) string {
	if r.Panicked {
		return "panic " + r.Panic
	}
	return "ok " + r.Ret
}

// ClassifyPanic maps a Go runtime panic message to a PanicKind.
func ClassifyPanic(msg string) contract.PanicKind {
	switch {
	case strings.Contains(msg, "index out of range"):
		return contract.PanicIndexOutOfRange
	case strings.Contains(msg, "integer divide by zero"):
		return contract.PanicDivideByZero
	case strings.Contains(msg, "nil pointer dereference"):
		return contract.PanicNilDeref
	case strings.Contains(msg, "negative shift amount"):
		return contract.PanicNegativeShift
	case strings.Contains(msg, "slice bounds out of range"):
		return contract.PanicSliceBounds
	case strings.Contains(msg, "makeslice"):
		return contract.PanicMakeSliceLen
	case strings.Contains(msg, "interface conversion"):
		return contract.PanicTypeAssertion
	case strings.Contains(msg, "assignment to entry in nil map"):
		return contract.PanicNilMapWrite
	case strings.Contains(msg, "closed channel") || strings.Contains(msg, "close of nil channel"):
		return contract.PanicClosedChannel
	case strings.HasPrefix(msg, "sync: ") && !strings.Contains(msg, "negative WaitGroup counter"):
		// A fatal error (an unlock of an unlocked mutex); a negative
		// WaitGroup counter is a plain panic.
		return contract.PanicSyncFatal
	case strings.Contains(msg, "all goroutines are asleep"):
		return contract.PanicDeadlock
	case strings.HasPrefix(msg, "runtime error:"):
		return contract.PanicOther
	}
	return contract.PanicExplicit
}

// parseResults maps each test's result line to its counterexample id,
// using the "=== RUN TestGo2LeanPoC_<id>" line that precedes it.
func parseResults(stdout string) map[string]result {
	out := map[string]result{}
	current := ""
	var probes []contract.Probe
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "=== RUN   TestGo2LeanPoC_"); ok {
			current, probes = name, nil
			continue
		}
		// "GO2LEAN_PROBE <label> <value as a Go quoted string>", printed by
		// a spec's go2leanProbe helper in the real run.
		if rest, ok := strings.CutPrefix(line, "GO2LEAN_PROBE "); ok && current != "" {
			label, val, _ := strings.Cut(rest, " ")
			if s, err := strconv.Unquote(val); err == nil {
				b, _ := json.Marshal(s)
				probes = append(probes, contract.Probe{Label: label, Value: b})
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "GO2LEAN_RESULT "); ok && current != "" {
			var r result
			if json.Unmarshal([]byte(rest), &r) == nil {
				r.probes = probes
				out[current] = r
			}
		}
	}
	return out
}

func tail(s string) string {
	const n = 2000
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// goList resolves a package's directory and name.
func goList(root, pkg string) (dir, name string, err error) {
	cmd := exec.Command("go", "list", "-f", "{{.Dir}}\t{{.Name}}", pkg)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// An external test package (package x_test) is not importable: its
		// directory is the base package's, and the harness joins x_test.
		if base, ok := strings.CutSuffix(pkg, "_test"); ok {
			if dir, name, berr := goList(root, base); berr == nil {
				xtestBase.Store(pkg, base)
				return dir, name + "_test", nil
			}
		}
		return "", "", fmt.Errorf("go list %s: %w", pkg, err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("go list %s: unexpected output %q", pkg, out)
	}
	return parts[0], parts[1], nil
}

// xtestBase maps an external test package's path to its directory's
// package path (what go test builds), as goList found it.
var xtestBase sync.Map

// testPath is the package path go test builds for target package pkg.
func testPath(pkg string) string {
	if base, ok := xtestBase.Load(pkg); ok {
		return base.(string)
	}
	return pkg
}

// frameRe matches a stack frame's file:line.
var frameRe = regexp.MustCompile(`(?m)^\t(\S+\.go):(\d+)`)

// panicSite is the innermost frame of the stack inside the module (under
// root), outside go2lean's harness: the line of the code that panicked,
// which may be in another package of the module than the target's, or ""
// when the stack has none.
func panicSite(stack, root string) string {
	if root == "" {
		return ""
	}
	for _, m := range frameRe.FindAllStringSubmatch(stack, -1) {
		if strings.HasPrefix(m[1], root+string(filepath.Separator)) && !strings.Contains(filepath.Base(m[1]), "zz_go2lean") {
			return m[1] + ":" + m[2]
		}
	}
	return ""
}

// scheduleDependent: the model's run of cx had more than one goroutine, so
// what it found (a panic, a false result) may need the model's
// interleaving, which one run under Go's scheduler need not take.
func scheduleDependent(cx *contract.Counterexample) bool {
	for _, s := range cx.Schedule {
		if s.G != 0 {
			return true
		}
	}
	return false
}

func modelPanic(cx *contract.Counterexample) contract.PanicKind {
	if cx.ModelOutput == nil {
		return ""
	}
	return cx.ModelOutput.Panic
}

// judgeConc judges a deadlock or a goroutine leak the model found, from the
// concurrency harness's runs: the call blocked (a deadlock), a goroutine of
// the package stayed blocked after it returned (a leak), or neither in
// every run, which does not refute a schedule-dependent bug: Go's scheduler
// may never have taken the model's interleaving.
func judgeConc(v *contract.Verdict, prop *contract.Property, model contract.PanicKind, r result, diag *contract.Diagnosis) {
	switch {
	case r.Panicked && model == contract.PanicDeadlock && ClassifyPanic(r.Panic) == contract.PanicDeadlock:
		// Go's own detector: every goroutine of the test binary blocked.
		v.Observed["panic"] = r.Panic
		v.Status, diag = contract.VerdictConfirmed, nil
	case r.Panicked:
		v.Status = contract.VerdictInconclusive
		diag.Reason = "panic_kind_mismatch"
		diag.Notes = []string{fmt.Sprintf("the model found %s, the real run panicked: %s", model, r.Panic)}
	case r.Blocked:
		v.Observed["blocked"] = fmt.Sprintf("the call did not return within 2s (run %d)", r.Runs)
		if model == contract.PanicDeadlock {
			v.Status, diag = contract.VerdictConfirmed, nil
		} else {
			v.Status = contract.VerdictInconclusive
			diag.Reason = "panic_kind_mismatch"
			diag.Notes = []string{"the model found a goroutine leak; the real call blocked"}
		}
	case r.Leaked != "":
		v.Observed["leaked"] = tail(r.Leaked)
		if model == contract.PanicGoroutineLeak {
			v.Status, diag = contract.VerdictConfirmed, nil
		} else {
			v.Status = contract.VerdictInconclusive
			diag.Reason = "panic_kind_mismatch"
			diag.Notes = []string{"the model found a deadlock; the real call returned and left a goroutine blocked"}
		}
	default:
		v.Status = contract.VerdictInconclusive
		diag.Reason = "schedule_not_reproduced"
		diag.Notes = []string{fmt.Sprintf("no %s in %d real runs; Go's scheduler may not have taken the model's interleaving", model, r.Runs)}
	}
	v.Diagnosis = diag
}
