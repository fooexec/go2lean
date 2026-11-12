package builtin

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/frontend/gossa"
	"github.com/fooexec/go2lean/internal/ir"
)

func testBinding(t *testing.T) *Binding {
	t.Helper()
	b, err := BindCompareOrder(fixture(t, ordered), "Compare", nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBindingValidation(t *testing.T) {
	valid := testBinding(t)
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Binding)
	}{
		{"version", func(b *Binding) { b.Version = "future" }},
		{"stage", func(b *Binding) { b.Stage = "passed" }},
		{"domain", func(b *Binding) { b.Domain = "only valid versions" }},
		{"law", func(b *Binding) { b.Law = "always true" }},
		{"assumptions", func(b *Binding) { b.Assumptions = nil }},
		{"source hash", func(b *Binding) { b.SHA256 = "incorrect" }},
		{"relative file", func(b *Binding) { b.File = CompareOrderFile }},
		{"unrelated filename", func(b *Binding) { b.File = filepath.Join(filepath.Dir(b.File), "target.go") }},
		{"package binding", func(b *Binding) { b.Package = "example.com/other" }},
		{"function binding", func(b *Binding) { b.Function = "SomethingElse" }},
		{"arbitrary rehashed source", func(b *Binding) {
			b.Source = strings.Replace(b.Source, "return reflexive && reversed && transitive", "return true", 1)
			b.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(b.Source)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := *valid
			b.Assumptions = slices.Clone(valid.Assumptions)
			tc.edit(&b)
			if err := b.Validate(); err == nil {
				t.Fatal("inconsistent binding accepted")
			}
		})
	}
	var absent *Binding
	if err := absent.Validate(); err == nil {
		t.Fatal("nil binding accepted")
	}
}

// Minimal IR fixtures isolate the static gate; they are not model/Go parity
// evidence or a claim that arbitrary supplied IR corresponds to source.
func capabilityPrograms(b *Binding) []*ir.Prog {
	leaf := func(name string) *ir.Func {
		return &ir.Func{Name: name, Status: contract.ModelComplete,
			Blocks: []ir.Block{{Term: ir.Term{Kind: ir.TermRet}}}}
	}
	caller := func(name string, callee int) *ir.Func {
		return &ir.Func{Name: name, Status: contract.ModelComplete, Blocks: []ir.Block{
			{Term: ir.Term{Kind: ir.TermCall, Callee: callee, Next: 1, Fallback: 2}},
			{Term: ir.Term{Kind: ir.TermRet}},
			{Instrs: []ir.Instr{{HavocSite: 0}}, Term: ir.Term{Kind: ir.TermRet}},
		}}
	}
	oracle := leaf(b.Oracle)
	oracle.SpecFor = b.Target
	return []*ir.Prog{{Package: b.Package, Frontend: "gossa", Funcs: []*ir.Func{
		caller(b.Target, 2), oracle, caller(b.Function, 3), leaf("helper"),
	}}}
}

func hasIssue(c *Capability, code string) bool {
	return slices.ContainsFunc(c.Issues, func(i CapabilityIssue) bool { return i.Code == code })
}

type futureOp struct{}

func (futureOp) Lean() string { return ".futureOperation" }

