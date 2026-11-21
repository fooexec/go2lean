package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/fooexec/go2lean/internal/symexec"
)

// cmdSymexec runs the bounded symbolic executor on the functions of a package
// and replays what it finds on the real code:
//
//	go2lean symexec [-module dir] [-func name,name] [-replay] <package>
func cmdSymexec(args []string) error {
	fs := flag.NewFlagSet("symexec", flag.ExitOnError)
	module := fs.String("module", ".", "directory of the Go module")
	only := fs.String("func", "", "comma-separated function names (default: every supported function)")
	replay := fs.Bool("replay", true, "run the inputs of each finding on the real function")
	loop := fs.Int("loop", 0, "visits of one block on a path (default 6)")
	depth := fs.Int("depth", 0, "inlined call depth (default 3)")
	maxLen := fs.Int("maxlen", 0, "length bound of slice parameters (default 12)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("symexec: exactly one package expected")
	}
	pkg, err := symexec.Load(*module, fs.Arg(0))
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n != "" {
			want[n] = true
		}
	}
	var all []symexec.Finding
	for _, fn := range pkg.Functions() {
		if len(want) > 0 && !want[fn.Name()] {
			continue
		}
		finds, err := symexec.Explore(fn, symexec.Options{LoopBound: *loop, CallDepth: *depth, MaxLen: *maxLen, Root: pkg.Root()})
		if err != nil {
			fmt.Printf("%s: %v\n", fn.Name(), err)
			continue
		}
		all = append(all, finds...)
	}
	total, confirmed := len(all), 0
	var okv []bool
	var msgs []string
	if *replay {
		okv, msgs = pkg.Replay(all)
	}
	for i, f := range all {
		var in []string
		for _, x := range f.Inputs {
			in = append(in, x.Name+"="+x.Go)
		}
		verdict := ""
		if *replay {
			if okv[i] {
				confirmed++
				verdict = "  REPRODUCED: " + msgs[i]
			} else {
				verdict = "  not reproduced (" + msgs[i] + ")"
			}
		}
		approx := ""
		if f.Approximate {
			approx = " [approximate path]"
		}
		fmt.Printf("%s: %s at %s:%d with %s%s%s\n", f.Func, f.Kind, shortPath(f.Pos.Filename), f.Pos.Line, strings.Join(in, ", "), approx, verdict)
	}
	fmt.Printf("symexec: %d candidate panics, %d reproduced\n", total, confirmed)
	return nil
}

func shortPath(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
