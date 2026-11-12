package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fooexec/go2lean/internal/fuzzexport"
)

// cmdFuzz hands the spec properties of a package to Go's fuzzer.
func cmdFuzz(args []string) error {
	fs := flag.NewFlagSet("fuzz", flag.ExitOnError)
	ws := workspaceFlag(fs)
	fuzzTime := fs.Duration("fuzztime", 30*time.Second, "how long each target is fuzzed")
	prop := fs.String("property", "", "fuzz only this property")
	specFile := fs.String("spec-file", "", "a spec file to add to the package for the build (e.g. the agent's, from <workspace>/agent/<pkg>/)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("fuzz: exactly one package import path expected")
	}
	w, env, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	opts := fuzzexport.Options{FuzzTime: *fuzzTime, Property: *prop}
	if *specFile != "" {
		b, err := os.ReadFile(*specFile)
		if err != nil {
			return err
		}
		opts.Overlay = map[string][]byte{filepath.Base(*specFile): b}
	}
	root := env.Root
	if env.Module != "" {
		root = env.Module
	}
	results, skipped, err := fuzzexport.Run(w, root, fs.Arg(0), opts)
	for _, s := range skipped {
		fmt.Printf("  %-60s skipped: %s\n", s.Property, s.Reason)
	}
	for _, r := range results {
		fmt.Printf("  %-60s %-8s %5.0fs %s execs\n", r.Property, r.Status, r.Seconds, r.Execs)
		if r.Status != "held" {
			fmt.Printf("      %s\n", r.Message)
		}
		if r.Reproduce != "" {
			fmt.Printf("      %s\n", r.Reproduce)
		}
	}
	return err
}
