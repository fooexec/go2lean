package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fooexec/go2lean/internal/agent"
)

func cmdAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	ws := workspaceFlag(fs)
	llmSpec := fs.String("llm", "claude", "LLM to use: claude, or mock:<file-or-dir> with scripted responses")
	model := fs.String("model", "", "model id for -llm claude (default claude-opus-5-5)")
	baseURL := fs.String("base-url", "", "Anthropic-compatible endpoint for -llm claude (default: the Anthropic API)")
	thinking := fs.Bool("thinking", true, "send adaptive thinking (turn off for providers that reject the parameter)")
	repairs := fs.Int("repairs", 2, "rounds spent repairing specs the frontend rejected")
	report := fs.Bool("report", true, "ask the agent for a report (and possibly revised specs) after the verdicts")
	guidance := fs.String("guidance", "", "file with guidance for the proposal round (bug classes, invariants, functions to target)")
	specFile := fs.String("spec-file", "", "name of the spec file the agent writes in the package (default go2lean_specs_test.go)")
	// On by default: specs drive the code under test through its
	// dependencies (an SDK's decoders and accessors), and a guessed result
	// there gives spurious counterexamples. The model grows (a stream
	// converter package: 17 s and 3 MB of Lean without, 98 s and 16 MB
	// with), which an agent run, hours long, absorbs.
	deps := fs.Bool("deps", true, "also model the functions of dependency packages the package calls (-deps=false: only the package)")
	// The agent's specs are few and target large functions (a trial on a
	// converter with a thousand generated fields takes seconds): a 10 s
	// budget left some of them with a handful of trials.
	fuzzTime := fs.Duration("fuzztime", 30*time.Second, "after the pipeline, fuzz each spec with go test -fuzz this long (0: off)")
	opts := searchFlags(fs, 120)
	fs.Parse(args)
	guidanceText := ""
	if *guidance != "" {
		b, err := os.ReadFile(*guidance)
		if err != nil {
			return err
		}
		guidanceText = string(b)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("agent: exactly one package pattern expected")
	}
	w, env, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	env.Deps = *deps
	llm, err := agent.New(*llmSpec, agent.ClaudeConfig{Model: *model, BaseURL: *baseURL, Thinking: *thinking})
	if err != nil {
		return err
	}
	return agent.Run(context.Background(), w, env, fs.Arg(0), llm, agent.Options{
		Search: opts.get(), MaxRepairs: *repairs, ReportRound: *report, Guidance: guidanceText, SpecFile: *specFile, FuzzTime: *fuzzTime,
	}, os.Stdout)
}
