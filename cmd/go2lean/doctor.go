package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"go/version"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/andreasfoo/go2lean/internal/sandbox"
	leanassets "github.com/andreasfoo/go2lean/lean"
)

// doctor inspects prerequisites without downloading a toolchain or executing
// target code. Target dependency loading is explicit and offline.
func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	deps := fs.Bool("deps", false, "also check target package dependencies offline, without building or running code")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*deps && fs.NArg() != 0 {
		return fmt.Errorf("doctor: package patterns require -deps")
	}
	err := diagnose(os.Stdout)
	if *deps {
		patterns := fs.Args()
		if len(patterns) == 0 {
			patterns = []string{"./..."}
		}
		err = errors.Join(err, diagnoseDeps(os.Stdout, patterns))
	}
	return err
}

func diagnose(out io.Writer) error {
	failed := false
	check := func(ok bool, name, detail string) {
		status := "ok"
		if !ok {
			status = "FAIL"
			failed = true
		}
		fmt.Fprintf(out, "%-5s %-15s %s\n", status, name, detail)
	}
	for _, tool := range []string{"go", "elan", "lake", "prlimit"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			check(false, tool, "not on PATH; install it and add its bin directory to PATH")
		} else {
			check(true, tool, p)
		}
	}
	// Force local toolchain selection: even inside a module requiring a newer
	// Go version, diagnostics must not trigger Go's automatic downloads.
	goCtx, goCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer goCancel()
	cmd := exec.CommandContext(goCtx, "go", "version")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOTOOLCHAIN=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GOTOOLCHAIN=local")
	goOut, goErr := cmd.CombinedOutput()
	goDetail := strings.TrimSpace(string(goOut))
	fields := strings.Fields(goDetail)
	goOK := goErr == nil && len(fields) >= 3 && fields[0] == "go" && fields[1] == "version" && version.IsValid(fields[2]) && version.Compare(fields[2], "go1.26") >= 0
	if !goOK {
		// The local Go is older, but GOTOOLCHAIN may select a newer one that
		// is already downloaded: the go command then runs that one, with no
		// download either.
		if v, ok := selectedToolchain(os.Getenv("GOTOOLCHAIN")); ok && toolchainCached(goCtx, v) {
			goOK = true
			goDetail = v + " via GOTOOLCHAIN (already downloaded); local toolchain: " + goDetail
		} else {
			goDetail += "; Go 1.26 or newer is required (local toolchain; no downloads)"
			if ok {
				goDetail += "; GOTOOLCHAIN=" + os.Getenv("GOTOOLCHAIN") + " selects " + v + ", which is not downloaded yet: `go version` in the module downloads it"
			}
		}
	}
	check(goOK, "Go version", goDetail)
	pinned, err := leanassets.Core.ReadFile("lean-toolchain")
	if err != nil {
		return err
	}
	pin := strings.TrimSpace(string(pinned))
	// `elan toolchain list` is local only; invoking lake --version could fetch
	// a missing default toolchain or use the caller's lean-toolchain file.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listing, err := exec.CommandContext(ctx, "elan", "toolchain", "list").CombinedOutput()
	installed := false
	if err == nil {
		for _, line := range strings.Split(string(listing), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == pin {
				installed = true
			}
		}
	}
	detail := pin
	if !installed {
		detail += "; run: elan toolchain install " + pin
	}
	check(installed, "Lean toolchain", detail)
	mode := sandbox.Mode()
	check(mode == sandbox.ModeBwrap, "replay isolation", mode+" (requires working bubblewrap/user namespaces and prlimit)")
	if sandbox.Unconfined() {
		check(false, "Lean isolation", "GO2LEAN_LEAN_SANDBOX=off disables proof confinement")
	} else {
		_, err := sandbox.Confine(nil, nil, "")
		check(err == nil, "Lean isolation", "bubblewrap/user namespaces required for untrusted Lean")
		err = sandbox.CheckComparator()
		detail := "bubblewrap supports pinned Lean comparator isolation"
		if err != nil {
			detail = err.Error()
		}
		check(err == nil, "proof judge", detail)
	}
	fmt.Fprintln(out, "Prerequisite scope: local tools and isolation only; use -deps for offline target metadata. Neither check promises that arbitrary target code is safe.")
	if failed {
		return fmt.Errorf("doctor: prerequisites missing or isolation disabled; see diagnostics above")
	}
	return nil
}

// selectedToolchain is the Go 1.26 or newer that GOTOOLCHAIN names
// ("go1.26.6", "go1.26.6+auto", "go1.26.6+path"), if it names one.
func selectedToolchain(gotoolchain string) (string, bool) {
	v, _, _ := strings.Cut(gotoolchain, "+")
	if !version.IsValid(v) || version.Compare(v, "go1.26") < 0 {
		return "", false
	}
	return v, true
}

// toolchainCached says whether toolchain v is in the module cache, where
// the go command keeps the toolchains it downloaded. It asks the local go
// for GOMODCACHE, so nothing is downloaded.
func toolchainCached(ctx context.Context, v string) bool {
	cmd := exec.CommandContext(ctx, "go", "env", "GOMODCACHE")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOTOOLCHAIN=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "golang.org", "toolchain@v0.0.1-"+v+"."+runtime.GOOS+"-"+runtime.GOARCH)
	_, err = os.Stat(filepath.Join(dir, "bin", "go"))
	return err == nil
}
