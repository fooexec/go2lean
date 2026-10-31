// Package contract defines the JSON records that the three go2lean modules
// exchange. M1 (programmatic) emits ABI records, M2 (agentic) emits
// Property / Counterexample / ProofResult / ModelGap, M3 (poc) emits Verdict.
//
// All integer values are carried as decimal strings so that 64-bit values
// survive JSON round trips. Rendering follows the Go type named in the ABI:
// signed decimal for signed integers, unsigned decimal for unsigned ones,
// "true"/"false" for bool.
package contract

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Target identifies one Go function and the model(s) M1 built for it.
type Target struct {
	ID      string `json:"id"`
	Module  string `json:"module"`  // Go module path
	Package string `json:"package"` // full import path of the package
	Func    string `json:"func"`
	GoSig   string `json:"go_sig"`
	// Models maps a frontend name ("gossa", "llvm") to the ABI record path,
	// relative to the workspace root.
	Models map[string]string `json:"models"`
}

// PropertyKind enumerates the property shapes M2 may propose.
type PropertyKind string

const (
	KindNoPanic       PropertyKind = "no_panic"
	KindPostcondition PropertyKind = "postcondition"
	KindEquivalence   PropertyKind = "equivalence"
	KindCrosscheck    PropertyKind = "crosscheck"
	KindCustom        PropertyKind = "custom"
	// KindSequence: a `go2lean:sequence` spec returns true without
	// panicking for every input sequence (bounded by the engines).
	KindSequence PropertyKind = "sequence"
	// KindNoHang: the target returns on every input; a run that comes back
	// to a state it was in never does (#86).
	KindNoHang PropertyKind = "no_hang"
)

// GoLean carries the two forms of a predicate: the Go expression M3 can
// evaluate on real values and the Lean term M2 uses on the model.
type GoLean struct {
	Go   string `json:"go,omitempty"`
	Lean string `json:"lean,omitempty"`
}

// SpecRef names a spec-in-Go function: a Go function in the target's package
// with signature func(<target params>..., ret <target result>) bool.
type SpecRef struct {
	GoFunc   string `json:"go_func"`
	LeanDecl string `json:"lean_decl,omitempty"`
}

