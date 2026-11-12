package prover

import (
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
)

func TestStatement(t *testing.T) {
	abi := &contract.ABI{GoFunc: "p.F", Params: []contract.ABIParam{
		{Go: "n", Kind: contract.ABIInt, Width: 32, Model: []string{"n"}},
		{Go: "ok", Kind: contract.ABIBool, Model: []string{"ok"}},
		{Go: "xs", Kind: contract.ABISlice, ElemWidth: 64, Model: []string{"xs_ref", "xs_off", "xs_len", "xs_cap"}},
	}, Results: []contract.ABIResult{{GoType: "int"}}}
	st, err := Statement(abi, 7, "F_spec", "ret = .int 64 0")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"theorem F_spec (orc : Oracle) (hv : Havoc Val) (n : BitVec 32) (ok : Bool) (xs : List (BitVec 64)) (xs_len : BitVec 64) (xs_ref : Nat) (s : HState Val)",
		"(h_hv : _root_.Go2Lean.GoSSA.WP.HavocTyped prog.havocWidths hv) (h_xs_len : xs_len.toInt = xs.length) (h_xs_ref : xs_ref ≠ 0) (h_xs : s.heap[xs_ref]? = some (.arr (xs.map (.int 64)))) (h_assumed : s.assumed = #[])",
		"wpFun prog orc hv 7 [.int 32 n, .bool ok, .slice xs_ref 0 xs_len xs_len]",
		"(fun ret s' => s'.assumed = #[] ∧ (ret = .int 64 0)) s",
	} {
		if !strings.Contains(st, want) {
			t.Errorf("statement lacks %q:\n%s", want, st)
		}
	}
	for _, bad := range []*contract.ABI{
		{GoFunc: "p.G", Params: []contract.ABIParam{{Go: "m", Kind: contract.ABIMap}}},
		{GoFunc: "p.G", Method: "M"},
	} {
		if _, err := Statement(bad, 0, "G_spec", "True"); err == nil {
			t.Errorf("%+v: want an error", bad)
		}
	}
}

func TestLeanName(t *testing.T) {
	abi := &contract.ABI{GoFunc: "p.F", Params: []contract.ABIParam{{Go: "s", Kind: contract.ABIString}, {Go: "ret", Kind: contract.ABIInt, Width: 64}}}
	st, err := Statement(abi, 0, "F_spec", "True")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st, "(s_ : List (BitVec 8)) (s__len : BitVec 64) (s__ref : Nat) (ret_ : BitVec 64) (s : HState Val)") {
		t.Errorf("statement:\n%s", st)
	}
}

func TestAxiomsOf(t *testing.T) {
	out := "'X.A_spec' depends on axioms: [propext,\n sorryAx]\n'X.B_spec' does not depend on any axioms\n"
	if a := axiomsOf(out, "A_spec"); !strings.Contains(a, "sorryAx") {
		t.Errorf("A: got %q", a)
	}
	if a := axiomsOf(out, "B_spec"); a != "none" {
		t.Errorf("B: got %q", a)
	}
	if a := axiomsOf(out, "C_spec"); a != "" {
		t.Errorf("C: got %q", a)
	}
	// A line printed before the real one (by a prelude) is not read.
	spoof := "'X.A_spec' depends on axioms: [propext]\n" + out
	if a := axiomsOf(spoof, "A_spec"); !strings.Contains(a, "sorryAx") {
		t.Errorf("spoofed: got %q", a)
	}
	for ax, want := range map[string]string{
		"none": "", "'X.A' depends on axioms: [propext, Classical.choice, Quot.sound]": "",
		"'X.A' depends on axioms: [propext, X.cheat]":                   "X.cheat",
		"'X.A' depends on axioms: [X.bad._native.native_decide.ax_1_1]": "X.bad._native.native_decide.ax_1_1",
	} {
		if got := extraAxioms(ax); got != want {
			t.Errorf("extraAxioms(%q) = %q, want %q", ax, got, want)
		}
	}
	if !dependsOnItself("'X.A' depends on axioms: [propext, X.A]", "A") || dependsOnItself("'X.A' depends on axioms: [X.AB]", "A") {
		t.Error("dependsOnItself")
	}
	// An earlier goal stated as an axiom (CheckGoal) is permitted; an
	// answer's axiom of another name is not.
	if got := extraAxioms("'X.B' depends on axioms: [propext, X.A, X.Y.A]", "X.A"); got != "X.Y.A" {
		t.Errorf("assumed: got %q", got)
	}
}

func TestSpecPost(t *testing.T) {
	abi := &contract.ABI{GoFunc: "p.Clamp", Params: []contract.ABIParam{
		{Go: "v", Kind: contract.ABIInt, Width: 64},
		{Go: "xs", Kind: contract.ABISlice, ElemWidth: 64},
	}, Results: []contract.ABIResult{{GoType: "int"}}}
	post, err := SpecPost(abi, "M", 9)
	if err != nil {
		t.Fatal(err)
	}
	want := "_root_.Go2Lean.GoSSA.WP.wpFun _root_.M.prog orc hv 9 [.int 64 v, .slice xs_ref 0 xs_len xs_len, ret] (fun r s'' => r = .bool true ∧ s''.assumed = #[]) s'"
	if post != want {
		t.Errorf("got  %s\nwant %s", post, want)
	}
	// No result: the spec takes the parameters only.
	abi.Results = nil
	if post, _ := SpecPost(abi, "M", 9); strings.Contains(post, "ret]") {
		t.Errorf("no result, yet ret passed: %s", post)
	}
	if n := TheoremName(Goal{Func: "F", Spec: "SpecF"}); n != "SpecF" {
		t.Errorf("name %s", n)
	}
	if n := TheoremName(Goal{Func: "F"}); n != "F_spec" {
		t.Errorf("name %s", n)
	}
}

