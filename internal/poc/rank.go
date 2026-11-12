package poc

import (
	"fmt"
	"go/types"
	"path/filepath"
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
	return &contract.Rank{Tier: 2, Reason: "plain inputs to an unexported function: check its callers"}
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
