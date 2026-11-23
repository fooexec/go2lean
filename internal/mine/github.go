package mine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The project's own issues and pull requests (#315 P5). A merged pull
// request that fixes something says why, in words a commit message often
// leaves out, and names the issue that reported it; an open issue that
// reports a bug is one the code may still have. Both come from GitHub's
// search API, or from a file of its results.

// Kinds of evidence from the project's tracker.
const (
	KindPR    = "pr"    // a merged pull request that fixes something
	KindIssue = "issue" // an open issue that reports a bug
)

// ghItem is an issue or pull request as the search API returns it.
type ghItem struct {
	Number      int                     `json:"number"`
	Title       string                  `json:"title"`
	Body        string                  `json:"body"`
	State       string                  `json:"state"`
	HTMLURL     string                  `json:"html_url"`
	Labels      []struct{ Name string } `json:"labels"`
	PullRequest *struct {
		MergedAt *string `json:"merged_at"`
	} `json:"pull_request"`
}

func (it ghItem) merged() bool {
	return it.PullRequest != nil && it.PullRequest.MergedAt != nil && *it.PullRequest.MergedAt != ""
}

func (it ghItem) labels() string {
	var ls []string
	for _, l := range it.Labels {
		ls = append(ls, l.Name)
	}
	return strings.Join(ls, ", ")
}

// bugLabel: a label that marks a defect.
var bugLabel = regexp.MustCompile(`(?i)\b(bug|security|crash|panic|regression|vulnerability|dos)\b`)

