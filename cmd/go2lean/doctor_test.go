package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDoctorMissingTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	if err := diagnose(&out); err == nil {
		t.Fatal("missing tools accepted")
	}
	for _, want := range []string{"FAIL", "go", "elan", "lake", "prlimit", "elan toolchain install leanprover/lean4:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
}

func TestDoctorNoTargetOrToolchainExecution(t *testing.T) {
	bin := t.TempDir()
	// A marker would be created if doctor invoked a build/version check.
	marker := filepath.Join(t.TempDir(), "executed")
	for _, name := range []string{"lake", "prlimit"} {
		src := "#!/bin/sh\necho invoked > '" + marker + "'\nexit 1\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(src), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	goScript := "#!/bin/sh\n[ \"$1\" = version ] && [ \"$GOTOOLCHAIN\" = local ] || exit 1\necho go version go1.26.5 linux/amd64\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(goScript), 0o755); err != nil {
		t.Fatal(err)
	}
	elan := "#!/bin/sh\n[ \"$1 $2\" = 'toolchain list' ] || exit 1\necho leanprover/lean4:v0.0.0\n"
	if err := os.WriteFile(filepath.Join(bin, "elan"), []byte(elan), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var out bytes.Buffer
	if err := diagnose(&out); err == nil {
		t.Fatal("wrong toolchain accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("doctor ran tool: %v", err)
	}
}

func TestDoctorRejectsOldGoWithoutDownloads(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "local-version-probed")
	script := "#!/bin/sh\n[ \"$1\" = version ] && [ \"$GOTOOLCHAIN\" = local ] || exit 1\necho probed > '" + marker + "'\necho go version go1.25.9 linux/amd64\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GOTOOLCHAIN", "auto")
	var out bytes.Buffer
	if err := diagnose(&out); err == nil {
		t.Fatal("old Go accepted")
	}
	if !strings.Contains(out.String(), "Go 1.26 or newer is required") {
		t.Fatalf("missing actionable version diagnostic: %s", out.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("local-only probe did not execute: %v", err)
	}
}

// An old local Go is fine when GOTOOLCHAIN selects a Go 1.26 that is
// already downloaded; one not downloaded is reported, and not fetched.
func TestDoctorAcceptsCachedToolchain(t *testing.T) {
	bin, cache := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\n[ \"$GOTOOLCHAIN\" = local ] || exit 1\n" +
		"[ \"$1\" = env ] && { echo '" + cache + "'; exit 0; }\n" +
		"echo go version go1.24.7 linux/amd64\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	tc := filepath.Join(cache, "golang.org", "toolchain@v0.0.1-go1.26.6."+runtime.GOOS+"-"+runtime.GOARCH, "bin")
	if err := os.MkdirAll(tc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tc, "go"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	t.Setenv("GOTOOLCHAIN", "go1.26.6+auto")
	diagnose(&out)
	if !strings.Contains(out.String(), "ok    Go version      go1.26.6 via GOTOOLCHAIN") {
		t.Fatalf("cached toolchain not accepted: %s", out.String())
	}
	out.Reset()
	t.Setenv("GOTOOLCHAIN", "go1.26.9")
	diagnose(&out)
	if !strings.Contains(out.String(), "selects go1.26.9, which is not downloaded yet") {
		t.Fatalf("missing toolchain not reported: %s", out.String())
	}
}
