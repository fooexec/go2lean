package poc

import (
	"fmt"
	"go/token"
	"go/types"
	"slices"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// Nil flow: can a caller in the module pass nil where a counterexample
// did? The arguments at the target's static call sites are traced back
// through the callers' SSA: a nil constant (possibly through phis and
// conversions), a parameter of the caller (whose own call sites are traced
// in turn, a few levels up), the result of a function that can return nil.
// Allocations, composite literals and make are never nil. A field, a
// global or the result of code outside the module is unknown.

// nilVerdict orders what a value may be: no < maybe < yes.
type nilVerdict int

const (
	nilNo nilVerdict = iota
	nilMaybe
	nilYes
)

// nilDepth bounds how many callers up (and callees down) a value is traced.
const nilDepth = 3

// callerIndex holds the module's SSA and its static call sites, built on
// first use.
type callerIndex struct {
	root   string
	loaded bool
	failed bool
	// calls maps "pkg.F" or "pkg.T.M" to the calls of that function.
	calls map[string][]*ssa.Call
	// funcs maps the same keys to the function.
	funcs map[string]*ssa.Function
	// callsTo maps a function to its static call sites (for tracing a
	// parameter up to the caller's callers).
	callsTo map[*ssa.Function][]*ssa.Call
	// boxes maps a pointer type to the places its values are converted to
	// an interface: a method called through the interface gets a nil
	// receiver when the pointer was nil (a typed nil in an interface).
	boxes map[string][]*ssa.MakeInterface
	// reach indexes stores, field addresses and closures for reachAPI.
	reach *reachIndex
}

func newCallerIndex(root string) *callerIndex { return &callerIndex{root: root} }

func (c *callerIndex) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.calls, c.funcs, c.callsTo = map[string][]*ssa.Call{}, map[string]*ssa.Function{}, map[*ssa.Function][]*ssa.Call{}
	c.boxes = map[string][]*ssa.MakeInterface{}
	cfg := &packages.Config{Mode: packages.LoadSyntax, Dir: c.root}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		c.failed = true
		return
	}
	prog, _ := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	all := ssautil.AllFunctions(prog)
	fns := make([]*ssa.Function, 0, len(all))
	for f := range all {
		fns = append(fns, f)
	}
	c.indexReach(fns)
	for f := range all {
		if obj, ok := f.Object().(*types.Func); ok && f.Pkg != nil && f.Synthetic == "" {
			c.funcs[funcKey(obj)] = f
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if mi, ok := in.(*ssa.MakeInterface); ok {
					if _, isPtr := mi.X.Type().Underlying().(*types.Pointer); isPtr {
						k := types.TypeString(mi.X.Type(), nil)
						c.boxes[k] = append(c.boxes[k], mi)
					}
					continue
				}
				call, ok := in.(*ssa.Call)
				if !ok {
					continue
				}
				callee := call.Call.StaticCallee()
				if callee == nil {
					continue
				}
				callee = callee.Origin()
				if callee == nil {
					callee = call.Call.StaticCallee()
				}
				c.callsTo[callee] = append(c.callsTo[callee], call)
				if obj, ok := callee.Object().(*types.Func); ok && obj.Pkg() != nil {
					k := funcKey(obj)
					c.calls[k] = append(c.calls[k], call)
				}
			}
		}
	}
}

