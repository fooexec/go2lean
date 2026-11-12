// Command go2lean drives the three modules: compile (M1), props/hunt (M2),
// poc (M3) and run (all three).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/pipeline"
	"github.com/fooexec/go2lean/internal/repo"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	limitMemory()
	startMemProfile()
	var err error
	switch os.Args[1] {
	case "version", "-version", "--version":
		fmt.Println("go2lean", buildVersion())
		return
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "compile":
		err = cmdCompile(os.Args[2:])
	case "props":
		err = cmdProps(os.Args[2:])
	case "hunt":
		err = cmdHunt(os.Args[2:])
	case "poc":
		err = cmdPoc(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "rank":
		err = cmdRank(os.Args[2:])
	case "agent":
		err = cmdAgent(os.Args[2:])
	case "fuzz":
		err = cmdFuzz(os.Args[2:])
	case "prove":
		err = cmdProve(os.Args[2:])
	case "summary":
		err = cmdSummary(os.Args[2:])
	case "llvm":
		err = cmdLLVM(os.Args[2:])
	case "tinygo":
		err = cmdTinyGo(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "go2lean:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  go2lean version                              the version (a release tag, or the commit it was built from)
  go2lean doctor [-deps [./...]]               diagnose prerequisites; optional offline target dependency check
  common flags: -workspace out   -module <dir of the Go module to analyse>
  go2lean compile [-workspace out] <packages...>  compile stage: Go -> Lean model, ABI, targets (./... allowed)
  go2lean props   [-workspace out]                spec & search stage: template properties from the model report
  go2lean hunt    [-workspace out] [-property id] spec & search stage: search for counterexamples
  go2lean poc     [-workspace out] -counterexample id   replay stage: realise and verify in Go
  go2lean run     [-workspace out] <package>      all of the above
  experimental: compile/run -compare-order Func <one package>
                explicitly assert pure, deterministic, total func(string,string) int ordering;
                default native 64-bit build, no go.work/GOFLAGS, fresh dedicated workspace
  go2lean fuzz    [-workspace out] [-fuzztime 30s] [-spec-file f] <package>   the package's specs as go test -fuzz targets
  go2lean prove   [-workspace out] -goals f.json | -func F -post P  <package>
                                                  state functional theorems (wpFun contracts) and prove them
  go2lean rank    [-workspace out]                 order confirmed verdicts by how plausible their input is (tiers 1-4)
  go2lean summary [-workspace out]                 all verdicts and proofs of a workspace as Markdown
  go2lean agent   [-workspace out] [-llm claude|mock:<file>] <package>
                                                  agent: propose spec-in-Go properties, run, report
  go2lean llvm    [-workspace out] <file.c|.ll|.bc> C / LLVM IR: model every function, search for UB,
                                                  replay counterexamples under ASan/UBSan (build with -tags llvm18)
  go2lean tinygo  [-workspace out] [-compare] <package>  Go through TinyGo's LLVM IR (-tags llvm22);
                                                  -compare: also the go/ssa line, side by side`)
	os.Exit(2)
}

// commonFlags are shared by every subcommand: where records go and which Go
// module is analysed.
type commonFlags struct {
	workspace *string
	module    *string
}

func workspaceFlag(fs *flag.FlagSet) *commonFlags {
	return &commonFlags{
		workspace: fs.String("workspace", "out", "workspace directory for JSON records"),
		module:    fs.String("module", "", "directory of the Go module to analyse (default: current module)"),
	}
}

// openWorkspace resolves paths from the invoking directory. The trusted Lean
// runtime is bundled; -module selects the module for loading and Go replay.
func openWorkspace(cf *commonFlags) (contract.Workspace, pipeline.Env, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return contract.Workspace{}, pipeline.Env{}, err
	}
	root, err := repo.Root()
	if err != nil {
		root = cwd // -module also works from a directory with no go.mod
	}
	path := *cf.workspace
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	env := pipeline.Env{Root: root}
	if *cf.module != "" {
		if env.Module, err = filepath.Abs(*cf.module); err != nil {
			return contract.Workspace{}, pipeline.Env{}, err
		}
	}
	w := contract.Workspace{Root: path}
	return w, env, w.Init()
}

// requireModule says, before the packages load, when the analysed
// directory is in no Go module: Go's loader would only report "directory
// prefix . does not contain main module" (kballard/go-shellquote has no
// go.mod).
func requireModule(env pipeline.Env) error {
	dir := env.Module
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	for d := dir; ; d = filepath.Dir(d) {
		for _, f := range []string{"go.mod", "go.work"} {
			if _, err := os.Stat(filepath.Join(d, f)); err == nil {
				return nil
			}
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return fmt.Errorf("no go.mod in %s or a directory above it: go2lean analyses a Go module; create one with `go mod init <module path>` and `go mod tidy`", dir)
}

func cmdCompile(args []string) error {
	fs := flag.NewFlagSet("compile", flag.ExitOnError)
	ws := workspaceFlag(fs)
	deps := fs.Bool("deps", true, "also model the functions of dependency packages the compiled packages call, and the standard-library code the model translates from source (-deps=false: only the compiled packages; dependency calls are havoc)")
	tests := fs.Bool("tests", false, "make the packages' test functions (TestXxx(t *testing.T)) targets")
	compareOrder := fs.String("compare-order", "", "explicitly select a pure, deterministic, total func(string,string) int ordering comparator (experimental; one package)")
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("compile: at least one package pattern expected (e.g. ./...)")
	}
	w, env, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	if err := requireModule(env); err != nil {
		return err
	}
	env.Deps, env.Tests = *deps, *tests
	if *compareOrder != "" {
		env, err = pipeline.ConfigureCompareOrder(w, env, *compareOrder, fs.Args()...)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "compare-order opt-in: asserting a pure, deterministic, total three-way ordering over all byte strings")
	}
	_, err = pipeline.CompileAll(w, env, os.Stdout, fs.Args()...)
	return err
}
