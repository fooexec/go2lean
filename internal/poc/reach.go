package poc

import (
	"fmt"
	"go/token"
	"go/types"
	"math/big"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// Tracing a confirmed panic in an unexported function back to the public
// API (#224). grpc-go's internal/transport.getJitter(v) panics for v below
// 10ns; its v is kp.MaxConnectionAge, a field of the config the server
// builds from the options grpc.KeepaliveParams(kp) stored. The value is
// followed backwards through the module's SSA: a parameter to the call
// sites of its function, a load to the stores into that location (a
// struct field anywhere in the module, a local, a global), a captured
// variable to the closures that bind it. It is reached when it comes from
// a parameter of an exported function of a public package, or from an
// exported field of an exported struct of one, which callers set.

// reachDepth bounds the steps from the panicking call back to the API.
const reachDepth = 24

// fieldRef is one field of a struct type.
type fieldRef struct {
	st    *types.Struct
	named string // the struct's name, for paths ("" when unnamed)
	pkg   *types.Package
	index int
}

func (f fieldRef) name() string { return f.st.Field(f.index).Name() }

// reachIndex is the SSA index the tracer reads, built with the caller index.
type reachIndex struct {
	// storesTo: the stores into an address value (an Alloc, a FieldAddr,
	// a Global).
	storesTo map[ssa.Value][]*ssa.Store
	// fieldStores: the stores into a field, of any value of its struct.
	fieldStores map[string][]*ssa.Store
	// fieldAddrs: the FieldAddrs of a base value.
	fieldAddrs map[ssa.Value][]*ssa.FieldAddr
	// closures: the MakeClosures of a function (its free variables'
	// bindings).
	closures map[*ssa.Function][]*ssa.MakeClosure
}

func fieldKey(st *types.Struct, i int) string {
	return types.TypeString(st, nil) + "#" + fmt.Sprint(i)
}

// structOf is the struct a FieldAddr's base points to, or a Field's base is.
func structOf(t types.Type) (*types.Struct, *types.Named) {
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = p.Elem()
	}
	n, _ := t.(*types.Named)
	st, _ := t.Underlying().(*types.Struct)
	return st, n
}

func refOf(t types.Type, i int) (fieldRef, bool) {
	st, n := structOf(t)
	if st == nil || i >= st.NumFields() {
		return fieldRef{}, false
	}
	r := fieldRef{st: st, index: i}
	if n != nil {
		r.named, r.pkg = n.Obj().Name(), n.Obj().Pkg()
	}
	return r, true
}

func (c *callerIndex) indexReach(fns []*ssa.Function) {
	r := &reachIndex{storesTo: map[ssa.Value][]*ssa.Store{}, fieldStores: map[string][]*ssa.Store{},
		fieldAddrs: map[ssa.Value][]*ssa.FieldAddr{}, closures: map[*ssa.Function][]*ssa.MakeClosure{}}
	for _, f := range fns {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch in := in.(type) {
				case *ssa.Store:
					r.storesTo[in.Addr] = append(r.storesTo[in.Addr], in)
					if fa, ok := in.Addr.(*ssa.FieldAddr); ok {
						if st, _ := structOf(fa.X.Type()); st != nil {
							k := fieldKey(st, fa.Field)
							r.fieldStores[k] = append(r.fieldStores[k], in)
						}
					}
				case *ssa.FieldAddr:
					r.fieldAddrs[in.X] = append(r.fieldAddrs[in.X], in)
				case *ssa.MakeClosure:
					if fn, ok := in.Fn.(*ssa.Function); ok {
						r.closures[fn] = append(r.closures[fn], in)
					}
				}
			}
		}
	}
	c.reach = r
}

// publicPkg: a package callers outside the module can import.
func publicPkg(p *types.Package) bool {
	if p == nil {
		return false
	}
	path := p.Path()
	return p.Name() != "main" && !strings.Contains(path, "/internal/") && !strings.HasSuffix(path, "/internal") && !strings.HasPrefix(path, "internal/")
}

// apiFunc: an exported function, or an exported method of an exported
// type, of a public package.
func apiFunc(f *ssa.Function) bool {
	if f.Parent() != nil || f.Pkg == nil || !publicPkg(f.Pkg.Pkg) || !token.IsExported(f.Name()) {
		return false
	}
	if recv := f.Signature.Recv(); recv != nil {
		_, n := structOf(recv.Type())
		if n == nil {
			t := recv.Type()
			if p, ok := t.(*types.Pointer); ok {
				t = p.Elem()
			}
			n, _ = t.(*types.Named)
		}
		return n != nil && n.Obj().Exported()
	}
	return true
}