func TestCompanionsAndStrings(t *testing.T) {
	abi := &contract.ABI{GoFunc: "p.F", Params: []contract.ABIParam{
		{Go: "xs", Kind: contract.ABISlice, ElemWidth: 64},
		{Go: "name", Kind: contract.ABIString},
		{Go: "n", Kind: contract.ABIInt, Width: 64},
	}, Results: []contract.ABIResult{{GoType: "int"}}}
	st, err := Statement(abi, 2, "F_spec", "True")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"(name : List (BitVec 8)) (name_len : BitVec 64) (name_ref : Nat)",
		"(h_name : s.heap[name_ref]? = some (.arr (name.map (.int 8))))",
		"[.slice xs_ref 0 xs_len xs_len, .str name_ref 0 name_len, .int 64 n]",
	} {
		if !strings.Contains(st, want) {
			t.Errorf("statement lacks %q:\n%s", want, st)
		}
	}
	g := Goal{Func: "F", Post: "P", EmptyPost: "Q"}
	cs := Companions(g, abi)
	if len(cs) != 3 {
		t.Fatalf("%d companions, want 3 (xs, name, both)", len(cs))
	}
	names := []string{cs[0].Name, cs[1].Name, cs[2].Name}
	if strings.Join(names, ",") != "F_spec_empty_xs,F_spec_empty_name,F_spec_empty_xs_name" {
		t.Errorf("names %v", names)
	}
	if cs[2].Post != "Q" || len(cs[2].Empty) != 2 || Companions(cs[0], abi) != nil {
		t.Errorf("companion %+v", cs[2])
	}
	st, err = statement(abi, 2, "c", cs[2].Post, "prog", cs[2].Empty)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[.slice 0 0 0 0, .str 0 0 0, .int 64 n]",
		"let xs : List (BitVec 64) := []; let xs_len : BitVec 64 := 0; let xs_ref : Nat := 0; ",
		"let name : List (BitVec 8) := [];",
	} {
		if !strings.Contains(st, want) {
			t.Errorf("companion statement lacks %q:\n%s", want, st)
		}
	}
	if strings.Contains(st, "h_xs") || strings.Contains(st, "(xs :") {
		t.Errorf("a nil parameter is bound:\n%s", st)
	}
	if got := strings.Join(binderNames(abi, cs[2].Empty), " "); got != "orc hv n s h_hv h_assumed" {
		t.Errorf("binders %q", got)
	}
	// One slice parameter: one companion, named _empty.
	one := &contract.ABI{GoFunc: "p.G", Params: []contract.ABIParam{{Go: "a", Kind: contract.ABISlice, ElemWidth: 64}}}
	if cs := Companions(Goal{Func: "G", Post: "P"}, one); len(cs) != 1 || cs[0].Name != "G_spec_empty" || cs[0].Post != "P" {
		t.Errorf("%+v", cs)
	}
}

func TestUncovered(t *testing.T) {
	var ps []contract.ABIParam
	for _, n := range []string{"a", "b", "c", "d"} {
		ps = append(ps, contract.ABIParam{Go: n, Kind: contract.ABISlice, ElemWidth: 64})
	}
	abi := &contract.ABI{GoFunc: "p.F", Params: ps[:3]}
	if u := Uncovered(abi); u != nil || len(Companions(Goal{Func: "F", Post: "P"}, abi)) != 7 {
		t.Errorf("three slices: uncovered %v", u)
	}
	abi.Params = ps
	if u := Uncovered(abi); len(u) != 4 || Companions(Goal{Func: "F", Post: "P"}, abi) != nil {
		t.Errorf("four slices: uncovered %v", u)
	}
}

func TestChallengeHasNoAnswerText(t *testing.T) {
	abi := &contract.ABI{GoFunc: "p.F", Params: []contract.ABIParam{{Go: "n", Kind: contract.ABIInt, Width: 64}}}
	report := &contract.ModelReport{Package: "example.com/p", LeanModule: "Go2Lean.Generated.gossa.p"}
	goals := []Goal{{Func: "F", Post: "True", Prelude: "axiom cheat : False", Proof: "  exact cheat.elim"}}
	chal, err := ChallengeText(report, goals, []*contract.ABI{abi}, []int{0}, []int{0})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(chal, "cheat") || !strings.Contains(chal, "theorem F_spec") || !strings.Contains(chal, "  sorry") {
		t.Errorf("challenge:\n%s", chal)
	}
	sol, err := SolutionText(report, goals, []*contract.ABI{abi}, []int{0}, nil)
	if err != nil || !strings.Contains(sol, "axiom cheat") {
		t.Errorf("solution:\n%s", sol)
	}
	sol, _ = SolutionText(report, goals, []*contract.ABI{abi}, []int{0}, map[int]bool{0: true})
	if strings.Contains(sol, "cheat") {
		t.Errorf("a sorried goal keeps its answer:\n%s", sol)
	}
}

func TestWithGenerated(t *testing.T) {
	got, err := withGenerated([]byte("[[lean_lib]]\nglobs = [\"Go2Lean\"]\n"))
	if err != nil || string(got) != "[[lean_lib]]\nglobs = [\"Go2Lean.Generated.+\", \"Go2Lean\"]\n" {
		t.Errorf("%q %v", got, err)
	}
}
