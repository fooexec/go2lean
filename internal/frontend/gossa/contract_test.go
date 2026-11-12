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
