package prover

import (
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
)

func TestAxiomLine(t *testing.T) {
	out := "warning: x\n'Go2Lean.Proofs.p.holds_all' depends on axioms: [propext,\n Classical.choice,\n Lean.ofReduceBool,\n Quot.sound]\n"
	if a := axiomLine(out); a != "holds_all' depends on axioms: [propext, Classical.choice, Lean.ofReduceBool, Quot.sound]" {
		t.Errorf("got %q", a)
	}
	if a := axiomLine("'X.holds_all' does not depend on any axioms\n"); a != "none" {
		t.Errorf("got %q", a)
	}
	if a := axiomLine("error: boom"); a != "" {
		t.Errorf("got %q", a)
	}
}

func TestEligible(t *testing.T) {
	ints := &contract.ABI{Params: []contract.ABIParam{
		{Kind: contract.ABIInt, Width: 64, Model: []string{"a"}},
		{Kind: contract.ABIBool, Model: []string{"b"}},
	}}
	if !Eligible(ints) {
		t.Error("ints and bools should be eligible")
	}
	slice := &contract.ABI{Params: []contract.ABIParam{{Kind: contract.ABISlice, Model: []string{"r", "o", "l", "c"}}}}
	if Eligible(slice) {
		t.Error("a slice parameter is not eligible")
	}
	if Eligible(&contract.ABI{}) {
		t.Error("no parameters: nothing to quantify over")
	}
}

func TestClosure(t *testing.T) {
	r := &contract.ModelReport{Funcs: []contract.ModelFunc{
		{ID: 0, Calls: []int{2}}, {ID: 1}, {ID: 2, Calls: []int{0, 3}}, {ID: 3},
	}}
	got := Closure(r, 0, -1)
	if len(got) != 3 || got[0] != 0 || got[1] != 2 || got[2] != 3 {
		t.Errorf("closure %v", got)
	}
}
