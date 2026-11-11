package fuzzimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fuzz target that is not read as a spec leaves none of its imports in
// the generated file: Go rejects an unused import, and the whole package
// would then fail to load (evaluation 7, containerd).
func TestRejectedTargetLeavesNoImport(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write("m.go", "package m\n\nfunc Echo(s string) string { return s }\n")
	write("m_test.go", `package m

import (
	"bytes"
	"strings"
	"testing"
)

func FuzzUpper(f *testing.F) {
	f.Fuzz(func(t *testing.T, s string) {
		if strings.ToUpper(Echo(s)) != strings.ToUpper(s) {
			t.Fatal("differs")
		}
	})
}

func FuzzOuter(f *testing.F) {
	prefix := []byte("x")
	f.Fuzz(func(t *testing.T, b []byte) {
		if !bytes.HasPrefix(append(prefix, b...), prefix) {
			t.Fatal("no prefix")
		}
	})
}
`)
	overlay, skipped, err := Specs(dir, "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(overlay) != 1 || len(skipped) != 1 {
		t.Fatalf("overlay %d files, skipped %v", len(overlay), skipped)
	}
	for _, src := range overlay {
		if !strings.Contains(string(src), `"strings"`) || strings.Contains(string(src), `"bytes"`) {
			t.Fatalf("generated file:\n%s", src)
		}
		// No any: a module without a go directive (google/uuid) is Go 1.16,
		// and the whole package would fail to load.
		if strings.Contains(string(src), "...any") {
			t.Fatalf("generated file uses any:\n%s", src)
		}
	}
}
