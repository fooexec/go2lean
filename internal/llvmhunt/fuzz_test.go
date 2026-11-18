package llvmhunt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	llvmfe "github.com/fooexec/go2lean/internal/frontend/llvm"
)

// The target decodes each parameter kind and aborts on a spec's 0.
func TestFuzzHarness(t *testing.T) {
	fn := llvmfe.Func{Name: "spec_f", Spec: true, Gens: []llvmfe.Gen{
		{K: "int", Name: "k", W: 32},
		{K: "fp", Name: "x", W: 64},
		{K: "typed", Name: "s", Ty: map[string]any{"k": "str"}},
		{K: "buf", Name: "b", EB: 1, MaxLen: 16},
		{K: "len", Name: "n", W: 64, Of: 3},
	}}
	h, err := fuzzHarness("/src/lib.c", fn)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"int32_t a0 = (int32_t)g2l_take(4);",
		"double a1 = (double)(int16_t)g2l_take(2);",
		"char *a2 = g2l_str();",
		"uint8_t *a3 = malloc(a3_n ? a3_n * 1 : 1);",
		"long long a4 = (long long)a3_n;",
		"if (!spec_f(a0, a1, a2, a3, a4))",
		"free(a2);",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("harness lacks %q:\n%s", want, h)
		}
	}
	// A pointer to a struct is not decoded yet.
	fn.Gens = []llvmfe.Gen{{K: "typed", Name: "item", Ty: map[string]any{"k": "ptr"}}}
	if _, err := fuzzHarness("/src/lib.c", fn); err == nil {
		t.Error("a struct pointer: want an error")
	}
}

// The harness compiles against a real source (when clang is there).
func TestFuzzHarnessCompiles(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("clang not found")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "lib.c")
	os.WriteFile(src, []byte("#include <stddef.h>\nint spec_f(int k, const char *s, const unsigned char *b, size_t n) { return k != 7 || s[0] || n; }\n"), 0o644)
	fn := llvmfe.Func{Name: "spec_f", Spec: true, Gens: []llvmfe.Gen{
		{K: "int", Name: "k", W: 32},
		{K: "typed", Name: "s", Ty: map[string]any{"k": "str"}},
		{K: "buf", Name: "b", EB: 1, MaxLen: 16},
		{K: "len", Name: "n", W: 64, Of: 2},
	}}
	h, err := fuzzHarness(src, fn)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "fuzz.c")
	os.WriteFile(p, []byte(h), 0o644)
	if out, err := exec.Command("clang", "-fsyntax-only", "-w", p).CombinedOutput(); err != nil {
		t.Fatalf("clang: %v\n%s", err, out)
	}
}
