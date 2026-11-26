package agent

import (
	"testing"

	"github.com/fooexec/go2lean/internal/constraint"
)

func TestCSearchOptions(t *testing.T) {
	d := cSearchOptions(Options{})
	if d.Trials != 2000 || d.Seed != 42 || d.Engine != "all" || d.ExhaustLen != 3 || d.Fuel != 20000 {
		t.Fatalf("defaults changed: %+v", d)
	}
	o := cSearchOptions(Options{Search: constraint.SearchOptions{Trials: 20, Seed: 7, Engine: constraint.EngineRandom, ExhaustLen: 2}})
	if o.Trials != 20 || o.Seed != 7 || o.Engine != "random" || o.ExhaustLen != 2 {
		t.Fatalf("agent flags not passed to the C search: %+v", o)
	}
	if o.Fuel != 20000 || !o.SpecsOnly || o.Init != -1 {
		t.Fatalf("C line constants lost: %+v", o)
	}
}
