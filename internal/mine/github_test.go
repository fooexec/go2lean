package mine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

const ghPages = `{"total_count": 3, "items": [
 {"number": 12, "title": "fix panic on an empty frame", "body": "Parse read past the buffer.\n\nFixes #10.",
  "state": "closed", "pull_request": {"merged_at": "2026-01-02T00:00:00Z"}},
 {"number": 13, "title": "docs: fix typo", "body": "", "state": "closed", "pull_request": {"merged_at": "2026-01-03T00:00:00Z"}},
 {"number": 14, "title": "fix overflow in Ack", "body": "", "state": "closed", "pull_request": {"merged_at": null}}
]}
{"total_count": 3, "items": [
 {"number": 10, "title": "server crashes on a zero-length frame", "body": "panic: index out of range", "state": "open", "labels": [{"name": "bug"}]},
 {"number": 20, "title": "Parse accepts a frame longer than 64 bytes", "body": "The doc says never.", "state": "open", "labels": [{"name": "bug"}]},
 {"number": 21, "title": "feature request: a Len method", "body": "", "state": "open", "labels": [{"name": "enhancement"}]}
]}`

func TestReadGitHub(t *testing.T) {
	items, err := ReadGitHub(strings.NewReader(ghPages + "\n" + `[{"number": 30, "title": "x"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 7 || items[0].Number != 12 || !items[0].merged() || items[2].merged() || items[6].Number != 30 {
		t.Errorf("items: %+v", items)
	}
}

func TestTrackerEvidence(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	write(t, dir, "go.mod", "module example.com/frame\n")
	write(t, dir, "frame.go", pkgSrc)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "initial")
	write(t, dir, "frame.go", strings.Replace(pkgSrc, "len(b)", "len(b) - 1", 1))
	run(t, dir, "commit", "-q", "-am", "fix panic on an empty frame (#12)")
	fix := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "remote", "add", "origin", "git@github.com:example/frame.git")
	if r := GitHubRepo(dir); r != "example/frame" {
		t.Errorf("repo %q", r)
	}

	items, err := ReadGitHub(strings.NewReader(ghPages))
	if err != nil {
		t.Fatal(err)
	}
	ev := TrackerEvidence(dir, items)
	var refs []string
	for _, e := range ev {
		refs = append(refs, e.Kind+" "+e.Ref)
	}
	if got := strings.Join(refs, ","); got != "pr #12,issue #10,issue #20" {
		t.Fatalf("evidence %s", got)
	}
	pr := ev[0]
	if pr.After != fix || pr.Before != fix+"^" || len(pr.Paths) != 1 || !strings.Contains(pr.Text, "Issue #10: server crashes") {
		t.Errorf("pr: %+v", pr)
	}
	if ev[1].After != "" || !strings.Contains(ev[1].Text, "[bug]") {
		t.Errorf("issue: %+v", ev[1])
	}

	// The fix commit's own evidence gains the pull request's words.
	hist, err := History(dir, 0)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history %v %+v", err, hist)
	}
	all := foldPRs(append(hist, ev...))
	if len(all) != 3 || all[0].Kind != KindCommit || len(all[0].Aliases) != 1 || all[0].Aliases[0] != "#12" ||
		!strings.HasPrefix(all[0].Text, "Pull request #12") || !strings.Contains(all[0].Text, "len(b) - 1") {
		t.Errorf("folded: %+v", all)
	}
}

func TestSearchGitHub(t *testing.T) {
	var queries []string
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("q"))
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"items": [{"number": 1, "title": "t"}]}`))
	}))
	defer srv.Close()
	items, err := SearchGitHub(context.Background(), srv.URL, "tok", "example/frame", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || strings.Join(queries, "|") != "repo:example/frame is:pr is:merged|repo:example/frame is:issue is:open" || auth != "Bearer tok" {
		t.Errorf("items %d, queries %v, auth %q", len(items), queries, auth)
	}
}
