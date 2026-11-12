package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/prover"
	"github.com/fooexec/go2lean/internal/sandbox"
)

func TestParseProof(t *testing.T) {
	resp := "Here it is.\n\n```lean prelude\ndef inv (n : Nat) : Prop := n = n\n```\n\nand\n\n```lean proof\napply foo\nexact bar\n```\n"
	prelude, proof, err := parseProof(resp)
	if err != nil {
		t.Fatal(err)
	}
	if prelude != "def inv (n : Nat) : Prop := n = n" {
		t.Errorf("prelude %q", prelude)
	}
	if proof != "  apply foo\n  exact bar" {
		t.Errorf("proof %q", proof)
	}
	// Already indented: kept as is.
	_, proof, _ = parseProof("```lean proof\n  intro x\n    simp\n```")
	if proof != "  intro x\n    simp" {
		t.Errorf("indented proof %q", proof)
	}
	if _, _, err := parseProof("```lean\nexact foo\n```"); err == nil {
		t.Error("an answer without a proof block should be refused")
	}
}

func TestVetProof(t *testing.T) {
	for _, bad := range []string{
		"exact sorry",
		"axiom cheat : False",
		"by native_decide",
		"local notation \"wpFun\" => True",
		"macro \"x\" : tactic => `(tactic| rfl)",
		"end Go2Lean.Proofs.Functional.p",
		"namespace Other",
		"theorem t : _root_.Foo := x",
		"@[simp] local notation \"x\" => 1",
		"open Foo renaming bar → baz",
		"  exact (sorry : False)",
	} {
		if err := vetProof(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	// Words in comments and names are not commands.
	for _, fine := range []string{
		"-- the sorted prefix grows\ntheorem sorted_prefix : True := trivial",
		"/- uses sorry nowhere: an axiom-free proof -/\nexact trivial",
		"theorem t : True := by\n  simp [List.isPrefixOf, prefix_eq]",
	} {
		if err := vetProof(fine); err != nil {
			t.Errorf("%q: %v", fine, err)
		}
	}
	ok := "def countInv (env : Env Val) : Prop := True\ntheorem l : True := trivial\n  apply wpPt_loop prog_f1 inv\n  · intro n env s hI ih\n    simp [List.isPrefixOf]"
	if err := vetProof(ok); err != nil {
		t.Errorf("%q: %v", ok, err)
	}
	if !strings.Contains(proveSystem, "```lean proof") {
		t.Error("the system prompt should show the answer format")
	}
}

func TestDeclared(t *testing.T) {
	goals := []prover.Goal{
		{Prelude: "def inv (x : Nat) : Prop := True\n@[simp] theorem inv_ok : True := trivial\nabbrev post := 1"},
		{Prelude: "def mine := 2"},
	}
	got := strings.Join(declared(goals, 1), ",")
	if got != "inv,inv_ok,post" {
		t.Errorf("got %s", got)
	}
}

func TestLoopPaths(t *testing.T) {
	// CountPositive: 0 → 1 (header); 1 → 2 (body) | 3 (done); 2 → 4 | 1; 4 → 1.
	f := contract.ModelFunc{ID: 1, Name: "CountPositive", LoopHeaders: []int{1},
		Succs: [][]int{{1}, {2, 3}, {4, 1}, nil, {1}}}
	got := LoopPaths(f)
	for _, want := range []string{
		"1 → 2 → 4 → 1  (back edge",
		"1 → 2 → 1  (back edge",
		"1 → 3  (returns or panics)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if LoopPaths(contract.ModelFunc{}) != "" {
		t.Error("a function without loops has no paths")
	}
}

func TestWithLeanTools(t *testing.T) {
	cfg := t.TempDir() + "/mcp.json"
	src := t.TempDir()
	os.MkdirAll(src+"/Proofs", 0o755)
	os.WriteFile(src+"/lakefile.toml", []byte("x"), 0o644)
	os.WriteFile(src+"/Proofs/P.lean", []byte("x"), 0o644)
	llm, err := WithLeanTools(&ClaudeCode{Effort: "high"}, "/bin/lean-lsp-mcp", src, cfg)
	if errors.Is(err, sandbox.ErrNoBwrap) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	// The tools get a copy of the project, without its proof files, and
	// run confined.
	work := filepath.Dir(cfg) + "/lean-tools"
	if _, err := os.Stat(work + "/lakefile.toml"); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(work + "/Proofs"); err == nil {
		t.Error("the copy has the proof files")
	}
	cc := llm.(*ClaudeCode)
	if cc.MCPConfig != cfg || cc.Effort != "high" || cc.MaxTurns == 0 {
		t.Errorf("%+v", cc)
	}
	for _, tool := range cc.AllowedTools {
		if !strings.HasPrefix(tool, "mcp__lean__lean_") || strings.Contains(tool, "search") && tool != "mcp__lean__lean_local_search" {
			t.Errorf("tool %s", tool)
		}
	}
	b, err := os.ReadFile(cfg)
	if err != nil || !strings.Contains(string(b), work) || strings.Contains(string(b), src) || !strings.Contains(string(b), "lean_loogle") ||
		!sandbox.Unconfined() && !strings.Contains(string(b), `"command": "bwrap"`) {
		t.Errorf("config %s (%v)", b, err)
	}
	// Other LLMs are left as they are.
	m := &Mock{Path: "x"}
	if got, _ := WithLeanTools(m, "b", "d", cfg); got != m {
		t.Error("a mock should not change")
	}
}

func TestModelExcerpt(t *testing.T) {
	model := `import Go2Lean.Dialect.GoSSA

namespace M

/-- f -/
def f0 : GoSSA.Func := {
  term := .call 2 [] 1 1 [] 1 }

/-- g -/
def f1 : GoSSA.Func := {
  term := .ret 0 }

/-- h -/
def f2 : GoSSA.Func := {
  instrs := #[.op 3 (.funcRef 3) []] }

/-- k -/
def f3 : GoSSA.Func := {
  term := .ret 0 }

def prog : GoSSA.Prog := { funcs := #[f0, f1, f2, f3] }

end M`
	got := ModelExcerpt(model, 0)
	for _, want := range []string{"namespace M", "def f0 ", "def f2 ", "def f3 ", "def prog ", "end M", "1 functions f0 does not reach"} {
		if !strings.Contains(got, want) {
			t.Errorf("excerpt lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "def f1 ") || strings.Contains(got, "/-- g -/") {
		t.Errorf("excerpt keeps f1:\n%s", got)
	}
}