// ReadGitHub reads search API results: one {"items": [...]} object per page
// (as `gh api --paginate search/issues` writes them, one after the other),
// or a JSON array of items.
func ReadGitHub(r io.Reader) ([]ghItem, error) {
	dec := json.NewDecoder(r)
	var out []ghItem
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			return out, nil
		} else if err != nil {
			return out, err
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) > 0 && raw[0] == '[' {
			var items []ghItem
			if err := json.Unmarshal(raw, &items); err != nil {
				return out, err
			}
			out = append(out, items...)
			continue
		}
		var page struct {
			Items []ghItem `json:"items"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return out, err
		}
		out = append(out, page.Items...)
	}
}

// SearchGitHub asks GitHub's search API (base: https://api.github.com when
// empty) for repo's merged pull requests and open issues, up to max of
// each, newest first. token may be empty (a lower rate limit).
func SearchGitHub(ctx context.Context, base, token, repo string, max int) ([]ghItem, error) {
	if base == "" {
		base = "https://api.github.com"
	}
	if max <= 0 {
		max = 300
	}
	var out []ghItem
	for _, q := range []string{"repo:" + repo + " is:pr is:merged", "repo:" + repo + " is:issue is:open"} {
		for page := 1; (page-1)*100 < max; page++ {
			u := base + "/search/issues?per_page=100&sort=updated&order=desc&page=" + strconv.Itoa(page) + "&q=" + url.QueryEscape(q)
			req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
			if err != nil {
				return out, err
			}
			req.Header.Set("Accept", "application/vnd.github+json")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return out, err
			}
			b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			resp.Body.Close()
			if err != nil {
				return out, err
			}
			if resp.StatusCode != 200 {
				return out, fmt.Errorf("search %q: %s: %s", q, resp.Status, clip(string(b), 200))
			}
			items, err := ReadGitHub(bytes.NewReader(b))
			if err != nil {
				return out, err
			}
			out = append(out, items...)
			if len(items) < 100 {
				break
			}
		}
	}
	return out, nil
}

// GitHubRepo is the owner/name of dir's origin remote on github.com, or "".
func GitHubRepo(dir string) string {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	m := regexp.MustCompile(`github\.com[:/]([^/\s]+/[^/\s]+?)(\.git)?/?$`).FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return ""
	}
	return m[1]
}

// linkedIssue: "fixes #12" and the like in a pull request's body.
var linkedIssue = regexp.MustCompile(`(?i)\b(?:fix(?:es|ed)?|close[sd]?|resolve[sd]?)\s+#(\d+)`)

// TrackerEvidence turns the items into evidence. A merged pull request
// that fixes something becomes a "pr" item, with the issues it closes
// (those among the items) and, when its commit is in dir's history (a
// squash merge's "(#N)"), the revisions before and after it and the Go
// files it changed. An open issue that reports a bug becomes an "issue"
// item, with no revisions: the defect may still be there.
func TrackerEvidence(dir string, items []ghItem) []Evidence {
	byNum := map[int]ghItem{}
	for _, it := range items {
		byNum[it.Number] = it
	}
	var ev []Evidence
	seen := map[int]bool{}
	for _, it := range items {
		if seen[it.Number] {
			continue
		}
		seen[it.Number] = true
		text := it.Title + "\n" + it.Body
		switch {
		case it.merged():
			if !fixWords.MatchString(text) && !bugLabel.MatchString(it.labels()) || noiseWords.MatchString(it.Title) && !bugLabel.MatchString(it.labels()) {
				continue
			}
			e := Evidence{Kind: KindPR, Ref: "#" + strconv.Itoa(it.Number), Title: it.Title}
			body := "Pull request " + e.Ref + ": " + it.Title + labelLine(it) + "\n" + strings.TrimSpace(it.Body)
			for _, m := range linkedIssue.FindAllStringSubmatch(it.Body, -1) {
				n, _ := strconv.Atoi(m[1])
				if is, ok := byNum[n]; ok && is.PullRequest == nil {
					body += "\n\nIssue #" + m[1] + ": " + is.Title + "\n" + strings.TrimSpace(is.Body)
				}
			}
			if sha := squashCommit(dir, it.Number); sha != "" {
				e.After, e.Before = sha, sha+"^"
				if files, err := goFiles(dir, sha); err == nil {
					e.Paths = files
				}
			}
			e.Text = clip(body, maxText)
			ev = append(ev, e)
		case it.PullRequest == nil && it.State == "open":
			if !bugLabel.MatchString(it.labels()) && !fixWords.MatchString(it.Title) {
				continue
			}
			ev = append(ev, Evidence{Kind: KindIssue, Ref: "#" + strconv.Itoa(it.Number), Title: it.Title,
				Text: clip("Open issue #"+strconv.Itoa(it.Number)+": "+it.Title+labelLine(it)+"\n"+strings.TrimSpace(it.Body), maxText)})
		}
	}
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].Kind == KindPR && ev[j].Kind != KindPR })
	return ev
}

func labelLine(it ghItem) string {
	if l := it.labels(); l != "" {
		return " [" + l + "]"
	}
	return ""
}

// squashCommit finds the commit a pull request was merged as: a squash
// merge's subject ends in "(#N)", a merge commit's says "pull request #N".
func squashCommit(dir string, n int) string {
	out, err := exec.Command("git", "-C", dir, "log", "-n1", "--format=%H", "-E",
		"--grep", fmt.Sprintf(`\(#%d\)$|[Mm]erge pull request #%d\b`, n, n)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// foldPRs merges a pull request into the commit evidence of the same fix:
// the commit keeps its diff and gains the pull request's words (and the
// issues it closes); the pull request is dropped as an item of its own.
func foldPRs(ev []Evidence) []Evidence {
	commitAt := map[string]int{}
	for i, e := range ev {
		if e.Kind == KindCommit && e.After != "" {
			commitAt[e.After] = i
		}
	}
	drop := map[int]bool{}
	for j, e := range ev {
		if e.Kind != KindPR || e.After == "" {
			continue
		}
		if i, ok := commitAt[e.After]; ok {
			c := &ev[i]
			c.Aliases = append(c.Aliases, e.Ref)
			c.Text = clip(e.Text+"\n\n"+c.Text, maxText)
			drop[j] = true
		}
	}
	var out []Evidence
	for j, e := range ev {
		if !drop[j] {
			out = append(out, e)
		}
	}
	return out
}