// BuiltinRef binds a generated property to a content-addressed compiled check
// snapshot. It is provenance/integrity metadata, not a proof certificate.
type BuiltinRef struct {
	ID             string `json:"id"`
	Version        string `json:"version"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
}

type BuiltinReplayRef struct {
	SnapshotSHA256       string `json:"snapshot_sha256"`
	CounterexampleSHA256 string `json:"counterexample_sha256"`
}

// Property is one claim about a Target.
type Property struct {
	ID           string       `json:"id"`
	Target       string       `json:"target"`
	Kind         PropertyKind `json:"kind"`
	NL           string       `json:"nl"`
	Precondition *GoLean      `json:"precondition,omitempty"`
	Spec         *SpecRef     `json:"spec,omitempty"`
	// MaxLen overrides the search's bound on generated strings and slices
	// (a spec's `go2lean:maxlen N`, for properties about long inputs).
	MaxLen int `json:"max_len,omitempty"`
	// ExpectedPanic restricts a no_panic property to one panic category
	// (see PanicKind). Empty means any panic counts.
	ExpectedPanic PanicKind   `json:"expected_panic,omitempty"`
	Frontend      string      `json:"frontend"`
	Source        string      `json:"source"` // "agent" | "human" | "template"
	CreatedBy     string      `json:"created_by,omitempty"`
	Builtin       *BuiltinRef `json:"builtin,omitempty"`
}

// PanicKind is the category of a Go runtime panic as both the Lean model and
// M3 classify it.
type PanicKind string

const (
	PanicIndexOutOfRange PanicKind = "index_out_of_range"
	PanicDivideByZero    PanicKind = "divide_by_zero"
	PanicNilDeref        PanicKind = "nil_dereference"
	PanicNegativeShift   PanicKind = "negative_shift"
	PanicSliceBounds     PanicKind = "slice_bounds"
	PanicMakeSliceLen    PanicKind = "make_slice_len"
	PanicTypeAssertion   PanicKind = "type_assertion"
	PanicNilMapWrite     PanicKind = "nil_map_write"
	PanicExplicit        PanicKind = "explicit"
	// PanicUndefined is undefined behaviour of a C/LLVM program that is not
	// one of the kinds above (signed overflow, a poison branch condition).
	PanicUndefined PanicKind = "undefined_behavior"
	// The concurrent machine's outcomes (docs/concurrency.md): a send on a
	// closed channel or a close of a closed or nil one; a fatal error of
	// package sync (unlock of an unlocked mutex); the function under test
	// blocked with no goroutine able to move (Go's "all goroutines are
	// asleep"); a goroutine it started blocked forever after it returned.
	PanicClosedChannel PanicKind = "closed_channel"
	PanicSyncFatal     PanicKind = "sync_fatal"
	PanicDeadlock      PanicKind = "deadlock"
	PanicGoroutineLeak PanicKind = "goroutine_leak"
	PanicDataRace      PanicKind = "data_race"
	PanicOther         PanicKind = "other"
)

// HavocRead names one read of a havoc site: its index and iteration.
type HavocRead struct {
	Idx  int `json:"idx"`
	Iter int `json:"iter"`
}

// HavocValue is the value the counterexample assigns to one havoc site on
// one iteration. GoHint, copied from the ABI, tells M3 how to realise it in
// Go (e.g. "xs[i]" means element Iter of parameter xs).
type HavocValue struct {
	Idx   int    `json:"idx"`
	Iter  int    `json:"iter"`
	Value string `json:"value"`
	// Origin is "<package key>#<index>": the site in its own package's
	// model report (Idx numbers the sites of the linked program).
	Origin string `json:"origin,omitempty"`
	GoHint string `json:"go_hint,omitempty"`
}

// ModelOutput is what the Lean model returned on the counterexample.
type ModelOutput struct {
	Res   string    `json:"res"` // "ok" | "panic" | "out_of_fuel" | "hang"
	Value FlexValue `json:"value,omitempty"`
	Panic PanicKind `json:"panic,omitempty"`
}

// FlexValue is a rendered model value: a single value as a string, or
// several results (a tuple) as a JSON array, read as "[v0, v1, ...]". Any
// other JSON (an object the driver printed for a boxed value) is kept as
// its compact text, so a value it cannot read does not lose the search.
type FlexValue string

func (v *FlexValue) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*v = FlexValue(s)
		return nil
	}
	var parts []FlexValue
	if err := json.Unmarshal(b, &parts); err != nil {
		var buf bytes.Buffer
		if cerr := json.Compact(&buf, b); cerr != nil {
			return err
		}
		*v = FlexValue(buf.String())
		return nil
	}
	strs := make([]string, len(parts))
	for i, p := range parts {
		strs[i] = string(p)
	}
	*v = FlexValue("[" + strings.Join(strs, ", ") + "]")
	return nil
}

// Counterexample is a concrete assignment under which the model violates a
// Property. Inputs are keyed by model parameter name (see ABIParam.Model).
type Counterexample struct {
	ID       string            `json:"id"`
	Property string            `json:"property"`
	Frontend string            `json:"frontend"`
	Engine   string            `json:"engine"` // "plausible" | "bv_decide" | "manual" | ...
	Fuel     int               `json:"fuel,omitempty"`
	Inputs   map[string]string `json:"inputs"`
	// Memory holds the slice contents the model read, keyed by Go parameter
	// name, so M3 can materialise the exact slices.
	Memory map[string][]string `json:"memory,omitempty"`
	// Objects holds struct and pointer arguments as nested JSON (a struct is
	// an array of its fields in order, a pointer is null or its pointee).
	Objects map[string]json.RawMessage `json:"objects,omitempty"`
	Havoc   []HavocValue               `json:"havoc,omitempty"`
	// RelevantHavoc are the havoc reads the violation depends on: changing
	// one of them (and only it) makes the model's violation go away (#84).
	// HavocPerturbed counts the reads that were tried (strings and
	// assumptions are fixed guesses, not perturbed). A violation whose
	// perturbed reads are all irrelevant does not depend on unknown code.
	// RelevanceSkipped: the run was too slow to rerun once per read (#87).
	RelevantHavoc     []HavocRead  `json:"relevant_havoc,omitempty"`
	HavocPerturbed    int          `json:"havoc_perturbed,omitempty"`
	RelevanceSkipped  bool         `json:"relevance_skipped,omitempty"`
	ModelOutput       *ModelOutput `json:"model_output,omitempty"`
	RevalidatedInLean bool         `json:"revalidated_in_lean"`
	// Probes are the values the spec's go2leanProbe calls recorded in the
	// model run, in order; M3 records the real run's in Verdict.Probes.
	Probes []Probe `json:"probes,omitempty"`
	// PanicAt names the function, block and op where the model run
	// panicked, when it did.
	PanicAt string `json:"panic_at,omitempty"`
	// Assumptions are the under-approximations the model run took.
	Assumptions []string `json:"assumptions,omitempty"`
	// Schedule is the concurrent machine's run, step by step, for M3's
	// deterministic replay (docs/concurrency.md).
	Schedule []SchedStep `json:"schedule,omitempty"`
}

// SchedStep is one step of a concurrent run: goroutine G (0 is the call
// itself, the others in the order they started) moved; Op is the operation
// it fired ("" when it only ran code), Partners the goroutines whose
// operation it completed (the other side of a rendezvous), Pkg the Go
// package of the code it ran (the replay gates only its target's, #56).
type SchedStep struct {
	G        int    `json:"g"`
	Op       string `json:"op"`
	Partners []int  `json:"partners,omitempty"`
	Pkg      string `json:"pkg,omitempty"`
}

// Probe is one go2leanProbe value: its label and the value (JSON as the
// model renders it, or the string the real run printed).
type Probe struct {
	Label string          `json:"label"`
	Value json.RawMessage `json:"value"`
}

// VerdictStatus is M3's judgement on a Counterexample.
type VerdictStatus string

const (
	VerdictConfirmed    VerdictStatus = "confirmed"
	VerdictSpurious     VerdictStatus = "spurious"
	VerdictInconclusive VerdictStatus = "inconclusive"
)

// Evidence records how M3 ran the PoC.
type Evidence struct {
	Cmd        string `json:"cmd"`
	Exit       int    `json:"exit"`
	StdoutTail string `json:"stdout_tail,omitempty"`
	StderrTail string `json:"stderr_tail,omitempty"`
	PanicMsg   string `json:"panic_msg,omitempty"`
	// PanicSite is the file:line in the target's package where the real
	// run panicked (the innermost frame there).
	PanicSite string `json:"panic_site,omitempty"`
}

// Diagnosis explains a spurious or inconclusive verdict so M1/M2 can act.
type Diagnosis struct {
	Reason       string   `json:"reason"`
	HavocUsed    []int    `json:"havoc_used,omitempty"`
	ModelVsReal  string   `json:"model_vs_real,omitempty"`
	IsCustomProp bool     `json:"is_custom_property,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

// Verdict is M3's output for one Counterexample.
type Verdict struct {
	Counterexample string            `json:"counterexample"`
	Status         VerdictStatus     `json:"status"`
	Evidence       Evidence          `json:"evidence"`
	Observed       map[string]string `json:"observed,omitempty"`
	Diagnosis      *Diagnosis        `json:"diagnosis,omitempty"`
	PocDir         string            `json:"poc_dir,omitempty"`
	// Probes are the go2leanProbe values the real run printed.
	Probes []Probe `json:"probes,omitempty"`
	// Rank orders a confirmed verdict by how plausible its input is for the
	// code's real callers.
	Rank    *Rank             `json:"rank,omitempty"`
	Builtin *BuiltinReplayRef `json:"builtin,omitempty"`
}

// Rank of a confirmed finding: tier 1 (any caller of the API can pass the
// input, or a call site passes the same nil) to 4 (an input the test
// forged). See internal/poc/rank.go.
type Rank struct {
	Tier   int    `json:"tier"`
	Reason string `json:"reason"`
	// Sites are call sites that pass the same nil (file:line).
	Sites []string `json:"sites,omitempty"`
}

// ProofStatus is the outcome of a proof attempt.
type ProofStatus string

const (
	ProofProved        ProofStatus = "proved"
	ProofBoundedProved ProofStatus = "bounded_proved"
	ProofFailed        ProofStatus = "failed"
	ProofUnknown       ProofStatus = "unknown"
	// ProofExhausted: every input in a small, explicitly enumerated domain
	// satisfies the property. Weaker than bounded_proved; Domain says what
	// was covered.
	ProofExhausted ProofStatus = "exhausted"
	// ProofExhaustedHavoc: as exhausted, but some inputs ran through values
	// the model does not know (havoc), of which only a few were tried: the
	// property holds for those, not for every value they stand for.
	ProofExhaustedHavoc ProofStatus = "exhausted_havoc"
	// ProofExhaustedAssuming: as exhausted, but some inputs ran through an
	// under-approximation (Assumptions): the property holds for the model,
	// which leaves out behaviours Go has there.
	ProofExhaustedAssuming ProofStatus = "exhausted_assuming"
)

// ProofResult records a proof attempt on a Property.
type ProofResult struct {
	Property string      `json:"property"`
	Status   ProofStatus `json:"status"`
	Fuel     int         `json:"fuel,omitempty"`
	Tactic   string      `json:"tactic,omitempty"`
	Domain   string      `json:"domain,omitempty"`
	// Assumptions are the under-approximations the runs took.
	Assumptions []string `json:"assumptions,omitempty"`
	LeanFile    string   `json:"lean_file,omitempty"`
	TimeMs      int64    `json:"time_ms,omitempty"`
	// Trust is what a proof's check relies on: "kernel" (the kernel and
	// Lean's standard axioms) or "compiler" (also Lean.ofReduceBool, which
	// bv_decide's certificate check uses).
	Trust string `json:"trust,omitempty"`
	// Certificate is the proof judge's record, for a proof it accepted.
	Certificate *ProofCertificate `json:"certificate,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// ProofCertificate is the proof judge's record of one accepted theorem:
// what was proved, against which sources, and how to check it again.
type ProofCertificate struct {
	Theorem   string `json:"theorem"` // full name
	Func      string `json:"func"`
	Toolchain string `json:"toolchain"`
	// CoreSHA256 hashes the core library's sources the judge built from
	// (repo.CoreFiles, in order, with their names and lengths).
	CoreSHA256 string `json:"core_sha256"`
	// ModelSHA256 is the model file's hash, as the compile stage wrote it.
	ModelSHA256 string `json:"model_sha256"`
	// StatementSHA256 hashes the theorem's statement as go2lean wrote it in
	// the challenge; ChallengeSHA256 and SolutionSHA256 the files judged.
	StatementSHA256 string   `json:"statement_sha256"`
	ChallengeSHA256 string   `json:"challenge_sha256"`
	SolutionSHA256  string   `json:"solution_sha256"`
	Axioms          []string `json:"permitted_axioms"`
	Judge           string   `json:"judge"`
	// Sandbox is "bwrap", or "off" when GO2LEAN_LEAN_SANDBOX=off.
	Sandbox string `json:"sandbox"`
	Dir     string `json:"dir"` // the judge's project: run Judge there again
}

// ModelGap is feedback from M3/M2 to M1: a place where the model's
// over-approximation produced a spurious counterexample.
type ModelGap struct {
	FromVerdict string `json:"from_verdict"`
	Frontend    string `json:"frontend"`
	Kind        string `json:"kind"` // "unmodeled-call" | "havoc-load" | "semantic-mismatch" | ...
	Site        string `json:"site"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// ABI maps the Go signature of a function onto the parameters of its model.
// Results lists every result in order; a spec-in-Go function takes them
// after the parameters, named ret for one result or ret0, ret1, ... for
// several.
type ABI struct {
	GoFunc   string `json:"go_func"`
	Package  string `json:"package,omitempty"`  // import path of the target's package (decides what the test may import)
	Variadic bool   `json:"variadic,omitempty"` // the last parameter is `...T` (its Go type is the slice)
	// Imports maps every import path a Go type mentions to the package's
	// name, which the harness needs when the two differ
	// (github.com/anthropics/anthropic-sdk-go is package anthropic).
	Imports  map[string]string `json:"imports,omitempty"`
	GoSig    string            `json:"go_sig"`
	Frontend string            `json:"frontend"`
	// Method is set for methods: the receiver is Params[0] and a call is
	// written recv.Method(rest...).
	Method string `json:"method,omitempty"`
	// Ctor is set for a method called on a receiver its package's
	// constructor built: the first N parameters go to the constructor.
	Ctor *ABICtor `json:"ctor,omitempty"`
	// Types is the table of recursive struct types referenced by TypeRef,
	// in a fixed order (the generators index it by position).
	Types   []ABIType   `json:"types,omitempty"`
	Params  []ABIParam  `json:"params"`
	Results []ABIResult `json:"results,omitempty"`
	Havoc   []ABIHavoc  `json:"havoc,omitempty"`
}

// ABICtor describes a constructor-built receiver: recv := Func(params[:N]...)
// (recv, err := ... when Err; a non-nil err ends the run as a false
// precondition), then recv.Method(params[N:]...).
type ABICtor struct {
	Func   string `json:"func"`
	N      int    `json:"n"`
	Err    bool   `json:"err,omitempty"`
	Method string `json:"method"`
}

// ABIParamKind classifies how a Go parameter is split into model values.
type ABIParamKind string

const (
	ABIInt    ABIParamKind = "int"    // one model value
	ABIBool   ABIParamKind = "bool"   // one model value
	ABIError  ABIParamKind = "error"  // one model value: nil or an opaque non-nil error
	ABISlice  ABIParamKind = "slice"  // model: ref, off, len, cap (gossa) or ptr, len, cap (llvm)
	ABIStruct ABIParamKind = "struct" // one model value; Fields describe the components
	ABIPtr    ABIParamKind = "ptr"    // one model value: nil or a pointer to Pointee
	ABIString ABIParamKind = "string" // one model value; reported under Objects as a JSON string
	ABIOpaque ABIParamKind = "opaque" // interface, map, func, chan, float or unsafe.Pointer: the model never looks inside; M3 passes the zero value
	ABIArray  ABIParamKind = "array"  // one model value; Len elements of Elem/ElemParam, reported under Objects as a JSON array
	ABIMap    ABIParamKind = "map"    // one model value: nil or a map of Key to Value, reported under Objects as {"map": [[k, v], ...]}
	ABIAny    ABIParamKind = "any"    // one model value: an empty interface holding nil or a value of one of Alts, reported as {"tag": t, "v": ...}
)

// ABIParam describes one Go parameter. GoType is spelled as Go source in
// the target package would spell it (`time.Duration`, `Level`); TypePkg is
// the import path a test file needs for it, empty for builtins and types of
// the target package. Width/Signed come from the underlying type, so named
// integer types are handled without parsing names.
type ABIParam struct {
	Go      string       `json:"go"`
	GoType  string       `json:"go_type"`
	TypePkg string       `json:"type_pkg,omitempty"`
	Width   int          `json:"width,omitempty"`
	Signed  bool         `json:"signed,omitempty"`
	Model   []string     `json:"model"` // model parameter names, in order
	Kind    ABIParamKind `json:"kind"`
	// Slice element type, spelled like GoType, with its own package and width.
	Elem       string `json:"elem,omitempty"`
	ElemPkg    string `json:"elem_pkg,omitempty"`
	ElemWidth  int    `json:"elem_width,omitempty"`
	ElemSigned bool   `json:"elem_signed,omitempty"`
	// ElemParam describes a non-integer element type (struct, string,
	// pointer, nested slice); the contents are then reported under Objects
	// as a JSON array instead of under Memory.
	ElemParam *ABIParam `json:"elem_param,omitempty"`
	// Len is the length of an array.
	Len int `json:"len,omitempty"`
	// Zero is the Go literal of an opaque value's zero ("0" for a float,
	// "nil" otherwise), used for elements of slices and arrays.
	Zero string `json:"zero,omitempty"`
	// ZeroModel is the model's zero value of an unexported field of a
	// foreign struct, which a test cannot set: the generator fixes the
	// field to it (shape decoded by the Lean driver's valOfJson).
	ZeroModel any `json:"zero_model,omitempty"`
	// MaxLen, when negative, pins a string parameter to the empty string
	// (strings.Builder: the test can only pass an empty one).
	MaxLen int `json:"max_len,omitempty"`
	// Enum lists the constants declared for a named string type in its
	// package (anthropic.MessageParamRoleUser, ...): the generator draws
	// mostly from them.
	Enum []string `json:"enum,omitempty"`
	// ExtraPkgs are the other import paths the Go type's spelling needs
	// (a func or map type naming types of several packages).
	ExtraPkgs []string `json:"extra_pkgs,omitempty"`
	// Struct fields (Go holds the field name) and the pointee of a pointer.
	Fields  []ABIParam `json:"fields,omitempty"`
	Pointee *ABIParam  `json:"pointee,omitempty"`
	// TypeRef names an entry of ABI.Types for a struct that refers to itself
	// (directly or through pointers); Fields is then empty.
	TypeRef string `json:"type_ref,omitempty"`
	// Key and Value describe the key and element types of a map.
	Key   *ABIParam `json:"key,omitempty"`
	Value *ABIParam `json:"value,omitempty"`
	// Alts are the dynamic types an empty interface is generated with, and
	// Tag the model's type tag of an Alts entry (a hash of the type's name).
	Alts []ABIParam `json:"alts,omitempty"`
	Tag  uint32     `json:"tag,omitempty"`
	// JSONText marks the raw JSON an SDK response type keeps of itself
	// (the unexported JSON.raw behind RawJSON()): generated as a JSON
	// object of extra fields, which the harness merges into the typed
	// value by a marshal / unmarshal round trip.
	JSONText bool `json:"json_text,omitempty"`
	// JSONName is a struct field's key in JSON (its json tag, else its Go
	// name; "-" when encoding skips it), used to build SDK response values
	// from a JSON document.
	JSONName string `json:"json_name,omitempty"`
	// JSONInline marks a field tagged `json:",inline"` (an SDK union's
	// variants): its encoding stands in for the struct's own.
	JSONInline bool `json:"json_inline,omitempty"`
}

// ABIType is a named struct type in the ABI's type table.
type ABIType struct {
	GoType  string     `json:"go_type"`
	TypePkg string     `json:"type_pkg,omitempty"`
	Fields  []ABIParam `json:"fields"`
	// JSONDesc is 1 + the type's entry in the package's JSON table for an
	// SDK response type: the search generates its values as decoded
	// documents, so typed fields and raw JSON agree.
	JSONDesc int `json:"json_desc,omitempty"`
}

// ABIResult describes the Go result.
type ABIResult struct {
	GoType  string `json:"go_type"`
	TypePkg string `json:"type_pkg,omitempty"`
	Func    bool   `json:"func,omitempty"` // a func value: the harness prints its nil-ness
	// ExtraPkgs are the other import paths the spelling needs.
	ExtraPkgs []string `json:"extra_pkgs,omitempty"`
}

// IntInfo returns the width and signedness of an integer parameter,
// falling back to the type name for records written before Width existed.
func (p ABIParam) IntInfo() (width int, signed bool, ok bool) {
	if p.Width > 0 {
		return p.Width, p.Signed, true
	}
	return GoIntType(p.GoType)
}

// ElemAsParam describes the element of a slice or array as a parameter of
// its own: ElemParam when set, else the integer element.
func (p ABIParam) ElemAsParam() ABIParam {
	if p.ElemParam != nil {
		return *p.ElemParam
	}
	return ABIParam{Go: p.Go, GoType: p.Elem, TypePkg: p.ElemPkg, Width: p.ElemWidth, Signed: p.ElemSigned, Model: []string{p.Go}, Kind: ABIInt}
}

// ElemInfo is IntInfo for a slice's element type.
func (p ABIParam) ElemInfo() (width int, signed bool, ok bool) {
	if p.ElemWidth > 0 {
		return p.ElemWidth, p.ElemSigned, true
	}
	return GoIntType(p.Elem)
}

// ABIHavoc describes one havoc site in the model.
type ABIHavoc struct {
	Idx    int    `json:"idx"`
	Site   string `json:"site"`
	Reason string `json:"reason"`
	GoHint string `json:"go_hint,omitempty"`
}

// ModelStatus says how much of a function M1 could model.
type ModelStatus string

const (
	ModelComplete ModelStatus = "complete" // every instruction modelled
	ModelPartial  ModelStatus = "partial"  // some values are havoc
	ModelRejected ModelStatus = "rejected" // not emitted
)

// ModelFunc is one function's entry in a ModelReport.
type ModelFunc struct {
	Name   string      `json:"name"`
	GoSig  string      `json:"go_sig"`
	ID     int         `json:"id"` // index into the Lean Prog.funcs array
	Status ModelStatus `json:"status"`
	Issues []string    `json:"issues,omitempty"`
	// SpecFor names the function this one is a `go2lean:spec` for.
	SpecFor string `json:"spec_for,omitempty"`
	// Closure marks an anonymous function: modelled so calls through func
	// values can follow it, never a target (its free variables come first).
	Closure bool `json:"closure,omitempty"`
	// WrappedBy names the constructor wrapper that targets this method in
	// its place (T.M~NewT).
	WrappedBy string `json:"wrapped_by,omitempty"`
	// CtorOnly names the constructor of this method's unexported type when
	// no wrapper could be built for it: only that constructor makes such a
	// receiver, so one built field by field is a misuse and the method is
	// not a target.
	CtorOnly string `json:"ctor_only,omitempty"`
	// TestFile marks a function declared in a _test.go file: modelled for
	// the specs and tests that call it, never a target itself.
	TestFile bool `json:"test_file,omitempty"`
	// Init: the slice of the package initializer the search runs before
	// the trials (#55); not a target.
	Init bool `json:"init,omitempty"`
	// SpecMaxLen is a spec's `go2lean:maxlen N` directive.
	SpecMaxLen int `json:"spec_max_len,omitempty"`
	// SequenceFor names the function this `go2lean:sequence` spec drives
	// over its input event sequence (the spec itself is the target).
	SequenceFor string `json:"sequence_for,omitempty"`
	// LoopHeaders are the blocks of the model's function that loops come
	// back to: a proof needs an invariant at each (go2lean prove).
	LoopHeaders []int `json:"loop_headers,omitempty"`
	// Succs are each block's successors, for a function with loops: the
	// proof agent is told the paths from each loop header.
	Succs [][]int `json:"succs,omitempty"`
	// Consts are the integer literals in the body (two's-complement bit
	// patterns as decimal strings); the engines seed their value pools with them.
	Consts []string `json:"consts,omitempty"`
	// StrConsts are the string literals in the body, for the same purpose.
	StrConsts []string `json:"str_consts,omitempty"`
	// MapKeys are the string constants the body uses as map keys
	// (m["role"]): the generator draws input map keys mostly from them.
	MapKeys []string `json:"map_keys,omitempty"`
	// UsesJSON marks a body that decodes JSON: the engines then add JSON
	// texts built from the literals to the string pool.
	UsesJSON bool `json:"uses_json,omitempty"`
	// CmpStrs are the string constants the body compares with == or !=
	// (k == "reasoning_content"): the keys generated JSON texts use.
	CmpStrs []string `json:"cmp_strs,omitempty"`
	// Variants are the union variant fields (OfTool, OfFile) the body
	// reads: the search makes them the live variant more often.
	Variants []string `json:"variants,omitempty"`
	// Calls lists the IDs of the package functions this one calls, so the
	// engines can mine the literals of the whole call tree.
	Calls []int `json:"calls,omitempty"`
	// XCalls lists the functions of other packages this one calls, when
	// those packages were compiled together with this one.
	XCalls []XCall `json:"xcalls,omitempty"`
	// Exits lists the function's return statements (read for specs: which
	// return a trial left through).
	Exits []ExitSite `json:"exits,omitempty"`
	ABI   string     `json:"abi,omitempty"` // path of the ABI record, relative to the workspace
}

// ExitSite is one `return` of a function: the model block it is in, its
// source line, and whether it returns the constant true (in a spec, a
// guard that makes the trial vacuous).
type ExitSite struct {
	Block     int  `json:"block"`
	Line      int  `json:"line"`
	ConstTrue bool `json:"const_true,omitempty"`
	// Last marks the textually last return of the function: a `return
	// true` there ends a check that passed, not a guard.
	Last bool `json:"last,omitempty"`
}

// XCall names a function of another package by its import path and its ID
// in that package's model.
type XCall struct {
	Package string `json:"package"`
	ID      int    `json:"id"`
}

// ModelReport is M1's summary of one package, read by M2 to decide what to
// attack and how.
type ModelReport struct {
	Package    string `json:"package"`
	Frontend   string `json:"frontend"`
	LeanModule string `json:"lean_module"` // e.g. Go2Lean.Generated.gossa.overflow
	LeanFile   string `json:"lean_file"`   // relative to the workspace
	// LeanParts are the part modules a large model is split into, in the
	// order LeanFile imports them (relative to the workspace).
	LeanParts []string `json:"lean_parts,omitempty"`
	// LeanSHA256 is the hash of the model as the compile stage wrote it
	// (its parts in order, then LeanFile): the proof judge copies the
	// files only if they still match.
	LeanSHA256 string      `json:"lean_sha256,omitempty"`
	Funcs      []ModelFunc `json:"funcs"`
	Havoc      []ABIHavoc  `json:"havoc,omitempty"` // program-wide havoc sites
	// Dependency marks a package modelled only for its callers (-deps).
	Dependency bool `json:"dependency,omitempty"`
}

// GoIntType describes a Go integer type name as the model sees it.
// ok is false for non-integer types.
func GoIntType(goType string) (width int, signed bool, ok bool) {
	switch goType {
	case "int", "int64":
		return 64, true, true
	case "uint", "uint64", "uintptr":
		return 64, false, true
	case "int32", "rune":
		return 32, true, true
	case "uint32":
		return 32, false, true
	case "int16":
		return 16, true, true
	case "uint16":
		return 16, false, true
	case "int8":
		return 8, true, true
	case "uint8", "byte":
		return 8, false, true
	}
	return 0, false, false
}
