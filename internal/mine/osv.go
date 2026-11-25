package mine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/fooexec/go2lean/internal/advisory"
)

// Advisories lists the advisories OSV holds for project: a Go module path,
// or "ecosystem:name" for another ecosystem (crates.io:quiche,
// PyPI:aioquic). An advisory of target itself carries its fix: the fix
// commit when a reference names one (After, and its parent Before), else
// the first fixed version (After).
func Advisories(ctx context.Context, base, project, target string) ([]Evidence, error) {
	if base == "" {
		base = "https://api.osv.dev"
	}
	eco, name := "Go", project
	if i := strings.Index(project, ":"); i > 0 && !strings.Contains(project[:i], "/") {
		eco, name = project[:i], project[i+1:]
	}
	var ev []Evidence
	token := ""
	for page := 0; page < 20; page++ {
		q := map[string]any{"package": map[string]string{"name": name, "ecosystem": eco}}
		if token != "" {
			q["page_token"] = token
		}
		body, _ := json.Marshal(q)
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		req, err := http.NewRequestWithContext(rctx, "POST", strings.TrimSuffix(base, "/")+"/v1/query", bytes.NewReader(body))
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		cancel()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("osv answered %s", resp.Status)
		}
		var r struct {
			Vulns []json.RawMessage `json:"vulns"`
			Next  string            `json:"next_page_token"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		for _, raw := range r.Vulns {
			a, err := advisory.Parse(raw)
			if err != nil {
				continue
			}
			if len(a.Fix) == 0 && len(a.GitFixed) == 0 && len(a.Upstream) > 0 {
				// A distribution's entry: the fix and the details are in
				// the record it tracks.
				withUpstream(ctx, base, a)
			}
			ev = append(ev, fromAdvisory(a, project, project == target))
		}
		if r.Next == "" {
			break
		}
		token = r.Next
	}
	return ev, nil
}

// distro is an ecosystem of a Linux distribution's packages.
var distro = regexp.MustCompile(`^(Debian|Ubuntu|Alpine|Red Hat|Rocky Linux|AlmaLinux|SUSE|openSUSE|Mageia|Wolfi|Chainguard)\b`)

// fixCommit finds a commit hash in a fix reference (a GitHub commit URL).
var fixCommit = regexp.MustCompile(`/commit/([0-9a-f]{7,40})\b`)

// withUpstream adds to a the fix commits, references and details of the
// records it is derived from (a DEBIAN-CVE entry's CVE record).
func withUpstream(ctx context.Context, base string, a *advisory.Advisory) {
	for _, id := range a.Upstream {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		req, err := http.NewRequestWithContext(rctx, "GET", strings.TrimSuffix(base, "/")+"/v1/vulns/"+id, nil)
		if err != nil {
			cancel()
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		cancel()
		u, err := advisory.Parse(b)
		if resp.StatusCode != 200 || err != nil {
			continue
		}
		if len(a.Upstream) == 1 {
			// The CVE is the reference; the distribution's id an alias.
			a.Aliases = append(a.Aliases, a.ID)
			a.ID = u.ID
		} else if !contains(a.Aliases, u.ID) {
			a.Aliases = append(a.Aliases, u.ID)
		}
		a.Fix = append(a.Fix, u.Fix...)
		a.GitFixed = append(a.GitFixed, u.GitFixed...)
		a.Reference = append(a.Reference, u.Reference...)
		if len(u.Details) > len(a.Details) {
			a.Details = u.Details
		}
		if a.Summary == "" {
			a.Summary = u.Summary
		}
	}
}

func fromAdvisory(a *advisory.Advisory, project string, own bool) Evidence {
	title := a.Summary
	if title == "" {
		if ss := sentences(a.Details); len(ss) > 0 {
			title = clip(ss[0], 200)
		}
	}
	e := Evidence{Kind: KindAdvisory, Ref: a.ID, Aliases: a.Aliases, Title: title, Text: clip(a.Text(), maxText), Project: project}
	if !own {
		return e
	}
	for _, u := range a.Fix {
		if m := fixCommit.FindStringSubmatch(u); m != nil {
			e.After, e.Before = m[1], m[1]+"^"
			return e
		}
	}
	for _, c := range a.GitFixed {
		if regexp.MustCompile(`^[0-9a-f]{7,40}$`).MatchString(c) {
			e.After, e.Before = c, c+"^"
			return e
		}
	}
	for _, p := range a.Packages {
		if distro.MatchString(p.Ecosystem) {
			// A distribution's package version (8.4.1-1): no revision of
			// the project.
			continue
		}
		if len(p.Fixed) > 0 {
			e.After = p.Fixed[0]
			return e
		}
	}
	return e
}
