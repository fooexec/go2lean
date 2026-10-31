package contract

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Workspace is a directory tree holding one JSON file per record:
//
//	<root>/targets/<id>.json
//	<root>/properties/<id>.json
//	<root>/counterexamples/<id>.json
//	<root>/verdicts/<id>.json        (keyed by counterexample id)
//	<root>/proofs/<id>.json          (keyed by property id)
//	<root>/gaps/<verdict id>-<n>.json
//	<root>/poc/<counterexample id>/  (M3 deliverables)
//
// ABI records live wherever Target.Models points, relative to the root.
type Workspace struct {
	Root string
}

// Init creates the workspace root and marks it as a separate Go module, so
// that the PoC tests it holds are not picked up by `go vet ./...` or
// `go test ./...` in the enclosing repository.
func (w Workspace) Init() error {
	if err := os.MkdirAll(w.Root, 0o755); err != nil {
		return err
	}
	gomod := filepath.Join(w.Root, "go.mod")
	if _, err := os.Stat(gomod); err == nil {
		return nil
	}
	return os.WriteFile(gomod, []byte("// go2lean workspace: keeps PoC tests out of the enclosing module.\nmodule go2lean.invalid/workspace\n"), 0o644)
}

func (w Workspace) path(kind, id string) string {
	return filepath.Join(w.Root, kind, id+".json")
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func (w Workspace) Target(id string) (*Target, error) {
	var t Target
	if err := readJSON(w.path("targets", id), &t); err != nil {
		return nil, err
	}
	return &t, t.Validate()
}

func (w Workspace) Property(id string) (*Property, error) {
	var p Property
	if err := readJSON(w.path("properties", id), &p); err != nil {
		return nil, err
	}
	return &p, p.Validate()
}

func (w Workspace) Counterexample(id string) (*Counterexample, error) {
	var c Counterexample
	if err := readJSON(w.path("counterexamples", id), &c); err != nil {
		return nil, err
	}
	return &c, c.Validate()
}

// ABI loads the ABI record a Target names for a frontend.
func (w Workspace) ABI(t *Target, frontend string) (*ABI, error) {
	rel, ok := t.Models[frontend]
	if !ok {
		return nil, fmt.Errorf("target %s has no model for frontend %q", t.ID, frontend)
	}
	var a ABI
	if err := readJSON(filepath.Join(w.Root, rel), &a); err != nil {
		return nil, err
	}
	return &a, a.Validate()
}

// HuntRecord says that a property was searched and what came of it, so a
// resumed run can skip it.
type HuntRecord struct {
	Property              string `json:"property"`
	Summary               string `json:"summary"`
	Counterexample        string `json:"counterexample,omitempty"`
	BuiltinSnapshotSHA256 string `json:"builtin_snapshot_sha256,omitempty"`
}

func (w Workspace) WriteHuntRecord(r *HuntRecord) error {
	return writeJSON(w.path("hunts", r.Property), r)
}

// HuntRecord returns the record of a property's search, or nil.
func (w Workspace) HuntRecord(propID string) *HuntRecord {
	var r HuntRecord
	if readJSON(w.path("hunts", propID), &r) != nil {
		return nil
	}
	return &r
}

// MutationResult is what the mutation engine found for a property that
// held: of the mutants of the code under test it tried (a branch negated,
// a comparison or + / - swapped), which ones some input told apart from
// the original (the property failed on the mutant only).
type MutationResult struct {
	Property string       `json:"property"`
	Total    int          `json:"mutants_total"`
	Tried    int          `json:"tried"`
	Killed   int          `json:"killed"`
	KilledBy []MutantKill `json:"killed_by,omitempty"`
	Survived []string     `json:"survived,omitempty"`
}

// MutantKill names a mutant and the trial that told it apart.
type MutantKill struct {
	Mutant string `json:"mutant"`
	Trial  int    `json:"trial"`
}

func (w Workspace) WriteMutation(r *MutationResult) error {
	return writeJSON(w.path("mutations", r.Property), r)
}

// Mutation returns the mutation result of a property, or nil.
func (w Workspace) Mutation(propID string) *MutationResult {
	var r MutationResult
	if readJSON(w.path("mutations", propID), &r) != nil {
		return nil
	}
	return &r
}

// Verdict returns the verdict of a counterexample, or nil.
func (w Workspace) Verdict(cxID string) *Verdict {
	var v Verdict
	if readJSON(w.path("verdicts", cxID), &v) != nil {
		return nil
	}
	return &v
}

func (w Workspace) WriteVerdict(v *Verdict) error {
	return writeJSON(w.path("verdicts", v.Counterexample), v)
}

func (w Workspace) WriteProperty(p *Property) error {
	return writeJSON(w.path("properties", p.ID), p)
}

func (w Workspace) WriteCounterexample(c *Counterexample) error {
	return writeJSON(w.path("counterexamples", c.ID), c)
}

func (w Workspace) WriteProofResult(r *ProofResult) error {
	return writeJSON(w.path("proofs", r.Property), r)
}

// PocDir is where M3 puts the deliverables for one counterexample.
// RemoveCounterexample deletes counterexample id and its verdict: the
// refinement loop drops a counterexample its refined model no longer has
// (#85).
func (w Workspace) RemoveCounterexample(id string) error {
	for _, kind := range []string{"counterexamples", "verdicts"} {
		if err := os.Remove(w.path(kind, id)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (w Workspace) PocDir(cxID string) string {
	return filepath.Join(w.Root, "poc", cxID)
}

func (w Workspace) WriteTarget(t *Target) error {
	return writeJSON(w.path("targets", t.ID), t)
}

// PackageKey is the workspace directory name for a package: the full import
// path with anything that is not an identifier character replaced, so that
// packages sharing a base name (util, types, ...) never collide.
func PackageKey(pkgPath string) string {
	return nonIdent.ReplaceAllString(pkgPath, "_")
}

var nonIdent = regexp.MustCompile(`[^A-Za-z0-9_]`)

// WriteABI stores an ABI record under models/<frontend>/<pkg>/<func>.json
// and returns the workspace-relative path for Target.Models.
func (w Workspace) WriteABI(a *ABI, pkgPath, funcName string) (string, error) {
	// Instances of generic functions carry their type arguments, import
	// paths included, in the name: keep them out of the directory layout.
	file := ShortID(strings.NewReplacer("/", "_", "*", "_").Replace(funcName))
	rel := filepath.Join("models", a.Frontend, PackageKey(pkgPath), file+".json")
	return rel, writeJSON(filepath.Join(w.Root, rel), a)
}

func (w Workspace) WriteModelReport(r *ModelReport) error {
	return writeJSON(filepath.Join(w.Root, "models", r.Frontend, PackageKey(r.Package), "report.json"), r)
}

func (w Workspace) ModelReport(frontend, pkgPath string) (*ModelReport, error) {
	var r ModelReport
	err := readJSON(filepath.Join(w.Root, "models", frontend, PackageKey(pkgPath), "report.json"), &r)
	return &r, err
}

// ModelReportByKey reads a package's report by its package key (the
// directory name under models/<frontend>/).
func (w Workspace) ModelReportByKey(frontend, key string) (*ModelReport, error) {
	var r ModelReport
	err := readJSON(filepath.Join(w.Root, "models", frontend, key, "report.json"), &r)
	return &r, err
}

// FuncKeys maps each function name of a package to the segment ids use
// for it: the name itself (ids are lower-cased), and for names that only
// differ from another in case (Parse and parse) the name followed by the
// positions of its capitals, so that each keeps an id of its own.
func FuncKeys(names []string) map[string]string {
	byLower := map[string]int{}
	for _, n := range names {
		byLower[strings.ToLower(n)]++
	}
	keys := make(map[string]string, len(names))
	for _, n := range names {
		keys[n] = n
		if byLower[strings.ToLower(n)] > 1 {
			var caps []string
			for i, r := range n {
				if unicode.IsUpper(r) {
					caps = append(caps, strconv.Itoa(i))
				}
			}
			keys[n] = n + "_c" + strings.Join(caps, "c")
		}
	}
	return keys
}

// TargetID is the workspace id of a function: package key and function
// key (FuncKeys; a method is "Type.Method").
func TargetID(pkgPath, funcName string) string {
	return ShortID("t_" + strings.ToLower(nonIdent.ReplaceAllString(PackageKey(pkgPath)+"_"+funcName, "_")))
}

// ShortID keeps an id usable as a file name, with room for the prefixes
// and suffixes counterexample ids add: one longer than 220 bytes (generic
// instances spell their type arguments, import paths included) is cut to
// 180 and suffixed with a hash of the whole.
func ShortID(id string) string {
	if len(id) <= 220 {
		return id
	}
	h := fnv.New64a()
	h.Write([]byte(id))
	return fmt.Sprintf("%s_%016x", id[:180], h.Sum64())
}

// Properties lists every property in the workspace.
func (w Workspace) Properties() ([]*Property, error) {
	matches, _ := filepath.Glob(filepath.Join(w.Root, "properties", "*.json"))
	var out []*Property
	for _, m := range matches {
		var p Property
		if err := readJSON(m, &p); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, nil
}
