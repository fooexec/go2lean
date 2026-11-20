package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fooexec/go2lean/internal/contract"
	llvmfe "github.com/fooexec/go2lean/internal/frontend/llvm"
	"github.com/fooexec/go2lean/internal/llvmhunt"
	"github.com/fooexec/go2lean/internal/repo"
)

// cmdLLVM runs the LLVM line on C or LLVM IR files.
func cmdLLVM(args []string) error {
	fs := flag.NewFlagSet("llvm", flag.ExitOnError)
	workspace := fs.String("workspace", "out", "workspace directory for JSON records")
	trials := fs.Int("trials", 2000, "random trials per function")
	seed := fs.Int("seed", 42, "random seed")
	fuel := fs.Int("fuel", 5000, "interpreter fuel (block entries + calls)")
	engine := fs.String("engine", "all", "engine: random, exhaustive or all")
	exhaustLen := fs.Int("exhaust-len", 3, "maximum buffer length in the exhaustive engine")
	internal := fs.Bool("internal", false, "also search static functions (by default only the externally visible ones)")
	specsOnly := fs.Bool("specs-only", false, "search only the functions marked annotate(\"go2lean:spec\")")
	memory := fs.Bool("memory", false, "memory errors only (out of bounds, use after free, double free): signed arithmetic wraps and the rest of UBSan is off")
	lib := fs.String("lib", "", "more C files of the library, comma-separated globs, linked with the source (a spec file that includes the library's headers)")
	cflags := fs.String("cflags", "", "flags for every compilation, comma-separated (-Iinclude,-DX=1)")
	fuzzFor := fs.Duration("fuzz", 0, "run libFuzzer this long on each function the model search did not settle (e.g. 60s)")
	fuzzConfirmed := fs.Bool("fuzz-confirmed", false, "with -fuzz: also fuzz the functions whose violation the model search confirmed (to compare the two)")
	fs.Parse(args)
	if !llvmfe.Available {
		return errors.New("this go2lean was built without the LLVM frontend: rebuild with -tags llvm18 (or your LLVM version's tag)")
	}
	if fs.NArg() == 0 {
		return errors.New("llvm: want a .c, .ll or .bc file")
	}
	root, err := repo.Root()
	if err != nil {
		return err
	}
	ws := contract.Workspace{Root: *workspace}
	opts := llvmhunt.Options{FuzzConfirmed: *fuzzConfirmed, Trials: *trials, Seed: *seed, Fuel: *fuel, Engine: *engine, ExhaustLen: *exhaustLen, Init: -1, Internal: *internal, SpecsOnly: *specsOnly, Fuzz: *fuzzFor, Memory: *memory, CFlags: splitList(*cflags)}
	for _, pat := range splitList(*lib) {
		ms, err := filepath.Glob(pat)
		if err != nil || len(ms) == 0 {
			return fmt.Errorf("llvm: -lib %s matches no file", pat)
		}
		for _, m := range ms {
			abs, _ := filepath.Abs(m)
			opts.Lib = append(opts.Lib, abs)
		}
	}
	for _, src := range fs.Args() {
		if err := llvmhunt.Run(root, ws, src, opts, os.Stdout); err != nil {
			return err
		}
	}
	return nil
}

// splitList splits a comma-separated flag value, dropping empty items.
func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}
