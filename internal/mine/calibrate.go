package mine

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fooexec/go2lean/internal/contract"
)

// Calibration (#315 P3): a spec written for a candidate drafted from a fix
// must be violated on the code before the fix and hold after it. Run on
// both revisions of the project, a spec that holds before checks nothing
// the fix changed, and one violated after is wrong or has found something
// the fix left.

// Spec outcomes on one revision.
const (
	OutViolated = "violated" // a counterexample the replay confirmed
	OutSpurious = "spurious" // counterexamples, none confirmed
	OutHeld     = "held"     // no counterexample
	OutNoBuild  = "no build" // the spec file does not build at this revision
	OutNotRun   = "not run"  // no property of the spec at this revision
)

// Calibration is the result for one candidate.
type Calibration struct {
	Candidate string            `json:"candidate"`
	Specs     []string          `json:"specs"`
	Before    string            `json:"before_rev"`
	After     string            `json:"after_rev"`
	AtBefore  map[string]Result `json:"at_before"` // by spec
	AtAfter   map[string]Result `json:"at_after"`
	// Passed: some spec is violated before and every spec holds after.
	Passed bool   `json:"passed"`
	Why    string `json:"why"`
}

// Result is a spec's outcome on one revision, with what the search
// covered (the hunt summary) or why the run stopped.
type Result struct {
	Outcome string `json:"outcome"`
	Search  string `json:"search,omitempty"`
}

// Runner runs the specs of the spec file installed in package pkg of the
// module checked out in dir, and gives each spec's result.
type Runner func(dir, pkg string, specs []string) (map[string]Result, error)

// candidateMark is how a spec names the candidate it was written for.
var candidateMark = regexp.MustCompile(`\bcandidate (c\d{3,})\b`)

// SpecsByCandidate reads a spec file: the specs (functions named Spec* or
// Seq*) each candidate's doc comments name.
func SpecsByCandidate(specFile string) (map[string][]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, specFile, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Doc == nil || !(strings.HasPrefix(fd.Name.Name, "Spec") || strings.HasPrefix(fd.Name.Name, "Seq")) {
			continue
		}
		for _, m := range candidateMark.FindAllStringSubmatch(fd.Doc.Text(), -1) {
			if !contains(out[m[1]], fd.Name.Name) {
				out[m[1]] = append(out[m[1]], fd.Name.Name)
			}
		}
	}
	return out, nil
}

// Calibrate checks the specs of specFile written for the candidates that
// record a fix, on worktrees of module at the revisions before and after
// it; pkg is the package directory relative to the module.
func Calibrate(module, pkg, specFile string, cs []Candidate, run Runner, scratch string) ([]Calibration, error) {
	byCand, err := SpecsByCandidate(specFile)
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(specFile)
	if err != nil {
		return nil, err
	}
	var out []Calibration
	for _, c := range cs {
		specs := byCand[c.ID]
		if c.After == "" || len(specs) == 0 {
			continue
		}
		sort.Strings(specs)
		cal := Calibration{Candidate: c.ID, Specs: specs, Before: c.Before, After: c.After}
		before := c.Before
		if before == "" {
			before = c.After + "^"
		}
		for i, rev := range []string{before, c.After} {
			res, err := runAt(module, pkg, filepath.Base(specFile), src, rev, specs, run, filepath.Join(scratch, fmt.Sprintf("%s-%d", c.ID, i)))
			if err != nil {
				return out, fmt.Errorf("calibrate %s at %s: %w", c.ID, rev, err)
			}
			if i == 0 {
				cal.AtBefore = res
			} else {
				cal.AtAfter = res
			}
		}
		cal.Passed, cal.Why = judge(specs, cal.AtBefore, cal.AtAfter)
		out = append(out, cal)
	}
	return out, nil
}

