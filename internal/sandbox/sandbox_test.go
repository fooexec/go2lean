package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunIsolation(t *testing.T) {
	if Mode() != ModeBwrap {
		t.Skipf("sandbox mode %s", Mode())
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "in.txt"), []byte("hello"), 0o644)
	os.MkdirAll(filepath.Join(dir, "testdata"), 0o755)
	os.WriteFile(filepath.Join(dir, "testdata", "x"), []byte("x"), 0o644)
	outside := filepath.Join(dir, "written")
	sh := `cat in.txt testdata/x; echo ok > here.txt && echo local-ok
echo bad > ` + outside + ` 2>/dev/null || echo outside-blocked
echo bad > /` + filepath.Base(outside) + ` 2>/dev/null || echo root-blocked`
	out, errOut, exit, err := Run("/bin/sh", dir, []string{"-c", sh}, nil, Default)
	if err != nil || exit != 0 {
		t.Fatalf("exit %d err %v stderr %s", exit, err, errOut)
	}
	for _, want := range []string{"hellox", "local-ok", "outside-blocked", "root-blocked"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Errorf("the run wrote outside its copy")
	}
	if _, err := os.Stat(filepath.Join(dir, "here.txt")); err == nil {
		t.Errorf("the run wrote into the original directory")
	}
}

func TestRunWallClock(t *testing.T) {
	if Mode() == ModeNone {
		t.Skip("no sandbox")
	}
	lim := Default
	lim.Wall = time.Second
	start := time.Now()
	_, errOut, exit, err := Run("/bin/sh", t.TempDir(), []string{"-c", "sleep 30"}, nil, lim)
	if err != nil {
		t.Fatal(err)
	}
	if exit == 0 || time.Since(start) > 15*time.Second || !strings.Contains(errOut, "wall-clock") {
		t.Errorf("exit %d after %s, stderr %q", exit, time.Since(start), errOut)
	}
}

func TestIsolationPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode, consent string
		allowed       bool
	}{
		{ModeBwrap, "", true}, {ModeLimits, "", false}, {ModeNone, "", false},
		{ModeLimits, "rlimits", true}, {ModeNone, "rlimits", false},
		{ModeNone, "off", true}, {ModeLimits, "off", true}, {ModeNone, "typo", false},
	} {
		if got := requireIsolation(tc.mode, tc.consent) == nil; got != tc.allowed {
			t.Errorf("%+v: allowed=%v", tc, got)
		}
	}
}

func TestRunHidesHome(t *testing.T) {
	if Mode() != ModeBwrap {
		t.Skip("requires bwrap")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := os.CreateTemp(home, "go2lean-private-test-")
	if err != nil {
		t.Fatal(err)
	}
	name := secret.Name()
	secret.Close()
	defer os.Remove(name)
	out, stderr, exit, err := Run("/bin/sh", "", []string{"-c", `test ! -e "$1" && echo hidden`, "sh", name}, nil, Default)
	if err != nil || exit != 0 || !strings.Contains(out, "hidden") {
		t.Fatalf("home visible: exit=%d err=%v stdout=%s stderr=%s", exit, err, out, stderr)
	}
}

func TestRunHidesHostCredentials(t *testing.T) {
	if Mode() != ModeBwrap {
		t.Skip("requires bwrap")
	}
	t.Setenv("GO2LEAN_TEST_SECRET", "must-not-reach-replay")
	out, stderr, exit, err := Run("/bin/sh", "", []string{"-c", `test -z "$GO2LEAN_TEST_SECRET" && echo scrubbed`}, nil, Default)
	if err != nil || exit != 0 || !strings.Contains(out, "scrubbed") {
		t.Fatalf("host environment exposed: %v exit=%d stdout=%s stderr=%s", err, exit, out, stderr)
	}
}

// TestRunShowsModule: the package's module stays visible read-only, even
// under the private /tmp, so a test that reads a file of the module
// outside its package by its source path finds it (#221), and cannot
// write it.
func TestRunShowsModule(t *testing.T) {
	if Mode() != ModeBwrap {
		t.Skip("requires bwrap")
	}
	mod := t.TempDir()
	pkg := filepath.Join(mod, "p")
	data := filepath.Join(mod, "testdata", "cert.pem")
	for _, d := range []string{pkg, filepath.Dir(data)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module m\n"), 0o644)
	os.WriteFile(filepath.Join(pkg, "p.go"), []byte("package p\n"), 0o644)
	os.WriteFile(data, []byte("cert"), 0o644)
	out, stderr, exit, err := Run("/bin/sh", pkg, []string{"-c", `cat "$1" && ! touch "$1" 2>/dev/null && echo " read-only"`, "sh", data}, nil, Default)
	if err != nil || exit != 0 || !strings.Contains(out, "cert read-only") {
		t.Fatalf("module file: exit=%d err=%v stdout=%s stderr=%s", exit, err, out, stderr)
	}
}
