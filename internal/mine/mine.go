// Package mine collects the evidence go2lean derives properties from
// (#315): what a project records about itself and what similar projects
// published. Its advisories, its fix commits, the RFC sections its code
// cites and the normative sentences of its documentation each become an
// Evidence item: the text a property can be drafted from, the files it
// concerns, and, when it records a fix, the revisions before and after it,
// on which a spec drafted from it is calibrated. Collecting is
// deterministic; the LLM steps that read the evidence come after.
package mine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Kinds of evidence.
const (
	KindAdvisory = "advisory" // a published advisory (OSV)
	KindCommit   = "commit"   // a commit of the project's history that looks like a fix
	KindRFC      = "rfc"      // an RFC section the code cites
	KindDoc      = "doc"      // a normative sentence of the documentation
)

// Evidence is one source a property can be drafted from.
type Evidence struct {
	Kind    string   `json:"kind"`
	Ref     string   `json:"ref"`               // GHSA id, commit hash, "RFC 9000 13.2", file:line
	Aliases []string `json:"aliases,omitempty"` // other ids of the same advisory (CVE, GO-, RUSTSEC-)
	Title   string   `json:"title"`             // one line
	Text    string   `json:"text"`              // the body, bounded (maxText)
	// Project is the module or package the evidence is about: the target
	// itself, or a similar project (an advisory of another implementation).
	Project string   `json:"project,omitempty"`
	Paths   []string `json:"paths,omitempty"` // the target's files it concerns
	// Before and After are the revisions without and with the fix it
	// records (commits, or module versions for an advisory); empty when it
	// records none (an RFC rule, a documented contract).
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Cites are the places in the target's code that cite this evidence
	// (an RFC section), as file:line.
	Cites []string `json:"cites,omitempty"`
}

// maxText bounds an item's text: enough for a commit's message and the
// start of its diff, or an RFC section.
const maxText = 6000

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[... cut]"
}

// Options say where to look.
type Options struct {
	Dir    string // the module's directory (a git checkout, for the history)
	Module string // its module path (for the advisories); read from go.mod when empty
	// MaxCommits bounds the history read (0: 2000).
	MaxCommits int
	// Similar are other projects whose advisories count, as
	// "ecosystem:name" (crates.io:quiche) or a Go module path.
	Similar []string
	// Offline skips everything that needs the network (advisories, RFC
	// texts); the citations are still collected.
	Offline bool
	// RFCCache is where fetched RFC texts are kept ("" : no cache).
	RFCCache string
	// OSV and RFCBase override the services' base URLs (tests).
	OSV, RFCBase string
	// GitHubRepo is the project's owner/name on GitHub, for its issues and
	// pull requests (default: from the origin remote); GitHubToken
	// authenticates the search (optional), GitHubBase overrides the API's
	// URL (tests), MaxIssues bounds each search (0: 300).
	GitHubRepo, GitHubToken, GitHubBase string
	MaxIssues                           int
	// GitHubFile, when set, is read for the issues and pull requests (search
	// API results, as `gh api --paginate search/issues` writes them) in
	// place of the search.
	GitHubFile string
}

// Report is the output: the evidence and what each collector did.
type Report struct {
	Module   string     `json:"module"`
	Evidence []Evidence `json:"evidence"`
	Notes    []string   `json:"notes,omitempty"` // collectors skipped or failed, and why
}

