package pipeline_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/builtin"
	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/pipeline"
)

func preparedOrder(t *testing.T, body string) (contract.Workspace, pipeline.Env) {
	t.Helper()
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GO2LEAN_CACHE", t.TempDir())
	dir := t.TempDir()
	for name, source := range map[string]string{
		"go.mod":   "module example.com/pipelineorder\n\ngo 1.26.0\n",
		"order.go": "package order\n" + body,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := builtin.BindCompareOrder(dir, "Compare", nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	w := contract.Workspace{Root: t.TempDir()}
	if err := w.Init(); err != nil {
		t.Fatal(err)
	}
	return w, pipeline.Env{Root: dir, Module: dir, Overlay: b.Overlay(), CompareOrder: b}
}

func compileOrder(w contract.Workspace, env pipeline.Env, linked bool, log *bytes.Buffer) error {
	if linked {
		_, err := pipeline.CompileLinked(w, env, ".", log)
		return err
	}
	_, err := pipeline.CompileAll(w, env, log, ".")
	return err
}

func TestComparisonGateWithStringsCompare(t *testing.T) {
	w, env := preparedOrder(t, `import "strings"
		func Compare(a,b string) int { return strings.Compare(a,b) }`)
	env.Deps = true
	var log bytes.Buffer
	if err := compileOrder(w, env, false, &log); err != nil {
		t.Fatalf("compile: %v\n%s", err, &log)
	}
	if a := readAssessment(t, w); a.Capability.Status != "static-model-eligible" {
		t.Fatalf("string comparator unsupported: %+v", a.Capability)
	}
}

func readAssessment(t *testing.T, w contract.Workspace) builtin.Assessment {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(w.Root, "builtins", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("missing/ambiguous assessment: %v %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var a builtin.Assessment
	if err := json.Unmarshal(data, &a); err != nil {
		t.Fatal(err)
	}
	if err := a.Binding.Validate(); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestComparisonCompileGate(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate", true: "linked"}[linked], func(t *testing.T) {
			w, env := preparedOrder(t, `func Compare(a,b string) int {
				if a<b { return -1 }; if a>b { return 1 }; return 0
			}`)
			var log bytes.Buffer
			if err := compileOrder(w, env, linked, &log); err != nil {
				t.Fatalf("compile: %v\n%s", err, &log)
			}
			a := readAssessment(t, w)
			if a.Capability.Status != "static-model-eligible" ||
				a.Capability.SourceSHA256 != env.CompareOrder.SHA256 {
				t.Fatalf("wrong static assessment: %+v", a)
			}
			if _, err := os.Stat(env.CompareOrder.File); !os.IsNotExist(err) {
				t.Fatalf("generated source written to checkout: %v", err)
			}
			props, err := w.Properties()
			if err != nil || len(props) != 0 {
				t.Fatalf("compilation activated properties: %v %v", props, err)
			}
		})
	}
}

func TestUnsupportedComparisonStopsBeforeModelEmission(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate", true: "linked"}[linked], func(t *testing.T) {
			w, env := preparedOrder(t, `var calls int
				func Compare(a,b string) int { calls++; return calls }`)
			var log bytes.Buffer
			err := compileOrder(w, env, linked, &log)
			if err == nil || !strings.Contains(err.Error(), "call graph is unsupported") {
				t.Fatalf("unsupported model accepted: %v\n%s", err, &log)
			}
			a := readAssessment(t, w)
			if a.Capability.Status != "unsupported" || len(a.Capability.Issues) == 0 {
				t.Fatalf("unsupported reasons lost: %+v", a)
			}
			if _, err := os.Stat(filepath.Join(w.Root, "lean")); !os.IsNotExist(err) {
				t.Fatalf("model emitted after failed gate: %v", err)
			}
		})
	}
}

func TestComparisonOverlayMustMatchBinding(t *testing.T) {
	for _, mode := range []string{"missing", "mismatched", "alias"} {
		t.Run(mode, func(t *testing.T) {
			w, env := preparedOrder(t, "func Compare(a,b string) int { return 0 }")
			switch mode {
			case "missing":
				env.Overlay = nil
			case "mismatched":
				env.Overlay[env.CompareOrder.File] = []byte("package order\n")
			case "alias":
				alias := filepath.Dir(env.CompareOrder.File) + "/sub/../" + builtin.CompareOrderFile
				env.Overlay[alias] = []byte("package order\n")
			}
			var log bytes.Buffer
			if err := compileOrder(w, env, false, &log); err == nil {
				t.Fatal("invalid source/overlay correspondence accepted")
			}
			if _, err := os.Stat(filepath.Join(w.Root, "builtins")); !os.IsNotExist(err) {
				t.Fatalf("invalid overlay recorded as assessed: %v", err)
			}
		})
	}
}

func TestOrdinaryCompilationDoesNotActivateBuiltin(t *testing.T) {
	w, env := preparedOrder(t, "func Compare(a,b string) int { return 0 }")
	env.CompareOrder, env.Overlay = nil, nil
	var log bytes.Buffer
	if err := compileOrder(w, env, false, &log); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "compare-order:") {
		t.Fatal("ordinary compile activated builtin assessment")
	}
	if _, err := os.Stat(filepath.Join(w.Root, "builtins")); !os.IsNotExist(err) {
		t.Fatal("ordinary compile wrote builtin artifacts")
	}
}
