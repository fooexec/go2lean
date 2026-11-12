package main

import (
	"errors"
	"flag"
	"os"

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
	opts := llvmhunt.Options{Trials: *trials, Seed: *seed, Fuel: *fuel, Engine: *engine, ExhaustLen: *exhaustLen, Init: -1}
	for _, src := range fs.Args() {
		if err := llvmhunt.Run(root, ws, src, opts, os.Stdout); err != nil {
			return err
		}
	}
	return nil
}
