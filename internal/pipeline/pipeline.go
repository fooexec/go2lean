// Package pipeline composes the three modules into the steps the CLI and the
// agent both run: compile (M1), propose and hunt (M2), verify (M3).
package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/andreasfoo/go2lean/internal/builtin"
	"github.com/andreasfoo/go2lean/internal/constraint"
	"github.com/andreasfoo/go2lean/internal/contract"
	"github.com/andreasfoo/go2lean/internal/frontend/gossa"
	"github.com/andreasfoo/go2lean/internal/fuzzimport"
	"github.com/andreasfoo/go2lean/internal/ir"
	"github.com/andreasfoo/go2lean/internal/poc"
	"github.com/andreasfoo/go2lean/internal/repo"
	"github.com/andreasfoo/go2lean/internal/ssaout"
)

// Env says where things are: Root is the default Go module directory,
// Module overrides it. The trusted Lean runtime is embedded in the binary.
type Env struct {
	Root   string
	Module string
	// Deps models the dependency functions the compiled packages call
	// (see gossa.Options.Deps).
	Deps bool
	// Resume skips properties whose search is recorded in the workspace and
	// counterexamples that already have a verdict.
	Resume bool
	// FuzzSpecs reads the packages' Go fuzz targets as sequence specs
	// (internal/fuzzimport); their generated file joins Overlay.
	FuzzSpecs bool
	// Overlay adds files to the packages for compiling and replaying
	// (absolute path to contents), never written into them.
	Overlay map[string][]byte
	// Tests makes test functions targets (gossa.Options.Tests).
	Tests bool
	// Refine is how many rounds the refinement loop runs under Deps (#85):
	// after the replay stage, the dependency functions whose unmodelled
	// calls made a counterexample spurious or inconclusive are translated,
	// and their properties searched and replayed again. 0 turns it off.
	Refine int
	// Translate names dependency functions to translate whatever their
	// depth (gossa.Options.Translate); the refinement loop adds to it.
	Translate []string
	// SpecsOnly searches the packages' specs only, not the no_panic
	// templates: on a large package the templates otherwise take the
	// budget first.
	SpecsOnly bool
	// Run, when set, keeps the properties whose target function (or
	// method, as T.M) it matches, as go test -run selects tests.
	Run *regexp.Regexp
	// CompareOrder requires a static call-graph assessment of the supplied
	// canonical generated overlay before emitting a model. The field alone
	// does not activate a property; ConfigureCompareOrder additionally binds
	// source fingerprints and a dedicated persistent check workspace.
	CompareOrder   *builtin.Binding
	compareSources *builtin.SourceGraph
	compareRef     *contract.BuiltinRef
}

func (e Env) module() string {
	if e.Module != "" {
		return e.Module
	}
	return e.Root
}

// Compile runs M1 on one package and prints a summary to out.
func Compile(w contract.Workspace, env Env, pattern string, out io.Writer) (*contract.ModelReport, error) {
	all, err := CompileAll(w, env, out, pattern)
	if err != nil {
		return nil, err
	}
	// The dependencies -deps models come along; the pattern names one.
	var reports []*contract.ModelReport
	for _, r := range all {
		if !r.Dependency {
			reports = append(reports, r)
		}
	}
	if len(reports) != 1 {
		return nil, fmt.Errorf("pattern %s matched %d packages; expected exactly one", pattern, len(reports))
	}
	return reports[0], nil
}

