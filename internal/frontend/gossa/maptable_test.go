package gossa

import (
	"strings"
	"testing"
)

// A package-level map literal keyed by arrays or structs of integer and
// bool constants is a lookup table the model builds at each load; one with
// a string inside a key is built by the package initializer, which runs in
// the model too (#176): no table is an unmodelled load (#46, c022).
func TestMapTableCompositeKeys(t *testing.T) {
	progs, _, err := TranslateAllOpts("../../..", Options{}, "./testdata/maptable")
	if err != nil {
		t.Fatal(err)
	}
	if len(progs) != 1 {
		t.Fatalf("%d programs, want 1", len(progs))
	}
	unmodelled := map[string]bool{}
	for _, f := range progs[0].Funcs {
		for _, is := range f.Issues {
			if strings.Contains(is, "unmodeled-load") {
				unmodelled[f.Name] = true
			}
		}
	}
	for name, want := range map[string]bool{"Tag": false, "Cell": false, "Named": false} {
		if unmodelled[name] != want {
			t.Errorf("%s: unmodelled table load %v, want %v", name, unmodelled[name], want)
		}
	}
}