// constructorOf names an exported function of pkg that returns a value of
// method fn's receiver type, a struct, or a pointer to one ("" when there
// is none, fn is not a method, or the module could not be loaded).
func (c *callerIndex) constructorOf(pkg, fn string) string {
	c.load()
	if c.failed {
		return ""
	}
	m, ok := c.funcs[pkg+"."+fn]
	if !ok || m.Signature.Recv() == nil {
		return ""
	}
	recv := m.Signature.Recv().Type()
	if p, ok := recv.(*types.Pointer); ok {
		recv = p.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok {
		return ""
	}
	if _, isStruct := named.Underlying().(*types.Struct); !isStruct {
		return ""
	}
	var names []string
	for key, f := range c.funcs {
		if f.Signature.Recv() != nil || !strings.HasPrefix(key, pkg+".") || strings.Contains(key[len(pkg)+1:], ".") {
			continue
		}
		if !exportedFunc(f.Name()) || f.Signature.Results().Len() == 0 {
			continue
		}
		r := f.Signature.Results().At(0).Type()
		if p, ok := r.(*types.Pointer); ok {
			r = p.Elem()
		}
		if rn, ok := r.(*types.Named); ok && rn.Obj() == named.Obj() {
			names = append(names, f.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return names[0]
}

// nilSites traces, at every static call site of pkg.fn, the arguments for
// the named parameters (the receiver included). yes are the call sites
// that can pass nil, with how; maybe those that pass a value whose nil-ness
// is unknown (a field, a global, code outside the module); calls counts the
// call sites; ok is false when the module could not be loaded.
func (c *callerIndex) nilSites(pkg, fn string, names []string) (yes, maybe []string, calls int, ok bool) {
	c.load()
	if c.failed {
		return nil, nil, 0, false
	}
	key := pkg + "." + fn
	target := c.funcs[key]
	sites := c.calls[key]
	if target == nil {
		return nil, nil, len(sites), true
	}
	var idx []int
	for i, p := range target.Params {
		if containsName(names, p.Name()) {
			idx = append(idx, i)
		}
	}
	for _, call := range sites {
		pos := target.Prog.Fset.Position(call.Pos())
		where := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
		best, why := nilNo, ""
		for _, i := range idx {
			if i >= len(call.Call.Args) {
				continue
			}
			if guarded(call.Call.Args[i], call) {
				continue
			}
			v, w := c.mayNil(call.Call.Args[i], nilDepth, map[ssa.Value]bool{})
			if v > best {
				best, why = v, fmt.Sprintf("%s: %s", target.Params[i].Name(), w)
			}
		}
		switch best {
		case nilYes:
			yes = append(yes, where+" ("+why+")")
		case nilMaybe:
			maybe = append(maybe, where+" ("+why+")")
		}
	}
	// A nil receiver also arrives through an interface holding a nil
	// pointer of the receiver's type.
	if recv := target.Signature.Recv(); recv != nil && len(idx) > 0 && idx[0] == 0 {
		for _, mi := range c.boxes[types.TypeString(recv.Type(), nil)] {
			if guarded(mi.X, mi) {
				continue
			}
			pos := target.Prog.Fset.Position(mi.Pos())
			where := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
			switch v, w := c.mayNil(mi.X, nilDepth, map[ssa.Value]bool{}); v {
			case nilYes:
				yes = append(yes, where+" (a nil "+recv.Type().String()+" stored in an interface: "+w+")")
			case nilMaybe:
				maybe = append(maybe, where+" (stored in an interface: "+w+")")
			}
		}
	}
	sort.Strings(yes)
	sort.Strings(maybe)
	return yes, maybe, len(sites), true
}

// mayNil says whether v can be nil, and why, tracing depth levels of
// callers and callees.
func (c *callerIndex) mayNil(v ssa.Value, depth int, seen map[ssa.Value]bool) (nilVerdict, string) {
	if seen[v] {
		return nilNo, ""
	}
	seen[v] = true
	switch v := v.(type) {
	case *ssa.Const:
		if v.IsNil() {
			return nilYes, "nil literal"
		}
		return nilNo, ""
	case *ssa.Alloc, *ssa.MakeMap, *ssa.MakeSlice, *ssa.MakeChan, *ssa.MakeClosure,
		*ssa.FieldAddr, *ssa.IndexAddr, *ssa.Function, *ssa.MakeInterface:
		return nilNo, ""
	case *ssa.Phi:
		best, why := nilNo, ""
		for _, e := range v.Edges {
			if r, w := c.mayNil(e, depth, seen); r > best {
				best, why = r, w
			}
		}
		return best, why
	case *ssa.ChangeType:
		return c.mayNil(v.X, depth, seen)
	case *ssa.ChangeInterface:
		return c.mayNil(v.X, depth, seen)
	case *ssa.Convert:
		return c.mayNil(v.X, depth, seen)
	case *ssa.Slice:
		return c.mayNil(v.X, depth, seen)
	case *ssa.Parameter:
		return c.paramMayNil(v, depth, seen)
	case *ssa.Call:
		return c.resultMayNil(v.Call.StaticCallee(), 0, depth, seen)
	case *ssa.Extract:
		if call, ok := v.Tuple.(*ssa.Call); ok {
			return c.resultMayNil(call.Call.StaticCallee(), v.Index, depth, seen)
		}
	case *ssa.UnOp:
		if v.Op.String() == "*" {
			if _, isGlobal := v.X.(*ssa.Global); isGlobal {
				return nilMaybe, "a package variable"
			}
			return nilMaybe, "a field or a value read through a pointer"
		}
	}
	return nilMaybe, "a value the trace does not follow"
}

// paramMayNil traces a parameter of a function to the arguments its
// callers pass.
func (c *callerIndex) paramMayNil(p *ssa.Parameter, depth int, seen map[ssa.Value]bool) (nilVerdict, string) {
	f := p.Parent()
	idx := -1
	for i, q := range f.Params {
		if q == p {
			idx = i
		}
	}
	key := f
	if f.Origin() != nil {
		key = f.Origin()
	}
	callers := c.callsTo[key]
	if idx < 0 || depth == 0 || len(callers) == 0 {
		return nilMaybe, "parameter " + p.Name() + " of " + f.Name() + " (its callers are not traced)"
	}
	best, why := nilNo, ""
	for _, call := range callers {
		if idx >= len(call.Call.Args) || guarded(call.Call.Args[idx], call) {
			continue
		}
		if r, w := c.mayNil(call.Call.Args[idx], depth-1, seen); r > best {
			pos := f.Prog.Fset.Position(call.Pos())
			best, why = r, fmt.Sprintf("%s, passed as %s of %s at %s:%d", w, p.Name(), f.Name(), pos.Filename, pos.Line)
		}
	}
	return best, why
}

// resultMayNil says whether result i of f can be nil: a return of a nil
// constant (traced into f) makes it so; code outside the module is unknown.
func (c *callerIndex) resultMayNil(f *ssa.Function, i, depth int, seen map[ssa.Value]bool) (nilVerdict, string) {
	if f == nil || len(f.Blocks) == 0 {
		name := "an unknown function"
		if f != nil {
			name = f.String()
		}
		return nilMaybe, "the result of " + name
	}
	if depth == 0 {
		return nilMaybe, "the result of " + f.Name() + " (not traced further)"
	}
	best, why := nilNo, ""
	for _, b := range f.Blocks {
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok || i >= len(ret.Results) {
			continue
		}
		if r, w := c.mayNil(ret.Results[i], depth-1, seen); r > best {
			best, why = r, w+", returned by "+f.Name()
		}
	}
	return best, strings.TrimPrefix(why, ", ")
}

// guarded says whether a nil check on v dominates the instruction: it is
// reached only through the true branch of v != nil or the false branch of
// v == nil, so v is not nil there.
func guarded(v ssa.Value, at ssa.Instruction) bool {
	b := at.Block()
	if calledBefore(v, at) {
		return true
	}
	for x := b; x != nil; x = x.Idom() {
		p := x.Idom()
		if p == nil || len(p.Instrs) == 0 {
			continue
		}
		ifc, ok := p.Instrs[len(p.Instrs)-1].(*ssa.If)
		if !ok {
			continue
		}
		cmp, ok := ifc.Cond.(*ssa.BinOp)
		if !ok || (cmp.Op != token.NEQ && cmp.Op != token.EQL) || !nilComparison(cmp, v) {
			continue
		}
		taken := -1
		switch {
		case p.Succs[0].Dominates(b):
			taken = 0
		case p.Succs[1].Dominates(b):
			taken = 1
		}
		if (cmp.Op == token.NEQ && taken == 0) || (cmp.Op == token.EQL && taken == 1) {
			return true
		}
	}
	return false
}

// calledBefore says whether the func value v is called on every path to
// at, before it: a nil v would have panicked there first. go-toml passes
// isInRange on only after calling isInRange(b[i]) itself (#194).
func calledBefore(v ssa.Value, at ssa.Instruction) bool {
	if _, ok := v.Type().Underlying().(*types.Signature); !ok {
		return false
	}
	called := func(in ssa.Instruction) bool {
		c, ok := in.(ssa.CallInstruction)
		return ok && in != at && !c.Common().IsInvoke() && c.Common().Value == v
	}
	b := at.Block()
	for _, in := range b.Instrs {
		if in == at {
			break
		}
		if called(in) {
			return true
		}
	}
	for x := b.Idom(); x != nil; x = x.Idom() {
		for _, in := range x.Instrs {
			if called(in) {
				return true
			}
		}
	}
	return false
}

// nilComparison says whether cmp compares v with nil.
func nilComparison(cmp *ssa.BinOp, v ssa.Value) bool {
	isNil := func(x ssa.Value) bool {
		c, ok := x.(*ssa.Const)
		return ok && c.IsNil()
	}
	return (cmp.X == v && isNil(cmp.Y)) || (cmp.Y == v && isNil(cmp.X))
}

// constArgs looks at the arguments every static call site of pkg.fn passes
// for the named integer parameters: all says whether each site passes a
// constant for each of them, and values lists the constants by parameter.
func (c *callerIndex) constArgs(pkg, fn string, names []string) (values map[string][]string, all bool, calls int) {
	c.load()
	target := c.funcs[pkg+"."+fn]
	sites := c.calls[pkg+"."+fn]
	if c.failed || target == nil || len(sites) == 0 {
		return nil, false, len(sites)
	}
	values = map[string][]string{}
	for i, p := range target.Params {
		if !containsName(names, p.Name()) {
			continue
		}
		for _, call := range sites {
			if i >= len(call.Call.Args) {
				return nil, false, len(sites)
			}
			k, ok := call.Call.Args[i].(*ssa.Const)
			if !ok || k.Value == nil {
				return nil, false, len(sites)
			}
			v := k.Value.ExactString()
			if !slices.Contains(values[p.Name()], v) {
				values[p.Name()] = append(values[p.Name()], v)
			}
		}
	}
	return values, len(values) > 0, len(sites)
}