// CompileLinked compiles the one package pattern names with the
// dependency functions it calls (-deps), and links them into a single
// model (ir.Link): the package's functions keep their ids, the
// dependencies' follow. A theorem about it is about the dependencies'
// models too, not havoc. Only the linked model is written.
func CompileLinked(w contract.Workspace, env Env, pattern string, out io.Writer) (*contract.ModelReport, error) {
	if err := refuseActiveCompile(w); err != nil {
		return nil, err
	}
	if env.compareSources != nil {
		return nil, fmt.Errorf("compare-order activation uses ordinary compilation, not proof linking")
	}
	if err := validateCompareOrderOverlay(env); err != nil {
		return nil, err
	}
	progs, modPath, err := gossa.TranslateAllOpts(env.module(), gossa.Options{Deps: true, Overlay: env.Overlay, Tests: env.Tests, Translate: env.Translate}, pattern)
	if err != nil {
		return nil, err
	}
	if err := assessCompareOrder(w, env, progs, out); err != nil {
		return nil, err
	}
	var main *ir.Prog
	deps := map[string]*ir.Prog{}
	for _, p := range progs {
		if p.Dependency {
			deps[p.Package] = p
		} else if main != nil {
			return nil, fmt.Errorf("pattern %s matched more than one package; expected exactly one", pattern)
		} else {
			main = p
		}
	}
	if main == nil {
		return nil, fmt.Errorf("pattern %s matched no package", pattern)
	}
	linked, err := ir.Link(main, deps, func(pkg string) string { return ssaout.RegistryKey(main.Frontend, pkg) })
	if err != nil {
		return nil, err
	}
	leanDir := repo.WorkspaceLeanDir(w.Root)
	core, err := repo.CoreDir(env.Root)
	if err != nil {
		return nil, err
	}
	if err := ssaout.EnsureProject(leanDir, core); err != nil {
		return nil, err
	}
	constraint.InvalidateDriver()
	report, err := ssaout.Emit(linked, leanDir, w, modPath)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "compiled %s with %d dependency packages linked -> %s\n", main.Package, len(deps), filepath.Join(w.Root, report.LeanFile))
	for i, f := range report.Funcs {
		if i >= len(main.Funcs) {
			break
		}
		fmt.Fprintf(out, "  %-12s %-9s %s\n", f.Name, f.Status, f.GoSig)
		for _, is := range f.Issues {
			fmt.Fprintf(out, "      - %s\n", is)
		}
	}
	return report, nil
}

// CompileAll runs M1 on every package matching the patterns (e.g. "./...")
// with a single load of the dependency graph.
func CompileAll(w contract.Workspace, env Env, out io.Writer, patterns ...string) ([]*contract.ModelReport, error) {
	if err := refuseActiveCompile(w); err != nil {
		return nil, err
	}
	if err := validateCompareOrderOverlay(env); err != nil {
		return nil, err
	}
	progs, modPath, err := gossa.TranslateAllOpts(env.module(), gossa.Options{Deps: env.Deps, Overlay: env.Overlay, Tests: env.Tests, Translate: env.Translate}, patterns...)
	if err != nil {
		return nil, err
	}
	if err := assessCompareOrder(w, env, progs, out); err != nil {
		return nil, err
	}
	var reports []*contract.ModelReport
	leanDir := repo.WorkspaceLeanDir(w.Root)
	core, err := repo.CoreDir(env.Root)
	if err != nil {
		return nil, err
	}
	if err := ssaout.EnsureProject(leanDir, core); err != nil {
		return nil, err
	}
	for _, prog := range progs {
		constraint.InvalidateDriver()
		report, err := ssaout.Emit(prog, leanDir, w, modPath)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(out, "compiled %s -> %s\n", prog.Package, filepath.Join(w.Root, report.LeanFile))
		for _, f := range report.Funcs {
			fmt.Fprintf(out, "  %-12s %-9s %s", f.Name, f.Status, f.GoSig)
			if f.SpecFor != "" {
				fmt.Fprintf(out, "  [spec for %s]", f.SpecFor)
			}
			fmt.Fprintln(out)
			for _, is := range f.Issues {
				fmt.Fprintf(out, "      - %s\n", is)
			}
		}
		reports = append(reports, report)
	}
	if env.compareSources != nil {
		ref, err := builtin.WriteSnapshot(w, env.module(), env.CompareOrder, env.compareSources, reports)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(out, "compiled builtin snapshot: %s (explicit ordering/purity/totality assumptions; not a passed check)\n", ref.SnapshotSHA256)
	}
	return reports, nil
}

// Propose runs M2's property templates and prints them.
func Propose(w contract.Workspace, report *contract.ModelReport, out io.Writer) ([]*contract.Property, error) {
	props, active, err := activeProperties(w, report)
	if err == nil && !active {
		props, err = constraint.Propose(w, report)
	}
	if err != nil {
		return nil, err
	}
	for _, p := range props {
		fmt.Fprintf(out, "  %-28s %-14s %s\n", p.ID, p.Kind, p.NL)
	}
	return props, nil
}