// Collect runs every collector. A collector that fails (no git, no
// network) leaves a note and the others still run.
func Collect(ctx context.Context, o Options) (*Report, error) {
	if o.Module == "" {
		m, err := modulePath(o.Dir)
		if err != nil {
			return nil, err
		}
		o.Module = m
	}
	r := &Report{Module: o.Module}
	note := func(format string, a ...any) { r.Notes = append(r.Notes, fmt.Sprintf(format, a...)) }

	if ev, err := History(o.Dir, o.MaxCommits); err != nil {
		note("history: %v", err)
	} else {
		r.Evidence = append(r.Evidence, ev...)
	}
	if ev, err := Docs(o.Dir); err != nil {
		note("docs: %v", err)
	} else {
		r.Evidence = append(r.Evidence, ev...)
	}
	cites, err := Citations(o.Dir)
	if err != nil {
		note("rfc citations: %v", err)
	}
	if o.Offline {
		note("offline: advisories not queried, RFC texts not fetched")
		r.Evidence = append(r.Evidence, cites...)
	} else {
		r.Evidence = append(r.Evidence, FetchRFCs(ctx, cites, o.RFCBase, o.RFCCache, note)...)
		for _, p := range append([]string{o.Module}, o.Similar...) {
			ev, err := Advisories(ctx, o.OSV, p, o.Module)
			if err != nil {
				note("advisories of %s: %v", p, err)
				continue
			}
			r.Evidence = append(r.Evidence, ev...)
		}
	}
	if items, err := trackerItems(ctx, o); err != nil {
		note("issues and pull requests: %v", err)
	} else if items != nil {
		r.Evidence = append(r.Evidence, TrackerEvidence(o.Dir, items)...)
	}
	r.Evidence = foldPRs(dedupAdvisories(r.Evidence))
	sort.SliceStable(r.Evidence, func(i, j int) bool { return kindRank(r.Evidence[i].Kind) < kindRank(r.Evidence[j].Kind) })
	return r, nil
}

// dedupAdvisories keeps one item per advisory: OSV lists the same one under
// each database's id (GHSA-, GO-, RUSTSEC-), aliases of each other. The
// GHSA id is kept as the reference, the others become aliases.
func dedupAdvisories(ev []Evidence) []Evidence {
	first := map[string]int{} // any id or alias -> index in out
	var out []Evidence
	for _, e := range ev {
		if e.Kind != KindAdvisory {
			out = append(out, e)
			continue
		}
		ids := append([]string{e.Ref}, e.Aliases...)
		at := -1
		for _, id := range ids {
			if i, ok := first[id]; ok {
				at = i
				break
			}
		}
		if at < 0 {
			out = append(out, e)
			at = len(out) - 1
		} else {
			k := &out[at]
			if !strings.HasPrefix(k.Ref, "GHSA-") && strings.HasPrefix(e.Ref, "GHSA-") {
				k.Aliases = append(k.Aliases, k.Ref)
				k.Ref, k.Title, k.Text = e.Ref, e.Title, e.Text
			}
			for _, id := range ids {
				if id != k.Ref && !contains(k.Aliases, id) {
					k.Aliases = append(k.Aliases, id)
				}
			}
			if k.After == "" {
				k.Before, k.After = e.Before, e.After
			}
		}
		for _, id := range append([]string{out[at].Ref}, out[at].Aliases...) {
			first[id] = at
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// kindRank orders the report: advisories, fixes, RFC rules, then docs.
func kindRank(k string) int {
	return map[string]int{KindAdvisory: 0, KindPR: 1, KindCommit: 2, KindIssue: 3, KindRFC: 4, KindDoc: 5}[k]
}

// Write writes the report as indented JSON.
func (r *Report) Write(w io.Writer) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(r)
}

// Summary counts the evidence by kind, one line.
func (r *Report) Summary() string {
	n := map[string]int{}
	for _, e := range r.Evidence {
		n[e.Kind]++
	}
	var parts []string
	for _, k := range []string{KindAdvisory, KindPR, KindCommit, KindIssue, KindRFC, KindDoc} {
		parts = append(parts, fmt.Sprintf("%d %s", n[k], k))
	}
	return strings.Join(parts, ", ")
}

// trackerItems reads the issues and pull requests from GitHubFile, or
// searches GitHub for them; nil when there is neither a file nor a GitHub
// repository, or the collector is offline.
func trackerItems(ctx context.Context, o Options) ([]ghItem, error) {
	if o.GitHubFile != "" {
		f, err := os.Open(o.GitHubFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return ReadGitHub(f)
	}
	repo := o.GitHubRepo
	if repo == "" {
		repo = GitHubRepo(o.Dir)
	}
	if repo == "" || o.Offline {
		return nil, nil
	}
	return SearchGitHub(ctx, o.GitHubBase, o.GitHubToken, repo, o.MaxIssues)
}