func shortFunc(f *ssa.Function) string {
	name := f.Name()
	if recv := f.Signature.Recv(); recv != nil {
		t := recv.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := t.(*types.Named); ok {
			name = n.Obj().Name() + "." + name
		}
	}
	if f.Pkg != nil {
		return f.Pkg.Pkg.Name() + "." + name
	}
	return name
}

// tracer follows one value; it collects the API entries reached and the
// checks met on the way.
type tracer struct {
	c      *callerIndex
	want   *big.Int // the counterexample's value, for == and != checks
	seen   map[string]bool
	hits   []string
	checks []string
}

func projDesc(proj []fieldRef) string {
	var b strings.Builder
	for _, f := range proj {
		b.WriteString("." + f.name())
	}
	return b.String()
}

// settable: every field of proj is an exported field of an exported
// struct of a public package, which a caller fills in.
func settable(proj []fieldRef) bool {
	if len(proj) == 0 {
		return false
	}
	for _, f := range proj {
		if f.named == "" || !token.IsExported(f.named) || !publicPkg(f.pkg) || !f.st.Field(f.index).Exported() {
			return false
		}
	}
	return true
}

func (t *tracer) key(v ssa.Value, proj []fieldRef, kind string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %p", kind, v)
	for _, f := range proj {
		fmt.Fprintf(&b, ".%p#%d", f.st, f.index)
	}
	return b.String()
}

// check records the comparisons of v against constants: == and != a
// constant other than the counterexample's value let it through; any other
// check may stop it.
func (t *tracer) check(v ssa.Value) {
	refs := v.Referrers()
	if refs == nil {
		return
	}
	for _, in := range *refs {
		bin, ok := in.(*ssa.BinOp)
		if !ok {
			continue
		}
		switch bin.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		default:
			continue
		}
		other := bin.X
		if other == v {
			other = bin.Y
		}
		k, isConst := other.(*ssa.Const)
		if (bin.Op == token.EQL || bin.Op == token.NEQ) && isConst && k.Value != nil && t.want != nil {
			if c, ok := new(big.Int).SetString(k.Value.ExactString(), 10); ok && c.Cmp(t.want) != 0 {
				continue
			}
		}
		where := ""
		if f := bin.Parent(); f != nil {
			where = " in " + shortFunc(f)
		}
		t.checks = append(t.checks, fmt.Sprintf("%s%s", bin.String(), where))
	}
}

// value traces where v (projected by proj) comes from.
func (t *tracer) value(v ssa.Value, proj []fieldRef, depth int, trail []string) {
	if depth > reachDepth || t.seen[t.key(v, proj, "v")] {
		return
	}
	t.seen[t.key(v, proj, "v")] = true
	if len(proj) == 0 {
		t.check(v)
	}
	switch v := v.(type) {
	case *ssa.Parameter:
		f := v.Parent()
		if apiFunc(f) {
			t.hits = append(t.hits, shortFunc(f)+"("+v.Name()+projDesc(proj)+")"+arrows(trail))
			return
		}
		idx := -1
		for i, p := range f.Params {
			if p == v {
				idx = i
			}
		}
		for _, call := range t.c.callsTo[f] {
			if idx >= 0 && idx < len(call.Call.Args) {
				t.value(call.Call.Args[idx], proj, depth+1, append([]string{shortFunc(f)}, trail...))
			}
		}
	case *ssa.FreeVar:
		f := v.Parent()
		idx := -1
		for i, fv := range f.FreeVars {
			if fv == v {
				idx = i
			}
		}
		for _, mc := range t.c.reach.closures[f] {
			if idx >= 0 && idx < len(mc.Bindings) {
				t.value(mc.Bindings[idx], proj, depth+1, trail)
			}
		}
	case *ssa.UnOp:
		if v.Op == token.MUL {
			t.load(v.X, proj, depth+1, trail)
		}
	case *ssa.Field:
		if r, ok := refOf(v.X.Type(), v.Field); ok {
			t.value(v.X, append([]fieldRef{r}, proj...), depth+1, trail)
		}
	case *ssa.Convert:
		t.value(v.X, proj, depth+1, trail)
	case *ssa.ChangeType:
		t.value(v.X, proj, depth+1, trail)
	case *ssa.Phi:
		for _, e := range v.Edges {
			t.value(e, proj, depth+1, trail)
		}
	}
}

