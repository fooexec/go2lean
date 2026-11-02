package gossa

import "testing"

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
