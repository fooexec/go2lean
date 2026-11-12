package poc

import (
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/sandbox"
)

const bundleModule = "module example.com/bundle\n\ngo 1.26.0\n"
const bundleTarget = "package subject\nfunc Value() int { return 7 }\n"

func putBundleTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func bundleFixture(t *testing.T) (string, contract.Workspace, *prepared, map[string][]byte) {
	t.Helper()
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOPROXY", "off")
	root := t.TempDir()
	putBundleTestFile(t, filepath.Join(root, "go.mod"), []byte(bundleModule))
	putBundleTestFile(t, filepath.Join(root, "subject", "target.go"), []byte(bundleTarget))
	cx := &contract.Counterexample{ID: "bundle", Property: "p_bundle", Frontend: "gossa", Inputs: map[string]string{}}
	prop := &contract.Property{ID: "p_bundle", Kind: contract.KindPostcondition,
		Spec: &contract.SpecRef{GoFunc: "generatedOracle"}, NL: "trusted packaging fixture, not a finding"}
	target := &contract.Target{Package: "example.com/bundle/subject", Func: "Value"}
	abi := &contract.ABI{GoFunc: "Value", Frontend: "gossa", Results: []contract.ABIResult{{GoType: "int"}}}
	fn, imps, err := HarnessFunc(target, prop, cx, abi)
	if err != nil {
		t.Fatal(err)
	}
	p := &prepared{cx: cx, prop: prop, target: target, fn: fn, imps: imps, abi: abi,
		v: &contract.Verdict{Counterexample: cx.ID, Status: contract.VerdictSpurious}}
	overlay := map[string][]byte{
		filepath.Join(root, "subject", "zz_generated_oracle.go"):    []byte("package subject\nfunc generatedOracle(ret int) bool { return helperAccept(ret) }\n"),
		filepath.Join(root, "subject", "zz_generated_predicate.go"): []byte("package subject\nfunc helperAccept(ret int) bool { return ret == 7 }\n"),
		// A sweep may also hold helpers for other target packages. They were
		// not applied by this package's replay and must not enter its bundle.
		filepath.Join(root, "other", "zz_unrelated.go"): []byte("package other\n"),
	}
	w := contract.Workspace{Root: t.TempDir()}
	if err := w.Init(); err != nil {
		t.Fatal(err)
	}
	return root, w, p, overlay
}

func copyBundleTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readBundle(t *testing.T, dir string) reproductionBundle {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var b reproductionBundle
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func saveBundle(t *testing.T, dir string, b reproductionBundle) {
	t.Helper()
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	putBundleTestFile(t, filepath.Join(dir, "bundle.json"), data)
}

func buildBundlePreparer(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "prepare")
	cmd := exec.Command("go", "build", "-o", bin, filepath.Join(dir, "prepare.go"))
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("standalone preparer did not build: %v\n%s", err, out)
	}
	return bin
}

