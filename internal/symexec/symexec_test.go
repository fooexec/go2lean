package symexec

import (
	"os/exec"
	"testing"
)

// The twelve functions of testdata/symexecbench panic only on inputs that a
// random or coverage-guided search reaches with difficulty (a hash that must
// match, two linear equations, a checksum). Each one must give a finding
// that the real code reproduces.
func TestBench(t *testing.T) {
	if _, err := exec.LookPath("z3"); err != nil {
		t.Skip("z3 not installed")
	}
	pkg, err := Load("../../testdata/symexecbench", ".")
	if err != nil {
		t.Fatal(err)
	}
	var all []Finding
	got := map[string]bool{}
	for _, fn := range pkg.Functions() {
		fs, err := Explore(fn, Options{MaxLen: 24, Root: pkg.Root()})
		if err != nil {
			t.Fatalf("%s: %v", fn.Name(), err)
		}
		for _, f := range fs {
			all = append(all, f)
			got[f.Func] = true
		}
	}
	ok, msgs := pkg.Replay(all)
	confirmed := map[string]bool{}
	for i, f := range all {
		if ok[i] {
			confirmed[f.Func] = true
		} else {
			t.Logf("%s: %s not reproduced: %s", f.Func, f.Kind, msgs[i])
		}
	}
	for _, n := range []string{"B01", "B02", "B03", "B04", "B05", "B06", "B07", "B08", "B09", "B10", "B11", "B12"} {
		if !confirmed[n] {
			t.Errorf("%s: no reproduced panic", n)
		}
	}
}