// Hunt runs the engines on one property and prints the outcome.
func Hunt(w contract.Workspace, env Env, p *contract.Property, opts constraint.SearchOptions, out io.Writer) (*contract.Counterexample, error) {
	if err := validateBuiltinHunt(w, env, p); err != nil {
		return nil, err
	}
	if env.Resume {
		if r := w.HuntRecord(p.ID); r != nil {
			if p.Builtin != nil && r.BuiltinSnapshotSHA256 != p.Builtin.SnapshotSHA256 {
				return nil, fmt.Errorf("builtin: cached hunt belongs to another compiled snapshot")
			}
			fmt.Fprintf(out, "  %-28s (resumed) %s\n", p.ID, r.Summary)
			if r.Counterexample == "" {
				return nil, nil
			}
			return w.Counterexample(r.Counterexample)
		}
	}
	cx, summary, err := constraint.Hunt(w, env.Root, p, opts)
	if err != nil {
		return nil, fmt.Errorf("hunt %s: %w", p.ID, err)
	}
	rec := &contract.HuntRecord{Property: p.ID, Summary: summary}
	if p.Builtin != nil {
		rec.BuiltinSnapshotSHA256 = p.Builtin.SnapshotSHA256
	}
	if cx != nil {
		rec.Counterexample = cx.ID
	}
	if err := w.WriteHuntRecord(rec); err != nil {
		return nil, err
	}
	if cx == nil {
		fmt.Fprintf(out, "  %-28s %s\n", p.ID, summary)
		return nil, nil
	}
	fmt.Fprintf(out, "  %-28s %s -> %s\n", p.ID, summary, cx.ID)
	fmt.Fprintf(out, "      inputs %v", cx.Inputs)
	if len(cx.Memory) > 0 {
		fmt.Fprintf(out, " memory %v", cx.Memory)
	}
	if cx.ModelOutput != nil {
		fmt.Fprintf(out, " model %s %s %s", cx.ModelOutput.Res, cx.ModelOutput.Value, cx.ModelOutput.Panic)
	}
	fmt.Fprintln(out)
	for _, pr := range cx.Probes {
		fmt.Fprintf(out, "      probe %s = %s\n", pr.Label, pr.Value)
	}
	return cx, nil
}

// HuntAll runs Hunt on every property, opts.Jobs at a time, printing each
// property's lines together in the order given.
func HuntAll(w contract.Workspace, env Env, props []*contract.Property, opts constraint.SearchOptions, out io.Writer) ([]*contract.Counterexample, error) {
	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = runtime.NumCPU()
	}
	type result struct {
		cx  *contract.Counterexample
		err error
		log bytes.Buffer
	}
	results := make([]result, len(props))
	var wg sync.WaitGroup
	sem := make(chan struct{}, jobs)
	for i, p := range props {
		wg.Add(1)
		go func(i int, p *contract.Property) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i].cx, results[i].err = Hunt(w, env, p, opts, &results[i].log)
		}(i, p)
	}
	wg.Wait()
	var cxs []*contract.Counterexample
	for i := range results {
		io.Copy(out, &results[i].log)
		if results[i].err != nil {
			return cxs, results[i].err
		}
		if results[i].cx != nil {
			cxs = append(cxs, results[i].cx)
		}
	}
	return cxs, nil
}

