package pipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andreasfoo/go2lean/internal/contract"
)

// Summary aggregates a workspace: every verdict grouped by status and every
// proof result, so a sweep over many packages can be read in one place.
type Summary struct {
	Confirmed    []Finding
	Spurious     []Finding
	Inconclusive []Finding
	Proofs       map[contract.ProofStatus]int
	Properties   int
}

// Finding is one verdict with the records it points at.
type Finding struct {
	Verdict        *contract.Verdict
	Counterexample *contract.Counterexample
	Property       *contract.Property
}

func readAll[T any](dir string) ([]T, error) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(matches)
	var out []T
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// Summarize reads a workspace.
func Summarize(w contract.Workspace) (*Summary, error) {
	verdicts, err := readAll[contract.Verdict](filepath.Join(w.Root, "verdicts"))
	if err != nil {
		return nil, err
	}
	proofs, err := readAll[contract.ProofResult](filepath.Join(w.Root, "proofs"))
	if err != nil {
		return nil, err
	}
	props, err := w.Properties()
	if err != nil {
		return nil, err
	}
	s := &Summary{Proofs: map[contract.ProofStatus]int{}, Properties: len(props)}
	for _, p := range proofs {
		s.Proofs[p.Status]++
	}
	for i := range verdicts {
		v := &verdicts[i]
		f := Finding{Verdict: v}
		if cx, err := w.Counterexample(v.Counterexample); err == nil {
			f.Counterexample = cx
			if p, err := w.Property(cx.Property); err == nil {
				f.Property = p
			}
		}
		switch v.Status {
		case contract.VerdictConfirmed:
			s.Confirmed = append(s.Confirmed, f)
		case contract.VerdictSpurious:
			s.Spurious = append(s.Spurious, f)
		default:
			s.Inconclusive = append(s.Inconclusive, f)
		}
	}
	return s, nil
}

// tierTitles head the confirmed findings by rank (internal/poc/rank.go);
// 0 is a verdict written before ranking (go2lean rank adds it).
var tierTitles = map[int]string{
	1: "Confirmed, tier 1: a caller can pass this input",
	2: "Confirmed, tier 2: depends on the callers",
	3: "Confirmed, tier 3: no caller in the module passes this input",
	4: "Confirmed, tier 4: an input the test forged",
	0: "Confirmed, unranked",
}

func tier(f Finding) int {
	if f.Verdict.Rank == nil {
		return 0
	}
	return f.Verdict.Rank.Tier
}

// WriteMarkdown renders the summary: confirmed findings by rank, the ones
// a reader should look at first on top.
func (s *Summary) WriteMarkdown(out io.Writer) {
	byTier := map[int][]Finding{}
	for _, f := range s.Confirmed {
		byTier[tier(f)] = append(byTier[tier(f)], f)
	}
	fmt.Fprintf(out, "# go2lean summary\n\n%d properties; %d confirmed (%d at tier 1, %d at tier 2, %d at tier 3, %d at tier 4, %d unranked), %d spurious, %d inconclusive; proofs: %v\n\n",
		s.Properties, len(s.Confirmed), len(byTier[1]), len(byTier[2]), len(byTier[3]), len(byTier[4]), len(byTier[0]),
		len(s.Spurious), len(s.Inconclusive), s.Proofs)
	section := func(title string, fs []Finding) {
		if len(fs) == 0 {
			return
		}
		fmt.Fprintf(out, "## %s (%d)\n\n", title, len(fs))
		for _, f := range fs {
			fmt.Fprintf(out, "### %s\n\n", f.Verdict.Counterexample)
			if r := f.Verdict.Rank; r != nil {
				fmt.Fprintf(out, "- rank %d: %s\n", r.Tier, r.Reason)
				for _, site := range r.Sites {
					fmt.Fprintf(out, "  - %s\n", site)
				}
			}
			if f.Property != nil {
				fmt.Fprintf(out, "- property: `%s` (%s) — %s\n", f.Property.ID, f.Property.Kind, f.Property.NL)
			}
			if cx := f.Counterexample; cx != nil {
				fmt.Fprintf(out, "- inputs: `%v`", cx.Inputs)
				if len(cx.Memory) > 0 {
					fmt.Fprintf(out, " memory: `%v`", cx.Memory)
				}
				if len(cx.Objects) > 0 {
					var parts []string
					for k, v := range cx.Objects {
						parts = append(parts, k+"="+string(v))
					}
					sort.Strings(parts)
					fmt.Fprintf(out, " objects: `%s`", strings.Join(parts, " "))
				}
				if cx.ModelOutput != nil {
					fmt.Fprintf(out, "\n- model: %s %s %s", cx.ModelOutput.Res, cx.ModelOutput.Value, cx.ModelOutput.Panic)
				}
				fmt.Fprintln(out)
			}
			if site := f.Verdict.Evidence.PanicSite; site != "" {
				fmt.Fprintf(out, "- panicked at `%s`\n", site)
			}
			fmt.Fprintf(out, "- real: `%v`", f.Verdict.Observed)
			if d := f.Verdict.Diagnosis; d != nil {
				fmt.Fprintf(out, " — %s %s", d.Reason, d.ModelVsReal)
			}
			fmt.Fprintln(out)
			if f.Verdict.PocDir != "" {
				fmt.Fprintf(out, "- PoC: `%s`\n", f.Verdict.PocDir)
			}
			fmt.Fprintln(out)
		}
	}
	for _, t := range []int{1, 2, 3, 4, 0} {
		section(tierTitles[t], byTier[t])
	}
	section("Spurious", s.Spurious)
	section("Inconclusive", s.Inconclusive)
}

