package pipeline

import (
	"bytes"
	"strings"
	"testing"

	"github.com/andreasfoo/go2lean/internal/contract"
)

// The end of a run: tier-1 findings with their PoC, the rest counted.
func TestWriteBrief(t *testing.T) {
	v := func(id string, tier int) Finding {
		return Finding{
			Verdict: &contract.Verdict{Counterexample: id, PocDir: "out/poc/" + id,
				Observed: map[string]string{"panic": "index out of range"},
				Evidence: contract.Evidence{PanicSite: "a.go:3"},
				Rank:     &contract.Rank{Tier: tier}},
			Property: &contract.Property{Target: id, NL: id + " does not panic on any input"},
		}
	}
	s := &Summary{Properties: 9, Confirmed: []Finding{v("F", 1), v("g", 2), v("h", 2)}, Spurious: []Finding{v("s", 0)}}
	var out bytes.Buffer
	s.WriteBrief(&out, "out/summary.md")
	got := out.String()
	for _, want := range []string{
		"9 properties; 3 confirmed (1 a caller can reach, 2 that depend on the callers or on package state), 1 spurious",
		"confirmed: F",
		"at a.go:3: map[panic:index out of range]",
		"PoC: out/poc/F",
		"2 other confirmed findings depend on inputs the callers may never pass",
		"full summary: out/summary.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "confirmed: g") {
		t.Errorf("a tier-2 finding is listed:\n%s", got)
	}
}

// Findings that panic at the same line are one entry.
func TestWriteBriefGroupsBySite(t *testing.T) {
	f := func(id string) Finding {
		return Finding{Verdict: &contract.Verdict{Counterexample: id, Evidence: contract.Evidence{PanicSite: "flag.go:950"},
			Rank: &contract.Rank{Tier: 1}}, Property: &contract.Property{Target: "t_" + id, NL: id + " does not panic on any input"}}
	}
	s := &Summary{Confirmed: []Finding{f("BoolP"), f("IntP"), f("StringP")}}
	var out bytes.Buffer
	s.WriteBrief(&out, "summary.md")
	if got := out.String(); strings.Count(got, "confirmed: ") != 1 || !strings.Contains(got, "confirmed: BoolP, IntP, StringP") {
		t.Errorf("not grouped by site:\n%s", got)
	}
}

// A runtime error comes before an explicit panic, which is labelled.
func TestWriteBriefRuntimeErrorsFirst(t *testing.T) {
	f := func(id, site, panic, kind string) Finding {
		return Finding{
			Verdict: &contract.Verdict{Counterexample: id, Observed: map[string]string{"panic": panic},
				Evidence: contract.Evidence{PanicSite: site}, Rank: &contract.Rank{Tier: 1}},
			Counterexample: &contract.Counterexample{ModelOutput: &contract.ModelOutput{Res: "panic", Panic: contract.PanicKind(kind)}},
			Property:       &contract.Property{NL: id + " does not panic on any input"},
		}
	}
	s := &Summary{Confirmed: []Finding{
		f("BoolP", "flag.go:969", "shorthand is more than one ASCII character", "explicit"),
		f("Error", "errors.go:70", "runtime error: index out of range [0] with length 0", "index_out_of_range"),
	}}
	var out bytes.Buffer
	s.WriteBrief(&out, "summary.md")
	got := out.String()
	if strings.Index(got, "confirmed: Error") > strings.Index(got, "confirmed: BoolP") {
		t.Errorf("the runtime error is not first:\n%s", got)
	}
	if strings.Count(got, "an explicit panic of the package") != 1 {
		t.Errorf("the explicit panic is not labelled once:\n%s", got)
	}
}

