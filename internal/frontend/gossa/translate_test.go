package gossa

import (
	"slices"
	"testing"
)

// -deps translates a dependency up to four calls deep; Options.Translate
// adds a function past that depth, and only that function (#85).
func TestTranslateForced(t *testing.T) {
	const deep = "github.com/andreasfoo/go2lean/testdata/refinedeep"
	funcs := func(opts Options) []string {
		progs, _, err := TranslateAllOpts("../../..", opts, "./examples/refine")
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, p := range progs {
			if p.Package == deep {
				for _, f := range p.Funcs {
					names = append(names, f.Name)
				}
			}
		}
		slices.Sort(names)
		return names
	}
	got := funcs(Options{Deps: true})
	if want := []string{"Level1", "Level2", "Level3", "Level4"}; !slices.Equal(got, want) {
		t.Errorf("-deps: %v, want %v", got, want)
	}
	got = funcs(Options{Deps: true, Translate: []string{deep + ".Level5"}})
	if want := []string{"Level1", "Level2", "Level3", "Level4", "Level5"}; !slices.Equal(got, want) {
		t.Errorf("-deps with Level5 forced: %v, want %v", got, want)
	}
}
