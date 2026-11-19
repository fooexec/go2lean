package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/fooexec/go2lean/internal/advisory"
	"github.com/fooexec/go2lean/internal/agent"
)

// cmdAdvisory drafts a catalog property (docs/specs) from a published
// advisory: go2lean advisory [-o file] GHSA-xxxx-xxxx-xxxx
func cmdAdvisory(args []string) error {
	fs := flag.NewFlagSet("advisory", flag.ExitOnError)
	llmSpec := fs.String("llm", "claude", "LLM to use: claude, claude-code, or mock:<file>")
	model := fs.String("model", "", "model id for -llm claude (default claude-opus-5-5)")
	baseURL := fs.String("base-url", "", "Anthropic-compatible endpoint for -llm claude")
	thinking := fs.Bool("thinking", true, "send adaptive thinking")
	out := fs.String("o", "", "write the draft here (default: stdout)")
	from := fs.String("from", "", "read the OSV record from this file instead of api.osv.dev")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("advisory: exactly one advisory id expected (GHSA-..., CVE-..., GO-...)")
	}
	var a *advisory.Advisory
	var err error
	if *from != "" {
		b, rerr := os.ReadFile(*from)
		if rerr != nil {
			return rerr
		}
		a, err = advisory.Parse(b)
	} else {
		a, err = advisory.Fetch(context.Background(), fs.Arg(0))
	}
	if err != nil {
		return err
	}
	llm, err := agent.New(*llmSpec, agent.ClaudeConfig{Model: *model, BaseURL: *baseURL, Thinking: *thinking})
	if err != nil {
		return err
	}
	d, err := advisory.Draft(context.Background(), llm, a)
	if err != nil {
		return err
	}
	if !advisory.Checkable(d) {
		fmt.Fprintln(os.Stderr, "go2lean advisory:", d)
		return fmt.Errorf("%s is not a property that can be checked", a.ID)
	}
	if *out == "" {
		fmt.Print(d)
		return nil
	}
	return os.WriteFile(*out, []byte(d), 0o644)
}
