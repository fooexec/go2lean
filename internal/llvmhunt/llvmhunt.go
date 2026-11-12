// Package llvmhunt runs go2lean on C and LLVM IR: the LLVM frontend writes a
// model of every defined function (M1), lean-llvm's search driver looks for
// inputs on which a function has undefined behaviour (M2), and each
// counterexample is replayed as a C program built with AddressSanitizer and
// UndefinedBehaviorSanitizer (M3). The property is the C analogue of the Go
// line's no_panic: no undefined behaviour for callers that pass a buffer
// together with its real length.
package llvmhunt

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fooexec/go2lean/internal/contract"
	llvmfe "github.com/fooexec/go2lean/internal/frontend/llvm"
	"github.com/fooexec/go2lean/internal/sandbox"
)

// Options configure the search.
type Options struct {
	Trials     int
	Seed       int
	Fuel       int
	Engine     string // random, exhaustive or all
	ExhaustLen int
	// Init is the model index of the function run before each trial (a Go
	// package's initialisation), or -1.
	Init int
	// Strs are string constants of the program, tried as byte buffers.
	Strs []string
}

// searchResult is the JSON line the search driver prints.
type searchResult struct {
	Found       bool                  `json:"found"`
	Engine      string                `json:"engine"`
	Trial       int                   `json:"trial"`
	Inputs      map[string]string     `json:"inputs"`
	Memory      map[string][]string   `json:"memory"`
	ModelOutput *contract.ModelOutput `json:"model_output"`
	PanicAt     string                `json:"panic_at"`
	Havoc       []json.RawMessage     `json:"havoc"`
	Trials      int                   `json:"trials"`
	Stuck       int                   `json:"stuck"`
	OutOfFuel   int                   `json:"out_of_fuel"`
	Domain      float64               `json:"domain"`
	Exhausted   bool                  `json:"exhausted"`
	Assumptions []string              `json:"assumptions"`
	// HavocDependent: every counterexample found went through havoc or an
	// assumption; this is the first.
	HavocDependent bool           `json:"havoc_dependent"`
	StuckSites     map[string]int `json:"stuck_sites"`
}

var nonIdent = regexp.MustCompile(`[^A-Za-z0-9_]`)

// Key is the model name of a source file.
func Key(src string) string {
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	return "m_" + nonIdent.ReplaceAllString(base, "_")
}

