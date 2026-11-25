package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fooexec/go2lean/internal/mine"
	"github.com/fooexec/go2lean/internal/repo"
)

// cmdCalibrate checks the specs written for candidates drafted from a fix
// (#315 P3): each must be violated before the fix and hold after it.
// go2lean calibrate -candidates candidates.json -spec ./http3/x_test.go [module].
func cmdCalibrate(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	cands := fs.String("candidates", "candidates.json", "the candidates go2lean propose wrote")
	spec := fs.String("spec", "", "the spec file, in the package it checks; its specs name their candidate (\"candidate c001\") in their doc comments")
	out := fs.String("o", "calibration.json", "write the results here (- for stdout)")
	specBefore := fs.String("spec-before", "", "the spec file for the revision before the fix, when the fix changed a signature the specs call: the same specs, written against the code before it (installed under -spec's name)")
	scratch := fs.String("scratch", "", "directory for the worktrees (default: a temporary one)")
	runFlags := fs.String("run-flags", "-trials 50000 -budget 600 -mutate=false", "flags for go2lean run on each revision (space-separated): a bug a fix removed is often rare, so the default search is larger than run's")
	cflags := fs.String("cflags", "", "a C spec: flags for every compilation, comma-separated (as go2lean llvm -cflags)")
	ldflags := fs.String("ldflags", "", "a C spec: flags for the links of the replay and the fuzzer (as go2lean llvm -ldflags)")
	maxLen := fs.Int("maxlen", 32, "a C spec: maximum elements of a generated buffer")
	atHead := fs.Bool("at-head", true, "a C spec: calibrate on the current code, with the fix reversed for the revision before it (a fix's own revision often no longer builds against the tree); false: check out the fix's files at its revisions")
	fuzzFor := fs.String("fuzz", "60s", "a C spec: libFuzzer time on each spec the model search did not settle (0: none)")
	fs.Parse(args)
	module := "."
	if fs.NArg() == 1 {
		module = fs.Arg(0)
	} else if fs.NArg() > 1 {
		return fmt.Errorf("calibrate: one module directory expected")
	}
	if *spec == "" {
		return fmt.Errorf("calibrate: -spec is required")
	}
	b, err := os.ReadFile(*cands)
	if err != nil {
		return err
	}
	var p mine.Proposal
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("%s: %v", *cands, err)
	}
	absMod, err := filepath.Abs(module)
	if err != nil {
		return err
	}
	absSpec, err := filepath.Abs(*spec)
	if err != nil {
		return err
	}
	pkg, err := filepath.Rel(absMod, filepath.Dir(absSpec))
	if err != nil || strings.HasPrefix(pkg, "..") {
		return fmt.Errorf("calibrate: %s is not in module %s", *spec, module)
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	dir := *scratch
	if dir == "" {
		if dir, err = os.MkdirTemp("", "go2lean-calibrate-wt-"); err != nil {
			return err
		}
		defer os.RemoveAll(dir)
	}
	var cals []mine.Calibration
	if strings.HasSuffix(absSpec, ".c") {
		// A C spec, on the LLVM line, in the project's own configured tree
		// (a worktree lacks the build's generated files): the files the fix
		// changes are checked out at each revision and put back.
		root, rerr := repo.Root()
		if rerr != nil {
			return fmt.Errorf("calibrate: a C spec runs go2lean llvm, which runs from the go2lean checkout (#335): %v", rerr)
		}
		extra := []string{"-maxlen", strconv.Itoa(*maxLen)}
		if *cflags != "" {
			extra = append(extra, "-cflags", *cflags)
		}
		if *ldflags != "" {
			extra = append(extra, "-ldflags", *ldflags)
		}
		if *fuzzFor != "" && *fuzzFor != "0" {
			extra = append(extra, "-fuzz", *fuzzFor)
		}
		if *atHead {
			cals, err = mine.CalibrateAtHead(absMod, pkg, absSpec, p.Candidates, mine.LLVMRunner(bin, root, extra...))
		} else {
			cals, err = mine.CalibrateInTree(absMod, pkg, absSpec, *specBefore, p.Candidates, mine.LLVMRunner(bin, root, extra...))
		}
	} else {
		cals, err = mine.Calibrate(absMod, pkg, absSpec, *specBefore, p.Candidates, mine.GoRunner(bin, strings.Fields(*runFlags)...), dir)
	}
	passed := 0
	for _, c := range cals {
		mark := "FAIL"
		if c.Passed {
			mark, passed = "ok  ", passed+1
		}
		fmt.Fprintf(os.Stderr, "%s %s %s: %s\n", mark, c.Candidate, strings.Join(c.Specs, ","), c.Why)
		if !c.Passed {
			for _, s := range c.Specs {
				for _, at := range []struct {
					when string
					r    mine.Result
				}{{"before", c.AtBefore[s]}, {"after", c.AtAfter[s]}} {
					if at.r.Search != "" {
						fmt.Fprintf(os.Stderr, "       %s %s: %s\n", s, at.when, at.r.Search)
					}
				}
			}
		}
	}
	fmt.Fprintf(os.Stderr, "go2lean calibrate: %d of %d candidates calibrated\n", passed, len(cals))
	enc, _ := json.MarshalIndent(cals, "", "  ")
	enc = append(enc, '\n')
	if *out == "-" {
		os.Stdout.Write(enc)
	} else if werr := os.WriteFile(*out, enc, 0o644); werr != nil {
		return werr
	}
	return err
}
