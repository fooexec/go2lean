package repo

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	leanassets "github.com/fooexec/go2lean/lean"
)

func TestBundledCore(t *testing.T) {
	t.Setenv("GO2LEAN_CACHE", t.TempDir())
	// A target module may contain a different or malicious lean/ directory.
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "lean"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "lean", "lean-toolchain"), []byte("untrusted"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, err := CoreDir(target)
	if err != nil {
		t.Fatal(err)
	}
	files, err := CoreFiles("/nonexistent-checkout")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		want, err := leanassets.Core.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("bundle mismatch: %s", name)
		}
	}
	for _, name := range []string{"Go2Lean.lean", "Go2Lean/Driver.lean", "Go2Lean/SSA/Core.lean", "lean-toolchain", "lakefile.toml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Error(err)
		}
	}
	for _, name := range []string{"Proofs", "Search", "Go2Lean/Generated"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("unexpected bundled %s: %v", name, err)
		}
	}
	again, err := CoreDir("/missing")
	if err != nil || again != dir {
		t.Fatalf("snapshot depends on checkout: %q %v", again, err)
	}
}

func TestConcurrentCore(t *testing.T) {
	t.Setenv("GO2LEAN_CACHE", t.TempDir())
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, err := CoreDir("/missing"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}