func TestBundlePreparerRejectsIncompleteOrUnsafeArtifacts(t *testing.T) {
	root, w, p, overlay := bundleFixture(t)
	if err := deliver(w, root, p, overlay); err != nil {
		t.Fatal(err)
	}
	original := w.PocDir(p.cx.ID)
	preparer := buildBundlePreparer(t, original)
	for _, mode := range []string{
		"version", "duplicate", "missing-harness", "traversal", "absolute-destination",
		"foreign-package", "source-traversal", "source-absolute", "tampered-source",
		"missing-source", "source-symlink", "destination-symlink", "parent-symlink",
		"trailing-json", "unknown-field", "existing-output",
	} {
		t.Run(mode, func(t *testing.T) {
			bundle, clone := t.TempDir(), t.TempDir()
			copyBundleTree(t, original, bundle)
			putBundleTestFile(t, filepath.Join(clone, "subject", "target.go"), []byte(bundleTarget))
			b := readBundle(t, bundle)
			output := filepath.Join(t.TempDir(), "overlay.json")
			switch mode {
			case "version":
				b.Version++
			case "duplicate":
				b.Files = append(b.Files, b.Files[0])
			case "missing-harness":
				b.Files = b.Files[1:]
			case "traversal":
				b.Files[0].Path = "../outside.go"
			case "absolute-destination":
				b.Files[0].Path = filepath.Join(t.TempDir(), "outside.go")
			case "foreign-package":
				b.Files[0].Path = "other/outside.go"
			case "source-traversal":
				b.Files[0].Source = "../outside.go"
			case "source-absolute":
				b.Files[0].Source = filepath.Join(t.TempDir(), "outside.go")
			case "tampered-source":
				putBundleTestFile(t, filepath.Join(bundle, b.Files[0].Source), []byte("package modified\n"))
			case "missing-source", "source-symlink":
				path := filepath.Join(bundle, b.Files[0].Source)
				data, _ := os.ReadFile(path)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if mode == "source-symlink" {
					other := filepath.Join(t.TempDir(), "outside.go")
					putBundleTestFile(t, other, data)
					if err := os.Symlink(other, path); err != nil {
						t.Fatal(err)
					}
				}
			case "destination-symlink":
				if err := os.Symlink(filepath.Join(clone, "subject", "target.go"), filepath.Join(clone, b.Files[0].Path)); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				if err := os.Rename(filepath.Join(clone, "subject"), filepath.Join(clone, "real-subject")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("real-subject", filepath.Join(clone, "subject")); err != nil {
					t.Fatal(err)
				}
			case "existing-output":
				putBundleTestFile(t, output, []byte("do not overwrite"))
			}
			saveBundle(t, bundle, b)
			switch mode {
			case "trailing-json":
				data, _ := os.ReadFile(filepath.Join(bundle, "bundle.json"))
				putBundleTestFile(t, filepath.Join(bundle, "bundle.json"), append(data, []byte("\n{}")...))
			case "unknown-field":
				data, _ := os.ReadFile(filepath.Join(bundle, "bundle.json"))
				putBundleTestFile(t, filepath.Join(bundle, "bundle.json"), []byte(strings.Replace(string(data), `"version":`, `"unexpected":true,"version":`, 1)))
			}
			cmd := exec.Command(preparer, "-bundle", bundle, "-module", clone, "-output", output)
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("invalid artifact accepted: %s", out)
			}
			data, err := os.ReadFile(output)
			if mode == "existing-output" {
				if err != nil || string(data) != "do not overwrite" {
					t.Fatal("existing output overwritten")
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("invalid artifact created output: %v", err)
			}
		})
	}
}

// Trusted packaging fixture only: the oracle holds, so both real-Go
// observations are spurious candidates, NOT confirmed defect/release evidence.
func TestBundleReplaysAfterOriginalWorkspaceAndOverlayAreGone(t *testing.T) {
	if sandbox.Mode() != sandbox.ModeBwrap {
		if os.Getenv("GO2LEAN_REQUIRE_SANDBOX_TESTS") == "1" {
			t.Fatal("portable bundle verification requires real isolation")
		}
		t.Skip("requires bubblewrap isolation; mandatory in the sandbox CI gate")
	}
	root, w, p, overlay := bundleFixture(t)
	p.target.ID, p.target.Module = "target_bundle", "example.com/bundle"
	p.prop.Target, p.prop.Frontend = p.target.ID, "gossa"
	p.cx.Engine = "trusted-packaging-unit-fixture"
	model, err := w.WriteABI(p.abi, p.target.Package, p.target.Func)
	if err != nil {
		t.Fatal(err)
	}
	p.target.Models = map[string]string{"gossa": model}
	for _, err := range []error{w.WriteTarget(p.target), w.WriteProperty(p.prop), w.WriteCounterexample(p.cx)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	verdicts, err := VerifyAll(w, root, []string{p.cx.ID}, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 1 || verdicts[0].Status != contract.VerdictSpurious ||
		verdicts[0].Observed["ret"] != "7" || !strings.Contains(verdicts[0].Evidence.StdoutTail, "GO2LEAN_RESULT") {
		t.Fatalf("initial public replay observation invalid: %+v", verdicts)
	}
	detached := filepath.Join(t.TempDir(), "detached bundle")
	copyBundleTree(t, w.PocDir(p.cx.ID), detached)
	b := readBundle(t, detached)
	if len(b.Files) != 3 || b.PackageDir != "subject" {
		t.Fatalf("lost helpers or included unrelated overlay: %+v", b)
	}
	clone := filepath.Join(t.TempDir(), "new clone")
	putBundleTestFile(t, filepath.Join(clone, "go.mod"), []byte(bundleModule))
	putBundleTestFile(t, filepath.Join(clone, "subject", "target.go"), []byte(bundleTarget))
	overlay = nil
	for _, path := range []string{root, w.Root} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	preparer := buildBundlePreparer(t, detached)
	reproductionOverlay := filepath.Join(t.TempDir(), "overlay.json")
	prepare := exec.Command(preparer, "-bundle", detached, "-module", clone, "-output", reproductionOverlay)
	if output, err := prepare.CombinedOutput(); err != nil {
		t.Fatalf("detached overlay preparation: %v\n%s", err, output)
	}
	// First show the helper is necessary: the delivered harness alone fails.
	harness, err := os.ReadFile(filepath.Join(detached, b.Files[0].Source))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "reproduction.test")
	_, failed, err := sandbox.BuildTest(clone, "./subject", filepath.Join(clone, "subject"),
		map[string][]byte{filepath.Base(b.Files[0].Path): harness}, bin)
	if err != nil || failed == 0 {
		t.Fatalf("harness-only negative control did not fail compilation: exit=%d err=%v", failed, err)
	}
	build := exec.Command("go", "test", "-c", "-overlay", reproductionOverlay, "-o", bin, "./subject")
	build.Dir = clone
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("detached sources failed to build: %v\n%s", err, output)
	}
	stdout, stderr, code, err := sandbox.Run(bin, filepath.Join(clone, "subject"),
		[]string{"-test.count=1", "-test.v", "-test.run", "^" + b.Test + "$"}, nil, sandbox.Default)
	r, found := parseResults(stdout)[p.cx.ID]
	if err != nil || code != 0 || !found || r.Panicked || r.Spec == nil || !*r.Spec || r.Ret != "7" {
		t.Fatalf("detached Go observation missing/wrong: exit=%d err=%v result=%+v\n%s\n%s", code, err, r, stdout, stderr)
	}
	for _, f := range b.Files {
		if filepath.Base(f.Path) != "target.go" {
			if _, err := os.Stat(filepath.Join(clone, filepath.FromSlash(f.Path))); !os.IsNotExist(err) {
				t.Fatal("preparation wrote generated source into clone")
			}
		}
	}
}

func TestBundleMatchesRewritePrecedenceAndRejectsStaleUpdates(t *testing.T) {
	root, w, p, overlay := bundleFixture(t)
	name := "zz_generated_predicate.go"
	rewritten := []byte("package subject\n// rewritten support fixture\nfunc helperAccept(ret int) bool { return ret == 7 }\n")
	p.replayed = map[string][]byte{name: rewritten}
	p.race = true // flag propagation only, not a planted race finding
	if err := deliver(w, root, p, overlay); err != nil {
		t.Fatal(err)
	}
	dir := w.PocDir(p.cx.ID)
	b := readBundle(t, dir)
	if !b.Race {
		t.Fatal("race detector requirement lost from manifest")
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil || !strings.Contains(string(readme), "go test -race ") {
		t.Fatal("race detector requirement lost from instructions")
	}
	found := false
	for _, f := range b.Files {
		if filepath.Base(f.Path) == name {
			found = true
			data, err := os.ReadFile(filepath.Join(dir, f.Source))
			if err != nil || !strings.HasPrefix(f.Source, "replay/") || string(data) != string(rewritten) {
				t.Fatal("rewrite did not take precedence over general overlay")
			}
		}
	}
	if !found {
		t.Fatal("rewritten helper omitted from manifest")
	}
	// Portable preparation must ignore the old absolute-path overlay.
	putBundleTestFile(t, filepath.Join(dir, "replay", "overlay.json"), []byte("not a usable legacy overlay"))
	preparer := buildBundlePreparer(t, dir)
	output := filepath.Join(t.TempDir(), "overlay.json")
	command := exec.Command(preparer, "-bundle", dir, "-module", root, "-output", output)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("portable preparation depended on legacy absolute overlay: %v\n%s", err, out)
	}
	invalid := maps.Clone(overlay)
	invalid[filepath.Join(root, "subject", "not-go.txt")] = []byte("unsupported overlay")
	if err := deliver(w, root, p, invalid); err == nil {
		t.Fatal("unsupported support file silently discarded")
	}
	if _, err := os.Stat(filepath.Join(dir, "bundle.json")); !os.IsNotExist(err) {
		t.Fatal("failed update retained a ready-looking manifest")
	}
}

func TestArtifactPathsAndOverlayAliases(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"../outside.go", "/absolute.go", "sub/../outside.go", "sub\\outside.go"} {
		if err := writeArtifactFile(dir, path, nil); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "support")); err != nil {
		t.Fatal(err)
	}
	if err := writeArtifactFile(dir, "support/outside.go", nil); err == nil {
		t.Fatal("artifact writer followed a directory symlink")
	}
	alias := dir + "/sub/../helper.go"
	if _, err := packageOverlayFiles(dir, map[string][]byte{alias: nil}); err == nil {
		t.Fatal("ambiguous package overlay path accepted")
	}
}

