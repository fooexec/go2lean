package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fooexec/go2lean/internal/contract"
	llvmfe "github.com/fooexec/go2lean/internal/frontend/llvm"
	"github.com/fooexec/go2lean/internal/llvmhunt"
	"github.com/fooexec/go2lean/internal/pipeline"
)

// cmdTinyGo runs a Go package through TinyGo's LLVM IR and, with -compare,
// through the go/ssa line too, and prints the two side by side.
func cmdTinyGo(args []string) error {
	fs := flag.NewFlagSet("tinygo", flag.ExitOnError)
	ws := workspaceFlag(fs)
	opts := searchFlags(fs, 10)
	compare := fs.Bool("compare", false, "also run the go/ssa line on the package and compare")
	fs.Parse(args)
	if !llvmfe.Available {
		return errors.New("this go2lean was built without the LLVM frontend: rebuild with -tags llvm22 (TinyGo's LLVM)")
	}
	if fs.NArg() != 1 {
		return errors.New("tinygo: one package expected")
	}
	w, env, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	module := env.Module
	if module == "" {
		module = env.Root
	}
	o := opts.get()
	lo := llvmhunt.Options{Trials: o.Trials, Seed: o.Seed, Fuel: 5000, Engine: string(o.Engine), ExhaustLen: o.ExhaustLen}
	fmt.Println("== TinyGo -> LLVM -> Lean")
	tg, err := llvmhunt.RunTinyGo(env.Root, w, module, fs.Arg(0), lo, os.Stdout)
	if err != nil {
		return err
	}
	if !*compare {
		return nil
	}
	fmt.Println("== go/ssa line")
	// Model dependency packages too, as TinyGo compiles the whole program.
	env.Deps = true
	results, err := pipeline.RunAll(w, env, o, os.Stdout, fs.Arg(0))
	if err != nil {
		return err
	}
	ssa := map[string]string{}
	for _, r := range results {
		for _, p := range r.Properties {
			if p.Kind != contract.KindNoPanic {
				continue
			}
			t, err := w.Target(p.Target)
			if err != nil || strings.Contains(t.Func, ".") {
				continue
			}
			ssa[t.Func] = goLineStatus(w, p)
		}
	}
	fmt.Println("\n== comparison (no_panic; verdicts after replay in Go)")
	fmt.Printf("%-24s %-38s %-38s %s\n", "function", "go/ssa", "tinygo/llvm", "")
	agree, differ, partial := 0, 0, 0
	for _, r := range tg {
		t := r.Status
		if r.Status == "panic" {
			t = "panic " + string(r.Panic) + " (" + r.Verdict + ")"
		}
		g := ssa[r.Func]
		if g == "" {
			g = "not modelled"
		}
		gb, tb := judged(g), judged(t)
		mark := "agree"
		switch {
		case gb == "?" || tb == "?":
			mark = "-"
			partial++
		case gb != tb:
			mark = "DIFFER"
			differ++
		default:
			agree++
		}
		fmt.Printf("%-24s %-38s %-38s %s\n", r.Func, g, t, mark)
	}
	fmt.Printf("\n%d agree, %d differ, %d not comparable (a line did not model the function)\n", agree, differ, partial)
	return nil
}

// judged is a line's result after replay: "bug" (a panic Go confirmed),
// "none" (no counterexample, or only spurious ones), or "?" (not decided:
// the model got stuck, or a counterexample was not replayed).
func judged(s string) string {
	switch {
	case strings.Contains(s, "confirmed"):
		return "bug"
	case strings.Contains(s, "spurious") || isClean(s):
		return "none"
	}
	return "?"
}

func isClean(s string) bool { return s == "holds" || strings.HasPrefix(s, "exhausted") }

// goLineStatus summarises the go/ssa line's result on a no_panic property.
func goLineStatus(w contract.Workspace, p *contract.Property) string {
	h := w.HuntRecord(p.ID)
	if h == nil {
		return "not searched"
	}
	if h.Counterexample == "" {
		if strings.Contains(h.Summary, "recorded as exhausted_assuming") {
			return "exhausted_assuming"
		}
		if strings.Contains(h.Summary, "exhausted") || strings.Contains(h.Summary, "holds on") {
			return "exhausted"
		}
		return "holds"
	}
	cx, err := w.Counterexample(h.Counterexample)
	if err != nil || cx.ModelOutput == nil {
		return "panic ?"
	}
	s := "panic " + string(cx.ModelOutput.Panic)
	if v := w.Verdict(cx.ID); v != nil {
		s += " (" + string(v.Status) + ")"
	}
	return s
}
