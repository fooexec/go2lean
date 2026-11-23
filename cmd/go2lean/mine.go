package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fooexec/go2lean/internal/mine"
)

// cmdMine collects the evidence properties are drafted from (#315): the
// module's advisories and those of similar projects, the fix commits of its
// history, the RFC sections its code cites and the normative sentences of
// its documentation.
func cmdMine(args []string) error {
	fs := flag.NewFlagSet("mine", flag.ExitOnError)
	out := fs.String("o", "evidence.json", "write the evidence here (- for stdout)")
	module := fs.String("module-path", "", "the module path (default: read from go.mod)")
	maxCommits := fs.Int("max-commits", 2000, "how many commits of the history to read")
	similar := fs.String("similar", "", "comma-separated projects whose advisories count too: Go module paths, or ecosystem:name (crates.io:quiche)")
	offline := fs.Bool("offline", false, "do not query OSV or fetch RFC texts")
	rfcCache := fs.String("rfc-cache", defaultRFCCache(), "directory the fetched RFC texts are kept in (empty: none)")
	ghRepo := fs.String("github-repo", "", "the project's owner/name on GitHub, for its merged pull requests and open bug issues (default: the origin remote's; GITHUB_TOKEN authenticates the search)")
	ghJSON := fs.String("github-json", "", "read the issues and pull requests from this file of search API results (gh api --paginate search/issues ...) instead of searching")
	maxIssues := fs.Int("max-issues", 300, "how many merged pull requests, and how many open issues, to read")
	fs.Parse(args)
	dir := "."
	if fs.NArg() > 1 {
		return fmt.Errorf("mine: one module directory expected")
	}
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	var sim []string
	for _, s := range strings.Split(*similar, ",") {
		if s = strings.TrimSpace(s); s != "" {
			sim = append(sim, s)
		}
	}
	r, err := mine.Collect(context.Background(), mine.Options{
		Dir: dir, Module: *module, MaxCommits: *maxCommits, Similar: sim, Offline: *offline, RFCCache: *rfcCache,
		GitHubRepo: *ghRepo, GitHubFile: *ghJSON, GitHubToken: os.Getenv("GITHUB_TOKEN"), MaxIssues: *maxIssues,
	})
	if err != nil {
		return err
	}
	for _, n := range r.Notes {
		fmt.Fprintln(os.Stderr, "go2lean mine:", n)
	}
	fmt.Fprintf(os.Stderr, "go2lean mine: %s: %s\n", r.Module, r.Summary())
	if *out == "-" {
		return r.Write(os.Stdout)
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := r.Write(f); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "go2lean mine: wrote %s\n", *out)
	return nil
}

func defaultRFCCache() string {
	if d, err := os.UserCacheDir(); err == nil {
		return d + "/go2lean/rfc"
	}
	return ""
}