func TestCapabilityRejectsIncompleteRequiredModels(t *testing.T) {
	b := testBinding(t)
	for _, tc := range []struct {
		name, code string
		edit       func([]*ir.Prog) []*ir.Prog
	}{
		{"partial callee", "incomplete-model", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[3].Status = contract.ModelPartial
			return ps
		}},
		{"rejected callee", "incomplete-model", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[3].Status = contract.ModelRejected
			return ps
		}},
		{"issue on complete callee", "reported-model-issue", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[3].Issues = []string{"ignored effect"}
			return ps
		}},
		{"missing callee", "missing-callee", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[2].Blocks[0].Term.Callee = 500
			return ps
		}},
		{"missing dependency", "missing-callee", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[2].Blocks[0].Term.Kind = ir.TermXCall
			ps[0].Funcs[2].Blocks[0].Term.CalleePkg = "example.com/missing"
			return ps
		}},
		{"missing root", "missing-root", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[1].Name = "differentOracle"
			return ps
		}},
		{"wrong oracle target", "oracle-target-mismatch", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[1].SpecFor = "unrelated"
			return ps
		}},
		{"duplicate root", "ambiguous-function", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs = append(ps[0].Funcs, ps[0].Funcs[1])
			return ps
		}},
		{"wrong frontend", "unsupported-frontend", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Frontend = "llvm"
			return ps
		}},
		{"missing block", "invalid-control-flow", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[3].Entry = 3
			return ps
		}},
		{"missing fallback", "invalid-fallback", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[0].Blocks[0].Term.Fallback = -1
			return ps
		}},
		{"missing terminator", "invalid-terminator", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[3].Blocks[0].Term.Kind = ir.TermNone
			return ps
		}},
		{"dynamic call", "dynamic-call", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[2].Blocks[0].Term.Kind = ir.TermCallV
			return ps
		}},
		{"goroutine", "concurrency", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[2].Blocks[0].Term.Kind = ir.TermSpawn
			return ps
		}},
		{"synchronisation", "concurrency", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[3].Sync = true
			return ps
		}},
		{"daemon", "concurrency", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[3].Daemon = true
			return ps
		}},
		{"nil program", "invalid-program", func(ps []*ir.Prog) []*ir.Prog { return append(ps, nil) }},
		{"duplicate package", "ambiguous-program", func(ps []*ir.Prog) []*ir.Prog { return append(ps, ps[0]) }},
		{"nil function", "missing-callee", func(ps []*ir.Prog) []*ir.Prog {
			ps[0].Funcs[3] = nil
			return ps
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := AssessCompareOrder(b, tc.edit(capabilityPrograms(b)))
			if c.Status != "unsupported" || !hasIssue(c, tc.code) {
				t.Fatalf("wanted %s; got %+v", tc.code, c)
			}
		})
	}
}

func TestCapabilityRejectsUnknownInstructions(t *testing.T) {
	b := testBinding(t)
	for _, tc := range []struct {
		name, code string
		in         ir.Instr
	}{
		{"havoc", "havoc", ir.Instr{HavocSite: 0}},
		{"assumption", "assumption", ir.Instr{Assume: "assumed zero"}},
		{"raw term", "unsupported-operation", ir.Instr{Op: ir.Raw("(.const .unit)")}},
		{"random read", "unsupported-operation", ir.Instr{Op: ir.Simple("randRead")}},
		{"random array", "unsupported-operation", ir.Instr{Op: ir.FreshArr{N: 16}}},
		{"global address", "unsupported-operation", ir.Instr{Op: ir.GlobalAddr{}}},
		{"concurrent op", "unsupported-operation", ir.Instr{Op: ir.Conc("ctxBackground")}},
		{"channel", "unsupported-operation", ir.Instr{Op: ir.ChanMake{}}},
		{"unknown simple op", "unsupported-operation", ir.Instr{Op: ir.Simple("newUnreviewedOp")}},
		{"unknown typed op", "unsupported-operation", ir.Instr{Op: futureOp{}}},
		{"malformed probe", "invalid-probe", ir.Instr{Probe: "value", Op: ir.Simple("randRead")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := capabilityPrograms(b)
			ps[0].Funcs[3].Blocks[0].Instrs = []ir.Instr{tc.in}
			c := AssessCompareOrder(b, ps)
			if c.Status != "unsupported" || !hasIssue(c, tc.code) {
				t.Fatalf("wanted %s; got %+v", tc.code, c)
			}
		})
	}
}

