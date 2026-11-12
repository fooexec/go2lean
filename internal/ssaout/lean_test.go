package ssaout

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/ir"
)

// bigProg has n functions of about 1 KB of model text each.
func bigProg(n int) *ir.Prog {
	p := &ir.Prog{Package: "example.com/big", Frontend: "gossa", JSONTable: "{ descs := #[], zeros := #[] }"}
	for i := 0; i < n; i++ {
		p.Funcs = append(p.Funcs, &ir.Func{
			Name: fmt.Sprintf("F%d", i), GoSig: "func() // " + strings.Repeat("x", 900),
			NVals: 1, Status: contract.ModelComplete,
			Blocks: []ir.Block{{Name: "entry", Term: ir.Term{Kind: ir.TermRet, V: 0}}},
		})
	}
	return p
}

// A small model stays one module, as before (#45).
func TestPrintPartsSmall(t *testing.T) {
	p := bigProg(10)
	main, parts := PrintParts(p, "M")
	if parts != nil || main != Print(p, "M") {
		t.Fatalf("a small model was split into %d parts", len(parts))
	}
}

// A large model is split into parts of about partBytes; together they
// hold every function once, the first holds the JSON table, and the main
// module imports them all and defines the program.
func TestPrintPartsLarge(t *testing.T) {
	p := bigProg(6000)
	const m = "Go2Lean.Generated.gossa.example_com_big"
	main, parts := PrintParts(p, m)
	if len(parts) < 3 {
		t.Fatalf("got %d parts, want at least 3", len(parts))
	}
	defs := 0
	for i, part := range parts {
		if len(part) > partBytes+4096 {
			t.Errorf("part %d is %d bytes", i, len(part))
		}
		want := "import Go2Lean.Dialect.GoSSA\n"
		if i > 0 {
			want = "import " + PartModule(m, 0) + "\n"
		}
		if !strings.HasPrefix(part, want) {
			t.Errorf("part %d starts %q", i, part[:60])
		}
		if strings.Contains(part, "\ndef jsonTable ") != (i == 0) {
			t.Errorf("part %d: jsonTable placement", i)
		}
		if !strings.Contains(part, "namespace "+m+"\n") || !strings.HasSuffix(part, "end "+m+"\n") {
			t.Errorf("part %d is not in the model's namespace", i)
		}
		defs += strings.Count(part, "\ndef f")
	}
	if defs != len(p.Funcs) {
		t.Errorf("parts define %d functions, want %d", defs, len(p.Funcs))
	}
	for i := range parts {
		if !strings.Contains(main, "import "+PartModule(m, i)+"\n") {
			t.Errorf("main does not import part %d", i)
		}
	}
	if strings.Contains(main, "\ndef f") || !strings.Contains(main, "\ndef prog ") {
		t.Errorf("main module should hold the program only")
	}
}

// Emit writes the parts, records them and hashes parts then main; a
// later, smaller model removes the old parts; the registry finds the
// table of a split model.
func TestEmitParts(t *testing.T) {
	root := t.TempDir()
	leanDir := filepath.Join(root, "lean")
	ws := contract.Workspace{Root: root}
	if err := ws.Init(); err != nil {
		t.Fatal(err)
	}
	p := bigProg(6000)
	r, err := Emit(p, leanDir, ws, "example.com/big")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.LeanParts) < 3 {
		t.Fatalf("report has %d parts", len(r.LeanParts))
	}
	h := sha256.New()
	for _, f := range append(append([]string{}, r.LeanParts...), r.LeanFile) {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		h.Write(b)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != r.LeanSHA256 {
		t.Errorf("hash %s, recorded %s", got, r.LeanSHA256)
	}
	reg, err := os.ReadFile(filepath.Join(leanDir, "Go2Lean", "Generated", "Registry.lean"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reg), "Go2Lean.Generated.gossa.example_com_big.jsonTable") {
		t.Errorf("registry misses the split model's table:\n%s", reg)
	}
	r, err = Emit(bigProg(5), leanDir, ws, "example.com/big")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.LeanParts) != 0 {
		t.Errorf("small model has parts %v", r.LeanParts)
	}
	if _, err := os.Stat(filepath.Join(leanDir, "Go2Lean", "Generated", "gossa", "example_com_big")); !os.IsNotExist(err) {
		t.Errorf("old part directory left behind (%v)", err)
	}
}
