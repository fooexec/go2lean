package builtin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/fooexec/go2lean/internal/contract"
	leanassets "github.com/fooexec/go2lean/lean"
)

type Snapshot struct {
	Version       int               `json:"version"`
	Binding       *Binding          `json:"binding"`
	Capability    *Capability       `json:"capability"`
	Sources       *SourceGraph      `json:"sources"`
	Models        map[string]string `json:"models"`
	RuntimeSHA256 string            `json:"runtime_sha256"`
	Target        string            `json:"target"`
	OracleDecl    string            `json:"oracle_decl"`
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validateRef(ref *contract.BuiltinRef) error {
	if ref == nil || ref.ID != CompareOrderID || ref.Version != CompareOrderVersion || !validDigest(ref.SnapshotSHA256) {
		return fmt.Errorf("builtin: unsupported or malformed snapshot reference")
	}
	return nil
}

func strictJSON(data []byte, dest any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dest); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("builtin: trailing snapshot data")
	}
	return nil
}

func atomicJSON(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".check-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func runtimeDigest() (string, error) {
	files := map[string]string{}
	err := fs.WalkDir(leanassets.Core, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := leanassets.Core.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = digest(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return canonicalDigest(files)
}

// Model inventory includes reports, ABIs, targets and generated models, not
// just the Lean body. Search writes requests/results/proofs outside these
// scopes. Lake's cache/path-dependent manifest is regenerated, not trusted.
func modelInventory(w contract.Workspace) (map[string]string, error) {
	out := map[string]string{}
	for _, dir := range []string{"models", "targets", "lean/Go2Lean/Generated"} {
		err := filepath.WalkDir(filepath.Join(w.Root, filepath.FromSlash(dir)), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("builtin: symlink in compiled model inventory")
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(w.Root, path)
			if err != nil {
				return err
			}
			sum, err := fileDigest(path)
			if err == nil {
				out[filepath.ToSlash(rel)] = sum
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"lean/lean-toolchain", "lean/Main.lean"} {
		sum, err := fileDigest(filepath.Join(w.Root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		out[name] = sum
	}
	return out, nil
}

func ActiveRef(w contract.Workspace) (*contract.BuiltinRef, error) {
	data, err := os.ReadFile(filepath.Join(w.Root, "checks", "active.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ref contract.BuiltinRef
	if err := strictJSON(data, &ref); err != nil {
		return nil, err
	}
	return &ref, validateRef(&ref)
}

func RequireFreshWorkspace(w contract.Workspace) error {
	entries, err := os.ReadDir(w.Root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "go.mod" {
			return fmt.Errorf("builtin checks require a dedicated fresh workspace; %s already exists", e.Name())
		}
	}
	return nil
}

func WriteSnapshot(w contract.Workspace, root string, b *Binding, before *SourceGraph, reports []*contract.ModelReport) (*contract.BuiltinRef, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	if before == nil || before.Package != b.Package {
		return nil, fmt.Errorf("builtin: missing precompile source fingerprint")
	}
	after, err := CaptureSources(root, before.PackageDir)
	if err != nil {
		return nil, err
	}
	if !before.Equal(after) {
		return nil, fmt.Errorf("builtin: source/build configuration changed during compilation; use a fresh workspace")
	}
	s := &Snapshot{Version: 1, Binding: b, Sources: after}
	assessment, err := os.ReadFile(filepath.Join(w.Root, "builtins", b.ID+"-"+b.SHA256+".json"))
	if err != nil {
		return nil, err
	}
	var a Assessment
	if err := strictJSON(assessment, &a); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(a.Binding, b) || a.Capability == nil || a.Capability.Status != "static-model-eligible" ||
		a.Capability.SourceSHA256 != b.SHA256 || len(a.Capability.Issues) != 0 || len(a.Capability.Functions) == 0 {
		return nil, fmt.Errorf("builtin: successful static compiler assessment is missing")
	}
	s.Capability = a.Capability
	for _, r := range reports {
		if r.Package != b.Package || r.Frontend != "gossa" {
			continue
		}
		var names []string
		for _, f := range r.Funcs {
			names = append(names, f.Name)
			if f.Name == b.Oracle && f.SpecFor == b.Target && f.Status == contract.ModelComplete {
				s.OracleDecl = fmt.Sprintf("%s.f%d", r.LeanModule, f.ID)
			}
		}
		s.Target = contract.TargetID(b.Package, contract.FuncKeys(names)[b.Target])
	}
	target, err := w.Target(s.Target)
	if err != nil || target.Package != b.Package || target.Func != b.Target || s.OracleDecl == "" {
		return nil, fmt.Errorf("builtin: compiled target/oracle identity is missing or inconsistent")
	}
	if s.Models, err = modelInventory(w); err != nil {
		return nil, err
	}
	if s.RuntimeSHA256, err = runtimeDigest(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	ref := &contract.BuiltinRef{ID: b.ID, Version: b.Version, SnapshotSHA256: digest(data)}
	if err := atomicJSON(filepath.Join(w.Root, "checks", "compare-order-"+ref.SnapshotSHA256+".json"), data); err != nil {
		return nil, err
	}
	active, _ := json.Marshal(ref)
	if err := atomicJSON(filepath.Join(w.Root, "checks", "active.json"), active); err != nil {
		return nil, err
	}
	return ref, nil
}

func LoadSnapshot(w contract.Workspace, ref *contract.BuiltinRef) (*Snapshot, error) {
	if err := validateRef(ref); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(w.Root, "checks", "compare-order-"+ref.SnapshotSHA256+".json"))
	if err != nil {
		return nil, err
	}
	if digest(data) != ref.SnapshotSHA256 {
		return nil, fmt.Errorf("builtin: compiled snapshot hash mismatch")
	}
	var s Snapshot
	if err := strictJSON(data, &s); err != nil {
		return nil, err
	}
	if s.Version != 1 || s.Sources == nil || s.Binding == nil || s.Binding.Validate() != nil ||
		s.Capability == nil || s.Capability.Status != "static-model-eligible" ||
		s.Capability.SourceSHA256 != s.Binding.SHA256 || len(s.Capability.Issues) != 0 || len(s.Capability.Functions) == 0 ||
		s.Sources.Package != s.Binding.Package || !localRelative(s.Sources.PackageDir) ||
		!validDigest(s.Sources.ConfigHash) || len(s.Sources.Files) == 0 || s.Sources.GoVersion == "" ||
		s.Target == "" || s.OracleDecl == "" {
		return nil, fmt.Errorf("builtin: malformed or unsupported compiled snapshot")
	}
	current, err := modelInventory(w)
	if err != nil || !reflect.DeepEqual(s.Models, current) {
		return nil, fmt.Errorf("builtin: compiled model/report/ABI inventory changed; use a fresh workspace")
	}
	runtime, err := runtimeDigest()
	if err != nil || runtime != s.RuntimeSHA256 {
		return nil, fmt.Errorf("builtin: bundled model runtime changed; recompile in a fresh workspace")
	}
	target, err := w.Target(s.Target)
	if err != nil || target.Package != s.Binding.Package || target.Func != s.Binding.Target {
		return nil, fmt.Errorf("builtin: snapshot target identity is inconsistent")
	}
	report, err := w.ModelReport("gossa", s.Binding.Package)
	if err != nil {
		return nil, err
	}
	oracleOK := false
	for _, f := range report.Funcs {
		if f.Name == s.Binding.Oracle && f.SpecFor == s.Binding.Target &&
			f.Status == contract.ModelComplete && s.OracleDecl == fmt.Sprintf("%s.f%d", report.LeanModule, f.ID) {
			oracleOK = true
		}
	}
	if !oracleOK {
		return nil, fmt.Errorf("builtin: snapshot oracle identity is inconsistent")
	}
	return &s, nil
}

func (s *Snapshot) Property(ref *contract.BuiltinRef) *contract.Property {
	copyRef := *ref
	return &contract.Property{
		ID: "p_builtin_compare_order_" + s.Binding.SHA256, Target: s.Target,
		Kind: contract.KindPostcondition, Frontend: "gossa", Source: "builtin", Builtin: &copyRef,
		NL:   fmt.Sprintf("explicitly selected %s.%s obeys compare-order v1 over all byte strings (assuming purity, determinism and totality)", s.Binding.Package, s.Binding.Function),
		Spec: &contract.SpecRef{GoFunc: s.Binding.Oracle, LeanDecl: s.OracleDecl},
	}
}

func ValidateProperty(w contract.Workspace, p *contract.Property) (*Snapshot, error) {
	if p == nil {
		return nil, fmt.Errorf("builtin: missing property")
	}
	active, err := ActiveRef(w)
	if err != nil || active == nil || !reflect.DeepEqual(active, p.Builtin) {
		return nil, fmt.Errorf("builtin: property does not reference the active compiled snapshot")
	}
	s, err := LoadSnapshot(w, p.Builtin)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(p, s.Property(p.Builtin)) {
		return nil, fmt.Errorf("builtin: property differs from its compiled canonical binding")
	}
	return s, nil
}

// RestoreOverlay never uses the old absolute source path as a destination.
// An identical target clone can move; its source/test graph must still match.
func (s *Snapshot) RestoreOverlay(root string) (map[string][]byte, error) {
	current, err := CaptureSources(root, s.Sources.PackageDir)
	if err != nil {
		return nil, err
	}
	if !s.Sources.Equal(current) {
		return nil, fmt.Errorf("builtin: Go source/test dependency graph or build configuration changed; recompile in a fresh workspace")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, filepath.FromSlash(s.Sources.PackageDir))
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, CompareOrderFile)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, fmt.Errorf("builtin: generated helper destination already exists or cannot be inspected: %s", path)
	}
	return map[string][]byte{path: []byte(s.Binding.Source)}, nil
}

// Snapshot validation is mandatory even for properties loaded through the
// ordinary workspace APIs. Refuse stripped provenance for generated targets.
func IsGeneratedProperty(p *contract.Property, target *contract.Target) bool {
	return p.Source == "builtin" || p.Builtin != nil || target.Func == CompareOrderTarget ||
		(p.Spec != nil && strings.HasPrefix(p.Spec.GoFunc, "go2leanSpecCompareOrder"))
}

func ReplayRef(p *contract.Property, cx *contract.Counterexample) (*contract.BuiltinReplayRef, error) {
	if p == nil || cx == nil || validateRef(p.Builtin) != nil {
		return nil, fmt.Errorf("builtin: missing replay provenance")
	}
	sum, err := canonicalDigest(cx)
	if err != nil {
		return nil, err
	}
	return &contract.BuiltinReplayRef{SnapshotSHA256: p.Builtin.SnapshotSHA256, CounterexampleSHA256: sum}, nil
}

func ValidateCachedVerdict(w contract.Workspace, root string, cx *contract.Counterexample, p *contract.Property, v *contract.Verdict) error {
	s, err := ValidateProperty(w, p)
	if err != nil {
		return err
	}
	if _, err := s.RestoreOverlay(root); err != nil {
		return err
	}
	expected, err := ReplayRef(p, cx)
	if err != nil || !reflect.DeepEqual(expected, v.Builtin) {
		return fmt.Errorf("builtin: cached verdict belongs to different model/source inputs")
	}
	return nil
}