// Verify runs M3 on one counterexample and prints the verdict.
func Verify(w contract.Workspace, env Env, id string, out io.Writer) (*contract.Verdict, error) {
	vs, err := VerifyAll(w, env, []string{id}, out)
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

// VerifyAll runs M3 on several counterexamples (one go test per package).
func VerifyAll(w contract.Workspace, env Env, ids []string, out io.Writer) ([]*contract.Verdict, error) {
	var done []*contract.Verdict
	if env.Resume {
		var todo []string
		for _, id := range ids {
			if v := w.Verdict(id); v != nil {
				cx, err := w.Counterexample(id)
				if err != nil {
					return nil, err
				}
				p, err := w.Property(cx.Property)
				if err != nil {
					return nil, err
				}
				t, err := w.Target(p.Target)
				if err != nil {
					return nil, err
				}
				if builtin.IsGeneratedProperty(p, t) {
					if err := builtin.ValidateCachedVerdict(w, env.module(), cx, p, v); err != nil {
						return nil, err
					}
				}
				done = append(done, v)
			} else {
				todo = append(todo, id)
			}
		}
		ids = todo
	}
	var vs []*contract.Verdict
	if len(ids) > 0 {
		var err error
		vs, err = poc.VerifyAll(w, env.module(), ids, env.Overlay)
		if err != nil {
			return nil, fmt.Errorf("poc: %w", err)
		}
	}
	vs = append(done, vs...)
	for _, v := range vs {
		printVerdict(v, out)
	}
	return vs, nil
}

func printVerdict(v *contract.Verdict, out io.Writer) {
	id := v.Counterexample
	fmt.Fprintf(out, "  %-28s %-12s", id, v.Status)
	if v.Observed != nil {
		fmt.Fprintf(out, " real: %v", v.Observed)
	}
	if v.Diagnosis != nil {
		fmt.Fprintf(out, " (%s)", v.Diagnosis.Reason)
	}
	fmt.Fprintln(out)
	if v.Status == contract.VerdictSpurious && v.Diagnosis != nil {
		for _, n := range v.Diagnosis.Notes {
			fmt.Fprintf(out, "      %s\n", n)
		}
	}
	if v.Status == contract.VerdictConfirmed {
		if v.Evidence.PanicSite != "" {
			fmt.Fprintf(out, "      at %s\n", v.Evidence.PanicSite)
		}
		if v.Rank != nil {
			fmt.Fprintf(out, "      rank %d: %s\n", v.Rank.Tier, v.Rank.Reason)
			for _, s := range v.Rank.Sites {
				fmt.Fprintf(out, "        %s\n", s)
			}
		}
		fmt.Fprintf(out, "      PoC: %s\n", v.PocDir)
	}
	for _, pr := range v.Probes {
		fmt.Fprintf(out, "      real probe %s = %s\n", pr.Label, pr.Value)
	}
}

// Result is what one full pass over a package produced.
type Result struct {
	Report     *contract.ModelReport
	Properties []*contract.Property
	Verdicts   []*contract.Verdict
}

// Run executes all three modules on one package.
func Run(w contract.Workspace, env Env, pattern string, opts constraint.SearchOptions, out io.Writer) (*Result, error) {
	results, err := RunAll(w, env, opts, out, pattern)
	if err != nil {
		return nil, err
	}
	if len(results) != 1 {
		return nil, fmt.Errorf("pattern %s matched %d packages; expected exactly one", pattern, len(results))
	}
	return results[0], nil
}

// RunAll executes all three modules on every package matching the patterns
// with a single load of the module, skipping packages with nothing modelled.
func RunAll(w contract.Workspace, env Env, opts constraint.SearchOptions, out io.Writer, patterns ...string) ([]*Result, error) {
	if env.FuzzSpecs {
		overlay, skipped, err := fuzzimport.Specs(env.module(), patterns...)
		if err != nil {
			return nil, err
		}
		if len(overlay) > 0 || len(skipped) > 0 {
			fmt.Fprintln(out, "== fuzz targets as specs")
		}
		for path, src := range overlay {
			fmt.Fprintf(out, "  %s: %d specs\n", filepath.Dir(path), strings.Count(string(src), "go2lean:sequence"))
		}
		for _, s := range skipped {
			fmt.Fprintf(out, "  %s.%s not read: %s\n", s.Package, s.Fuzz, s.Reason)
		}
		env.Overlay = maps.Clone(env.Overlay)
		if env.Overlay == nil {
			env.Overlay = map[string][]byte{}
		}
		maps.Copy(env.Overlay, overlay)
	}
	fmt.Fprintln(out, "== Compile stage")
	var reports []*contract.ModelReport
	var err error
	if env.compareRef != nil {
		s, loadErr := builtin.LoadSnapshot(w, env.compareRef)
		if loadErr != nil {
			return nil, loadErr
		}
		r, loadErr := w.ModelReport("gossa", s.Binding.Package)
		if loadErr != nil {
			return nil, loadErr
		}
		reports = []*contract.ModelReport{r}
		fmt.Fprintln(out, "  resumed immutable compiled builtin snapshot")
	} else {
		reports, err = CompileAll(w, env, out, patterns...)
	}
	if err != nil {
		return nil, err
	}
	var results []*Result
	var failed []string
	for _, report := range reports {
		if report.Dependency {
			// Modelled for its callers (-deps); not a target of the run.
			continue
		}
		modelled := false
		for _, f := range report.Funcs {
			if f.Status != contract.ModelRejected {
				modelled = true
			}
		}
		if !modelled {
			continue
		}
		fmt.Fprintf(out, "######## %s\n", report.Package)
		res, err := runReport(w, env, report, opts, out)
		if err != nil {
			// One package's trouble (a Lean build failure, a harness that does
			// not compile) must not end a sweep, but the sweep reports it.
			fmt.Fprintf(out, "  error: %v\n", err)
			failed = append(failed, report.Package)
			continue
		}
		results = append(results, res)
	}
	if env.Deps && env.Refine > 0 && len(results) > 0 {
		if err := refine(w, env, opts, out, results, patterns); err != nil {
			fmt.Fprintf(out, "  error: refinement: %v\n", err)
		}
	}
	if len(failed) > 0 {
		return results, fmt.Errorf("%d of %d packages failed: %s", len(failed), len(failed)+len(results), strings.Join(failed, ", "))
	}
	return results, nil
}

// refine is the refinement loop (#85). A spurious or inconclusive verdict
// whose counterexample read the result of a dependency function the model
// left unmodelled (past -deps' depth or limit) names that function; each
// round translates the new ones, recompiles, and searches and replays
// those properties again. A counterexample the refined model no longer has
// is dropped. The loop stops when no verdict names a function not yet
// translated, or after env.Refine rounds; the verdicts in results are
// updated in place.
func refine(w contract.Workspace, env Env, opts constraint.SearchOptions, out io.Writer, results []*Result, patterns []string) error {
	env.Resume = false
	for round := 1; round <= env.Refine; round++ {
		var add []string
		retry := map[string]*contract.Property{}
		for _, res := range results {
			for _, v := range res.Verdicts {
				if v.Status != contract.VerdictSpurious && v.Status != contract.VerdictInconclusive {
					continue
				}
				cx, err := w.Counterexample(v.Counterexample)
				if err != nil {
					return err
				}
				var fresh []string
				for _, c := range poc.RefinableCalls(w, cx) {
					if !slices.Contains(env.Translate, c) && !slices.Contains(add, c) {
						fresh = append(fresh, c)
					}
				}
				if len(fresh) == 0 {
					continue
				}
				add = append(add, fresh...)
				p, err := w.Property(cx.Property)
				if err != nil {
					return err
				}
				retry[p.ID] = p
			}
		}
		if len(add) == 0 {
			return nil
		}
		fmt.Fprintf(out, "== Refinement round %d: translating %s\n", round, strings.Join(add, ", "))
		env.Translate = append(env.Translate, add...)
		if _, err := CompileAll(w, env, io.Discard, patterns...); err != nil {
			return err
		}
		var props []*contract.Property
		for _, id := range slices.Sorted(maps.Keys(retry)) {
			props = append(props, retry[id])
		}
		// The old counterexamples go: a property the refined model holds
		// keeps none, and one it still refutes gets a new one.
		for _, res := range results {
			kept := res.Verdicts[:0]
			for _, v := range res.Verdicts {
				if cx, err := w.Counterexample(v.Counterexample); err == nil && retry[cx.Property] != nil {
					if err := w.RemoveCounterexample(v.Counterexample); err != nil {
						return err
					}
					continue
				}
				kept = append(kept, v)
			}
			res.Verdicts = kept
		}
		cxs, err := HuntAll(w, env, props, opts, out)
		if err != nil {
			return err
		}
		if len(cxs) == 0 {
			continue
		}
		var ids []string
		for _, cx := range cxs {
			ids = append(ids, cx.ID)
		}
		vs, err := VerifyAll(w, env, ids, out)
		if err != nil {
			return err
		}
		// Each new verdict joins its package's result.
		for _, v := range vs {
			cx, err := w.Counterexample(v.Counterexample)
			if err != nil {
				return err
			}
			p := retry[cx.Property]
			t, err := w.Target(p.Target)
			if err != nil {
				return err
			}
			for _, res := range results {
				if res.Report.Package == t.Package {
					res.Verdicts = append(res.Verdicts, v)
				}
			}
		}
	}
	return nil
}

func runReport(w contract.Workspace, env Env, report *contract.ModelReport, opts constraint.SearchOptions, out io.Writer) (*Result, error) {
	fmt.Fprintln(out, "== Spec & search stage: properties")
	props, err := Propose(w, report, out)
	if err != nil {
		return nil, err
	}
	if env.SpecsOnly {
		props = specsOnly(props)
	}
	if env.Run != nil {
		props = matchingTargets(w, props, env.Run)
	}
	fmt.Fprintln(out, "== Spec & search stage: hunt")
	cxs, err := HuntAll(w, env, props, opts, out)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(out, "== Replay stage: generate PoC and verify")
	res := &Result{Report: report, Properties: props}
	if len(cxs) == 0 {
		fmt.Fprintln(out, "  nothing to verify")
		return res, nil
	}
	var ids []string
	for _, cx := range cxs {
		ids = append(ids, cx.ID)
	}
	vs, err := VerifyAll(w, env, ids, out)
	if err != nil {
		return nil, err
	}
	res.Verdicts = vs
	return res, nil
}

// specsOnly drops the spec-free templates (no_panic, no_hang). It goes by
// kind: properties derived from specs also carry Source "template".
func specsOnly(props []*contract.Property) []*contract.Property {
	var out []*contract.Property
	for _, p := range props {
		if p.Kind != contract.KindNoPanic && p.Kind != contract.KindNoHang {
			out = append(out, p)
		}
	}
	return out
}

// matchingTargets keeps the properties whose target's function matches re.
func matchingTargets(w contract.Workspace, props []*contract.Property, re *regexp.Regexp) []*contract.Property {
	var out []*contract.Property
	for _, p := range props {
		if t, err := w.Target(p.Target); err == nil && re.MatchString(t.Func) {
			out = append(out, p)
		}
	}
	return out
}