// Run analyses one source file and reports per function.
func Run(root string, ws contract.Workspace, src string, opts Options, log io.Writer) error {
	if err := ws.Init(); err != nil {
		return err
	}
	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	work := filepath.Join(ws.Root, "llvm")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	bc, err := llvmfe.Compile(src, work)
	if err != nil {
		return err
	}
	model, err := llvmfe.Load(bc, llvmfe.Options{})
	if err != nil {
		return err
	}
	leanDir := filepath.Join(ws.Root, "lean-llvm")
	key := Key(src)
	if err := llvmfe.WriteModule(model, leanDir, key); err != nil {
		return err
	}
	if err := ensureProject(leanDir, filepath.Join(root, "lean-llvm")); err != nil {
		return err
	}
	fmt.Fprintf(log, "compiled %s -> %s (%d functions)\n", src, key, len(model.Funcs))
	if out, err := lake(leanDir, "build", "go2lean-llvm-search"); err != nil {
		return fmt.Errorf("lake build: %v\n%s", err, out)
	}
	for _, fn := range model.Funcs {
		fmt.Fprintf(log, "  %s\n", fn.Name)
		for _, is := range fn.Issues {
			fmt.Fprintf(log, "      - %s\n", is)
		}
		res, err := search(leanDir, key, fn, opts)
		if err != nil {
			return err
		}
		if !res.Found {
			certificate := ""
			if res.Exhausted {
				certificate = fmt.Sprintf("; exhaustive: no UB on all %.0f inputs", res.Domain)
			}
			fmt.Fprintf(log, "    no UB in %d trials (%d stuck, %d out of fuel)%s\n", res.Trials, res.Stuck, res.OutOfFuel, certificate)
			continue
		}
		cx := &contract.Counterexample{
			ID:                fmt.Sprintf("cx_llvm_%s_%s_noub_%s_s%d", key, fn.Name, res.Engine, opts.Seed),
			Property:          fmt.Sprintf("p_llvm_%s_%s_noub", key, fn.Name),
			Frontend:          "llvm",
			Engine:            res.Engine,
			Fuel:              opts.Fuel,
			Inputs:            res.Inputs,
			Memory:            res.Memory,
			ModelOutput:       res.ModelOutput,
			RevalidatedInLean: true,
			PanicAt:           res.PanicAt,
		}
		if err := ws.WriteCounterexample(cx); err != nil {
			return err
		}
		fmt.Fprintf(log, "    %s: model %s at %s, inputs %v %v\n", res.Engine, res.ModelOutput.Panic, res.PanicAt, res.Inputs, res.Memory)
		v, err := replay(ws, src, fn, cx)
		if err != nil {
			return err
		}
		fmt.Fprintf(log, "    -> %s %s\n", v.Status, firstLine(v.Evidence.PanicMsg))
	}
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func search(leanDir, key string, fn llvmfe.Func, opts Options) (*searchResult, error) {
	spec := map[string]any{
		"model": key, "func": fn.Index, "gens": fn.Gens,
		"trials": opts.Trials, "seed": opts.Seed, "fuel": opts.Fuel,
		"engine": opts.Engine, "exhaustLen": opts.ExhaustLen, "extra": fn.Consts,
	}
	if opts.Init >= 0 {
		spec["init"] = opts.Init
	}
	if len(opts.Strs) > 0 {
		spec["strs"] = opts.Strs
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	specFile := filepath.Join(leanDir, "Search", key+"_"+fn.Name+".json")
	if err := os.MkdirAll(filepath.Dir(specFile), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(specFile, b, 0o644); err != nil {
		return nil, err
	}
	out, err := exec.Command(filepath.Join(leanDir, ".lake", "build", "bin", "go2lean-llvm-search"), specFile).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("search %s: %v\n%s", fn.Name, err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var res searchResult
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil {
		return nil, fmt.Errorf("search %s: %v\n%s", fn.Name, err, out)
	}
	return &res, nil
}

// ensureProject makes leanDir the workspace's lake project for LLVM
// models: it requires lean-llvm by path and links the search driver over a
// registry of every model written there.
func ensureProject(leanDir, pkgDir string) error {
	pkg, err := filepath.Abs(pkgDir)
	if err != nil {
		return err
	}
	toolchain, err := os.ReadFile(filepath.Join(pkg, "lean-toolchain"))
	if err != nil {
		return err
	}
	modDir := filepath.Join(leanDir, "Go2Lean", "Generated", "llvm")
	entries, err := os.ReadDir(modDir)
	if err != nil {
		return err
	}
	var imports, regs []string
	for _, e := range entries {
		if k, ok := strings.CutSuffix(e.Name(), ".lean"); ok {
			imports = append(imports, "import Go2Lean.Generated.llvm."+k)
			regs = append(regs, fmt.Sprintf("(%q, Go2Lean.Generated.llvm.%s.prog, Go2Lean.Generated.llvm.%s.globals)", k, k, k))
		}
	}
	files := map[string]string{
		"lakefile.toml": fmt.Sprintf(`# Written by go2lean: this workspace's LLVM models and their search driver.
name = "go2lean-llvm-models"
defaultTargets = ["go2lean-llvm-search"]

[[require]]
name = "go2lean-llvm"
path = %q

[[lean_lib]]
name = "Go2LeanLLVMModels"
globs = ["Go2Lean.Generated.+"]

[[lean_exe]]
name = "go2lean-llvm-search"
root = "Main"
`, pkg),
		"lean-toolchain": string(toolchain),
		filepath.Join("Go2Lean", "Generated", "Registry.lean"): fmt.Sprintf(
			"-- Generated by go2lean: every LLVM model of this workspace. Do not edit.\n%s\n\nnamespace Go2Lean.Generated\n\ndef llvmRegistry : List (String × Go2Lean.LLVM.Prog × List Go2Lean.LLVM.GlobalInit) := [%s]\n\nend Go2Lean.Generated\n",
			strings.Join(imports, "\n"), strings.Join(regs, ", ")),
		"Main.lean": `-- Written by go2lean: the search driver over this workspace's LLVM models.
import Go2LeanLLVM.Search
import Go2Lean.Generated.Registry

def main (args : List String) : IO UInt32 :=
  Go2Lean.LLVM.Search.driverMain Go2Lean.Generated.llvmRegistry args
`,
	}
	for name, content := range files {
		path := filepath.Join(leanDir, name)
		if old, err := os.ReadFile(path); err == nil && string(old) == content {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		if name == "lakefile.toml" {
			// lake keeps resolving path dependencies from its manifest.
			if err := os.Remove(filepath.Join(leanDir, "lake-manifest.json")); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func lake(dir string, args ...string) (string, error) {
	bin, err := exec.LookPath("lake")
	if err != nil {
		home, _ := os.UserHomeDir()
		bin = filepath.Join(home, ".elan", "bin", "lake")
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// replay builds the counterexample as a C program that includes the source
// and calls the function on the same inputs, with buffers from malloc of
// exactly their size, under ASan and UBSan.
func replay(ws contract.Workspace, src string, fn llvmfe.Func, cx *contract.Counterexample) (*contract.Verdict, error) {
	dir := ws.PocDir(cx.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// go2lean PoC for %s: %s\n#include <stdint.h>\n#include <stdlib.h>\n#include <string.h>\n", cx.ID, cx.Property)
	fmt.Fprintf(&b, "#define main go2lean_source_main\n#include %q\n#undef main\n\nint main(void) {\n", src)
	var args []string
	for i, g := range fn.Gens {
		v := fmt.Sprintf("a%d", i)
		switch g.K {
		case "buf":
			elems := cx.Memory[g.Name]
			ctype := map[int]string{1: "int8_t", 2: "int16_t", 4: "int32_t", 8: "int64_t"}[g.EB]
			if ctype == "" {
				ctype = "int8_t"
			}
			fmt.Fprintf(&b, "  void *%s = malloc(%d);\n", v, len(elems)*g.EB)
			if len(elems) > 0 {
				lits := make([]string, len(elems))
				for k, e := range elems {
					lits[k] = "(" + ctype + ")" + cInt(e)
				}
				fmt.Fprintf(&b, "  %s %s_init[] = {%s};\n  memcpy(%s, %s_init, sizeof %s_init);\n", ctype, v, strings.Join(lits, ", "), v, v, v)
			}
			args = append(args, v)
		default:
			fmt.Fprintf(&b, "  long long %s = %s;\n", v, cInt(cx.Inputs[g.Name]))
			args = append(args, v)
		}
	}
	fmt.Fprintf(&b, "  (void)%s(%s);\n  return 0;\n}\n", fn.Name, strings.Join(args, ", "))
	harness := filepath.Join(dir, "poc.c")
	if err := os.WriteFile(harness, []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	bin := filepath.Join(dir, "poc")
	v := &contract.Verdict{Counterexample: cx.ID, PocDir: dir}
	build := exec.Command("clang", "-g", "-O0", "-fsanitize=address,undefined", "-fno-sanitize-recover=all", "-fno-omit-frame-pointer", "-w", harness, "-o", bin)
	if out, err := build.CombinedOutput(); err != nil {
		v.Status = contract.VerdictInconclusive
		v.Evidence = contract.Evidence{Cmd: strings.Join(build.Args, " "), Exit: -1, StderrTail: tail(string(out))}
		v.Diagnosis = &contract.Diagnosis{Reason: "poc_build_failed"}
		return v, ws.WriteVerdict(v)
	}
	lim := sandbox.Default
	lim.MemBytes, lim.Wall = 0, 20*time.Second
	stdout, stderr, exit, err := sandbox.Run(bin, "", nil, []string{"ASAN_OPTIONS=detect_leaks=0", "UBSAN_OPTIONS=print_stacktrace=1"}, lim)
	if err != nil {
		return nil, err
	}
	v.Evidence = contract.Evidence{Cmd: bin, Exit: exit, StdoutTail: tail(stdout), StderrTail: tail(stderr)}
	report := sanitizerReport(stderr)
	switch {
	case report != "":
		v.Status = contract.VerdictConfirmed
		v.Evidence.PanicMsg = report
	case exit == 0:
		v.Status = contract.VerdictSpurious
		v.Diagnosis = &contract.Diagnosis{Reason: "no_ub_in_real_run"}
	default:
		v.Status = contract.VerdictInconclusive
		v.Diagnosis = &contract.Diagnosis{Reason: "crash_without_sanitizer_report"}
	}
	return v, ws.WriteVerdict(v)
}

// cInt is a decimal as a C long long literal (the minimum has no literal
// of its own).
func cInt(s string) string {
	if s == "-9223372036854775808" {
		return "(-9223372036854775807LL - 1)"
	}
	return s + "LL"
}

// sanitizerReport is the line where ASan or UBSan named the error.
func sanitizerReport(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "ERROR: AddressSanitizer") || strings.Contains(line, "runtime error:") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func tail(s string) string {
	if len(s) > 2000 {
		return s[len(s)-2000:]
	}
	return s
}
