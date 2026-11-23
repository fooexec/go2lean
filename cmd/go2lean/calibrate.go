package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fooexec/go2lean/internal/mine"
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
	cals, err := mine.Calibrate(absMod, pkg, absSpec, *specBefore, p.Candidates, mine.GoRunner(bin, strings.Fields(*runFlags)...), dir)
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
