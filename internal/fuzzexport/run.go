package fuzzexport

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/sandbox"
)

// Options of a fuzzing run.
type Options struct {
	// FuzzTime is how long each target is fuzzed (default 30s).
	FuzzTime time.Duration
	// Property limits the run to one property ("" for all specs).
	Property string
	// Overlay adds files to the package directory for the build (base name
	// to contents), e.g. the agent's spec file.
	Overlay map[string][]byte
}

// Result is what fuzzing one target found.
type Result struct {
	Property string `json:"property"`
	Fuzz     string `json:"fuzz"`
	// Status: "violated" (the fuzzer found a failing input), "held" (none
	// within the time), "error" (the target did not run).
	Status  string  `json:"status"`
	Seconds float64 `json:"seconds"`
	Execs   string  `json:"execs,omitempty"`
	// Message is the failure the target reported (the spec false, a panic).
	Message string `json:"message,omitempty"`
	// Input is the failing input in Go's corpus format; Corpus is the path
	// of its copy in the workspace, under testdata/fuzz/<target>/.
	Input  string `json:"input,omitempty"`
	Corpus string `json:"corpus,omitempty"`
	// Reproduce says how to rerun it in the package.
	Reproduce string `json:"reproduce,omitempty"`
}

var (
	failingRe = regexp.MustCompile(`Failing input written to (testdata/fuzz/\S+)`)
	reportRe  = regexp.MustCompile(`(go2lean: \S+ is false|panic: .*)`)
	execsRe   = regexp.MustCompile(`execs: (\d+)`)
)

// Run exports the spec properties of pkg in the workspace as fuzz targets,
// builds them with coverage instrumentation (through an overlay: nothing
// is written into the package) and fuzzes each in the sandbox. Results go
// to <workspace>/fuzz/, with the generated test file and the failing
// inputs, so a finding can be replayed with `go test` in the package.
func Run(ws contract.Workspace, root, pkg string, opts Options) ([]Result, []Skipped, error) {
	if opts.FuzzTime == 0 {
		opts.FuzzTime = 30 * time.Second
	}
	props, err := ws.Properties()
	if err != nil {
		return nil, nil, err
	}
	var specs []Spec
	for _, p := range props {
		if opts.Property != "" && p.ID != opts.Property {
			continue
		}
		t, err := ws.Target(p.Target)
		if err != nil || t.Package != pkg {
			continue
		}
		switch {
		case p.Kind == contract.KindPostcondition && p.Spec != nil:
			specs = append(specs, Spec{Property: p.ID, Kind: p.Kind, Spec: p.Spec.GoFunc, Target: t.Func})
		case p.Kind == contract.KindSequence:
			// A sequence spec is its own target.
			specs = append(specs, Spec{Property: p.ID, Kind: p.Kind, Spec: t.Func})
		}
	}
	if len(specs) == 0 {
		return nil, nil, fmt.Errorf("fuzz: no spec properties of %s in the workspace", pkg)
	}
	dir, err := pkgDir(root, pkg)
	if err != nil {
		return nil, nil, err
	}
	overlay := map[string][]byte{}
	files := map[string][]byte{}
	for name, b := range opts.Overlay {
		overlay[filepath.Join(dir, name)] = b
		files[name] = b
	}
	src, targets, skipped, err := Export(root, pkg, overlay, specs)
	if err != nil || len(targets) == 0 {
		return nil, skipped, err
	}
	files[FileName] = []byte(src)
	outDir := filepath.Join(ws.Root, "fuzz")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, skipped, err
	}
	if err := os.WriteFile(filepath.Join(outDir, FileName), []byte(src), 0o644); err != nil {
		return nil, skipped, err
	}
	bin := filepath.Join(outDir, "fuzz.test")
	// -fuzz at build time instruments the package for coverage guidance.
	stderr, exit, err := sandbox.BuildTest(root, pkg, dir, files, bin, "-fuzz=^FuzzGo2Lean_")
	if err != nil {
		return nil, skipped, err
	}
	if exit != 0 {
		return nil, skipped, fmt.Errorf("fuzz: the generated targets do not build:\n%s", stderr)
	}
	lim := sandbox.Default
	lim.Wall = opts.FuzzTime + 2*time.Minute
	lim.CPUSeconds = int64(opts.FuzzTime.Seconds())*int64(runtime.NumCPU()+1) + 120
	var results []Result
	for _, t := range targets {
		start := time.Now()
		stdout, stderr, exit, got, err := sandbox.RunCollect(bin, dir, []string{
			"-test.run=^$", "-test.fuzz=^" + t.Fuzz + "$", "-test.fuzztime=" + opts.FuzzTime.String(),
			"-test.fuzzcachedir=go2lean-fuzzcache"}, nil, lim, "testdata/fuzz")
		r := Result{Property: t.Property, Fuzz: t.Fuzz, Seconds: time.Since(start).Seconds()}
		out := stdout + stderr
		if m := execsRe.FindAllStringSubmatch(out, -1); len(m) > 0 {
			r.Execs = m[len(m)-1][1]
		}
		switch m := failingRe.FindStringSubmatch(out); {
		case err != nil:
			r.Status, r.Message = "error", err.Error()
		case m != nil:
			r.Status = "violated"
			rel := strings.TrimPrefix(m[1], "testdata/fuzz/")
			r.Input = string(got[rel])
			r.Corpus = filepath.Join("fuzz", "testdata", "fuzz", rel)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(ws.Root, r.Corpus)), 0o755); err == nil {
				os.WriteFile(filepath.Join(ws.Root, r.Corpus), got[rel], 0o644)
			}
			r.Message = failureMessage(out)
			r.Reproduce = fmt.Sprintf("copy %s and %s into %s, then: go test -run='%s/%s' %s",
				filepath.Join(ws.Root, "fuzz", FileName), filepath.Join(ws.Root, r.Corpus), filepath.Join(dir, "testdata", "fuzz", t.Fuzz)+"/", t.Fuzz, filepath.Base(rel), pkg)
		case exit == 0 || timedOut(out):
			r.Status = "held"
		default:
			r.Status, r.Message = "error", lastLines(out, 6)
		}
		b, _ := json.MarshalIndent(r, "", "  ")
		if err := os.WriteFile(filepath.Join(outDir, t.Property+".json"), b, 0o644); err != nil {
			return results, skipped, err
		}
		results = append(results, r)
	}
	return results, skipped, nil
}

// timedOut says whether a run without a failing input ended with Go's
// "context deadline exceeded" when -fuzztime ran out (a quirk of the
// fuzzer, not a finding).
func timedOut(out string) bool {
	return strings.Contains(out, "context deadline exceeded") && !strings.Contains(out, "Failing input")
}

// failureMessage is what the failing target reported: its t.Fatalf line or
// the panic.
func failureMessage(out string) string {
	if m := reportRe.FindString(out); m != "" {
		return m
	}
	return lastLines(out, 4)
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}

func pkgDir(root, pkg string) (string, error) {
	cmd := exec.Command("go", "list", "-f", "{{.Dir}}", pkg)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list %s: %v", pkg, err)
	}
	return strings.TrimSpace(string(out)), nil
}
