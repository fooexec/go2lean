package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreasfoo/go2lean/internal/pipeline"
)

func TestWorkspaceOutsideCheckout(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	module := t.TempDir()
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.org/external\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cf := workspaceFlag(fs)
	if err := fs.Parse([]string{"-module", module, "-workspace", "results"}); err != nil {
		t.Fatal(err)
	}
	w, env, err := openWorkspace(cf)
	if err != nil {
		t.Fatal(err)
	}
	if w.Root != filepath.Join(cwd, "results") {
		t.Fatalf("workspace = %s", w.Root)
	}
	if env.Module != module {
		t.Fatalf("module = %s", env.Module)
	}
}

// A directory in no Go module is reported before anything loads.
func TestRequireModule(t *testing.T) {
	dir := t.TempDir()
	if err := requireModule(pipeline.Env{Module: dir}); err == nil || !strings.Contains(err.Error(), "go mod init") {
		t.Fatalf("no go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := requireModule(pipeline.Env{Module: sub}); err != nil {
		t.Fatalf("a go.mod above: %v", err)
	}
}