func TestInvalidReproductionIDDoesNotCreateDirectories(t *testing.T) {
	root, w, p, overlay := bundleFixture(t)
	p.cx.ID = "../escape"
	if err := deliver(w, root, p, overlay); err == nil {
		t.Fatal("unsafe test identifier accepted")
	}
	if _, err := os.Stat(filepath.Join(w.Root, "poc")); !os.IsNotExist(err) {
		t.Fatal("unsafe identifier created artifact directories")
	}
}

func TestBundleForModuleRootPackage(t *testing.T) {
	root, w, p, overlay := bundleFixture(t)
	sub := filepath.Join(root, "subject")
	if err := os.Rename(filepath.Join(sub, "target.go"), filepath.Join(root, "target.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	p.target.Package = "example.com/bundle"
	rootOverlay := map[string][]byte{}
	for path, data := range overlay {
		if filepath.Dir(path) == sub {
			rootOverlay[filepath.Join(root, filepath.Base(path))] = data
		}
	}
	if err := deliver(w, root, p, rootOverlay); err != nil {
		t.Fatal(err)
	}
	dir := w.PocDir(p.cx.ID)
	b := readBundle(t, dir)
	if b.PackageDir != "." {
		t.Fatalf("wrong root package path: %q", b.PackageDir)
	}
	preparer := buildBundlePreparer(t, dir)
	output := filepath.Join(t.TempDir(), "overlay.json")
	cmd := exec.Command(preparer, "-bundle", dir, "-module", root, "-output", output)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("root package overlay: %v\n%s", err, out)
	}
	build := exec.Command("go", "test", "-c", "-overlay", output, "-o", filepath.Join(t.TempDir(), "root.test"), ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("root package bundle did not compile: %v\n%s", err, out)
	}
}

func TestReplayRefusesToDeleteCopiedHarness(t *testing.T) {
	root, _, p, overlay := bundleFixture(t)
	pkgDir := filepath.Join(root, "subject")
	path := filepath.Join(pkgDir, "zz_go2lean_poc_user_test.go")
	source := []byte("package subject\n// user-owned copied observation\n")
	putBundleTestFile(t, path, source)
	_, _, _, _, _, err := runBatch(root, pkgDir, "subject", p.target.Package, []*prepared{p}, overlay)
	if err == nil || !strings.Contains(err.Error(), "no files were removed") {
		t.Fatalf("copied harness not reported safely: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(source) {
		t.Fatal("replay modified or deleted a source file")
	}
}
