package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
)

func TestParseDiff(t *testing.T) {
	diff := `diff --git a/p/a.go b/p/a.go
--- a/p/a.go
+++ b/p/a.go
@@ -3 +3 @@ func A() {
-	return 1
+	return 2
@@ -10,2 +10,0 @@ func B() {
@@ -20,0 +19,3 @@ func C() {
diff --git a/p/gone.go b/p/gone.go
--- a/p/gone.go
+++ /dev/null
@@ -1,5 +0,0 @@
`
	got := parseDiff(diff, "/repo")
	want := []lineRange{{3, 3}, {10, 11}, {19, 21}}
	if !slices.Equal(got["/repo/p/a.go"], want) {
		t.Errorf("a.go: %v, want %v", got["/repo/p/a.go"], want)
	}
	if len(got) != 1 {
		t.Errorf("files: %v", got)
	}
}

// A target is touched when it, or a function it reaches through the
// package's calls, has a changed line.
func TestTouchedFuncs(t *testing.T) {
	r := &contract.ModelReport{Funcs: []contract.ModelFunc{
		{ID: 0, Name: "Top", File: "/r/a.go", Lines: []int{1, 5}, Calls: []int{1}},
		{ID: 1, Name: "mid", File: "/r/a.go", Lines: []int{7, 9}, Calls: []int{2}},
		{ID: 2, Name: "leaf", File: "/r/b.go", Lines: []int{3, 8}},
		{ID: 3, Name: "Other", File: "/r/a.go", Lines: []int{11, 20}},
	}}
	got := touchedFuncs(r, map[string][]lineRange{"/r/b.go": {{8, 8}}})
	if !got["Top"] || !got["mid"] || !got["leaf"] || got["Other"] {
		t.Errorf("touched = %v", got)
	}
	if got := touchedFuncs(r, map[string][]lineRange{"/r/a.go": {{6, 6}}}); len(got) != 0 {
		t.Errorf("a change between functions touched %v", got)
	}
}

// changedLines on a real repository: a committed change and an untracked
// file, against the first commit.
func TestChangedLines(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("a.go", "package p\n\nfunc A() int {\n\treturn 1\n}\n")
	git("add", ".")
	git("commit", "-q", "-m", "one")
	write("a.go", "package p\n\nfunc A() int {\n\treturn 2\n}\n")
	write("new.go", "package p\n")
	got, err := changedLines(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if r := got[resolve(filepath.Join(dir, "a.go"))]; !slices.Equal(r, []lineRange{{4, 4}}) {
		t.Errorf("a.go: %v", r)
	}
	if r := got[resolve(filepath.Join(dir, "new.go"))]; len(r) != 1 {
		t.Errorf("untracked new.go: %v", r)
	}
	if _, err := changedLines(dir, "no-such-rev"); err == nil {
		t.Error("a bad revision: no error")
	}
}
