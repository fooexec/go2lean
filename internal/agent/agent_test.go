package agent

import (
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
)

func TestExtractGo(t *testing.T) {
	resp := "some words\n```go\npackage p\n\nfunc F() {}\n```\ntrailing"
	got := extractGo(resp)
	if !strings.HasPrefix(got, "package p") || !strings.HasSuffix(got, "func F() {}\n") {
		t.Fatalf("unexpected block %q", got)
	}
	if extractGo("no code here") != "" {
		t.Fatal("expected empty result without a block")
	}
	// A block without a go2lean:spec marker is a fix snippet and stays in the report.
	if strings.Contains(stripGo(resp), "(revised spec file") || !strings.Contains(stripGo(resp), "func F() {}") {
		t.Fatalf("stripGo removed a non-spec block: %q", stripGo(resp))
	}
	spec := "```go\npackage p\n// go2lean:spec F\nfunc SpecF(ret int) bool { return true }\n```\n"
	if got := stripGo(spec); !strings.Contains(got, "(revised spec file") || strings.Contains(got, "SpecF") {
		t.Fatalf("stripGo kept the spec block: %q", got)
	}
}

func TestExtractGoPrefersSpecBlock(t *testing.T) {
	resp := "fix:\n```go\nfunc Last(xs []int) int { return xs[len(xs)-1] }\n```\nspecs:\n```go\npackage p\n\n// go2lean:spec Last\nfunc SpecLast(xs []int, ret int) bool { return true }\n```\n"
	got := extractGo(resp)
	if !strings.Contains(got, "go2lean:spec") || strings.Contains(got, "func Last(") {
		t.Fatalf("picked the wrong block: %q", got)
	}
	if !hasSpecBlock(resp) || hasSpecBlock("```go\nfunc F() {}\n```") {
		t.Fatal("hasSpecBlock misclassified")
	}
}

func TestExtractGoIndentedFence(t *testing.T) {
	resp := "- fix:\n  ```go\n  func Last(xs []int) int { return xs[len(xs)-1] }\n  ```\n\n```go\npackage p\n\n// go2lean:spec Last\nfunc SpecLast(xs []int, ret int) bool { return true }\n```\n"
	got := extractGo(resp)
	if !strings.HasPrefix(got, "package p") || strings.Contains(got, "func Last(") {
		t.Fatalf("indented fence swallowed the spec block: %q", got)
	}
}

func TestExtractGoTruncated(t *testing.T) {
	resp := "fix:\n```go\nfunc Last() {}\n```\nspecs:\n```go\npackage p\n\n// go2lean:spec F\nfunc SpecF(ret int) bool {\n\treturn true\n}\n\n// go2lean:spec G\nfunc SpecG(ret int) bool {\n\tif ret"
	got := extractGo(resp)
	if !strings.HasSuffix(got, "func SpecF(ret int) bool {\n\treturn true\n}\n") || strings.Contains(got, "SpecG") {
		t.Fatalf("truncated block not cut at the last declaration: %q", got)
	}
	closed := "```go\npackage p\n\n// go2lean:spec F\nfunc SpecF(ret int) bool { return true }\n```\n"
	if got := extractGo(closed); !strings.Contains(got, "SpecF") {
		t.Fatalf("closed block lost: %q", got)
	}
}

func TestNormalizeSpec(t *testing.T) {
	got := normalizeSpec("// header\npackage yourpkg\n\nfunc F() {}\n", "oob")
	if !strings.Contains(got, "package oob\n") || strings.Contains(got, "yourpkg") {
		t.Fatalf("package clause not normalised: %q", got)
	}
	if got := normalizeSpec("func F() {}\n", "oob"); !strings.HasPrefix(got, "package oob\n") {
		t.Fatalf("missing clause not added: %q", got)
	}
}

func TestEntryPoints(t *testing.T) {
	r := &contract.ModelReport{Funcs: []contract.ModelFunc{
		{ID: 0, Name: "Convert", Calls: []int{1, 2}},
		{ID: 1, Name: "helper"},
		{ID: 2, Name: "Exported"},                   // called by Convert: not an entry point
		{ID: 3, Name: "conv.Next", Calls: []int{3}}, // reached through an interface
		{ID: 4, Name: "SpecConvertOK", SpecFor: "Convert"},
		{ID: 5, Name: "Broken", Status: "rejected"},
	}}
	got := entryPoints(r)
	if strings.Join(got, ",") != "Convert,conv.Next" {
		t.Errorf("entry points %v, want [Convert conv.Next]", got)
	}
}

func TestExtractGoSequenceOnly(t *testing.T) {
	resp := "```go\npackage p\n\n// go2lean:sequence C.Process\nfunc SeqC(xs []int) bool { return true }\n```\n"
	if got := extractGo(resp); !strings.Contains(got, "go2lean:sequence") {
		t.Errorf("a file of sequence specs only is the spec file, got %q", got)
	}
}

func TestProposeRetriesWithoutCode(t *testing.T) {
	answers := []string{"<invoke name=\"Bash\">ls</invoke>", "```go\npackage p\n```"}
	var prompts []string
	ask := func(_, user string) (string, error) {
		prompts = append(prompts, user)
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	spec, err := propose(ask, "PROMPT")
	if err != nil || !strings.Contains(spec, "package p") {
		t.Fatalf("propose = %q, %v", spec, err)
	}
	if len(prompts) != 2 || !strings.HasPrefix(prompts[1], noCodeRetryPrompt) {
		t.Fatalf("prompts = %q", prompts)
	}
	answers = []string{"no code", "still none"}
	if _, err := propose(ask, "PROMPT"); err == nil {
		t.Fatal("two answers without code: want an error")
	}
}

func TestAskReportRetriesNonReport(t *testing.T) {
	answers := []string{"I'll ground the report first.\n```bash\ncat proto.go\n```", "# report\n\nnone"}
	n := 0
	ask := func(_, user string) (string, error) {
		n++
		a := answers[0]
		answers = answers[1:]
		if n == 2 && !strings.HasPrefix(user, noToolsReportPrompt) {
			t.Errorf("second prompt lacks the note: %q", user)
		}
		return a, nil
	}
	got, err := askReport(ask, "P")
	if err != nil || !strings.HasPrefix(got, "# report") || n != 2 {
		t.Fatalf("askReport = %q, %v after %d calls", got, err, n)
	}
}