// judge: passed when some spec is violated before the fix and every spec
// holds after it.
func judge(specs []string, before, after map[string]Result) (bool, string) {
	var bad []string
	caught := false
	for _, s := range specs {
		switch after[s].Outcome {
		case OutHeld:
		case "":
			bad = append(bad, s+" not run after the fix")
		default:
			bad = append(bad, s+" "+after[s].Outcome+" after the fix")
		}
		if before[s].Outcome == OutViolated {
			caught = true
		}
	}
	if len(bad) > 0 {
		return false, strings.Join(bad, "; ")
	}
	if !caught {
		var bs []string
		for _, s := range specs {
			bs = append(bs, s+": "+before[s].Outcome)
		}
		return false, "no spec violated before the fix (" + strings.Join(bs, ", ") + ")"
	}
	return true, "violated before the fix, holds after"
}

// runAt checks rev out in a fresh worktree, installs the spec file in pkg
// and runs the specs.
func runAt(module, pkg, name string, src []byte, rev string, specs []string, run Runner, wt string) (map[string]Result, error) {
	os.RemoveAll(wt)
	if out, err := exec.Command("git", "-C", module, "worktree", "add", "--detach", "--force", wt, rev).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git worktree add %s: %v: %s", rev, err, strings.TrimSpace(string(out)))
	}
	defer func() {
		exec.Command("git", "-C", module, "worktree", "remove", "--force", wt).Run()
		os.RemoveAll(wt)
	}()
	dir := filepath.Join(wt, pkg)
	if _, err := os.Stat(dir); err != nil {
		res := map[string]Result{}
		for _, s := range specs {
			res[s] = Result{OutNoBuild, "no package " + pkg + " at " + rev}
		}
		return res, nil
	}
	if err := os.WriteFile(filepath.Join(dir, name), src, 0o644); err != nil {
		return nil, err
	}
	return run(wt, pkg, specs)
}

// GoRunner runs go2lean (the binary at bin) on the package, specs only,
// with the extra run flags (a larger search budget, say), and reads each
// spec's result from the workspace.
func GoRunner(bin string, extra ...string) Runner {
	return func(dir, pkg string, specs []string) (map[string]Result, error) {
		ws, err := os.MkdirTemp("", "go2lean-calibrate-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(ws)
		// -specs-only keeps the package's specs; -run would select by the
		// function under test, so the specs are picked out of the results.
		args := append([]string{"run", "-prove=false", "-specs-only", "-workspace", ws}, extra...)
		args = append(args, "./"+strings.TrimPrefix(pkg, "./"))
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		out, runErr := cmd.CombinedOutput()
		res := Outcomes(contract.Workspace{Root: ws}, specs)
		for s, r := range res {
			if r.Outcome == OutNotRun && runErr != nil {
				// A spec the run did not reach: the spec file, or the
				// package, does not build at this revision.
				res[s] = Result{OutNoBuild, lastLines(string(out), 5)}
			}
		}
		return res, nil
	}
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}

// Outcomes reads the result of each spec from a workspace a run wrote.
func Outcomes(ws contract.Workspace, specs []string) map[string]Result {
	props, _ := ws.Properties()
	ofProp := map[string]string{} // property id -> spec
	res := map[string]Result{}
	for _, p := range props {
		if p.Spec == nil {
			continue
		}
		for _, s := range specs {
			if p.Spec.GoFunc == s || strings.HasSuffix(p.Spec.GoFunc, "."+s) {
				ofProp[p.ID] = s
				r := Result{Outcome: OutHeld}
				if h := ws.HuntRecord(p.ID); h != nil {
					r.Search = h.Summary
				}
				res[s] = r
			}
		}
	}
	vs, _ := filepath.Glob(filepath.Join(ws.Root, "verdicts", "*.json"))
	for _, f := range vs {
		cxID := strings.TrimSuffix(filepath.Base(f), ".json")
		v := ws.Verdict(cxID)
		cx, _ := ws.Counterexample(cxID) // read, even if incomplete
		if v == nil || cx == nil {
			continue
		}
		s, ok := ofProp[cx.Property]
		if !ok {
			continue
		}
		r := res[s]
		switch {
		case v.Status == contract.VerdictConfirmed:
			r.Outcome = OutViolated
		case r.Outcome != OutViolated:
			r.Outcome = OutSpurious
		}
		res[s] = r
	}
	for _, s := range specs {
		if _, ok := res[s]; !ok {
			res[s] = Result{Outcome: OutNotRun}
		}
	}
	return res
}