// WriteBrief is the end of a run's output: the counts, the tier-1
// findings (an input a caller can pass) with where they panicked and their
// PoC, and how many depend on callers. The rest is in the summary file at
// path (WriteMarkdown).
func (s *Summary) WriteBrief(out io.Writer, path string) {
	byTier := map[int][]Finding{}
	for _, f := range s.Confirmed {
		byTier[tier(f)] = append(byTier[tier(f)], f)
	}
	// One entry per place the program fails: findings that panic at the
	// same line are one bug reached from several functions.
	type group struct {
		first Finding
		names []string
	}
	var groups []*group
	bySite := map[string]*group{}
	// Listed: what a caller can reach, and every spec the user (or the
	// agent) wrote that fails, whatever its rank: the ranking keeps a spec
	// violation below tier 1 because the spec may be wrong, but it is the
	// finding the user asked for.
	var listed []Finding
	for _, f := range s.Confirmed {
		if tier(f) == 1 || specFinding(f) {
			listed = append(listed, f)
		}
	}
	for _, f := range listed {
		name := findingName(f)
		site := f.Verdict.Evidence.PanicSite
		if g := bySite[site]; site != "" && g != nil {
			g.names = append(g.names, name)
			continue
		}
		g := &group{first: f, names: []string{name}}
		groups = append(groups, g)
		if site != "" {
			bySite[site] = g
		}
	}
	// Runtime errors (an index out of range, a nil dereference) first: an
	// explicit panic of the package is often its documented check of an
	// argument (pflag's "shorthand is more than one ASCII character").
	sort.SliceStable(groups, func(i, j int) bool { return !explicitPanic(groups[i].first) && explicitPanic(groups[j].first) })
	// The headline is what a caller can reach: on toml, 6 of 111 confirmed
	// findings; the others are in unexported functions or rest on package
	// state, and counted after.
	fmt.Fprintf(out, "== Summary\n  %d properties checked\n", s.Properties)
	if len(groups) == 0 {
		fmt.Fprintf(out, "  no confirmed finding a caller can reach, and no spec violated\n")
	} else {
		fmt.Fprintf(out, "  %s a caller can reach or that violate a spec, %s:\n", plural(len(listed), "confirmed finding"), plural(len(groups), "place"))
	}
	const most = 10
	for i, g := range groups {
		if i == most {
			fmt.Fprintf(out, "  ... and %d more places\n", len(groups)-most)
			break
		}
		f := g.first
		names := g.names
		if len(names) > 5 {
			names = append(names[:5:5], fmt.Sprintf("%d more", len(g.names)-5))
		}
		fmt.Fprintf(out, "  confirmed: %s\n", strings.Join(names, ", "))
		if specFinding(f) {
			fmt.Fprintf(out, "    spec violated: %s\n", f.Property.NL)
		}
		if explicitPanic(f) {
			fmt.Fprintf(out, "    an explicit panic of the package: check whether its documentation says it panics there\n")
		}
		if site := f.Verdict.Evidence.PanicSite; site != "" {
			fmt.Fprintf(out, "    at %s: %v\n", site, f.Verdict.Observed)
		} else {
			fmt.Fprintf(out, "    real: %v\n", f.Verdict.Observed)
		}
		if f.Verdict.PocDir != "" {
			fmt.Fprintf(out, "    PoC: %s\n", f.Verdict.PocDir)
		}
	}
	if n := len(s.Confirmed) - len(listed); n > 0 {
		fmt.Fprintf(out, "  %d more confirmed that depend on what callers pass or on package state (unexported functions, nil arguments, forged state): ranked in %s\n", n, path)
	}
	fmt.Fprintf(out, "  %d spurious, %d inconclusive\n", len(s.Spurious), len(s.Inconclusive))
	fmt.Fprintf(out, "  full summary: %s\n", path)
}

// specFinding says whether a finding violates a spec, not a template
// (no_panic, no_hang).
func specFinding(f Finding) bool {
	p := f.Property
	return p != nil && p.Kind != "" && p.Kind != contract.KindNoPanic && p.Kind != contract.KindNoHang
}

// plural is "1 place", "2 places".
func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// findingName is the Go function a finding is about, as its property names
// it ("BoolP does not panic on any input": BoolP), else its target's id.
func findingName(f Finding) string {
	if p := f.Property; p != nil {
		for _, cut := range []string{" does not ", " returns ", " holds ", " satisfies "} {
			if i := strings.Index(p.NL, cut); i > 0 {
				return p.NL[:i]
			}
		}
		if p.Target != "" {
			return p.Target
		}
	}
	return f.Verdict.Counterexample
}

// explicitPanic says whether a finding is a panic the package's own code
// raises with panic(...), not a runtime error.
func explicitPanic(f Finding) bool {
	if cx := f.Counterexample; cx != nil && cx.ModelOutput != nil && cx.ModelOutput.Panic == "explicit" {
		return !strings.HasPrefix(f.Verdict.Observed["panic"], "runtime error")
	}
	return false
}
