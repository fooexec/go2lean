package mine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
)

const calSpec = `package frame

// SpecParseShort: candidate c001. A short frame is rejected.
func SpecParseShort(b []byte, n int, err error) bool { return len(b) > 0 || err != nil }

// SeqParseTwice checks candidate c001 and candidate c002.
func SeqParseTwice(b []byte) bool { return true }

// SpecOther names no candidate.
func SpecOther(b []byte, n int, err error) bool { return true }
`

func TestSpecsByCandidate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "specs_test.go")
	if err := os.WriteFile(p, []byte(calSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := SpecsByCandidate(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m["c001"], ",") != "SpecParseShort,SeqParseTwice" || strings.Join(m["c002"], ",") != "SeqParseTwice" || len(m) != 2 {
		t.Errorf("got %v", m)
	}
}

func TestCalibrate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	write(t, dir, "go.mod", "module example.com/frame\n")
	write(t, dir, "frame/frame.go", pkgSrc)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "initial")
	before := run(t, dir, "rev-parse", "HEAD")
	write(t, dir, "frame/frame.go", strings.Replace(pkgSrc, "return len(b), nil", "if len(b) == 0 { return 0, nil }; return len(b), nil", 1))
	run(t, dir, "commit", "-q", "-am", "fix")
	after := run(t, dir, "rev-parse", "HEAD")

	spec := filepath.Join(t.TempDir(), "specs_test.go")
	if err := os.WriteFile(spec, []byte(calSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	// The fake runner: the spec "catches" the code without the fix.
	var seen []string
	fake := func(wt, pkg, _ string, specs []string) (map[string]Result, error) {
		src, err := os.ReadFile(filepath.Join(wt, pkg, "frame.go"))
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(wt, pkg, "specs_test.go")); err != nil {
			t.Errorf("spec file not installed: %v", err)
		}
		fixed := strings.Contains(string(src), "len(b) == 0")
		seen = append(seen, strings.Join(specs, ","))
		res := map[string]Result{}
		for _, s := range specs {
			switch {
			case s == "SpecParseShort" && !fixed:
				res[s] = Result{Outcome: OutViolated}
			case s == "SeqParseTwice" && fixed:
				res[s] = Result{Outcome: OutSpurious}
			default:
				res[s] = Result{Outcome: OutHeld}
			}
		}
		return res, nil
	}
	cs := []Candidate{
		{ID: "c001", Before: before, After: after},
		{ID: "c002", After: after}, // Before defaults to After^
		{ID: "c003", After: after}, // no spec
		{ID: "c004"},               // no fix
	}
	cals, err := Calibrate(dir, "frame", spec, "", cs, fake, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(cals) != 2 || len(seen) != 4 {
		t.Fatalf("calibrations %+v, runs %v", cals, seen)
	}
	// c001: SpecParseShort is violated before, but SeqParseTwice is
	// spurious after the fix.
	if cals[0].Passed || !strings.Contains(cals[0].Why, "SeqParseTwice spurious after the fix") {
		t.Errorf("c001: %+v", cals[0])
	}
	// c002: SeqParseTwice holds before the fix.
	if cals[1].Passed || !strings.Contains(cals[1].Why, "no spec violated before") && !strings.Contains(cals[1].Why, "spurious") {
		t.Errorf("c002: %+v", cals[1])
	}
	if ok, why := judge([]string{"A", "B"}, map[string]Result{"A": {Outcome: OutViolated}, "B": {Outcome: OutHeld}}, map[string]Result{"A": {Outcome: OutHeld}, "B": {Outcome: OutHeld}}); !ok {
		t.Errorf("judge: %s", why)
	}
	// A spec file for the revision before the fix, installed under the
	// spec file's name there.
	beforeSpec := filepath.Join(t.TempDir(), "specs_before_test.go")
	if err := os.WriteFile(beforeSpec, []byte(calSpec+"\n// before the fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var installed []bool
	saw := func(wt, pkg, file string, specs []string) (map[string]Result, error) {
		b, err := os.ReadFile(filepath.Join(wt, pkg, "specs_test.go"))
		if err != nil {
			return nil, err
		}
		installed = append(installed, strings.Contains(string(b), "before the fix"))
		return fake(wt, pkg, file, specs)
	}
	if _, err := Calibrate(dir, "frame", spec, beforeSpec, cs[:1], saw, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(installed) != 2 || !installed[0] || installed[1] {
		t.Errorf("before file installed at %v, want [true false]", installed)
	}
	// The two files must name the same specs.
	other := filepath.Join(t.TempDir(), "other_test.go")
	os.WriteFile(other, []byte("package frame\n\n// SpecX checks candidate c001.\nfunc SpecX() bool { return true }\n"), 0o644)
	if _, err := Calibrate(dir, "frame", spec, other, cs[:1], saw, t.TempDir()); err == nil || !strings.Contains(err.Error(), "same specs") {
		t.Errorf("mismatched spec files: %v", err)
	}
	// The worktrees are gone.
	if out := run(t, dir, "worktree", "list"); strings.Count(out, "\n") != 0 {
		t.Errorf("worktrees left:\n%s", out)
	}
}

func TestOutcomes(t *testing.T) {
	ws := contract.Workspace{Root: t.TempDir()}
	for _, p := range []*contract.Property{
		{ID: "p1", Kind: contract.KindSequence, Spec: &contract.SpecRef{GoFunc: "example.com/frame.SeqA"}},
		{ID: "p2", Kind: contract.KindSequence, Spec: &contract.SpecRef{GoFunc: "example.com/frame.SeqB"}},
		{ID: "p3", Kind: contract.KindPostcondition, Spec: &contract.SpecRef{GoFunc: "example.com/frame.SpecC"}},
		// A sequence spec is named by its target.
		{ID: "p4", Kind: contract.KindSequence, Target: "t4"},
	} {
		if err := ws.WriteProperty(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := ws.WriteTarget(&contract.Target{ID: "t4", Func: "SeqE"}); err != nil {
		t.Fatal(err)
	}
	ws.WriteHuntRecord(&contract.HuntRecord{Property: "p2", Summary: "random: none in 2000 trials (0 out of fuel, 0 moot/stuck); spec checked 2000, vacuous 0 []"})
	// Every run out of fuel: nothing was checked.
	ws.WriteHuntRecord(&contract.HuntRecord{Property: "p4", Summary: "random: none in 563 trials (563 out of fuel, 0 moot/stuck)"})
	for i, st := range []contract.VerdictStatus{contract.VerdictConfirmed, contract.VerdictSpurious} {
		id := []string{"cx1", "cx3"}[i]
		ws.WriteCounterexample(&contract.Counterexample{ID: id, Property: []string{"p1", "p3"}[i]})
		ws.WriteVerdict(&contract.Verdict{Counterexample: id, Status: st})
	}
	got := Outcomes(ws, []string{"SeqA", "SeqB", "SpecC", "SeqD", "SeqE"})
	want := map[string]Result{
		"SeqA":  {Outcome: OutViolated},
		"SeqB":  {Outcome: OutHeld, Search: "random: none in 2000 trials (0 out of fuel, 0 moot/stuck); spec checked 2000, vacuous 0 []"},
		"SpecC": {Outcome: OutSpurious},
		"SeqD":  {Outcome: OutNotRun},
		"SeqE":  {Outcome: OutUnknown, Search: "random: none in 563 trials (563 out of fuel, 0 moot/stuck)"},
	}
	for s, w := range want {
		if got[s] != w {
			t.Errorf("%s: got %+v, want %+v", s, got[s], w)
		}
	}
}

func TestCalibrateInTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	write(t, dir, "bgpd/attr.c", "int bound = 4;\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "initial")
	write(t, dir, "bgpd/attr.c", "int bound = 5;\n")
	run(t, dir, "commit", "-q", "-am", "fix")
	fix := run(t, dir, "rev-parse", "HEAD")
	// A file of the build, not in git, that a worktree would lack.
	write(t, dir, "config.h", "#define X 1\n")
	write(t, dir, "bgpd/zz_spec.c", "#define SPEC __attribute__((annotate(\"go2lean:spec\")))\n\n/* The bound: candidate c001. */\nSPEC int spec_bound(void)\n{\n\treturn 1;\n}\n\nstatic int helper(void)\n{\n\treturn 0;\n}\n")

	var seen []string
	fake := func(d, pkg, file string, specs []string) (map[string]Result, error) {
		b, _ := os.ReadFile(filepath.Join(d, "bgpd/attr.c"))
		if _, err := os.Stat(filepath.Join(d, "config.h")); err != nil {
			t.Error("config.h missing in the tree")
		}
		seen = append(seen, strings.TrimSpace(string(b)))
		out := map[string]Result{}
		for _, s := range specs {
			if strings.Contains(string(b), "4") {
				out[s] = Result{Outcome: OutViolated}
			} else {
				out[s] = Result{Outcome: OutHeld}
			}
		}
		return out, nil
	}
	cals, err := CalibrateInTree(dir, "bgpd", filepath.Join(dir, "bgpd/zz_spec.c"), "", []Candidate{{ID: "c001", After: fix}}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(cals) != 1 || !cals[0].Passed || strings.Join(seen, "|") != "int bound = 4;|int bound = 5;" {
		t.Errorf("calibrations %+v, runs saw %v", cals, seen)
	}
	// The tree is as it was: the fixed file, and nothing changed in git.
	if b, _ := os.ReadFile(filepath.Join(dir, "bgpd/attr.c")); string(b) != "int bound = 5;\n" {
		t.Errorf("attr.c left as %q", b)
	}
	if st := run(t, dir, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("tree changed: %s", st)
	}
}

func TestLLVMOutcomes(t *testing.T) {
	ws := contract.Workspace{Root: t.TempDir()}
	ws.WriteHuntRecord(&contract.HuntRecord{Property: "p_llvm_m_zz_spec_SpecHeld_noub", Summary: "no UB in 2000 trials (0 stuck, 0 out of fuel)"})
	ws.WriteHuntRecord(&contract.HuntRecord{Property: "p_llvm_m_zz_spec_SpecStuck_noub", Summary: "no UB in 2000 trials (2000 stuck, 0 out of fuel)"})
	ws.WriteHuntRecord(&contract.HuntRecord{Property: "p_llvm_m_zz_spec_SpecFuzzed_noub", Summary: "no UB in 2000 trials (2000 stuck, 0 out of fuel); libfuzzer: no crash in 7408301 runs (1m0s)"})
	ws.WriteCounterexample(&contract.Counterexample{ID: "cx1", Property: "p_llvm_m_zz_spec_SpecBad_noub"})
	ws.WriteVerdict(&contract.Verdict{Counterexample: "cx1", Status: contract.VerdictConfirmed})
	got := LLVMOutcomes(ws, []string{"SpecHeld", "SpecStuck", "SpecFuzzed", "SpecBad", "SpecGone"})
	for s, want := range map[string]string{"SpecHeld": OutHeld, "SpecStuck": OutUnknown, "SpecFuzzed": OutHeld, "SpecBad": OutViolated, "SpecGone": OutNotRun} {
		if got[s].Outcome != want {
			t.Errorf("%s: %s, want %s", s, got[s].Outcome, want)
		}
	}
}

func TestCalibrateAtHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	pad := "int p1;\nint p2;\nint p3;\nint p4;\n"
	write(t, dir, "bgpd/attr.c", "int a;\n"+pad+"int bound = 4;\nint z;\n")
	write(t, dir, "bgpd/attr.h", "int h1;\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "initial")
	write(t, dir, "bgpd/attr.c", "int a;\n"+pad+"int bound = 5;\nint z;\n")
	run(t, dir, "commit", "-q", "-am", "fix")
	fix := run(t, dir, "rev-parse", "HEAD")
	// Later work changes another line of the file and the header.
	write(t, dir, "bgpd/attr.c", "int a2;\n"+pad+"int bound = 5;\nint z;\n")
	write(t, dir, "bgpd/attr.h", "int h2;\n")
	run(t, dir, "commit", "-q", "-am", "later")
	write(t, dir, "bgpd/zz_spec.c", "/* candidate c001 */\nSPEC int spec_bound(void)\n{\n\treturn 1;\n}\n")

	var seen []string
	fake := func(d, pkg, file string, specs []string) (map[string]Result, error) {
		b, _ := os.ReadFile(filepath.Join(d, "bgpd/attr.c"))
		seen = append(seen, strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(string(b)), pad, ""), "\n", " "))
		out := map[string]Result{}
		for _, s := range specs {
			out[s] = Result{Outcome: OutHeld}
			if strings.Contains(string(b), "bound = 4") {
				out[s] = Result{Outcome: OutViolated}
			}
		}
		return out, nil
	}
	cals, err := CalibrateAtHead(dir, "bgpd", filepath.Join(dir, "bgpd/zz_spec.c"), []Candidate{{ID: "c001", After: fix}}, fake)
	if err != nil {
		t.Fatal(err)
	}
	// Before: today's code with the fix reverted (a2 kept, bound back to 4).
	if len(cals) != 1 || !cals[0].Passed || strings.Join(seen, "|") != "int a2; int bound = 4; int z;|int a2; int bound = 5; int z;" {
		t.Errorf("calibrations %+v, runs saw %v", cals, seen)
	}
	if st := run(t, dir, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("tree changed: %s", st)
	}
}
