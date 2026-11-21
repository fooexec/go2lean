package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/fooexec/go2lean/internal/zorya"
)

// cmdZorya runs Zorya on a compiled Go program and replays its reports on the
// program:
//
//	go2lean zorya run    -dir <zorya checkout> [-ghidra dir] [-seed aaaa] <binary>
//	go2lean zorya replay -results FOUND_SAT_STATE.json [-seed aaaa] <binary>
func cmdZorya(args []string) error {
	if len(args) == 0 || (args[0] != "run" && args[0] != "replay") {
		return fmt.Errorf("zorya: run or replay expected")
	}
	fs := flag.NewFlagSet("zorya "+args[0], flag.ExitOnError)
	dir := fs.String("dir", os.Getenv("ZORYA_DIR"), "the Zorya checkout (default $ZORYA_DIR)")
	ghidra := fs.String("ghidra", os.Getenv("GHIDRA_INSTALL_DIR"), "Ghidra installation (default $GHIDRA_INSTALL_DIR)")
	seed := fs.String("seed", "aaaaaaaa", "the first argument Zorya starts from: one symbolic byte per byte")
	results := fs.String("results", "", "replay: Zorya's FOUND_SAT_STATE.json")
	fs.Parse(args[1:])
	if fs.NArg() != 1 {
		return fmt.Errorf("zorya: exactly one binary expected")
	}
	var vs []zorya.Verdict
	var err error
	if args[0] == "run" {
		vs, err = zorya.Run(fs.Arg(0), zorya.Options{Dir: *dir, Ghidra: *ghidra, Seed: *seed})
	} else {
		var b []byte
		if b, err = os.ReadFile(*results); err == nil {
			var cands []zorya.Candidate
			if cands, err = zorya.ParseResults(b); err == nil {
				vs = zorya.Replay(fs.Arg(0), *seed, cands)
			}
		}
	}
	if err != nil {
		return err
	}
	panics := 0
	for _, v := range zorya.Sorted(vs) {
		state := "does not panic"
		switch {
		case !v.Replayed:
			state = "not replayed"
		case v.Panicked:
			state = "PANICS"
			panics++
		}
		fmt.Printf("%-15s branch %s -> %s: %q: %s\n", state, v.Candidate.Instruction, v.Candidate.Panic, v.Arg, v.Detail)
	}
	fmt.Printf("zorya: %d reports, %d reproduced as panics\n", len(vs), panics)
	return nil
}
