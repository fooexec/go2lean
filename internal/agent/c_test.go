package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
)

// scripted answers each call with the next response.
type scripted struct{ resps []string }

func (s *scripted) Name() string { return "scripted" }
func (s *scripted) Complete(ctx context.Context, system, user string) (string, error) {
	r := s.resps[0]
	s.resps = s.resps[1:]
	return r, nil
}

// The agent on C: a spec file that clang rejects goes back to the agent
// with clang's diagnostics, and the repaired one is written next to the
// source with the source included (#270).
func TestRunCRepair(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("no clang")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "lib.c")
	os.WriteFile(src, []byte("int twice(int x) { return 2 * x; }\n"), 0o644)
	bad := "```c\n__attribute__((annotate(\"go2lean:spec\"))) int spec_twice(int x) { return twice(x) == x + x }\n```"
	good := "```c\n__attribute__((annotate(\"go2lean:spec\"))) int spec_twice(int x) { return x > 1000 || x < -1000 || twice(x) == x + x; }\n```"
	llm := &scripted{resps: []string{bad, good}}
	var out bytes.Buffer
	ws := contract.Workspace{Root: filepath.Join(dir, "ws")}
	_ = RunC(context.Background(), dir, ws, src, llm, Options{MaxRepairs: 1}, &out)
	if !strings.Contains(out.String(), "repair 1") {
		t.Errorf("no repair round:\n%s", out.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, CSpecFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `#include "`+src+`"`) || !strings.Contains(string(b), "x > 1000") {
		t.Errorf("spec file:\n%s", b)
	}
	if len(llm.resps) != 0 {
		t.Errorf("%d responses left", len(llm.resps))
	}
}

func TestSpecErrors(t *testing.T) {
	log := "  spec_a\n    -> inconclusive specs.c:3: runtime error: signed integer overflow\n      ub_in_spec: undefined behaviour in the spec itself, not in the code it checks\n  spec_b\n    -> confirmed ==2==ERROR: AddressSanitizer: heap-buffer-overflow\n  spec_c\n    no UB in 2000 trials (0 stuck, 0 out of fuel)\n"
	got := specErrors(log)
	if len(got) != 1 || !strings.HasPrefix(got[0], "spec_a: inconclusive") {
		t.Errorf("specErrors = %q, want spec_a only", got)
	}
}
