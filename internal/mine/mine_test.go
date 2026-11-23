package mine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, src string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

const pkgSrc = `// Package frame parses frames.
//
// A frame is never longer than 64 bytes.
package frame

// Parse reads a frame. It returns an error if the length field exceeds the
// buffer. The result is cached.
func Parse(b []byte) (int, error) { return len(b), nil }

// helper is unexported: its doc is not collected. It must not panic.
func helper() {}

// Ack acknowledges a frame, as RFC 9000, Section 13.2 requires. Unknown
// settings are ignored (Section 7.2.4 of RFC 9114); see also RFC 9000.
func Ack() {}
`

func TestHistory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	write(t, dir, "go.mod", "module example.com/frame\n")
	write(t, dir, "frame.go", pkgSrc)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "initial")
	parent := run(t, dir, "rev-parse", "HEAD")

	write(t, dir, "frame.go", strings.Replace(pkgSrc, "len(b)", "len(b) - 1", 1))
	run(t, dir, "commit", "-q", "-am", "fix panic on an empty frame", "-m", "Reported as an out of range read.")
	fix := run(t, dir, "rev-parse", "HEAD")

	write(t, dir, "frame_test.go", "package frame\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "fix flaky test")
	write(t, dir, "frame.go", pkgSrc+"\n// x\n")
	run(t, dir, "commit", "-q", "-am", "docs: fix typo")
	write(t, dir, "frame.go", pkgSrc+"\n// y\n")
	run(t, dir, "commit", "-q", "-am", "refactor the parser")

	ev, err := History(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 1 {
		t.Fatalf("want only the fix, got %d: %+v", len(ev), ev)
	}
	e := ev[0]
	if e.Kind != KindCommit || e.After != fix || e.Before != parent || e.Ref != fix[:12] {
		t.Errorf("revisions: %+v", e)
	}
	if len(e.Paths) != 1 || e.Paths[0] != "frame.go" || !strings.Contains(e.Text, "out of range read") || !strings.Contains(e.Text, "len(b) - 1") {
		t.Errorf("paths or text: %+v", e)
	}
}

func TestDocs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "frame.go", pkgSrc)
	write(t, dir, "frame_test.go", "package frame\n\n// Never collected: it must not be read.\nfunc TestX() {}\n")
	write(t, dir, "README.md", "# frame\n\nFrames.\n\nA reader must reject a frame\nlonger than its buffer.\n")
	ev, err := Docs(dir)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, e := range ev {
		titles = append(titles, e.Ref+" "+e.Title)
	}
	got := strings.Join(titles, "\n")
	for _, want := range []string{
		"frame.go:1 package frame: A frame is never longer than 64 bytes.",
		"frame.go:6 Parse: It returns an error if the length field exceeds the buffer.",
		"README.md:5 A reader must reject a frame longer than its buffer.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, not := range []string{"helper", "cached", "TestX", "Frames."} {
		if strings.Contains(got, not) {
			t.Errorf("unexpected %q in\n%s", not, got)
		}
	}
}

func TestCitations(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "frame.go", pkgSrc)
	write(t, dir, "conn/conn.go", "package conn\n\n// Per RFC 9000 §13.2 and RFC9002.\nfunc A() {}\n")
	ev, err := Citations(dir)
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]Evidence{}
	for _, e := range ev {
		refs[e.Ref] = e
	}
	for _, want := range []string{"RFC 9000 13.2", "RFC 9114 7.2.4", "RFC 9002"} {
		if _, ok := refs[want]; !ok {
			t.Errorf("missing %s in %v", want, refs)
		}
	}
	if e := refs["RFC 9000 13.2"]; len(e.Cites) != 2 || len(e.Paths) != 2 {
		t.Errorf("RFC 9000 13.2 cites: %+v", e)
	}
	// "see also RFC 9000" sits in a comment that cites a section of it.
	if _, ok := refs["RFC 9000"]; ok {
		t.Errorf("bare RFC 9000 kept beside its section: %v", refs)
	}
}

const rfcText = `RFC 9000                    QUIC Transport                      May 2021


13.  Packetization and Reliability

   A sender bundles frames.

13.1.  Packet Processing

   A packet MUST NOT be acknowledged until processed.

13.2.  Generating Acknowledgments

   Endpoints acknowledge all packets.

Iyengar & Thomson            Standards Track                  [Page 92]
` + "\f" + `
RFC 9000                    QUIC Transport                      May 2021


13.2.1.  Sending ACK Frames

   Every packet SHOULD be acknowledged at least once.

14.  Datagram Size

   A UDP datagram can include one or more QUIC packets.
`

