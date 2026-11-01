package gossa

import (
	"testing"

	"github.com/andreasfoo/go2lean/internal/ir"
)

// A package whose functions start goroutines and use a variable its
// initializer builds gets the initializer's slice as an Init function, run
// before the search, and loads of the variable go through its cell (#55).
func TestInitCells(t *testing.T) {
	progs, _, err := TranslateAllOpts("../../..", Options{}, "./examples/initcells")
	if err != nil {
		t.Fatal(err)
	}
	if len(progs) != 1 {
		t.Fatalf("%d programs, want 1", len(progs))
	}
	p := progs[0]
	// The variable initializers' slice, then the func init()'s, in Go's
	// order.
	if len(p.Inits) != 2 || p.Funcs[p.Inits[0]].Name != "init" || p.Funcs[p.Inits[1]].Name != "init#1" {
		t.Fatalf("Inits %v, want init then init#1", p.Inits)
	}
	init := p.Funcs[p.Inits[0]]
	if !init.Init || init.Status != "complete" {
		t.Fatalf("init function: Init %v, status %s, issues %v", init.Init, init.Status, init.Issues)
	}
	if !p.Funcs[p.Inits[1]].Init {
		t.Fatalf("init#1 is not an Init function")
	}
	cells := func(f *ir.Func) int {
		n := 0
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if _, ok := in.Op.(ir.GlobalAddr); ok {
					n++
				}
			}
		}
		return n
	}
	if cells(init) == 0 {
		t.Errorf("the initializer's slice stores no cell")
	}
	for _, f := range p.Funcs {
		if f.Name == "RegisterRacy" && cells(f) == 0 {
			t.Errorf("RegisterRacy loads defaultReg without its cell")
		}
	}
}
