package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"

	"github.com/andreasfoo/go2lean/internal/constraint"
	"github.com/andreasfoo/go2lean/internal/contract"
	"github.com/andreasfoo/go2lean/internal/pipeline"
	"github.com/andreasfoo/go2lean/internal/poc"
)

// engineOptions defers reading the -engine flag until after Parse.
type engineOptions struct {
	*constraint.SearchOptions
	engine *string
}

func (e *engineOptions) get() constraint.SearchOptions {
	e.Engine = constraint.Engine(*e.engine)
	o := *e.SearchOptions
	if o.BudgetSeconds == 0 {
		o.BudgetSeconds = -1 // -budget 0: unlimited (0 in SearchOptions is the default)
	}
	return o
}

// searchFlags declares the search options; budget is the default -budget:
// short for sweeps over many properties, longer where there are few.
func searchFlags(fs *flag.FlagSet, budget int) *engineOptions {
	o := &constraint.SearchOptions{}
	engine := fs.String("engine", "all", "engine: random, exhaustive or all")
	fs.IntVar(&o.Seed, "seed", 42, "random seed")
	fs.IntVar(&o.Trials, "trials", 2000, "random trials per property")
	fs.IntVar(&o.Fuel, "fuel", 1000, "interpreter fuel (block entries + calls)")
	fs.IntVar(&o.MaxLen, "maxlen", 8, "maximum generated slice length")
	fs.IntVar(&o.ExhaustLen, "exhaust-len", 3, "maximum slice length in the exhaustive engine")
	fs.IntVar(&o.ExhaustLimit, "exhaust-limit", 300000, "maximum domain size the exhaustive engine attempts")
	fs.BoolVar(&o.NilInputs, "nil-inputs", false, "let top-level pointer arguments and receivers be nil")
	fs.IntVar(&o.Jobs, "jobs", 0, "properties searched in parallel (default: number of CPUs)")
	fs.IntVar(&o.BudgetSeconds, "budget", budget, "seconds each engine may spend on one property (0: unlimited)")
	fs.BoolVar(&o.Prove, "prove", true, "after an exhaustive pass on integer and bool parameters, try a Lean proof for every input")
	fs.BoolVar(&o.Mutate, "mutate", true, "for a spec that held, check that mutants of the code under test (a branch negated, a comparison or +/- swapped) fail it")
	fs.IntVar(&o.MaxMutants, "max-mutants", 24, "mutants tried per spec")
	return &engineOptions{o, engine}
}

func cmdProps(args []string) error {
	fs := flag.NewFlagSet("props", flag.ExitOnError)
	ws := workspaceFlag(fs)
	frontend := fs.String("frontend", "gossa", "frontend whose model report to use")
	pkg := fs.String("package", "", "import path of the compiled package")
	fs.Parse(args)
	if *pkg == "" {
		return fmt.Errorf("props: -package is required")
	}
	w, _, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	report, err := w.ModelReport(*frontend, *pkg)
	if err != nil {
		return err
	}
	_, err = pipeline.Propose(w, report, os.Stdout)
	return err
}

func cmdHunt(args []string) error {
	fs := flag.NewFlagSet("hunt", flag.ExitOnError)
	ws := workspaceFlag(fs)
	id := fs.String("property", "", "property id (default: all)")
	opts := searchFlags(fs, 10)
	fs.Parse(args)
	w, env, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	props, err := selectProps(w, *id)
	if err != nil {
		return err
	}
	_, err = pipeline.HuntAll(w, env, props, opts.get(), os.Stdout)
	return err
}

func selectProps(w contract.Workspace, id string) ([]*contract.Property, error) {
	if id != "" {
		p, err := w.Property(id)
		return []*contract.Property{p}, err
	}
	return w.Properties()
}

func cmdPoc(args []string) error {
	fs := flag.NewFlagSet("poc", flag.ExitOnError)
	ws := workspaceFlag(fs)
	id := fs.String("counterexample", "", "counterexample id (more ids may follow as arguments)")
	fs.Parse(args)
	ids := fs.Args()
	if *id != "" {
		ids = append([]string{*id}, ids...)
	}
	if len(ids) == 0 {
		return fmt.Errorf("poc: at least one counterexample id is required")
	}
	w, env, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	_, err = pipeline.VerifyAll(w, env, ids, os.Stdout)
	return err
}

func cmdRank(args []string) error {
	fs := flag.NewFlagSet("rank", flag.ExitOnError)
	ws := workspaceFlag(fs)
	fs.Parse(args)
	w, env, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	root := env.Module
	if root == "" {
		root = env.Root
	}
	vs, err := poc.RankAll(w, root)
	if err != nil {
		return err
	}
	count := map[int]int{}
	for _, v := range vs {
		count[v.Rank.Tier]++
		fmt.Printf("%d  %s\n   %s\n", v.Rank.Tier, v.Counterexample, v.Rank.Reason)
		for _, s := range v.Rank.Sites {
			fmt.Printf("     %s\n", s)
		}
	}
	fmt.Printf("\n%d confirmed: tier 1 %d, tier 2 %d, tier 3 %d, tier 4 %d\n", len(vs), count[1], count[2], count[3], count[4])
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	ws := workspaceFlag(fs)
	opts := searchFlags(fs, 10)
	deps := fs.Bool("deps", false, "also model the functions of dependency packages the compiled packages call")
	resume := fs.Bool("resume", false, "skip properties already searched and counterexamples already verified in the workspace")
	fuzzSpecs := fs.Bool("fuzz-specs", true, "read the packages' Go fuzz targets (FuzzXxx) as specs")
	tests := fs.Bool("tests", false, "make the packages' test functions (TestXxx(t *testing.T)) targets")
	specsOnly := fs.Bool("specs-only", false, "search the packages' specs only, not the no_panic templates")
	runRe := fs.String("run", "", "search only the properties of target functions matching this regexp (as go test -run; methods as T.M)")
	compareOrder := fs.String("compare-order", "", "explicitly select a pure, deterministic, total func(string,string) int ordering comparator (experimental; one package)")
	refineRounds := fs.Int("refine", 2, "with -deps, rounds of the refinement loop: translate the dependency functions whose unmodelled calls made a counterexample spurious or inconclusive, then search and replay again (0: off)")
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("run: at least one package pattern expected (e.g. ./...)")
	}
	w, env, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	env.Deps, env.Resume, env.FuzzSpecs, env.Tests, env.SpecsOnly = *deps, *resume, *fuzzSpecs, *tests, *specsOnly
	env.Refine = *refineRounds
	if *runRe != "" {
		re, err := regexp.Compile(*runRe)
		if err != nil {
			return fmt.Errorf("run: -run: %v", err)
		}
		env.Run = re
	}
	if *compareOrder != "" {
		env, err = pipeline.ConfigureCompareOrder(w, env, *compareOrder, fs.Args()...)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "compare-order opt-in: asserting a pure, deterministic, total three-way ordering over all byte strings")
	}
	_, err = pipeline.RunAll(w, env, opts.get(), os.Stdout, fs.Args()...)
	return err
}