func TestSection(t *testing.T) {
	s := Section(rfcText, "13.2")
	if !strings.HasPrefix(s, "13.2.  Generating Acknowledgments") || !strings.Contains(s, "13.2.1.  Sending ACK Frames") ||
		strings.Contains(s, "Datagram Size") || strings.Contains(s, "[Page") || strings.Contains(s, "May 2021") ||
		strings.Contains(s, "Packet Processing") {
		t.Errorf("section 13.2:\n%s", s)
	}
	if Section(rfcText, "15") != "" {
		t.Error("absent section not empty")
	}
}

func TestFetchRFCs(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/rfc9000.txt" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(rfcText))
	}))
	defer srv.Close()
	cache := t.TempDir()
	cites := []Evidence{{Kind: KindRFC, Ref: "RFC 9000 13.2", Text: "Cited as: x"}, {Kind: KindRFC, Ref: "RFC 9114 4.1"}}
	var notes []string
	note := func(f string, a ...any) { notes = append(notes, f) }
	out := FetchRFCs(context.Background(), cites, srv.URL, cache, note)
	if !strings.Contains(out[0].Text, "Endpoints acknowledge all packets") || len(notes) != 1 {
		t.Errorf("text %q, notes %v", out[0].Text, notes)
	}
	// From the cache the second time.
	before := hits
	FetchRFCs(context.Background(), []Evidence{{Kind: KindRFC, Ref: "RFC 9000 13.1"}}, srv.URL, cache, note)
	if hits != before {
		t.Error("cache not used")
	}
}

const osvResp = `{"vulns":[
 {"id":"GHSA-aaaa-bbbb-cccc","summary":"Panic on a short frame","details":"A peer can crash the server.",
  "affected":[{"package":{"ecosystem":"Go","name":"example.com/frame"},"ranges":[{"events":[{"introduced":"0"},{"fixed":"1.2.3"}]}]}],
  "references":[{"type":"FIX","url":"https://github.com/example/frame/commit/0123456789abcdef0123456789abcdef01234567"}]},
 {"id":"GO-2024-0001","summary":"Unbounded allocation","details":"...",
  "affected":[{"package":{"ecosystem":"Go","name":"example.com/frame"},"ranges":[{"events":[{"introduced":"0"},{"fixed":"1.4.0"}]}]}]}
]}`

func TestAdvisories(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 512)
		n, _ := r.Body.Read(b)
		asked = append(asked, string(b[:n]))
		if strings.Contains(string(b[:n]), `"crates.io"`) {
			w.Write([]byte(`{"vulns":[{"id":"RUSTSEC-1","summary":"other","details":"d"}]}`))
			return
		}
		w.Write([]byte(osvResp))
	}))
	defer srv.Close()
	ev, err := Advisories(context.Background(), srv.URL, "example.com/frame", "example.com/frame")
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 2 || ev[0].After != "0123456789abcdef0123456789abcdef01234567" || ev[0].Before != ev[0].After+"^" || ev[1].After != "1.4.0" {
		t.Errorf("own advisories: %+v", ev)
	}
	other, err := Advisories(context.Background(), srv.URL, "crates.io:quiche", "example.com/frame")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 || other[0].Project != "crates.io:quiche" || other[0].After != "" {
		t.Errorf("similar project: %+v", other)
	}
	if !strings.Contains(asked[1], `"name":"quiche"`) {
		t.Errorf("query: %s", asked[1])
	}
}

func TestCollectOffline(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/frame\n")
	write(t, dir, "frame.go", pkgSrc)
	r, err := Collect(context.Background(), Options{Dir: dir, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Module != "example.com/frame" || !strings.Contains(r.Summary(), "2 rfc") || !strings.Contains(r.Summary(), "2 doc") || !strings.Contains(r.Summary(), "0 advisory") {
		t.Errorf("module %q, summary %s, notes %v", r.Module, r.Summary(), r.Notes)
	}
	// The report is ordered by kind: no doc before an rfc item.
	seenDoc := false
	for _, e := range r.Evidence {
		if e.Kind == KindDoc {
			seenDoc = true
		} else if seenDoc {
			t.Errorf("%s after a doc item", e.Kind)
		}
	}
}
