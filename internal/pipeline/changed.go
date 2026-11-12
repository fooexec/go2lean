package pipeline

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fooexec/go2lean/internal/contract"
)

// lineRange is a range of lines of a file, both ends included.
type lineRange [2]int

// changedLines is what `git diff <rev>` (the working tree against rev)
// changed in the repository holding dir: for each file (absolute, symlinks
// resolved), the line ranges of its new version that are new or changed. A
// deletion counts as a change of the line after it; a file git does not
// track yet changed as a whole.
func changedLines(dir, rev string) (map[string][]lineRange, error) {
	top, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	top = strings.TrimSpace(top)
	diff, err := gitOut(top, "diff", "-U0", "--no-color", "--no-ext-diff", rev, "--")
	if err != nil {
		return nil, err
	}
	out := parseDiff(diff, top)
	untracked, err := gitOut(top, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	for _, f := range strings.Split(strings.TrimSpace(untracked), "\n") {
		if f != "" {
			p := resolve(filepath.Join(top, f))
			out[p] = append(out[p], lineRange{1, 1 << 30})
		}
	}
	return out, nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// parseDiff reads a unified diff with no context lines (-U0): the new-side
// ranges of each hunk, by file under top.
func parseDiff(diff, top string) map[string][]lineRange {
	out := map[string][]lineRange{}
	file := ""
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++ "):
			name := strings.TrimPrefix(l, "+++ ")
			if name == "/dev/null" {
				file = "" // a deleted file: nothing of it is left to search
			} else {
				file = resolve(filepath.Join(top, strings.TrimPrefix(name, "b/")))
			}
		case strings.HasPrefix(l, "@@ ") && file != "":
			// @@ -a[,b] +c[,d] @@
			fields := strings.Fields(l)
			if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
				continue
			}
			start, count, _ := strings.Cut(strings.TrimPrefix(fields[2], "+"), ",")
			c, err := strconv.Atoi(start)
			if err != nil {
				continue
			}
			d := 1
			if count != "" {
				if d, err = strconv.Atoi(count); err != nil {
					continue
				}
			}
			if d == 0 {
				// Lines deleted after line c: the code around them changed.
				out[file] = append(out[file], lineRange{c, c + 1})
			} else {
				out[file] = append(out[file], lineRange{c, c + d - 1})
			}
		}
	}
	return out
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// touchedFuncs is the set of a model report's functions (by name) that
// reach, through the package's calls, a function whose lines changed.
func touchedFuncs(r *contract.ModelReport, changed map[string][]lineRange) map[string]bool {
	direct := map[int]bool{}
	for _, f := range r.Funcs {
		if f.File == "" || len(f.Lines) != 2 {
			continue
		}
		for _, lr := range changed[resolve(f.File)] {
			if lr[0] <= f.Lines[1] && f.Lines[0] <= lr[1] {
				direct[f.ID] = true
				break
			}
		}
	}
	// Reverse reachability: a function is touched if one it calls is.
	callers := map[int][]int{}
	for _, f := range r.Funcs {
		for _, c := range f.Calls {
			callers[c] = append(callers[c], f.ID)
		}
	}
	seen := map[int]bool{}
	stack := make([]int, 0, len(direct))
	for id := range direct {
		stack = append(stack, id)
	}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		stack = append(stack, callers[id]...)
	}
	out := map[string]bool{}
	for _, f := range r.Funcs {
		if seen[f.ID] {
			out[f.Name] = true
		}
	}
	return out
}
