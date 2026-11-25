package mine

import (
	"os/exec"
	"testing"
)

func TestResolveFixes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "master")
	write(t, dir, "bgpd/attr.c", "int parse(void) { return 0; }\n")
	write(t, dir, "configure.ac", "AC_INIT(frr, 1.0)\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "initial")
	run(t, dir, "checkout", "-q", "-b", "fix")
	write(t, dir, "bgpd/attr.c", "int parse(void) { return 1; }\n")
	run(t, dir, "commit", "-q", "-am", "bgpd: check the attribute length")
	run(t, dir, "checkout", "-q", "master")
	run(t, dir, "merge", "-q", "--no-ff", "fix", "-m", "Merge pull request #5 from someone/fix")
	merge := run(t, dir, "rev-parse", "HEAD")
	write(t, dir, "configure.ac", "AC_INIT(frr, 1.1)\n")
	run(t, dir, "commit", "-q", "-am", "FRR Release 1.1")
	release := run(t, dir, "rev-parse", "HEAD")

	ev := []Evidence{
		// The CVE record's GIT range names the release; the advisory's
		// references name the pull request.
		{Kind: KindAdvisory, Ref: "CVE-1", Project: "frr", After: release, Before: release + "^",
			Text: "Fix: https://github.com/FRRouting/frr/pull/5"},
		// Only the release: no revision is better than one without the fix.
		{Kind: KindAdvisory, Ref: "CVE-2", Project: "frr", After: release},
		// A similar project's advisory is left alone.
		{Kind: KindAdvisory, Ref: "CVE-3", Project: "other", After: "abc"},
	}
	ev = resolveFixes(dir, "frr", ev)
	if ev[0].After != merge || ev[0].Before != merge+"^1" {
		t.Errorf("CVE-1: %s..%s, want the merge %s", ev[0].Before, ev[0].After, merge)
	}
	if ev[1].After != "" {
		t.Errorf("CVE-2: %q, want no revision (the release changes no source)", ev[1].After)
	}
	if ev[2].After != "abc" {
		t.Errorf("CVE-3 changed: %q", ev[2].After)
	}
}
