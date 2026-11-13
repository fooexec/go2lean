package gossa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/ir"
)

// The results of unmodelled calls stay within their callee's documented
// contract (#85): a havoc count outside it made evaluation 7's spurious
// counterexamples (bufio's "negative read" on k045, a constant global read
// as zero bytes on k038).
func TestContractHavoc(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"m.go": `package m

import (
	"hash"
	"io"
	"math/rand"
	"reflect"
	"runtime"
)

func NF(t reflect.Type) int { return t.NumField() }

func ReadN(rd io.Reader, p []byte) int { n, _ := rd.Read(p); return n }

func HashW(h hash.Hash64, p []byte) error { _, err := h.Write(p); return err }

type lener interface{ Len() int }

func L(l lener) int { return l.Len() }

func C(pc []uintptr) int { return runtime.Callers(0, pc) }

func P(r *rand.Rand, n int) []int { return r.Perm(n) }

var start = []byte("‹")

func S() int { return len(start) }
`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, _, err := Translate(dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	fns := map[string]*ir.Func{}
	for _, f := range p.Funcs {
		fns[f.Name] = f
	}
	has := func(name string, want func(ir.Instr) bool) bool {
		f := fns[name]
		if f == nil {
			t.Fatalf("%s not translated", name)
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if want(in) {
					return true
				}
			}
		}
		return false
	}
	op := func(s string) func(ir.Instr) bool {
		return func(in ir.Instr) bool { o, ok := in.Op.(ir.Simple); return ok && string(o) == s }
	}
	for _, name := range []string{"ReadN", "L", "C", "NF"} {
		if !has(name, op("urem")) {
			t.Errorf("%s: havoc count not bounded (no urem)", name)
		}
	}
	if !has("HashW", func(in ir.Instr) bool { c, ok := in.Op.(ir.Const); return ok && c.V == ir.ErrVal{} }) {
		t.Errorf("HashW: hash.Hash's Write may fail in the model")
	}
	if !has("P", op("slt")) || !strings.Contains(strings.Join(fns["P"].Issues, "\n"), "identity permutation") {
		t.Errorf("P: rand.Perm not lowered to the identity permutation: %v", fns["P"].Issues)
	}
	if has("S", func(in ir.Instr) bool { return in.Op == nil && in.Probe == "" && in.Assume == "" }) {
		t.Errorf("S: []byte(constant) global is havoc: %v", fns["S"].Issues)
	}
}

// The zero-copy conversions through package unsafe are conversions in the
// model (#87: prometheus' labels encode every label set with yoloString).
func TestUnsafeViews(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"m.go": `package m

import "unsafe"

func yoloString(b []byte) string { return unsafe.String(unsafe.SliceData(b), len(b)) }

func yoloBytes(s string) []byte { return unsafe.Slice(unsafe.StringData(s), len(s)) }
`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, _, err := Translate(dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"yoloString": "bytesToStr", "yoloBytes": "strToBytes"}
	for _, f := range p.Funcs {
		op, ok := want[f.Name]
		if !ok {
			continue
		}
		found := false
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if o, ok := in.Op.(ir.Simple); ok && string(o) == op {
					found = true
				}
			}
		}
		if !found || len(f.Issues) > 0 {
			t.Errorf("%s: no %s, or issues %v", f.Name, op, f.Issues)
		}
		delete(want, f.Name)
	}
	if len(want) > 0 {
		t.Errorf("not translated: %v", want)
	}
}

// Two packages of the same name keep one qualifier each across all the
// ABIs of a package: a PoC chains several functions' ABIs, and grpc's
// internal/stats spelled experimental/stats and grpc/stats both as
// "stats" in different ABIs, so the merged imports collided (#220).
func TestImportAliasesPerPackage(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":   "module example.com/m\n\ngo 1.22\n",
		"a/x/x.go": "package x\n\ntype T struct{ N int }\n",
		"b/x/x.go": "package x\n\ntype U struct{ N int }\n",
		"m.go": `package m

import (
	ax "example.com/m/a/x"
	bx "example.com/m/b/x"
)

func F(p *ax.T) int { return p.N }

func G(q *bx.U) int { return q.N }
`,
	}
	for name, src := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, _, err := Translate(dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	// The qualifier of each path in each ABI that mentions its type:
	// the alias recorded, else the package name.
	spell := map[string]map[string]bool{}
	for _, f := range p.Funcs {
		if f.Name != "F" && f.Name != "G" {
			continue
		}
		path := map[string]string{"F": "example.com/m/a/x", "G": "example.com/m/b/x"}[f.Name]
		q := f.ABI.Imports[path]
		if q == "" {
			q = "x"
		}
		if spell[path] == nil {
			spell[path] = map[string]bool{}
		}
		spell[path][q] = true
	}
	a, b := spell["example.com/m/a/x"], spell["example.com/m/b/x"]
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("qualifiers %v", spell)
	}
	for q := range a {
		if b[q] {
			t.Fatalf("both packages spelled %q: %v", q, spell)
		}
	}
}

// TestTestHelpersLeftOut: a test file's helper is not translated, nor the
// package only it imports (#222); a spec, the helper it calls, and
// go2lean's own files are.
func TestTestHelpersLeftOut(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":                      "module example.com/m\n\ngo 1.22\n",
		"util/u.go":                   "package util\n\nfunc Help(n int) int { return n + 1 }\n",
		"m.go":                        "package m\n\nfunc F(n int) int { return n * 2 }\n",
		"m_test.go":                   "package m\n\nimport \"example.com/m/util\"\n\nfunc helper(n int) int { return util.Help(n) }\n",
		"spec_test.go":                "package m\n\nfunc twice(n int) int { return n + n }\n\n// go2lean:spec F\nfunc SpecF(n, ret int) bool { return ret == twice(n) }\n",
		"zz_go2lean_generics_test.go": "package m\n\nfunc Go2LeanGen_X() int { return F(1) }\n",
	}
	for name, src := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	progs, _, err := TranslateAllOpts(dir, Options{Deps: true}, ".")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, p := range progs {
		for _, f := range p.Funcs {
			have[p.Package+"."+f.Name] = true
		}
	}
	for _, want := range []string{"example.com/m.F", "example.com/m.SpecF", "example.com/m.twice", "example.com/m.Go2LeanGen_X"} {
		if !have[want] {
			t.Errorf("%s not translated; have %v", want, have)
		}
	}
	for _, not := range []string{"example.com/m.helper", "example.com/m/util.Help"} {
		if have[not] {
			t.Errorf("%s translated, though only a test helper uses it", not)
		}
	}
}

// TestIfaceAltsSkipEnclosing: a struct embedding the interface it
// implements does not get itself as that field's value (grpc's
// oauth.TokenSource, whose generation recursed without end).
func TestIfaceAltsSkipEnclosing(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"m.go": `package m

type Source interface{ Token() int }

type Wrap struct{ Source }

func NewWrap(s Source) Source { return Wrap{s} }

func (w Wrap) Get() int { return w.Token() }
`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, _, err := Translate(dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Funcs {
		if f.Name != "Wrap.Get" {
			continue
		}
		for _, ty := range f.ABI.Types {
			for _, fd := range ty.Fields {
				for _, a := range fd.Alts {
					if a.TypeRef == ty.GoType || a.GoType == ty.GoType {
						t.Errorf("field %s of %s takes %s itself", fd.Go, ty.GoType, a.GoType)
					}
				}
			}
		}
		return
	}
	t.Fatal("Wrap.Get not translated")
}
