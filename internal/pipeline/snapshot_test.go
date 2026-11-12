package pipeline_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/builtin"
	"github.com/fooexec/go2lean/internal/constraint"
	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/pipeline"
	"github.com/fooexec/go2lean/internal/sandbox"
)

func activatedOrder(t *testing.T, body string) (contract.Workspace, pipeline.Env, *contract.Property) {
	t.Helper()
	w, env := preparedOrder(t, body)
	env.CompareOrder, env.Overlay = nil, nil
	// This is not an installation/cache test; reuse the installed model core.
	t.Setenv("GO2LEAN_CACHE", "")
	env, err := pipeline.ConfigureCompareOrder(w, env, "Compare", ".")
	if err != nil {
		t.Fatal(err)
	}
	reports, err := pipeline.CompileAll(w, env, &bytes.Buffer{}, ".")
	if err != nil {
		t.Fatal(err)
	}
	var main *contract.ModelReport
	for _, r := range reports {
		if !r.Dependency {
			main = r
		}
	}
	if main == nil {
		t.Fatal("no main report")
	}
	props, err := pipeline.Propose(w, main, &bytes.Buffer{})
	if err != nil || len(props) != 1 || props[0].Builtin == nil {
		t.Fatalf("canonical property missing: %v %v", props, err)
	}
	return w, env, props[0]
}

func copySnapshotTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompiledBuiltinSnapshotRoundTripAndRebase(t *testing.T) {
	w, env, p := activatedOrder(t, `func Compare(a,b string) int {
		if a<b { return -1 }; if a>b { return 1 }; return 0
	}`)
	s, err := builtin.ValidateProperty(w, p)
	if err != nil {
		t.Fatal(err)
	}
	clone := t.TempDir()
	copySnapshotTree(t, env.Module, clone)
	overlay, err := s.RestoreOverlay(clone)
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(clone, builtin.CompareOrderFile)
	if string(overlay[newPath]) != s.Binding.Source || len(overlay) != 1 {
		t.Fatal("restore depended on original absolute helper path")
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatal("restoration wrote into source checkout")
	}
	if err := os.WriteFile(filepath.Join(clone, "order.go"), []byte("package order\nfunc Compare(a,b string) int{return 1}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreOverlay(clone); err == nil {
		t.Fatal("changed target source accepted")
	}
	resume := env
	resume.Overlay, resume.CompareOrder, resume.Resume = nil, nil, true
	resume, err = pipeline.ConfigureCompareOrder(w, resume, "Compare", ".")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.CompileAll(w, resume, &bytes.Buffer{}, "."); err == nil {
		t.Fatal("active models could be overwritten by recompilation")
	}
}

func TestCompiledBuiltinSnapshotRejectsTampering(t *testing.T) {
	w, _, p := activatedOrder(t, "func Compare(a,b string) int {return 0}")
	for _, mode := range []string{"snapshot", "model", "extra-model", "property", "stripped-ref"} {
		t.Run(mode, func(t *testing.T) {
			copy := contract.Workspace{Root: t.TempDir()}
			copySnapshotTree(t, w.Root, copy.Root)
			property := *p
			spec := *p.Spec
			property.Spec = &spec
			switch mode {
			case "snapshot":
				path := filepath.Join(copy.Root, "checks", "compare-order-"+p.Builtin.SnapshotSHA256+".json")
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString(" ")
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "model":
				if err := os.WriteFile(filepath.Join(copy.Root, "lean", "Main.lean"), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			case "extra-model":
				if err := os.WriteFile(filepath.Join(copy.Root, "models", "extra.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "property":
				property.Spec.GoFunc = "unrelatedOracle"
			case "stripped-ref":
				property.Builtin, property.Source = nil, "template"
			}
			if _, err := builtin.ValidateProperty(copy, &property); err == nil {
				t.Fatal("tampered compiled check accepted")
			}
		})
	}
}

func TestBuiltinSnapshotRejectsStaleResumeCaches(t *testing.T) {
	w, env, p := activatedOrder(t, "func Compare(a,b string) int {return 0}")
	env.Resume = true
	if err := w.WriteHuntRecord(&contract.HuntRecord{Property: p.ID, Summary: "unit cache fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Hunt(w, env, p, constraint.SearchOptions{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "cached hunt") {
		t.Fatalf("unbound hunt cache accepted: %v", err)
	}
	cx := &contract.Counterexample{ID: "metadata_fixture", Property: p.ID, Frontend: "gossa", Engine: "metadata-unit", Inputs: map[string]string{}}
	v := &contract.Verdict{Counterexample: cx.ID, Status: contract.VerdictSpurious}
	if err := builtin.ValidateCachedVerdict(w, env.Module, cx, p, v); err == nil {
		t.Fatal("unbound verdict cache accepted")
	}
	stamp, err := builtin.ReplayRef(p, cx)
	if err != nil {
		t.Fatal(err)
	}
	v.Builtin = stamp
	cx.Fuel++
	if err := builtin.ValidateCachedVerdict(w, env.Module, cx, p, v); err == nil {
		t.Fatal("modified counterexample accepted with old verdict")
	}
}

// Requires actual Lean search and sandboxed Go replay. This planted unit
// comparator is NOT external-project/release evidence; no witness is supplied.
func TestBuiltinSearchReplayFromPersistedSnapshot(t *testing.T) {
	if os.Getenv("GO2LEAN_REQUIRE_BUILTIN_TESTS") != "1" {
		t.Skip("run the mandatory builtin integration CI gate")
	}
	if sandbox.Mode() != sandbox.ModeBwrap {
		t.Fatal("builtin integration requires actual bubblewrap isolation")
	}
	w, env, p := activatedOrder(t, "func Compare(a,b string) int {return 1}")
	plain := pipeline.Env{Root: env.Module, Module: env.Module}
	cx, err := pipeline.Hunt(w, plain, p, constraint.SearchOptions{
		Engine: constraint.EngineRandom, Seed: 42, Trials: 20, Fuel: 3000,
		MaxLen: 4, BudgetSeconds: -1, Jobs: 1,
	}, &bytes.Buffer{})
	if err != nil || cx == nil || cx.ModelOutput == nil || cx.ModelOutput.Res != "ok" {
		t.Fatalf("real model search did not produce a non-panic witness: cx=%+v err=%v", cx, err)
	}
	// Deliberately provide NO Env overlay or binding to the replay stage.
	v, err := pipeline.Verify(w, plain, cx.ID, &bytes.Buffer{})
	if err != nil || v.Status != contract.VerdictConfirmed || v.Builtin == nil || v.Observed["panic"] != "" {
		t.Fatalf("independent real Go replay failed: v=%+v err=%v", v, err)
	}
	if _, err := os.Stat(filepath.Join(v.PocDir, "bundle.json")); err != nil {
		t.Fatal(err)
	}
	plain.Resume = true
	if _, err := pipeline.Verify(w, plain, cx.ID, &bytes.Buffer{}); err != nil {
		t.Fatalf("unchanged bound verdict did not resume: %v", err)
	}
}
