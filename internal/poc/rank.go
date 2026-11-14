package poc

import (
	"encoding/json"
	"fmt"
	"go/types"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/fooexec/go2lean/internal/contract"
)

// Ranking orders confirmed findings by how plausible their input is for the
// code's real callers. A confirmed verdict shows that the counterexample
// reproduces, not that a caller can build it: a nil argument no caller
// passes, or a value the test forged, is a finding about the test.
//
//	1: inputs any caller of the package's API can pass, or a caller in the
//	   module can pass the same nil (traced through its SSA, nilflow.go)
//	2: plain inputs to an unexported function (depends on its callers), a
//	   caller passes a value whose nil-ness is unknown (a field, a
//	   global), a receiver built field by field where the package has a
//	   constructor for its type (#195), or a hand-written spec violated
//	3: a nil argument no caller in the module can pass, or an agent-written
//	   spec violated (check the spec)
//	4: an input the test forged (an opaque value left nil, package state
//	   the program may initialise first)

// rankOf ranks one confirmed verdict.
func rankOf(p *prepared, callers *callerIndex) *contract.Rank {
	// A hang, like a panic, is ranked by how plausible its input is.
	if p.prop.Kind != contract.KindNoPanic && p.prop.Kind != contract.KindNoHang {
		if p.prop.Source == "agent" {
			return &contract.Rank{Tier: 3, Reason: "agent-written spec violated: check that the spec is right"}
		}
		return &contract.Rank{Tier: 2, Reason: "hand-written spec violated"}
	}
	if d := p.v.Diagnosis; d != nil {
		switch d.Reason {
		case "nil_opaque_input":
			return &contract.Rank{Tier: 4, Reason: "the test left an opaque input nil (interface, map, func or another package's private field)"}
		case "reads_unknown_state":
			return &contract.Rank{Tier: 4, Reason: "reads package state the program may initialise before such calls"}
		}
	}
	if p.test {
		return &contract.Rank{Tier: 1, Reason: "the package's own test fails this way"}
	}
	// An interface input holding a value of an unexported type, built
	// field by field: only the package's own code makes one (grpc's
	// mem.BufferSlice of *buffer with forged reference counts).
	if t := unexportedAlt(p.abi, p.cx); t != "" {
		return &contract.Rank{Tier: 2, Reason: "an interface input holds a " + t + " built field by field, which only the package's own code makes: check that its values can get here"}
	}
	fn := baseFunc(p.target.Func)
	if nils := nilArguments(p.cx); len(nils) > 0 {
		what := "nil for " + strings.Join(nils, ", ")
		yes, maybe, calls, ok := callers.nilSites(p.target.Package, fn, nils)
		switch {
		case len(yes) > 0:
			return &contract.Rank{Tier: 1, Reason: "a caller in the module can pass " + what, Sites: yes}
		case len(maybe) > 0:
			return &contract.Rank{Tier: 2, Reason: what + ": a caller passes a value that may be nil (not traced to a nil)", Sites: maybe}
		case !ok:
			return &contract.Rank{Tier: 3, Reason: what + ": the module's call sites could not be loaded"}
		case calls == 0:
			return &contract.Rank{Tier: 3, Reason: what + ": no static call site in the module (called through an interface, or from outside)"}
		default:
			return &contract.Rank{Tier: 3, Reason: fmt.Sprintf("%s: none of its %d call sites in the module can pass nil there", what, calls)}
		}
	}
	if exportedFunc(fn) {
		// A method whose receiver the test built field by field, of a type
		// the package builds with a constructor (#195): the fields can break
		// an invariant the package keeps (chi's Context.URLParam, Keys and
		// Values filled together by RouteParams.Add). The ~NewT target runs
		// the method on the constructor's value instead.
		if !strings.Contains(p.target.Func, "~") {
			if ctor := callers.constructorOf(p.target.Package, fn); ctor != "" {
				return &contract.Rank{Tier: 2, Reason: "a receiver built field by field, where the package builds one with " + ctor + ": check that the package's own values can get here"}
			}
			// The test set an unexported field of the receiver, which no
			// caller outside the package can (grpc's mem.Reader, built by
			// BufferSlice.Reader, with its length set and no data, #220).
			if f := unexportedSet(p.abi, p.cx); f != "" {
				return &contract.Rank{Tier: 2, Reason: "the receiver's unexported field " + f + " set by the test, which only the package's own code can do: check that its values can get here"}
			}
		}
		if f := unexportedArg(p.abi, p.cx); f != "" {
			return &contract.Rank{Tier: 2, Reason: "the argument's unexported field " + f + " set by the test, which only the package's own code can do: check that its values can get here"}
		}
		return &contract.Rank{Tier: 1, Reason: "plain inputs to the package's API"}
	}
	// An unexported function whose callers all pass constants for the
	// integers the counterexample set: are its values among them?
	var ints []string
	for name := range p.cx.Inputs {
		if !strings.Contains(name, "_") {
			ints = append(ints, name)
		}
	}
	if values, all, _ := callers.constArgs(p.target.Package, fn, ints); all {
		hit := true
		var desc []string
		for name, vs := range values {
			hit = hit && slices.Contains(vs, p.cx.Inputs[name])
			desc = append(desc, name+" in {"+strings.Join(vs, ", ")+"}")
		}
		sort.Strings(desc)
		if hit {
			return &contract.Rank{Tier: 1, Reason: "every caller passes constants, and these are among them: " + strings.Join(desc, "; ")}
		}
		return &contract.Rank{Tier: 3, Reason: "every caller passes constants, not these: " + strings.Join(desc, "; ")}
	}
	// Traced back to the public API (#224): a caller's value reaches the
	// input, through no check that may stop it.
	if reached, checks, ok := callers.reachAPI(p.target.Package, fn, scalarInputs(p.cx)); ok {
		path := strings.Join(reached, "; ")
		if len(checks) == 0 {
			return &contract.Rank{Tier: 1, Reason: "a caller's value reaches it: " + path}
		}
		return &contract.Rank{Tier: 2, Reason: "a caller's value reaches it (" + path + "), through checks that may stop it: " + strings.Join(checks, "; ")}
	}
	return &contract.Rank{Tier: 2, Reason: "plain inputs to an unexported function: check its callers"}
}

