package gossa

import (
	"strings"
	"testing"
)

// A t.Run subtest's closure is translated, its test calls it, and a
// suite's TestY method is a function of the model (a target under -tests)
// (#48).
func TestSubtestsAndSuites(t *testing.T) {
	progs, _, err := TranslateAllOpts("../../..", Options{Tests: true}, "./testdata/subtests")
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, p := range progs {
		for _, f := range p.Funcs {
			status[f.Name] = string(f.Status)
		}
	}
	for _, name := range []string{"TestStack", "TestStack$2", "suite.TestPopTwice"} {
		if s, ok := status[name]; !ok || s == "rejected" {
			t.Errorf("%s: status %q, want translated", name, s)
		}
	}
}

// With a subtest pattern (-run TestX/sub), the test's own t.Run calls are
// guarded by a match on the subtest's name; without one they are not
// (#48). A pattern regexp/syntax rejects is an error.
func TestSubtestSelection(t *testing.T) {
	guarded := func(opts Options) int {
		progs, _, err := TranslateAllOpts("../../..", opts, "./testdata/subtests")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, p := range progs {
			for _, f := range p.Funcs {
				if f.Name != "TestStack" {
					continue
				}
				for _, b := range f.Blocks {
					if strings.HasSuffix(b.Name, ".subtest-selected") {
						n++
					}
				}
			}
		}
		return n
	}
	if n := guarded(Options{Tests: true}); n != 0 {
		t.Errorf("no pattern: %d guarded subtests, want 0", n)
	}
	if n := guarded(Options{Tests: true, Subtest: "push"}); n == 0 {
		t.Error("pattern push: no guarded subtest")
	}
	if _, _, err := TranslateAllOpts("../../..", Options{Tests: true, Subtest: "a("}, "./testdata/subtests"); err == nil {
		t.Error("pattern a(: no error")
	}
}
