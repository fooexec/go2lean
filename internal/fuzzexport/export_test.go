package fuzzexport

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/repo"
)

// TestExportBuildsAndFinds exports a postcondition over strings and a
// sequence spec over a slice of structs (decoded from bytes), builds them
// into a scratch copy of examples/strs and checks that the fuzzer finds
// the planted violation and passes the true spec.
func TestExportBuildsAndFinds(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test -fuzz")
	}
	root, err := repo.Root()
	if err != nil {
		t.Fatal(err)
	}
	extra := []byte(`package strs

type go2leanStep struct {
	Op   int
	Text string
}

// go2lean:sequence Before
func SeqBeforeNeverBoth(steps []go2leanStep) bool {
	for _, s := range steps {
		if Before(s.Text, "m") && Before("m", s.Text) {
			return false
		}
	}
	return true
}

// go2lean:spec Before
func SpecBeforePlantedWrong(a, b string, ret bool) bool { return !ret || len(a) < 3 }
`)
	dir := filepath.Join(root, "examples", "strs")
	overlay := map[string][]byte{filepath.Join(dir, "zz_extra_test.go"): extra}
	src, targets, skipped, err := Export(root, "github.com/fooexec/go2lean/examples/strs", overlay, []Spec{
		{Property: "p_true", Kind: contract.KindSequence, Spec: "SeqBeforeNeverBoth"},
		{Property: "p_wrong", Kind: contract.KindPostcondition, Spec: "SpecBeforePlantedWrong", Target: "Before"},
		{Property: "p_missing", Kind: contract.KindPostcondition, Spec: "SpecNoSuch", Target: "Before"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || len(skipped) != 1 || !strings.Contains(src, "go2leanFuzzReader{b: go2leanData}") {
		t.Fatalf("targets %v skipped %v\n%s", targets, skipped, src)
	}
	// Build and fuzz in a scratch copy, so the package directory stays as it is.
	tmp := t.TempDir()
	cp := exec.Command("cp", "-r", dir, filepath.Join(tmp, "strs"))
	if out, err := cp.CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module scratch\n\ngo 1.24\n"), 0o644)
	sdir := filepath.Join(tmp, "strs")
	os.WriteFile(filepath.Join(sdir, "zz_extra_test.go"), extra, 0o644)
	os.WriteFile(filepath.Join(sdir, FileName), []byte(src), 0o644)
	for _, c := range []struct {
		fuzz string
		fail bool
	}{{"FuzzGo2Lean_SpecBeforePlantedWrong", true}, {"FuzzGo2Lean_SeqBeforeNeverBoth", false}} {
		cmd := exec.Command("go", "test", "-run=^$", "-fuzz=^"+c.fuzz+"$", "-fuzztime=5s", ".")
		cmd.Dir = sdir
		out, err := cmd.CombinedOutput()
		if err != nil && timedOut(string(out)) {
			err = nil
		}
		if (err != nil) != c.fail {
			t.Errorf("%s: failed=%v, want %v\n%s", c.fuzz, err != nil, c.fail, out)
		}
	}
}

// TestExportVariadicTarget passes the spec's slice to a variadic target as
// its variadic parameter.
func TestExportVariadicTarget(t *testing.T) {
	root, err := repo.Root()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "examples", "strs")
	overlay := map[string][]byte{
		filepath.Join(dir, "zz_join.go"): []byte(`package strs

func JoinAll(sep string, parts ...string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
`),
		filepath.Join(dir, "zz_join_test.go"): []byte(`package strs

// go2lean:spec JoinAll
func SpecJoinAllLen(sep string, parts []string, ret string) bool { return len(parts) > 0 || ret == "" }
`),
	}
	src, targets, skipped, err := Export(root, "github.com/fooexec/go2lean/examples/strs", overlay, []Spec{
		{Property: "p_join", Kind: contract.KindPostcondition, Spec: "SpecJoinAllLen", Target: "JoinAll"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || len(skipped) != 0 || !strings.Contains(src, "JoinAll(p0, p1...)") {
		t.Fatalf("targets %v skipped %v\n%s", targets, skipped, src)
	}
}

// TestExportDropsImportsOfSkippedTargets: a target that cannot be built
// leaves no import behind, and a decoder that reads nothing leaves r used
// (both made the file fail to compile on grpc-go's encoding/proto, #220).
func TestExportDropsImportsOfSkippedTargets(t *testing.T) {
	root, err := repo.Root()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "examples", "strs")
	overlay := map[string][]byte{
		filepath.Join(dir, "zz_skip_test.go"): []byte(`package strs

import "strings"

type go2leanEmpty struct{}

// go2lean:sequence Before
func SeqSkipped(b *strings.Builder, f func()) bool { return true }

// go2lean:sequence Before
func SeqEmpty(e go2leanEmpty) bool { return Before("a", "b") }
`),
	}
	src, targets, skipped, err := Export(root, "github.com/fooexec/go2lean/examples/strs", overlay, []Spec{
		{Property: "p_skip", Kind: contract.KindSequence, Spec: "SeqSkipped"},
		{Property: "p_empty", Kind: contract.KindSequence, Spec: "SeqEmpty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || len(skipped) != 1 || strings.Contains(src, `"strings"`) {
		t.Fatalf("targets %v skipped %v\n%s", targets, skipped, src)
	}
	tmp := t.TempDir()
	f := filepath.Join(tmp, "x_test.go")
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ov := filepath.Join(tmp, "overlay.json")
	files := map[string]string{filepath.Join(dir, "zz_skip_test.go"): filepath.Join(tmp, "skip_test.go"), filepath.Join(dir, "zz_fuzz_test.go"): f}
	if err := os.WriteFile(filepath.Join(tmp, "skip_test.go"), overlay[filepath.Join(dir, "zz_skip_test.go")], 0o644); err != nil {
		t.Fatal(err)
	}
	var js strings.Builder
	js.WriteString(`{"Replace":{`)
	first := true
	for k, v := range files {
		if !first {
			js.WriteString(",")
		}
		first = false
		js.WriteString(`"` + k + `":"` + v + `"`)
	}
	js.WriteString("}}")
	if err := os.WriteFile(ov, []byte(js.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "vet", "-overlay", ov, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated file does not build: %v\n%s\n%s", err, out, src)
	}
}

// TestExportSeedsReachWideIntegers: the seed corpus alone (go test -run,
// no fuzzing) falsifies a spec only values far from zero violate, which an
// empty corpus seldom reaches (#290); seeds of every fuzz argument kind
// compile, and a true spec passes them.
func TestExportSeedsReachWideIntegers(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	root, err := repo.Root()
	if err != nil {
		t.Fatal(err)
	}
	extra := []byte(`package strs

type go2leanEv struct {
	N uint64
	S string
}

// go2lean:sequence Before
func SeqWideIsSmall(x uint64, y int32) bool { return x < 1<<40 }

// go2lean:sequence Before
func SeqAllKinds(a int8, b uint16, c float32, d float64, e bool, f string, g []byte, h int, evs []go2leanEv) bool {
	return true
}
`)
	dir := filepath.Join(root, "examples", "strs")
	overlay := map[string][]byte{filepath.Join(dir, "zz_extra_test.go"): extra}
	src, targets, skipped, err := Export(root, "github.com/fooexec/go2lean/examples/strs", overlay, []Spec{
		{Property: "p_wide", Kind: contract.KindSequence, Spec: "SeqWideIsSmall"},
		{Property: "p_kinds", Kind: contract.KindSequence, Spec: "SeqAllKinds"},
	})
	if err != nil || len(targets) != 2 || len(skipped) != 0 {
		t.Fatalf("err %v targets %v skipped %v", err, targets, skipped)
	}
	if n := strings.Count(src, "f.Add("); n < 2*(1+len(boundaries)) {
		t.Errorf("%d seeds:\n%s", n, src)
	}
	tmp := t.TempDir()
	if out, err := exec.Command("cp", "-r", dir, filepath.Join(tmp, "strs")).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module scratch\n\ngo 1.24\n"), 0o644)
	sdir := filepath.Join(tmp, "strs")
	os.WriteFile(filepath.Join(sdir, "zz_extra_test.go"), extra, 0o644)
	os.WriteFile(filepath.Join(sdir, FileName), []byte(src), 0o644)
	for _, c := range []struct {
		fuzz string
		fail bool
	}{{"FuzzGo2Lean_SeqWideIsSmall", true}, {"FuzzGo2Lean_SeqAllKinds", false}} {
		cmd := exec.Command("go", "test", "-run=^"+c.fuzz+"$", ".")
		cmd.Dir = sdir
		out, err := cmd.CombinedOutput()
		if (err != nil) != c.fail || (c.fail && !strings.Contains(string(out), "go2lean: SeqWideIsSmall is false")) {
			t.Errorf("%s on the seeds: failed=%v, want %v\n%s", c.fuzz, err != nil, c.fail, out)
		}
	}
}

// A seed corpus entry that fails is reported by Go without a corpus file;
// seedRe and seedArgs recover which entry and its arguments.
func TestSeedFailureParsing(t *testing.T) {
	out := "        zz_go2lean_fuzz_test.go:70: go2lean: SeqAckDelayScaling is false\n    \nFAIL\n" +
		"fuzz: elapsed: 0s, gathering baseline coverage: 0/16 completed\n" +
		"failure while testing seed corpus entry: FuzzGo2Lean_SeqAckDelayScaling/seed#2\n"
	m := seedRe.FindStringSubmatch(out)
	if m == nil || m[1] != "seed#2" || m[2] != "2" || !reportRe.MatchString(out) {
		t.Fatalf("match %v", m)
	}
	src := "func FuzzGo2Lean_Other(f *testing.F) {\n\tf.Add(uint64(9))\n}\n" +
		"func FuzzGo2Lean_SeqAckDelayScaling(f *testing.F) {\n\tf.Add(uint64(0), uint8(0))\n\tf.Add(uint64(1), uint8(1))\n\tf.Add(uint64(18446744073709551615), uint8(255))\n\tf.Fuzz(func(t *testing.T, a0 uint64, a1 uint8) {\n"
	if got := seedArgs(src, "FuzzGo2Lean_SeqAckDelayScaling", 2); got != "uint64(18446744073709551615), uint8(255)" {
		t.Errorf("seedArgs = %q", got)
	}
	if seedArgs(src, "FuzzGo2Lean_SeqAckDelayScaling", 5) != "" || seedArgs(src, "FuzzGo2Lean_None", 0) != "" {
		t.Error("absent seed not empty")
	}
}
