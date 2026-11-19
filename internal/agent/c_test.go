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
type scripted struct {
	resps []string
	users []string // the user message of each call
}

func (s *scripted) Name() string { return "scripted" }
func (s *scripted) Complete(ctx context.Context, system, user string) (string, error) {
	s.users = append(s.users, user)
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

// A library of several files: the agent shows the model the public header
// (not the implementation), the spec file includes that header, and clang
// is given the -I flags.
func TestRunCHeaderLibrary(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("no clang")
	}
	dir := t.TempDir()
	inc := filepath.Join(dir, "include")
	os.MkdirAll(inc, 0o755)
	hdr := filepath.Join(inc, "lib.h")
	os.WriteFile(hdr, []byte("#include \"types.h\"\nint twice(MyInt x);\n"), 0o644)
	os.WriteFile(filepath.Join(inc, "types.h"), []byte("typedef int MyInt;\n"), 0o644)
	impl := filepath.Join(dir, "lib.c")
	os.WriteFile(impl, []byte("#include \"lib.h\"\nint twice(MyInt x) { return 2 * x; }\n"), 0o644)
	// types.h is found only through -I: the spec file does not compile without the flag.
	spec := "```c\n__attribute__((annotate(\"go2lean:spec\"))) int spec_twice(int x) { return x > 1000 || x < -1000 || twice(x) == x + x; }\n```"
	llm := &scripted{resps: []string{spec, spec}}
	var out bytes.Buffer
	ws := contract.Workspace{Root: filepath.Join(dir, "ws")}
	_ = RunC(context.Background(), dir, ws, hdr, llm, Options{
		MaxRepairs: 1, Lib: []string{impl}, CFlags: []string{"-I" + inc},
	}, &out)
	if strings.Contains(out.String(), "does not compile") || strings.Contains(out.String(), "repair 1") {
		t.Fatalf("the spec should compile with the -I flag:\n%s", out.String())
	}
	if len(llm.users) == 0 || !strings.Contains(llm.users[0], "Public header of the library") ||
		strings.Contains(llm.users[0], "return 2 * x") {
		t.Errorf("the model should see the header, not the implementation:\n%s", llm.users[0])
	}
	b, err := os.ReadFile(filepath.Join(inc, CSpecFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `#include "`+hdr+`"`) || strings.Contains(string(b), impl) {
		t.Errorf("spec file should include the header only:\n%s", b)
	}
}
