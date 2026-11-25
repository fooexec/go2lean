package agent

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
)

// A source that builds only with a define its build passes (FRR's pimd
// needs -DPIM_IPV=4) stops the agent before any call to the LLM.
func TestRunCSourceMustBuild(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("no clang")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "lib.c")
	os.WriteFile(src, []byte("#ifndef IPV\n#error IPV unset\n#endif\nint f(int x) { return x; }\n"), 0o644)
	ws := contract.Workspace{Root: t.TempDir()}
	err := RunC(context.Background(), dir, ws, src, nil, Options{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "does not compile with the -cflags given") {
		t.Fatalf("err = %v", err)
	}
	// With the define it gets past the check (and fails later, on the nil LLM).
	err = nil
	func() {
		defer func() { recover() }()
		err = RunC(context.Background(), dir, ws, src, nil, Options{CFlags: []string{"-DIPV=4"}}, io.Discard)
	}()
	if err != nil && strings.Contains(err.Error(), "does not compile with the -cflags given") {
		t.Fatalf("with the define: %v", err)
	}
}
