package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/fooexec/go2lean/internal/agent"
	"github.com/fooexec/go2lean/internal/mine"
)

// cmdPropose drafts candidate properties from the evidence go2lean mine
// collected (#315 P2): go2lean propose -evidence evidence.json [dir].
func cmdPropose(args []string) error {
	fs := flag.NewFlagSet("propose", flag.ExitOnError)
	evidence := fs.String("evidence", "evidence.json", "the evidence go2lean mine wrote")
	out := fs.String("o", "candidates.json", "write the candidates here (- for stdout)")
	llmSpec := fs.String("llm", "claude", "LLM to use: claude, claude-code, or mock:<file-or-dir>")
	model := fs.String("model", "", "model id for -llm claude or claude-code")
	baseURL := fs.String("base-url", "", "Anthropic-compatible endpoint for -llm claude")
	thinking := fs.Bool("thinking", true, "send adaptive thinking")
	kinds := fs.String("kinds", "", "only evidence of these kinds, comma-separated (advisory,commit,rfc,doc)")
	limit := fs.Int("limit", 0, "only the first n evidence items (0: all)")
	fs.Parse(args)
	dir := "."
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	} else if fs.NArg() > 1 {
		return fmt.Errorf("propose: one module directory expected")
	}
	b, err := os.ReadFile(*evidence)
	if err != nil {
		return err
	}
	var r mine.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("%s: %v", *evidence, err)
	}
	r.Evidence = mine.Select(r.Evidence, *kinds, *limit)
	llm, err := agent.New(*llmSpec, agent.ClaudeConfig{Model: *model, BaseURL: *baseURL, Thinking: *thinking})
	if err != nil {
		return err
	}
	p, err := mine.Propose(context.Background(), llm, &r, dir)
	if p == nil {
		return err
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "go2lean propose:", err, "(writing what was proposed so far)")
	}
	fmt.Fprintf(os.Stderr, "go2lean propose: %d evidence items, %d candidates, %d dropped\n", len(r.Evidence), len(p.Candidates), len(p.Dropped))
	enc, _ := json.MarshalIndent(p, "", "  ")
	enc = append(enc, '\n')
	if *out == "-" {
		_, werr := os.Stdout.Write(enc)
		return werr
	}
	if werr := os.WriteFile(*out, enc, 0o644); werr != nil {
		return werr
	}
	fmt.Fprintf(os.Stderr, "go2lean propose: wrote %s\n", *out)
	return err
}
