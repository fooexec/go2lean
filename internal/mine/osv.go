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
			ev = append(ev, fromAdvisory(a, project, project == target))
		}
		if r.Next == "" {
			break
		}
		token = r.Next
	}
	return ev, nil
}

// fixCommit finds a commit hash in a fix reference (a GitHub commit URL).
var fixCommit = regexp.MustCompile(`/commit/([0-9a-f]{7,40})\b`)

func fromAdvisory(a *advisory.Advisory, project string, own bool) Evidence {
	e := Evidence{Kind: KindAdvisory, Ref: a.ID, Aliases: a.Aliases, Title: a.Summary, Text: clip(a.Text(), maxText), Project: project}
	if !own {
		return e
	}
	for _, u := range a.Fix {
		if m := fixCommit.FindStringSubmatch(u); m != nil {
			e.After, e.Before = m[1], m[1]+"^"
			return e
		}
	}
	for _, p := range a.Packages {
		if len(p.Fixed) > 0 {
			e.After = p.Fixed[0]
			return e
		}
	}
	return e
}
