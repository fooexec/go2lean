package mine

import (
	"regexp"
	"strconv"
	"strings"
)

// The fix commit of a project's own advisory. A CVE record's GIT range often
// names the release that shipped the fix (a version bump of configure.ac,
// an empty merge), and the advisory's references name the fix commit or the
// pull request that made it. resolveFixes picks, in that order, a commit
// the references name, the merge (or squash) of a pull request they name,
// and the GIT range's commit: the first that changes the project's source.

var (
	refCommit = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/commit/([0-9a-f]{7,40})`)
	refPull   = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/(\d+)`)
)

// resolveFixes sets the revisions of the own advisories in ev (those of
// project module) from dir's history.
func resolveFixes(dir, module string, ev []Evidence) []Evidence {
	for i := range ev {
		e := &ev[i]
		if e.Kind != KindAdvisory || e.Project != module {
			continue
		}
		var cands [][2]string // before, after
		for _, m := range refCommit.FindAllStringSubmatch(e.Text, -1) {
			if sha := fullHash(dir, m[1]); sha != "" {
				cands = append(cands, [2]string{sha + "^", sha})
			}
		}
		for _, m := range refPull.FindAllStringSubmatch(e.Text, -1) {
			n, _ := strconv.Atoi(m[1])
			if sha := mergeOf(dir, n); sha != "" {
				cands = append(cands, [2]string{sha + "^1", sha})
			} else if sha := squashCommit(dir, n); sha != "" {
				cands = append(cands, [2]string{sha + "^", sha})
			}
		}
		if e.After != "" {
			if sha := fullHash(dir, e.After); sha != "" {
				cands = append(cands, [2]string{sha + "^", sha})
			}
		}
		e.Before, e.After = "", ""
		for _, c := range cands {
			if changesSource(dir, c[0], c[1]) {
				e.Before, e.After = c[0], c[1]
				break
			}
		}
		if e.After != "" {
			// The fix itself: the functions it changes are what a spec
			// drafted from the advisory should drive, and its diff says
			// what kind of check was missing (a bound, a semantic rule).
			// An advisory's text often names neither.
			files := sourceFiles(dir, e.Before, e.After)
			e.Paths = files
			if diff, err := git(dir, append([]string{"diff", "--unified=3", e.Before, e.After, "--"}, files...)...); err == nil && len(diff) > 0 {
				e.Text = clip(e.Text+"\n\nThe fix ("+short(e.After)+"):\n"+clip(string(diff), maxDiff), maxText)
			}
		}
	}
	return ev
}

// fullHash is the commit a hash prefix names in dir, or "".
func fullHash(dir, h string) string {
	out, err := git(dir, "rev-parse", "--verify", "--quiet", h+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// mergeOf is the merge commit of pull request n ("Merge pull request #n").
func mergeOf(dir string, n int) string {
	out, err := git(dir, "log", "-n1", "--merges", "--format=%H", "-E", "--grep", `^Merge pull request #`+strconv.Itoa(n)+`\b`)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// changesSource: the diff from before to after changes source files (Go or
// C) outside the tests.
func changesSource(dir, before, after string) bool {
	return len(sourceFiles(dir, before, after)) > 0
}

// sourceFiles are the source files (Go or C, not tests) the diff from
// before to after changes.
func sourceFiles(dir, before, after string) []string {
	out, err := git(dir, "diff", "--name-only", before, after)
	if err != nil {
		return nil
	}
	var fs []string
	for _, p := range strings.Split(string(out), "\n") {
		if (strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")) || (isCSource(p) && !cTestPath(p)) {
			fs = append(fs, p)
		}
	}
	return fs
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
