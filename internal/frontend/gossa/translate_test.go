package gossa

import (
	"slices"
	"strings"
	"testing"
)

// -deps translates a dependency up to four calls deep; Options.Translate
// adds a function past that depth, with its callees two calls deep (#85).
func TestTranslateForced(t *testing.T) {
	const deep = "github.com/fooexec/go2lean/testdata/refinedeep"
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
	if want := []string{"Level1", "Level2", "Level3", "Level4", "Level5", "Level6", "Level7"}; !slices.Equal(got, want) {
		t.Errorf("-deps with Level5 forced: %v, want %v", got, want)
	}
}

// The model is translated with purego added to the tags GOFLAGS sets.
func TestModelTags(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "purego,math_big_pure_go"},
		{"-mod=mod", "purego,math_big_pure_go"},
		{"-tags=netgo,osusergo -mod=mod", "netgo,osusergo,purego,math_big_pure_go"},
		{"-tags=purego", "purego,math_big_pure_go"},
	} {
		if got := strings.Join(modelTags(c.in), ","); got != c.want {
			t.Errorf("modelTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// `a & ^b` is one andNot, with no bnot; a `^b` with another use stays a bnot.
func TestAndNotFold(t *testing.T) {
	progs, _, err := TranslateAllOpts("../../..", Options{}, "./testdata/andnotfold")
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string][]string{}
	for _, p := range progs {
		for _, f := range p.Funcs {
			for _, b := range f.Blocks {
				for _, in := range b.Instrs {
					if in.Op != nil {
						ops[f.Name] = append(ops[f.Name], in.Op.Lean())
					}
				}
			}
		}
	}
	has := func(fn, op string) bool { return slices.Contains(ops[fn], op) }
	for _, fn := range []string{"Fold", "FoldLeft"} {
		if !has(fn, ".andNot") || has(fn, ".bnot") || has(fn, ".and") {
			t.Errorf("%s: ops %v, want one andNot and no bnot or and", fn, ops[fn])
		}
	}
	if !has("Shared", ".bnot") || !has("Shared", ".and") || has("Shared", ".andNot") {
		t.Errorf("Shared: ops %v, want bnot and and", ops["Shared"])
	}
	if !has("Plain", ".and") || has("Plain", ".andNot") {
		t.Errorf("Plain: ops %v, want and", ops["Plain"])
	}
}
