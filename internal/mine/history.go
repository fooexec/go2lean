package mine

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// fixWords are what a commit message says when it fixes a defect a
// property can catch: a crash, a bound, a protocol rule, a security issue.
// A message that only says "fix" (a typo, a test, CI) is kept only when
// it also touches non-test Go code; the LLM step ranks what is left.
var fixWords = regexp.MustCompile(`(?i)\b(fix(es|ed)?|bug|cve-\d{4}-\d+|ghsa-[0-9a-z]{4}-|security|vulnerab\w*|panic\w*|crash\w*|overflow\w*|underflow|out[- ]of[- ](bounds|range)|oob|denial[- ]of[- ]service|dos|amplif\w*|exhaust\w*|unbounded|infinite|deadlock|race|leak\w*|bypass\w*|spoof\w*|smuggl\w*|injection|validat\w*|reject\w*|limit\w*|rfc ?\d{3,5}|violat\w*|malformed|invalid)\b`)

// noiseWords mark commits that fix the build, the tests, the docs, the
// interop runner or the logs, not the code's behaviour.
var noiseWords = regexp.MustCompile(`(?i)\b(typo|lint|linter|flaky|ci|readme|changelog|docs?|comment|test only|go\.mod|bump|dependabot|deps|interop|logging|log statements?|tracing|example|benchmarks?)\b|^(fuzzing|interop|ci|docs|example|integrationtests?):`)

// maxDiff bounds the diff kept for a commit.
const maxDiff = 4000

// History lists the commits of dir's git history that look like fixes,
// newest first: each with its message, the Go files it changed, the start
// of its diff of those files, and its parent and itself as the revisions
// before and after.
func History(dir string, max int) ([]Evidence, error) {
	if max <= 0 {
		max = 2000
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not found")
	}
	// Commit, parents, subject, body, separated by NUL; records by RS.
	out, err := git(dir, "log", "--no-merges", fmt.Sprintf("-n%d", max), "--format=%H%x00%P%x00%s%x00%b%x1e")
	if err != nil {
		return nil, err
	}
	var ev []Evidence
	for _, rec := range strings.Split(string(out), "\x1e") {
		f := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x00", 4)
		if len(f) < 4 {
			continue
		}
		hash, parents, subject, body := f[0], strings.Fields(f[1]), f[2], strings.TrimSpace(f[3])
		msg := subject + "\n" + body
		if len(parents) != 1 || !fixWords.MatchString(msg) || noiseWords.MatchString(subject) && !strings.Contains(strings.ToLower(msg), "security") {
			continue
		}
		files, err := goFiles(dir, hash)
		if err != nil || len(files) == 0 {
			continue
		}
		code := false
		for _, p := range files {
			if !strings.HasSuffix(p, "_test.go") && !(isCSource(p) && cTestPath(p)) {
				code = true
			}
		}
		if !code {
			continue
		}
		diff, _ := git(dir, append([]string{"show", "--format=", "--unified=3", hash, "--"}, files...)...)
		text := msg
		if len(diff) > 0 {
			text += "\n\n" + clip(string(diff), maxDiff)
		}
		ev = append(ev, Evidence{
			Kind: KindCommit, Ref: hash[:12], Title: subject, Text: clip(text, maxText),
			Paths: files, Before: parents[0], After: hash,
		})
	}
	return ev, nil
}

// goFiles are the source files a commit changed: Go, or C (.c, .h).
func goFiles(dir, hash string) ([]string, error) {
	out, err := git(dir, "show", "--format=", "--name-only", hash)
	if err != nil {
		return nil, err
	}
	var fs []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(l, ".go") || isCSource(l) {
			fs = append(fs, l)
		}
	}
	return fs, nil
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// modulePath reads the module line of dir/go.mod.
func modulePath(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("no module path: %v", err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if l := strings.TrimSpace(s.Text()); strings.HasPrefix(l, "module ") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(l, "module ")), `"`), nil
		}
	}
	return "", fmt.Errorf("no module line in %s/go.mod", dir)
}