// scalarInputs are the counterexample's plain inputs (no slices, structs
// or nils), by parameter name.
func scalarInputs(cx *contract.Counterexample) map[string]string {
	out := map[string]string{}
	for name, v := range cx.Inputs {
		if !strings.Contains(name, "_") && v != "nil" {
			out[name] = v
		}
	}
	return out
}

// baseFunc strips what the target id adds to a function's name: the
// constructor of its receiver (`T.M~NewT`) and type arguments.
func baseFunc(f string) string {
	f, _, _ = strings.Cut(f, "~")
	f, _, _ = strings.Cut(f, "[")
	return f
}

// APIFunc says whether a target function (`F`, `T.M~NewT`, `F[int]`) is
// part of its package's API.
func APIFunc(target string) bool { return exportedFunc(baseFunc(target)) }

// exportedFunc says whether a function (`F`) or method (`T.M`) is part of
// its package's API; closures (`F$1`) are not.
func exportedFunc(f string) bool {
	if strings.Contains(f, "$") {
		return false
	}
	for _, part := range strings.Split(f, ".") {
		part = strings.TrimLeft(part, "(*")
		if part == "" || !unicode.IsUpper([]rune(part)[0]) {
			return false
		}
	}
	return true
}

// funcKey is "pkg.F" or "pkg.T.M" (pointer receivers as T).
func funcKey(f *types.Func) string {
	f = f.Origin()
	sig := f.Type().(*types.Signature)
	if recv := sig.Recv(); recv != nil {
		t := recv.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := t.(*types.Named); ok {
			return f.Pkg().Path() + "." + n.Obj().Name() + "." + f.Name()
		}
	}
	return f.Pkg().Path() + "." + f.Name()
}

func containsName(names []string, n string) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}

// RankAll ranks the confirmed verdicts already in the workspace (written
// before ranking existed, or against older code) and writes them back.
func RankAll(ws contract.Workspace, root string) ([]*contract.Verdict, error) {
	files, err := filepath.Glob(filepath.Join(ws.Root, "verdicts", "*.json"))
	if err != nil {
		return nil, err
	}
	callers := newCallerIndex(root)
	var out []*contract.Verdict
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		v := ws.Verdict(id)
		if v == nil || v.Status != contract.VerdictConfirmed {
			continue
		}
		p, err := prepare(ws, id)
		if err != nil {
			return nil, err
		}
		p.v = v
		v.Rank = rankOf(p, callers)
		if err := ws.WriteVerdict(v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank.Tier < out[j].Rank.Tier })
	return out, nil
}

// unexportedSet names an unexported field of a method's receiver that the
// counterexample set to a value other than its zero value, or "".
func unexportedSet(abi *contract.ABI, cx *contract.Counterexample) string {
	if abi == nil || abi.Method == "" || len(abi.Params) == 0 || cx == nil {
		return ""
	}
	return unexportedSetIn(abi, cx, abi.Params[0])
}

// unexportedArg names a parameter other than the receiver, and its
// unexported field, that the counterexample set to a non-zero value: a
// struct of the package's own that callers outside it get only from the
// package (regexp2's *Match, which FindNextMatch takes back, built with a
// negative position, #237). "" when there is none.
func unexportedArg(abi *contract.ABI, cx *contract.Counterexample) string {
	if abi == nil || cx == nil {
		return ""
	}
	for i, p := range abi.Params {
		if i == 0 && abi.Method != "" && abi.Ctor == nil {
			continue // the receiver: unexportedSet
		}
		if abi.Ctor != nil && i < abi.Ctor.N {
			continue // the constructor's arguments
		}
		if f := unexportedSetIn(abi, cx, p); f != "" {
			return p.Go + "." + f
		}
		if f := unexportedSetInAlt(abi, cx, p); f != "" {
			return p.Go + "." + f
		}
	}
	return ""
}