func TestCapabilityOnStatefulCompilerOutput(t *testing.T) {
	dir := fixture(t, `var calls int
	func Compare(a,b string) int { calls++; return calls }`)
	b, err := BindCompareOrder(dir, "Compare", nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	ps, _, err := gossa.TranslateAllOpts(dir, gossa.Options{Overlay: b.Overlay()}, ".")
	if err != nil {
		t.Fatal(err)
	}
	c := AssessCompareOrder(b, ps)
	// The counter is a cell (every package's variables are, #176), whose
	// address the capability does not accept; before, its write was an
	// ignored effect the model reported. Either way it is rejected.
	if c.Status != "unsupported" || !(hasIssue(c, "reported-model-issue") || hasIssue(c, "unsupported-operation")) {
		t.Fatalf("ignored shared-state effect accepted: %+v", c)
	}
}

func TestAssessmentWriterRejectsInconsistentEvidence(t *testing.T) {
	b := testBinding(t)
	w := contract.Workspace{Root: t.TempDir()}
	valid := AssessCompareOrder(b, capabilityPrograms(b))
	for _, tc := range []struct {
		name string
		edit func(*Capability)
	}{
		{"hash", func(c *Capability) { c.SourceSHA256 = "other" }},
		{"builtin", func(c *Capability) { c.Builtin = "other" }},
		{"passed status", func(c *Capability) { c.Status = "passed" }},
		{"unsupported without reason", func(c *Capability) { c.Status = "unsupported" }},
		{"eligible with issues", func(c *Capability) { c.Issues = []CapabilityIssue{{Code: "missing-callee"}} }},
		{"eligible without functions", func(c *Capability) { c.Functions = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := *valid
			tc.edit(&c)
			if _, err := WriteAssessment(w, b, &c); err == nil {
				t.Fatal("inconsistent diagnostic persisted")
			}
		})
	}
	if _, err := WriteAssessment(w, b, nil); err == nil {
		t.Fatal("nil diagnostic persisted")
	}
}

func TestCapabilityNormalEdgesAndRecursiveCalls(t *testing.T) {
	b := testBinding(t)
	ps := capabilityPrograms(b)
	c := AssessCompareOrder(b, ps)
	if c.Status != "static-model-eligible" || len(c.Functions) != 4 || c.CallFallbacks != 2 {
		t.Fatalf("complete call graph rejected: %+v", c)
	}
	// An unrelated partial function does not disqualify the selected call tree.
	ps[0].Funcs = append(ps[0].Funcs, &ir.Func{Name: "unrelated", Status: contract.ModelPartial})
	// Recursive direct calls are finite graph traversal, not a totality proof.
	ps[0].Funcs[3].Blocks = slices.Clone(ps[0].Funcs[2].Blocks)
	c = AssessCompareOrder(b, ps)
	if c.Status != "static-model-eligible" || len(c.Functions) != 4 {
		t.Fatalf("recursion/unrelated function handling: %+v", c)
	}
	// A fallback reached by an ordinary edge must NOT be ignored.
	ps[0].Funcs[0].Blocks[0].Term = ir.Term{Kind: ir.TermBr, T: 1, F: 2}
	c = AssessCompareOrder(b, ps)
	if !hasIssue(c, "havoc") {
		t.Fatal("ordinary control-flow edge to havoc ignored")
	}
	if c := AssessCompareOrder(nil, ps); !hasIssue(c, "invalid-binding") {
		t.Fatal("missing binding accepted")
	}
	if c := AssessCompareOrder(b, nil); !hasIssue(c, "missing-package") {
		t.Fatal("missing package accepted")
	}
}

func TestCapabilityOnGeneratedCompilerOutput(t *testing.T) {
	dir := fixture(t, "import \"example.com/orderfixture/helper\"\nfunc Compare(a,b string) int { return helper.Cmp(a,b) }")
	helper := filepath.Join(dir, "helper")
	if err := os.Mkdir(helper, 0o700); err != nil {
		t.Fatal(err)
	}
	code := "package helper\n" + strings.Replace(ordered, "Compare", "Cmp", 1)
	if err := os.WriteFile(filepath.Join(helper, "helper.go"), []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := BindCompareOrder(dir, "Compare", nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	ps, _, err := gossa.TranslateAllOpts(dir, gossa.Options{Overlay: b.Overlay(), Deps: true}, ".")
	if err != nil {
		t.Fatal(err)
	}
	c := AssessCompareOrder(b, ps)
	if c.Status != "static-model-eligible" {
		t.Fatalf("generated compiler output rejected: %+v", c)
	}
	if !slices.ContainsFunc(c.Functions, func(f FunctionRef) bool {
		return f.Package == "example.com/orderfixture/helper" && f.Name == "Cmp"
	}) {
		t.Fatalf("actual cross-package callee not inspected: %+v", c)
	}
	withoutHelper := slices.DeleteFunc(slices.Clone(ps), func(p *ir.Prog) bool {
		return p.Package == "example.com/orderfixture/helper"
	})
	c = AssessCompareOrder(b, withoutHelper)
	if c.Status != "unsupported" || !hasIssue(c, "missing-callee") {
		t.Fatal("missing real dependency model accepted")
	}
	// Keep source present but make the dependency model partial.
	for _, p := range ps {
		if p.Package == "example.com/orderfixture/helper" {
			p.Funcs[p.FuncID("Cmp")].Status = contract.ModelPartial
		}
	}
	c = AssessCompareOrder(b, ps)
	if c.Status != "unsupported" || !hasIssue(c, "incomplete-model") {
		t.Fatal("partial real dependency model accepted")
	}
}
