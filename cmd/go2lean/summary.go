package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fooexec/go2lean/internal/pipeline"
)

func cmdSummary(args []string) error {
	fs := flag.NewFlagSet("summary", flag.ExitOnError)
	ws := workspaceFlag(fs)
	fs.Parse(args)
	w, _, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	s, err := pipeline.Summarize(w)
	if err != nil {
		return err
	}
	s.WriteMarkdown(os.Stdout)
	path := filepath.Join(w.Root, "summary.md")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	s.WriteMarkdown(f)
	fmt.Fprintf(os.Stderr, "written to %s\n", path)
	return nil
}