// unexportedSetInAlt is unexportedSetIn for an interface parameter
// holding one of its alternatives (blackfriday's Renderer holding an
// *Html with a forged tocMarker, #237): the alternative the value's tag
// names, checked on the value inside.
func unexportedSetInAlt(abi *contract.ABI, cx *contract.Counterexample, p contract.ABIParam) string {
	if len(p.Alts) == 0 {
		return ""
	}
	raw, ok := cx.Objects[p.Go]
	if !ok {
		return ""
	}
	var boxed struct {
		Tag uint32          `json:"tag"`
		V   json.RawMessage `json:"v"`
	}
	if json.Unmarshal(raw, &boxed) != nil || boxed.V == nil {
		return ""
	}
	for _, a := range p.Alts {
		if a.Tag != boxed.Tag {
			continue
		}
		inner := &contract.Counterexample{Objects: map[string]json.RawMessage{a.Go: boxed.V}}
		return unexportedSetIn(abi, inner, a)
	}
	return ""
}

// unexportedSetIn names an unexported field of the struct (or pointer to
// struct) parameter p that the counterexample set to a non-zero value.
func unexportedSetIn(abi *contract.ABI, cx *contract.Counterexample, recv contract.ABIParam) string {
	st := &recv
	if recv.Pointee != nil {
		st = recv.Pointee
	}
	fields := st.Fields
	if st.TypeRef != "" {
		// A struct that refers to itself is in the ABI's type table.
		for _, t := range abi.Types {
			if t.GoType == st.TypeRef {
				fields = t.Fields
			}
		}
	}
	if len(fields) == 0 {
		return ""
	}
	raw, ok := cx.Objects[recv.Go]
	if !ok {
		return ""
	}
	var vals []json.RawMessage
	if json.Unmarshal(raw, &vals) != nil || len(vals) != len(fields) {
		return ""
	}
	for i, f := range fields {
		if f.Go == "" || !unicode.IsLower(rune(f.Go[0])) && f.Go[0] != '_' {
			continue
		}
		switch strings.Join(strings.Fields(string(vals[i])), "") {
		case "null", "[]", "{}", `"0"`, `""`, `"false"`, "0", "false", `{"map":[]}`:
			continue
		}
		return f.Go
	}
	return ""
}

var tagRe = regexp.MustCompile(`"tag":\s*(\d+)`)

// unexportedAlt names the unexported dynamic type of an interface input
// the counterexample holds ("" when there is none).
func unexportedAlt(abi *contract.ABI, cx *contract.Counterexample) string {
	if abi == nil {
		return ""
	}
	byTag := map[string]string{}
	var walk func(p contract.ABIParam)
	walk = func(p contract.ABIParam) {
		for _, a := range p.Alts {
			byTag[fmt.Sprint(a.Tag)] = a.GoType
			walk(a)
		}
		for _, f := range p.Fields {
			walk(f)
		}
		if p.ElemParam != nil {
			walk(*p.ElemParam)
		}
		if p.Key != nil {
			walk(*p.Key)
		}
		if p.Value != nil {
			walk(*p.Value)
		}
	}
	for _, p := range abi.Params {
		walk(p)
	}
	for _, t := range abi.Types {
		for _, f := range t.Fields {
			walk(f)
		}
	}
	if len(byTag) == 0 {
		return ""
	}
	var found []string
	for _, raw := range cx.Objects {
		for _, m := range tagRe.FindAllStringSubmatch(string(raw), -1) {
			if t, ok := byTag[m[1]]; ok && namedType(t) && !exportedTypeName(t) && !slices.Contains(found, t) {
				found = append(found, t)
			}
		}
	}
	sort.Strings(found)
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

// exportedTypeName: the type a Go type expression names (`*pkg.T`, `T`)
// is exported.
// namedType says whether the spelling t names a defined type (*buffer,
// pkg.T), not a type literal or a predeclared type ([]interface{},
// map[string]interface{}, string), which no package owns: an interface
// holding one was not forged (raymond's MustRender with a []interface{}
// context was ranked so).
func namedType(t string) bool {
	t = strings.TrimLeft(t, "*")
	if t == "" || strings.ContainsAny(t, "[]{}() ") || strings.HasPrefix(t, "map") || strings.HasPrefix(t, "chan") || strings.HasPrefix(t, "func") {
		return false
	}
	switch t {
	case "string", "bool", "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128", "byte", "rune", "error", "any":
		return false
	}
	return true
}

func exportedTypeName(t string) bool {
	t = strings.TrimLeft(t, "*[]")
	if i := strings.LastIndex(t, "."); i >= 0 {
		t = t[i+1:]
	}
	return t != "" && unicode.IsUpper([]rune(t)[0])
}