// load traces the value read from addr, projected by proj.
func (t *tracer) load(addr ssa.Value, proj []fieldRef, depth int, trail []string) {
	if depth > reachDepth || t.seen[t.key(addr, proj, "l")] {
		return
	}
	t.seen[t.key(addr, proj, "l")] = true
	switch a := addr.(type) {
	case *ssa.FieldAddr:
		r, ok := refOf(a.X.Type(), a.Field)
		if !ok {
			return
		}
		// The stores into this very field address, and the loads of it
		// another check may read.
		for _, st := range t.c.reach.storesTo[a] {
			t.value(st.Val, proj, depth+1, trail)
		}
		t.load(a.X, append([]fieldRef{r}, proj...), depth+1, trail)
	case *ssa.FreeVar:
		// A captured variable: the closure was made with the address of
		// its maker's local (KeepaliveParams' kp, modified before).
		f := a.Parent()
		idx := -1
		for i, fv := range f.FreeVars {
			if fv == a {
				idx = i
			}
		}
		for _, mc := range t.c.reach.closures[f] {
			if idx >= 0 && idx < len(mc.Bindings) {
				t.load(mc.Bindings[idx], proj, depth+1, trail)
			}
		}
	case *ssa.Alloc, *ssa.Global:
		if g, ok := a.(*ssa.Global); ok && len(proj) == 0 && publicPkg(g.Pkg.Pkg) && token.IsExported(g.Name()) {
			t.hits = append(t.hits, g.Pkg.Pkg.Name()+"."+g.Name()+arrows(trail))
		}
		for _, st := range t.c.reach.storesTo[a] {
			t.value(st.Val, proj, depth+1, trail)
		}
		if len(proj) > 0 {
			for _, fa := range t.c.reach.fieldAddrs[a] {
				if fa.Field != proj[0].index {
					continue
				}
				t.locationChecks(fa)
				for _, st := range t.c.reach.storesTo[fa] {
					t.value(st.Val, proj[1:], depth+1, trail)
				}
			}
		}
	default:
		// A pointer from elsewhere: the stores into the field anywhere in
		// the module, and the callers that fill a public struct.
		if len(proj) == 0 {
			return
		}
		if settable(proj) {
			f := proj[0]
			t.hits = append(t.hits, f.pkg.Name()+"."+f.named+projDesc(proj)+arrows(trail))
		}
		// Every field on the path: o.kp = kp fills s.opts.kp through a
		// pointer to the inner struct.
		for i, f := range proj {
			via := append([]string{f.named + "." + f.name()}, trail...)
			for _, st := range t.c.reach.fieldStores[fieldKey(f.st, f.index)] {
				t.value(st.Val, proj[i+1:], depth+1, via)
			}
		}
	}
}

// locationChecks records the checks on the loads of a field address (the
// `if kp.MaxConnectionAge == 0` before the call reads it apart).
func (t *tracer) locationChecks(fa *ssa.FieldAddr) {
	refs := fa.Referrers()
	if refs == nil {
		return
	}
	for _, in := range *refs {
		if u, ok := in.(*ssa.UnOp); ok && u.Op == token.MUL {
			t.check(u)
		}
	}
}

func arrows(trail []string) string {
	if len(trail) == 0 {
		return ""
	}
	return " → " + strings.Join(trail, " → ")
}

// reachAPI traces the named integer parameters of pkg.fn, at every static
// call site, back to the public API. reached lists, for each parameter,
// the entries it comes from (all of them must be reached); checks lists
// the comparisons on the way that may stop the counterexample's value.
func (c *callerIndex) reachAPI(pkg, fn string, inputs map[string]string) (reached []string, checks []string, ok bool) {
	c.load()
	target := c.funcs[pkg+"."+fn]
	if c.failed || c.reach == nil || target == nil {
		return nil, nil, false
	}
	var names []string
	for _, p := range target.Params {
		if _, set := inputs[p.Name()]; set {
			names = append(names, p.Name())
		}
	}
	if len(names) == 0 {
		return nil, nil, false
	}
	sort.Strings(names)
	for _, name := range names {
		idx := -1
		for i, p := range target.Params {
			if p.Name() == name {
				idx = i
			}
		}
		tr := &tracer{c: c, seen: map[string]bool{}}
		if w, ok := new(big.Int).SetString(inputs[name], 10); ok {
			tr.want = w
		}
		for _, call := range c.callsTo[target] {
			if idx < len(call.Call.Args) {
				tr.value(call.Call.Args[idx], nil, 0, []string{shortFunc(target) + "(" + name + ")"})
			}
		}
		if len(tr.hits) == 0 {
			return nil, nil, false
		}
		// A function's parameter first: it names the call a user makes.
		sort.Slice(tr.hits, func(i, j int) bool {
			if fi, fj := funcEntry(tr.hits[i]), funcEntry(tr.hits[j]); fi != fj {
				return fi
			}
			return tr.hits[i] < tr.hits[j]
		})
		reached = append(reached, tr.hits[0])
		checks = append(checks, tr.checks...)
	}
	sort.Strings(checks)
	return reached, dedup(checks), true
}

func dedup(xs []string) []string {
	var out []string
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// funcEntry: an entry that is a function's parameter, "pkg.F(p...) → ...".
func funcEntry(hit string) bool {
	first, _, _ := strings.Cut(hit, " ")
	return strings.Contains(first, "(")
}
