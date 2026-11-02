// Package gossa is M1's go/ssa frontend: it lowers golang.org/x/tools/go/ssa
// into ir.Prog over the GoSSA dialect.
//
// Stage A subset: integers of every width, bools, read-only slices of
// integers, calls between modelled functions in the same package. Phi nodes
// become block parameters; `&xs[i]` followed by `*` becomes a `load`;
// anything else that produces a value is havoc (function marked partial) or
// rejects the function.
package gossa

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"hash/fnv"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"regexp/syntax"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/andreasfoo/go2lean/internal/contract"
	"github.com/andreasfoo/go2lean/internal/ir"
	"github.com/andreasfoo/go2lean/internal/ssaout"
)

const Frontend = "gossa"

var specRe = regexp.MustCompile(`go2lean:spec\s+([\w.]+)`)

// maxLenRe is a spec's `go2lean:maxlen N`: its property searches inputs
// up to N long instead of the run's -maxlen.
var maxLenRe = regexp.MustCompile(`go2lean:maxlen\s+(\d+)`)

// seqRe marks a sequence spec: `go2lean:sequence Target` on a function
// returning bool that drives Target over its input events.
var seqRe = regexp.MustCompile(`go2lean:sequence\s+([\w.()*]+)`)

// Translate loads the single package matching pattern (relative to dir) and
// lowers every top-level function. It returns the program and the Go module
// path.
func Translate(dir, pattern string) (*ir.Prog, string, error) {
	progs, modPath, err := TranslateAll(dir, pattern)
	if err != nil {
		return nil, "", err
	}
	if len(progs) != 1 {
		return nil, "", fmt.Errorf("pattern %s matched %d packages; expected exactly one", pattern, len(progs))
	}
	return progs[0], modPath, nil
}

// TranslateAll loads every package matching the patterns in one go (one
// type-check of the dependency graph, which dominates the cost on real
// modules) and lowers each. Packages that fail to load are skipped with
// their errors reported through the returned map, so one broken package
// does not stop a sweep.
func TranslateAll(dir string, patterns ...string) ([]*ir.Prog, string, error) {
	return TranslateAllOpts(dir, Options{}, patterns...)
}

// Options tune TranslateAll.
type Options struct {
	// Deps also models the functions of dependency packages (outside the
	// standard library) that the compiled packages reach through static
	// calls, as small extra programs the search driver links in; without
	// it those calls are unmodelled (havoc results).
	Deps bool
	// Overlay adds or replaces files of the loaded packages (absolute path
	// to contents): generated specs that are not in the package directory.
	Overlay map[string][]byte
	// Tests makes the package's test functions (TestXxx(t *testing.T)) targets:
	// t is an opaque value in the model and the test's own t in M3. For
	// kernels written as tests, such as GoKer's.
	Tests bool
	// Translate names dependency functions (as ssa prints them:
	// pkg/path.Func, (*pkg/path.T).Method) to model with Deps even past
	// the walk's depth and count limits: the refinement loop's callees
	// whose havoc made a counterexample spurious (#85).
	Translate []string
}

// Limits of the dependency walk: how deep it follows calls from the
// compiled packages, and how many functions it models at most.
const (
	depDepth = 4
	depLimit = 1500
)

// TranslateAllOpts is TranslateAll with options.
func TranslateAllOpts(dir string, opts Options, patterns ...string) ([]*ir.Prog, string, error) {
	// Tests: true also loads the package's internal test variant
	// ("path [path.test]"), which includes *_test.go files of the same
	// package: that is where agent-written spec-in-Go functions live.
	cfg := &packages.Config{Mode: packages.LoadAllSyntax | packages.NeedModule, Dir: dir, Tests: true, Overlay: opts.Overlay}
	loaded, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, "", err
	}
	if errs := loadErrors(loaded); len(errs) > 0 {
		return nil, "", fmt.Errorf("packages %s do not load:\n  %s", strings.Join(patterns, " "), strings.Join(errs, "\n  "))
	}
	pkgs := selectVariant(loaded, opts.Tests)
	if len(pkgs) == 0 {
		return nil, "", fmt.Errorf("patterns %s matched no packages", strings.Join(patterns, " "))
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].PkgPath < pkgs[j].PkgPath })
	build := ssautil.Packages
	if opts.Deps {
		// Bodies for the dependencies too: Packages creates those from type
		// information only.
		build = ssautil.AllPackages
	}
	sprog, spkgs := build(pkgs, ssa.InstantiateGenerics)
	sprog.Build()
	zeros := zeroGlobals(sprog, spkgs, opts.Deps)

	modPath := ""
	if pkgs[0].Module != nil {
		modPath = pkgs[0].Module.Path
	}
	// Cross-package references: every candidate function of every package
	// of this compile, so a call into a sibling package links to its model
	// instead of becoming a havoc value.
	// Keyed by name: a package compiled as its test variant holds other
	// function objects than the plain variant its importers call.
	xrefs := map[string]contract.XCall{}
	var units []unit
	mainPaths := map[string]bool{}
	var roots []*ssa.Function
	for i, spkg := range spkgs {
		if spkg == nil {
			continue
		}
		fns := candidates(spkg, opts.Tests)
		units = append(units, unit{pkgs[i].PkgPath, fns})
		mainPaths[pkgs[i].PkgPath] = true
		roots = append(roots, fns...)
	}
	if opts.Deps {
		deps := depClosure(roots, mainPaths, zeros, invokedNames(sprog), opts.Translate)
		var paths []string
		for path := range deps {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			fns := deps[path]
			sort.Slice(fns, func(i, j int) bool { return funcName(fns[i]) < funcName(fns[j]) })
			if mainPaths[path] {
				// Generic instances of a compiled package: after its own
				// functions, so their FuncIds do not move.
				for k := range units {
					if units[k].path == path {
						units[k].fns = append(units[k].fns, fns...)
					}
				}
				continue
			}
			units = append(units, unit{path, fns})
		}
	}
	// Anonymous functions after the rest, so calls through closures can
	// follow them; their FuncIds come last and move nothing.
	for k := range units {
		units[k].fns = withAnon(withPoolNews(withAnon(units[k].fns)))
	}
	// Which package variables are cells: one decision for every linked
	// package (#55).
	plan := planCells(units, mainPaths)
	for k := range units {
		if plan.concurrent[units[k].path] {
			units[k].fns = withAnon(withInitCallees(units[k].path, units[k].fns, plan.initUsed()))
		}
	}
	plan.collectInits(units)
	// The types modelled code boxes into interfaces: the dynamic types an
	// interface value the model built can have.
	var boxed []types.Type
	seenBoxed := map[string]bool{}
	for _, u := range units {
		for _, f := range u.fns {
			for _, b := range f.Blocks {
				for _, in := range b.Instrs {
					if mi, ok := in.(*ssa.MakeInterface); ok {
						if k := types.TypeString(mi.X.Type(), nil); !seenBoxed[k] {
							seenBoxed[k] = true
							boxed = append(boxed, mi.X.Type())
						}
					}
				}
			}
		}
	}
	for _, u := range units {
		for id, f := range u.fns {
			xrefs[f.String()] = contract.XCall{Package: u.path, ID: id}
		}
	}
	plan.dropUntranslatable(units, xrefs, zeros, sprog, boxed)
	var progs []*ir.Prog
	for _, u := range units {
		p, err := translatePackage(u.path, u.fns, xrefs, zeros, sprog, boxed, plan)
		if err != nil {
			return nil, "", err
		}
		p.Dependency = !mainPaths[u.path]
		if !p.Dependency {
			addCtorWrappers(p, u.fns)
		}
		progs = append(progs, p)
	}
	return progs, modPath, nil
}

// depClosure walks the static calls of roots into packages outside the
// compile and the standard library, up to depDepth calls deep and depLimit
// functions, and returns the functions reached by package path. Besides
// static calls it follows functions used as values, the function a
// package-level variable holds from its initializer (zeros), and the bodies
// of closures (added to their package by withAnon). A call of a small
// helper within a dependency package stays at its caller's depth. Generic functions and
// wrappers are left out (they stay unmodelled). The walk is breadth-first,
// so a function's depth is that of its shortest call path from a root
// and does not depend on which root reached it first. A function named in
// force is modelled whatever its depth and the limit, and walked from at
// its caller's depth.
func depClosure(roots []*ssa.Function, mainPaths map[string]bool, zeros map[*ssa.Global]ssa.Value, invoked map[string]bool, force []string) map[string][]*ssa.Function {
	out := map[string][]*ssa.Function{}
	seen := map[*ssa.Function]bool{}
	count := 0
	// next holds the functions to walk one call deeper than the current
	// level; a modelled standard-library helper stays on the current one.
	var next []*ssa.Function
	var visit func(f *ssa.Function)
	visit = func(f *ssa.Function) {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				var refs []*ssa.Function
				for _, op := range in.Operands(nil) {
					switch v := (*op).(type) {
					case *ssa.Function:
						refs = append(refs, v)
					case *ssa.Global:
						if fn, ok := zeros[v].(*ssa.Function); ok {
							refs = append(refs, fn)
						}
					}
				}
				// fmt.Fprintf into a *bytes.Buffer is lowered to its
				// WriteString (fprintf).
				if c, ok := in.(*ssa.Call); ok && len(c.Call.Args) == 3 {
					if fn := c.Call.StaticCallee(); fn != nil && fn.String() == "fmt.Fprintf" {
						if mi, ok := c.Call.Args[0].(*ssa.MakeInterface); ok {
							if pt, ok := mi.X.Type().(*types.Pointer); ok {
								if n, ok := pt.Elem().(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "bytes" && n.Obj().Name() == "Buffer" {
									if m := f.Prog.LookupMethod(pt, n.Obj().Pkg(), "WriteString"); m != nil {
										refs = append(refs, m)
									}
								}
							}
						}
					}
				}
				// A boxed value: the methods of its type some call on an
				// interface may reach.
				if mi, ok := in.(*ssa.MakeInterface); ok {
					ms := f.Prog.MethodSets.MethodSet(mi.X.Type())
					for i := 0; i < ms.Len(); i++ {
						if invoked[ms.At(i).Obj().Name()] {
							if fn := f.Prog.MethodValue(ms.At(i)); fn != nil {
								refs = append(refs, fn)
							}
						}
					}
				}
				for _, callee := range refs {
					if callee.Parent() != nil {
						// A closure: walk its body as part of its parent's.
						if !seen[callee] {
							seen[callee] = true
							visit(callee)
						}
						continue
					}
					// Instances of generic functions (SystemMessage[string]) have
					// bodies with concrete types under InstantiateGenerics; their
					// package is that of the generic origin.
					instance := len(callee.TypeArgs()) > 0 && callee.Origin() != nil
					if seen[callee] || (callee.Synthetic != "" && !instance) ||
						callee.Parent() != nil || (callee.TypeParams().Len() > 0 && !instance) ||
						(isInit(callee.Name()) && callee.Signature.Recv() == nil) || len(callee.Blocks) == 0 {
						continue
					}
					pkg := callee.Pkg
					if instance {
						pkg = callee.Origin().Pkg
					}
					if pkg == nil {
						continue
					}
					path := pkg.Pkg.Path()
					// A compiled package's own functions are modelled already,
					// except instances of its generic functions.
					// The generic helpers of package slices (ContainsFunc,
					// IndexFunc) are plain loops over func values: modelled.
					if (mainPaths[path] && !instance) || (isStdlib(path) && !(instance && path == "slices") && !modelledStdlib[path] && !modelledFunc(callee.String())) {
						continue
					}
					if _, std := stdlibOp(callee.String()); std {
						continue
					}
					forced := slices.Contains(force, callee.String())
					if count >= depLimit && !forced {
						return
					}
					seen[callee] = true
					count++
					out[path] = append(out[path], callee)
					if forced || modelledStdlib[path] || modelledFunc(callee.String()) || (f.Pkg != nil && f.Pkg.Pkg.Path() == path && !mainPaths[path] && smallHelper(callee)) {
						// Within a modelled standard-library package, every
						// helper is needed (unescape calls ishex calls ...);
						// within a dependency, its small helpers (xxhash's
						// Sum64 calls round calls rol31) cost no depth (#71).
						visit(callee)
					} else {
						next = append(next, callee)
					}
				}
			}
		}
	}
	level := roots
	for depth := 1; depth <= depDepth && len(level) > 0; depth++ {
		next = nil
		for _, f := range level {
			visit(f)
		}
		level = next
	}
	// Past the depth, only the forced callees: each is modelled, its own
	// calls stay unmodelled unless forced too.
	for len(force) > 0 && len(level) > 0 {
		next = nil
		for _, f := range level {
			for _, b := range f.Blocks {
				for _, in := range b.Instrs {
					c, ok := in.(ssa.CallInstruction)
					if !ok {
						continue
					}
					callee := c.Common().StaticCallee()
					if callee == nil || seen[callee] || callee.Pkg == nil || len(callee.Blocks) == 0 || !slices.Contains(force, callee.String()) {
						continue
					}
					seen[callee] = true
					path := callee.Pkg.Pkg.Path()
					out[path] = append(out[path], callee)
					next = append(next, callee)
				}
			}
		}
		level = next
	}
	return out
}

// smallHelper says whether f is a small function (rol1, round, u64): few
// blocks and instructions, translated with its caller in a dependency
// whatever the depth, where a large one would bring in its package.
func smallHelper(f *ssa.Function) bool {
	if len(f.Blocks) > 4 {
		return false
	}
	n := 0
	for _, b := range f.Blocks {
		n += len(b.Instrs)
	}
	return n <= 40
}

// foldInit evaluates a package initializer's value when it is a constant
// expression over constants and globals initialised to constants before it
// (var mb = kb * 1024): the value the initializer stores, as a constant.
// Package variables are initialised before any init function or other code
// runs, so a later write to kb does not change mb. Arithmetic that would wrap in the global's type, or divide by zero, is
// left alone (the run-time value differs from the exact one).
// pureConstCall says whether c, a package variable's initializer, is a
// call of a function with constant arguments whose result is a plain value
// (no pointer, slice, map, channel, func or interface in it): var
// V0_11_0_0 = newKafkaVersion(0, 11, 0, 0). A load of the variable runs
// the call again, which gives the same value, and a value has no
// identity to keep (#55).
func pureConstCall(c *ssa.Call) bool {
	fn := c.Call.StaticCallee()
	if fn == nil || len(fn.Blocks) == 0 || fn.Signature.Results().Len() != 1 || !plainValue(fn.Signature.Results().At(0).Type(), 4) {
		return false
	}
	for _, a := range c.Call.Args {
		if _, ok := a.(*ssa.Const); !ok {
			return false
		}
	}
	return true
}

// plainValue says whether a value of type t holds no reference: booleans,
// numbers, strings, and arrays and structs of them (down to depth).
func plainValue(t types.Type, depth int) bool {
	if depth < 0 {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Kind() != types.UnsafePointer && u.Kind() != types.UntypedNil
	case *types.Array:
		return plainValue(u.Elem(), depth-1)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if !plainValue(u.Field(i).Type(), depth-1) {
				return false
			}
		}
		return true
	}
	return false
}

func foldInit(v ssa.Value, inits map[*ssa.Global]ssa.Value) (*ssa.Const, bool) {
	var eval func(v ssa.Value, depth int) (constant.Value, bool)
	eval = func(v ssa.Value, depth int) (constant.Value, bool) {
		if depth > 16 {
			return nil, false
		}
		var r constant.Value
		switch v := v.(type) {
		case *ssa.Const:
			if v.Value == nil {
				return nil, false
			}
			r = v.Value
		case *ssa.UnOp:
			if g, ok := v.X.(*ssa.Global); ok && v.Op == token.MUL {
				c, ok := inits[g].(*ssa.Const)
				if !ok || c.Value == nil {
					return nil, false
				}
				r = c.Value
				break
			}
			if v.Op != token.SUB && v.Op != token.XOR {
				return nil, false
			}
			x, ok := eval(v.X, depth+1)
			if !ok {
				return nil, false
			}
			r = constant.UnaryOp(v.Op, x, 0)
		case *ssa.BinOp:
			x, ok := eval(v.X, depth+1)
			if !ok {
				return nil, false
			}
			y, ok := eval(v.Y, depth+1)
			if !ok {
				return nil, false
			}
			switch v.Op {
			case token.ADD, token.SUB, token.MUL, token.AND, token.OR, token.XOR, token.AND_NOT:
				r = constant.BinaryOp(x, v.Op, y)
			case token.QUO, token.REM:
				if y.Kind() != constant.Int || constant.Sign(y) == 0 {
					return nil, false
				}
				op := v.Op
				if op == token.QUO {
					op = token.QUO_ASSIGN // integer division
				}
				r = constant.BinaryOp(x, op, y)
			case token.SHL, token.SHR:
				n, ok := constant.Uint64Val(y)
				if !ok || n > 64 {
					return nil, false
				}
				r = constant.Shift(x, v.Op, uint(n))
			default:
				return nil, false
			}
		default:
			return nil, false
		}
		b, ok := v.Type().Underlying().(*types.Basic)
		if !ok || b.Info()&(types.IsInteger|types.IsString|types.IsBoolean) == 0 {
			return nil, false
		}
		if b.Info()&types.IsInteger != 0 && !representable(r, b) {
			return nil, false
		}
		return r, true
	}
	switch v.(type) {
	case *ssa.BinOp, *ssa.UnOp:
	default:
		return nil, false
	}
	r, ok := eval(v, 0)
	if !ok {
		return nil, false
	}
	return ssa.NewConst(r, v.Type()), true
}

// representable says whether the integer constant c fits the basic type b.
func representable(c constant.Value, b *types.Basic) bool {
	if c.Kind() != constant.Int {
		return false
	}
	size := map[types.BasicKind]uint{types.Int8: 8, types.Int16: 16, types.Int32: 32, types.Uint8: 8, types.Uint16: 16, types.Uint32: 32}[b.Kind()]
	if size == 0 {
		size = 64
	}
	if b.Info()&types.IsUnsigned != 0 {
		if constant.Sign(c) < 0 {
			return false
		}
		u, ok := constant.Uint64Val(c)
		return ok && (size == 64 || u < 1<<size)
	}
	i, ok := constant.Int64Val(c)
	return ok && (size == 64 || (i >= -(1<<(size-1)) && i < 1<<(size-1)))
}

// zeroGlobals returns the package-level variables whose value a load can
// know: a variable no code writes holds its zero value (nil in the map), one
// the package initializer sets to a constant or a function, or to a map it
// fills with constants only (a lookup table, the *ssa.MakeMap: each load
// rebuilds it) that every load only reads, and nothing else
// writes holds that value. Writes from test helpers (functions named
// ...ForTest or ...ForTesting, and their closures) do not count. With bodies
// for every package (deps) that covers every variable; otherwise only the
// unexported ones of the compiled packages, which no other package can
// reach. A store an init function makes on every path through it (see
// initStore) counts as the initializer's, when the initializer does not
// set the variable too: init functions run after the variable
// initializers and before any other code.
func zeroGlobals(sprog *ssa.Program, spkgs []*ssa.Package, deps bool) map[*ssa.Global]ssa.Value {
	touched := map[*ssa.Global]bool{}
	inits := map[*ssa.Global]ssa.Value{}
	loads := map[*ssa.Global][]*ssa.UnOp{}
	alias := map[*ssa.Global]*ssa.Global{} // var B = A, set by an initializer
	var userStores []*ssa.Store            // made by init functions, on every path
	for f := range ssautil.AllFunctions(sprog) {
		if testHelper(f) {
			continue
		}
		pkgInit := f.Name() == "init" && f.Synthetic != ""
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if u, ok := in.(*ssa.UnOp); ok && u.Op == token.MUL {
					if g, ok := u.X.(*ssa.Global); ok {
						loads[g] = append(loads[g], u)
					}
					continue
				}
				if st, ok := in.(*ssa.Store); ok && userInit(f) && initStore(b) {
					if g, ok := st.Addr.(*ssa.Global); ok && g.Pkg == f.Pkg {
						userStores = append(userStores, st)
						// Its operands are read after the initializers ran.
						if _, ok := st.Val.(*ssa.Global); ok {
							touched[st.Val.(*ssa.Global)] = true
						}
						continue
					}
				}
				if st, ok := in.(*ssa.Store); ok && pkgInit {
					if g, ok := st.Addr.(*ssa.Global); ok && g.Pkg == f.Pkg {
						known := false
						if c, ok := foldInit(st.Val, inits); ok {
							st := *st // the folded value, not the instructions computing it
							st.Val = c
							in = &st
						}
						switch v := in.(*ssa.Store).Val.(type) {
						case *ssa.Const, *ssa.Function:
							known = true
						case *ssa.MakeMap:
							known = constMapEntries(v) != nil
						case *ssa.Slice, *ssa.Convert:
							_, known = constSlice(v, inits)
						case *ssa.Call:
							_, known = constSlice(v, inits)
							known = known || pureConstCall(v)
						case *ssa.UnOp:
							if a, ok := v.X.(*ssa.Global); ok && v.Op == token.MUL && alias[g] == nil {
								alias[g] = a
								continue
							}
						}
						if known {
							if _, twice := inits[g]; twice {
								touched[g] = true
							}
							inits[g] = in.(*ssa.Store).Val
							continue
						}
					}
				}
				for _, op := range in.Operands(nil) {
					if g, ok := (*op).(*ssa.Global); ok {
						touched[g] = true
					}
				}
			}
		}
	}
	// Stores of init functions, after every initializer: a variable the
	// initializer sets as well, or two init stores set, is written twice.
	initSet := map[*ssa.Global]int{}
	for _, st := range userStores {
		initSet[st.Addr.(*ssa.Global)]++
	}
	for _, st := range userStores {
		g := st.Addr.(*ssa.Global)
		if _, ok := inits[g]; ok || initSet[g] > 1 || alias[g] != nil {
			touched[g] = true
			continue
		}
		val := st.Val
		if c, ok := foldInit(val, inits); ok {
			val = c
		}
		switch v := val.(type) {
		case *ssa.Const, *ssa.Function:
			inits[g] = val
			continue
		case *ssa.MakeMap:
			if constMapEntries(v) != nil {
				inits[g] = val
				continue
			}
		}
		touched[g] = true
	}
	// An alias of a lookup table (var B = A) is the same table; one that
	// another initializer also writes is not.
	for b, a := range alias {
		_, table := inits[a].(*ssa.MakeMap)
		call, _ := inits[a].(*ssa.Call)
		if table || call != nil && pureConstCall(call) {
			if _, twice := inits[b]; twice {
				touched[b] = true
			}
			inits[b] = inits[a]
		} else {
			touched[b] = true
		}
	}
	// A constant slice stays one while no code writes an element of it
	// (each load builds a fresh copy; see sliceOf).
	for g, init := range inits {
		if _, ok := constSlice(init, inits); !ok {
			continue
		}
		for _, u := range loads[g] {
			for _, r := range *u.Referrers() {
				if ia, ok := r.(*ssa.IndexAddr); ok && !addrRead(ia) {
					touched[g] = true
				}
			}
		}
	}
	// A lookup table stays one only while every load just reads it (or
	// sets an alias).
	for g, init := range inits {
		if _, ok := init.(*ssa.MakeMap); !ok {
			continue
		}
		for _, u := range loads[g] {
			for _, r := range *u.Referrers() {
				switch r := r.(type) {
				case *ssa.Lookup, *ssa.Range:
				case *ssa.Store:
					if b, ok := r.Addr.(*ssa.Global); !ok || alias[b] != g {
						touched[g] = true
					}
				case *ssa.Call:
					if b, ok := r.Call.Value.(*ssa.Builtin); !ok || b.Name() != "len" {
						touched[g] = true
					}
				default:
					touched[g] = true
				}
			}
		}
	}
	// A table and its aliases share one map: a write through one spoils all.
	for changed := true; changed; {
		changed = false
		for b, a := range alias {
			if _, table := inits[a].(*ssa.MakeMap); table && touched[a] != touched[b] {
				touched[a], touched[b], changed = true, true, true
			}
		}
	}
	compiled := map[*ssa.Package]bool{}
	for _, p := range spkgs {
		if p != nil {
			compiled[p] = true
		}
	}
	out := map[*ssa.Global]ssa.Value{}
	for _, p := range sprog.AllPackages() {
		if !deps && !compiled[p] {
			continue
		}
		for _, m := range p.Members {
			g, ok := m.(*ssa.Global)
			if !ok || touched[g] || (!deps && g.Object() != nil && g.Object().Exported()) {
				continue
			}
			out[g] = inits[g]
		}
	}
	return out
}

// constMapEntries returns the updates that fill the map mm makes, when
// they all put a constant (or a package function) under a constant key and mm is used for nothing
// else but being stored; nil otherwise.
func constMapEntries(mm *ssa.MakeMap) []*ssa.MapUpdate {
	var ups []*ssa.MapUpdate
	for _, r := range *mm.Referrers() {
		switch r := r.(type) {
		case *ssa.MapUpdate:
			_, kc := r.Key.(*ssa.Const)
			_, vc := r.Value.(*ssa.Const)
			if fn, ok := tableFunc(r.Value); ok && len(fn.FreeVars) == 0 {
				vc = true // a table of functions (an operator table)
			}
			if !kc || !vc {
				return nil
			}
			ups = append(ups, r)
		case *ssa.Store:
		default:
			return nil
		}
	}
	if ups == nil {
		return []*ssa.MapUpdate{}
	}
	return ups
}

// tableFunc is the package function a table entry holds: the function
// itself, or converted to a named func type (map[string]cfunc{"": f}).
func tableFunc(v ssa.Value) (*ssa.Function, bool) {
	if ct, ok := v.(*ssa.ChangeType); ok {
		v = ct.X
	}
	fn, ok := v.(*ssa.Function)
	return fn, ok
}

// testHelper says whether f exists for tests only: named ...ForTest or
// ...ForTesting, or a closure inside one.
func testHelper(f *ssa.Function) bool {
	for f.Parent() != nil {
		f = f.Parent()
	}
	return strings.HasSuffix(f.Name(), "ForTest") || strings.HasSuffix(f.Name(), "ForTesting")
}

// addCtorWrappers adds, for each method of a type T whose package has a
// constructor (NewT or newT returning T or *T, possibly with an error), a
// function T.M~NewT that builds the receiver with the constructor and calls
// the method on it. The method is then targeted through the wrapper: a
// receiver no constructor made (nil maps, nil collaborators) is a misuse,
// not a bug. Wrappers go after every other function, so no FuncId moves.
func addCtorWrappers(p *ir.Prog, fns []*ssa.Function) {
	ids := map[*ssa.Function]int{}
	ctors := map[*types.TypeName]*ssa.Function{}
	for i, f := range fns {
		ids[f] = i
	}
	for _, f := range fns {
		if f.Signature.Recv() != nil || f.Parent() != nil || p.Funcs[ids[f]].Status == contract.ModelRejected {
			continue
		}
		if tn, ok := ctorOf(f); ok {
			if _, dup := ctors[tn]; !dup {
				ctors[tn] = f
			}
		}
	}
	for _, m := range fns {
		recv := m.Signature.Recv()
		if recv == nil || m.Parent() != nil || p.Funcs[ids[m]].Status == contract.ModelRejected || p.Funcs[ids[m]].SpecFor != "" {
			continue
		}
		rt := recv.Type()
		ptrRecv := false
		if pt, ok := rt.(*types.Pointer); ok {
			rt, ptrRecv = pt.Elem(), true
		}
		named, ok := rt.(*types.Named)
		if !ok {
			continue
		}
		ctor, ok := ctors[named.Obj()]
		if !ok {
			continue
		}
		// An unexported type is built by its constructor only: without a
		// wrapper the method is no target (a receiver made field by field
		// can hold states the constructor never makes).
		noWrapper := func() {
			if !named.Obj().Exported() {
				p.Funcs[ids[m]].CtorOnly = ctor.Name()
			}
		}
		if ctor.Signature.Variadic() {
			noWrapper()
			continue
		}
		_, ctorPtr := ctor.Signature.Results().At(0).Type().(*types.Pointer)
		if ptrRecv && !ctorPtr {
			noWrapper()
			continue // the constructor's value would need an address
		}
		w, err := ctorWrapper(p, ctor, m, ids[ctor], ids[m], ctorPtr && !ptrRecv)
		if err != nil {
			noWrapper()
			continue
		}
		w.TestFile = p.Funcs[ids[m]].TestFile || p.Funcs[ids[ctor]].TestFile
		// Wrappers come after markSync: the concurrent machine must give a
		// call of a synchronising method a frame of its own.
		w.Sync = p.Funcs[ids[m]].Sync || p.Funcs[ids[ctor]].Sync
		p.Funcs[ids[m]].WrappedBy = w.Name
		p.Funcs = append(p.Funcs, w)
	}
}

// ctorOf reports the type f constructs: a package-level function named
// New<T> or new<T> whose first result is T or *T for a named T of its own
// package, and whose other result, if any, is an error.
func ctorOf(f *ssa.Function) (*types.TypeName, bool) {
	res := f.Signature.Results()
	if res.Len() == 0 || res.Len() > 2 || (res.Len() == 2 && !isError(res.At(1).Type())) {
		return nil, false
	}
	t := res.At(0).Type()
	if pt, ok := t.(*types.Pointer); ok {
		t = pt.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || f.Pkg == nil || named.Obj().Pkg() != f.Pkg.Pkg {
		return nil, false
	}
	// NewT or newT for T; for an unexported t also NewT and newT with its
	// first letter capitalised (newAnthropicBetaToResponsesConverter); and
	// New or new for the type named like its package (retrier.New makes a
	// *retrier.Retrier, #15).
	n := named.Obj().Name()
	upper := strings.ToUpper(n[:1]) + n[1:]
	if f.Name() == "New"+n || f.Name() == "new"+n || f.Name() == "New"+upper || f.Name() == "new"+upper {
		return named.Obj(), true
	}
	if (f.Name() == "New" || f.Name() == "new") && strings.EqualFold(n, f.Pkg.Pkg.Name()) {
		return named.Obj(), true
	}
	return nil, false
}

// ctorWrapper builds T.M~NewT: call the constructor on the first
// parameters, stop (returning unit) when it reports an error, call the
// method on its result and the remaining parameters. Either call getting
// stuck makes the run stuck (the wrapper exists to be checked, so an
// unmodelled callee must not read as "no panic"). deref loads the receiver
// from the constructor's pointer for a value receiver.
func ctorWrapper(p *ir.Prog, ctor, m *ssa.Function, ctorID, mID int, deref bool) (*ir.Func, error) {
	cabi, err := abiOf(ctor, nil)
	if err != nil {
		return nil, err
	}
	mabi, err := abiOf(m, nil)
	if err != nil {
		return nil, err
	}
	name := funcName(m) + "~" + ctor.Name()
	abi := *mabi
	abi.GoFunc = m.String()
	abi.GoSig = fmt.Sprintf("func%s via %s", strings.TrimPrefix(m.Signature.String(), "func"), ctor.Name())
	abi.Method = ""
	abi.Variadic = m.Signature.Variadic()
	errRes := ctor.Signature.Results().Len() == 2
	abi.Ctor = &contract.ABICtor{Func: ctor.Name(), N: len(cabi.Params), Err: errRes, Method: m.Name()}
	abi.Imports = map[string]string{}
	for k, v := range cabi.Imports {
		abi.Imports[k] = v
	}
	for k, v := range mabi.Imports {
		abi.Imports[k] = v
	}
	seenT := map[string]bool{}
	abi.Types = nil
	for _, t := range append(append([]contract.ABIType{}, cabi.Types...), mabi.Types...) {
		if !seenT[t.GoType] {
			seenT[t.GoType] = true
			abi.Types = append(abi.Types, t)
		}
	}
	// Constructor parameters, then the method's without the receiver; a
	// method parameter named like a constructor one is renamed.
	taken := map[string]bool{}
	abi.Params = nil
	for _, prm := range cabi.Params {
		taken[prm.Go] = true
		abi.Params = append(abi.Params, prm)
	}
	for _, prm := range mabi.Params[1:] {
		for taken[prm.Go] {
			prm.Go += "_"
			prm.Model = append([]string{prm.Go}, prm.Model[1:]...)
		}
		taken[prm.Go] = true
		abi.Params = append(abi.Params, prm)
	}
	nc, nm := len(ctor.Params), len(m.Params)-1
	w := &ir.Func{Name: name, GoSig: abi.GoSig, ABI: abi, Status: contract.ModelComplete, Calls: []int{ctorID, mID}}
	v := 0
	fresh := func() ir.Var { v++; return ir.Var(v - 1) }
	for i := 0; i < nc+nm; i++ {
		w.Params = append(w.Params, fresh())
	}
	r, res, unit := fresh(), fresh(), fresh()
	// Blocks: 0 call ctor; 1 receiver (and error check); 2 call method;
	// 3 return the method's result; 4 return unit; 5 stuck.
	w.Blocks = make([]ir.Block, 6)
	w.Blocks[0] = ir.Block{Name: "ctor", Term: ir.Term{Kind: ir.TermCall, Callee: ctorID, CArgs: w.Params[:nc], Dst: r, Next: 1, Fallback: 5}}
	recv := r
	b1 := ir.Block{Name: "recv"}
	if errRes {
		recv = fresh()
		e, nilErr, isNil := fresh(), fresh(), fresh()
		b1.Instrs = append(b1.Instrs,
			ir.Instr{Dst: recv, Op: ir.Extract{I: 0}, Args: []ir.Var{r}},
			ir.Instr{Dst: e, Op: ir.Extract{I: 1}, Args: []ir.Var{r}},
			ir.Instr{Dst: nilErr, Op: ir.Const{V: ir.ErrVal{}}},
			ir.Instr{Dst: isNil, Op: ir.Simple("eq"), Args: []ir.Var{e, nilErr}})
		b1.Term = ir.Term{Kind: ir.TermBr, Cond: isNil, T: 2, F: 4}
	} else {
		b1.Term = ir.Term{Kind: ir.TermJump, B: 2}
	}
	// A value receiver is read only once the error is known to be nil: a
	// constructor that fails returns a nil pointer with its error.
	var b2 []ir.Instr
	if deref {
		val := fresh()
		b2 = append(b2, ir.Instr{Dst: val, Op: ir.Simple("loadPath"), Args: []ir.Var{recv}})
		recv = val
	}
	w.Blocks[1] = b1
	w.Blocks[2] = ir.Block{Name: "method", Instrs: b2, Term: ir.Term{Kind: ir.TermCall, Callee: mID, CArgs: append([]ir.Var{recv}, w.Params[nc:]...), Dst: res, Next: 3, Fallback: 5}}
	w.Blocks[3] = ir.Block{Name: "ret", Term: ir.Term{Kind: ir.TermRet, V: res}}
	w.Blocks[4] = ir.Block{Name: "ctor-error", Instrs: []ir.Instr{{Dst: unit, Op: ir.Const{V: ir.UnitVal{}}}}, Term: ir.Term{Kind: ir.TermRet, V: unit}}
	w.Blocks[5] = ir.Block{Name: "stuck", Term: ir.Term{Kind: ir.TermPanic, Panic: contract.PanicOther}}
	w.NVals = v
	return w, nil
}

// inTestFile says whether f (or the function enclosing a closure) is
// declared in a _test.go file.
func inTestFile(f *ssa.Function) bool {
	for f.Parent() != nil {
		f = f.Parent()
	}
	if f.Prog == nil || !f.Pos().IsValid() {
		return false
	}
	return strings.HasSuffix(f.Prog.Fset.Position(f.Pos()).Filename, "_test.go")
}

// withAnon appends the anonymous functions of fns (and theirs, nested)
// that are not in fns yet.
func withAnon(fns []*ssa.Function) []*ssa.Function {
	seen := map[*ssa.Function]bool{}
	for _, f := range fns {
		seen[f] = true
	}
	out := append([]*ssa.Function(nil), fns...)
	for i := 0; i < len(out); i++ {
		for _, a := range out[i].AnonFuncs {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

// invokedNames returns the names of the methods called on interface values
// anywhere in the program.
func invokedNames(sprog *ssa.Program) map[string]bool {
	out := map[string]bool{}
	for f := range ssautil.AllFunctions(sprog) {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if c, ok := in.(ssa.CallInstruction); ok && c.Common().IsInvoke() {
					out[c.Common().Method.Name()] = true
				}
			}
		}
	}
	return out
}

// bytealgCall lowers the assembly primitives of internal/bytealg (functions
// without a Go body) that the standard library's string and byte code
// bottoms out in: the only hand-written part of strings, bytes and strconv
// when those are translated from source. The []byte forms read the bytes
// as a string. The runtime string comparator uses the same unsigned byte
// ordering as Go's string operators; it returns exactly -1, 0 or 1.
func (t *fnState) bytealgCall(x *ssa.Call, callee *ssa.Function) (bool, error) {
	if callee.Pkg == nil || callee.Pkg.Pkg.Path() != "internal/bytealg" || len(callee.Blocks) > 0 {
		return false, nil
	}
	if callee.Name() == "abigen_runtime_cmpstring" && len(x.Call.Args) == 2 &&
		isString(x.Call.Args[0].Type()) && isString(x.Call.Args[1].Type()) {
		a, err := t.val(x.Call.Args[0])
		if err != nil {
			return true, err
		}
		b, err := t.val(x.Call.Args[1])
		if err != nil {
			return true, err
		}
		less, greater := t.fresh(), t.fresh()
		t.emit(ir.Instr{Dst: less, Op: ir.Simple("ult"), Args: []ir.Var{a, b}})
		t.emit(ir.Instr{Dst: greater, Op: ir.Simple("ugt"), Args: []ir.Var{a, b}})
		minus, zero, plus := t.fresh(), t.fresh(), t.fresh()
		t.emit(ir.Instr{Dst: minus, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "18446744073709551615"}}})
		t.emit(ir.Instr{Dst: zero, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "0"}}})
		t.emit(ir.Instr{Dst: plus, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "1"}}})
		nonLess := t.fresh()
		t.emit(ir.Instr{Dst: nonLess, Op: ir.Simple("ite"), Args: []ir.Var{greater, plus, zero}})
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("ite"), Args: []ir.Var{less, minus, nonLess}})
		return true, nil
	}
	ops := map[string]string{"IndexByteString": "strIndexByte", "IndexString": "strIndexOf", "CountString": "strCountByte",
		"IndexByte": "strIndexByte", "Index": "strIndexOf", "Count": "strCountByte"}
	if callee.Name() == "MakeNoZero" && len(x.Call.Args) == 1 {
		n, err := t.val(x.Call.Args[0])
		if err != nil {
			return true, err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.MakeSlice{Zero: ir.IntVal{Width: 8, Bits: "0"}}, Args: []ir.Var{n, n}})
		return true, nil
	}
	op, ok := ops[callee.Name()]
	if !ok || len(x.Call.Args) != 2 {
		return false, nil
	}
	var args []ir.Var
	for _, a := range x.Call.Args {
		v, err := t.val(a)
		if err != nil {
			return true, err
		}
		if isByteSlice(a.Type()) {
			s := t.fresh()
			t.emit(ir.Instr{Dst: s, Op: ir.Simple("bytesToStr"), Args: []ir.Var{v}})
			v = s
		}
		args = append(args, v)
	}
	t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple(op), Args: args})
	return true, nil
}

// stdlibOp is the hand-written model of a standard-library function, if
// it has one. GO2LEAN_NO_STDLIB_OPS (a comma-separated list of package
// paths) turns those of the listed packages off, so their functions are
// translated from source: to check whether a hand model is still needed.
func stdlibOp(name string) (ir.Op, bool) {
	op, ok := ir.StdlibOps[name]
	if !ok {
		return nil, false
	}
	if off := os.Getenv("GO2LEAN_NO_STDLIB_OPS"); off != "" {
		pkg := strings.TrimPrefix(name, "(*")
		pkg = strings.TrimPrefix(pkg, "(")
		if i := strings.LastIndex(pkg, "/"); i >= 0 {
			pkg = pkg[:i] + strings.SplitN(pkg[i:], ".", 2)[0]
		} else {
			pkg = strings.SplitN(pkg, ".", 2)[0]
		}
		for _, p := range strings.Split(off, ",") {
			if p == pkg {
				return nil, false
			}
		}
	}
	return op, true
}

// isStdlib says whether an import path belongs to the standard library
// (no dot in its first element).
// modelledStdlib lists standard-library packages whose functions are
// modelled from their Go source under -deps, like any dependency, instead
// of being unmodelled calls: their code only uses what the model covers.
var modelledStdlib = map[string]bool{
	// The pure-Go layers under them: internal/bytealg's assembly
	// primitives are lowered by bytealgCall, its Go functions translated.
	"internal/bytealg":     true,
	"internal/stringslite": true,
	"internal/strconv":     true,
	"math/bits":            true,
	"unicode":              true,
	"strings":              true,
	"bytes":                true,
	"strconv":              true,
	"net/url":              true,
	"unicode/utf8":         true,
	"unicode/utf16":        true,
	// Only the functions a package calls are translated: the varints,
	// not the reflection-based Read and Write.
	"encoding/binary": true,
	// Pure Go over pointers, slices and interfaces (#69).
	"container/list": true,
	"bufio":          true,
	"io":             true,
}

// modelledTime are the functions of package time translated from source
// (#59): the clock readings (Now, Since, Until), whose runtime hooks read
// the model's monotone clock (clockCall), and the comparisons and
// arithmetic on Time, which then compute what Go computes.
var modelledTime = map[string]bool{
	"time.Now": true, "time.Since": true, "time.Until": true, "time.subMono": true,
	"(time.Time).After": true, "(time.Time).Before": true, "(time.Time).Compare": true,
	"(time.Time).Equal": true, "(time.Time).Add": true, "(time.Time).Sub": true,
	"(time.Time).IsZero": true, "(*time.Time).sec": true, "(*time.Time).nsec": true,
	"(*time.Time).addSec": true, "(*time.Time).stripMono": true,
}

// modelledNet are the functions of package net translated from source
// (#122): net.Pipe, an in-memory connection over channels, a mutex and
// time.AfterFunc deadlines, and the host:port helpers, which are string
// code. The rest of net (sockets, the resolver) stays unmodelled.
var modelledNet = map[string]bool{
	"net.Pipe": true, "net.makePipeDeadline": true, "net.isClosedChan": true,
	"(*net.pipeDeadline).set": true, "(*net.pipeDeadline).wait": true,
	"(net.pipeAddr).Network": true, "(net.pipeAddr).String": true,
	"(*net.pipe).LocalAddr": true, "(*net.pipe).RemoteAddr": true,
	"(*net.pipe).Read": true, "(*net.pipe).read": true,
	"(*net.pipe).Write": true, "(*net.pipe).write": true,
	"(*net.pipe).SetDeadline": true, "(*net.pipe).SetReadDeadline": true,
	"(*net.pipe).SetWriteDeadline": true, "(*net.pipe).Close": true,
	"(*net.OpError).Unwrap": true,
	"net.SplitHostPort":     true, "net.JoinHostPort": true, "(*net.AddrError).Error": true,
}

// modelledFunc says whether a function of a standard-library package that
// is not modelled as a whole is translated from source on its own.
func modelledFunc(name string) bool { return modelledTime[name] || modelledNet[name] }

func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// candidates picks the functions a package's model holds (package-level
// functions and the methods of the package's named types), in the order of
// their FuncIds. A generic function is held per instance (Map[int], with
// concrete types), not as itself: its body has type parameters no input
// and no harness can stand for.
func candidates(spkg *ssa.Package, tests bool) []*ssa.Function {
	var fns []*ssa.Function
	seen := map[*ssa.Function]bool{}
	add := func(f *ssa.Function) {
		if f != nil && !seen[f] && f.Synthetic == "" && !(isInit(f.Name()) && f.Signature.Recv() == nil) && (!isTestFunc(f.Name()) || tests && isTestEntry(f)) && f.TypeParams().Len() == 0 {
			seen[f] = true
			fns = append(fns, f)
		}
	}
	for _, m := range spkg.Members {
		switch m := m.(type) {
		case *ssa.Function:
			add(m)
		case *ssa.Type:
			for _, typ := range []types.Type{m.Type(), types.NewPointer(m.Type())} {
				ms := spkg.Prog.MethodSets.MethodSet(typ)
				for i := 0; i < ms.Len(); i++ {
					add(spkg.Prog.MethodValue(ms.At(i)))
				}
			}
		}
	}
	sort.Slice(fns, func(i, j int) bool { return funcName(fns[i]) < funcName(fns[j]) })
	return fns
}

func translatePackage(pkgPath string, fns []*ssa.Function, xrefs map[string]contract.XCall, zeros map[*ssa.Global]ssa.Value, sprog *ssa.Program, boxed []types.Type, plan *cellPlan) (*ir.Prog, error) {
	st := newProgState(pkgPath, fns, xrefs, zeros, sprog, boxed, plan)
	p := st.prog

	var initFn *ssa.Function
	var keep map[ssa.Instruction]bool
	if plan.concurrent[pkgPath] {
		initFn, keep = initCells(pkgPath, fns, plan.initUsed())
	}
	// The initializer's slice. Its variables whose part of it does not
	// translate are not cells (dropUntranslatable); should the rest still
	// fail, its stub gets stuck, and the search skips it and says so.
	// The variable initializers first, then each func init() the slice is
	// in, in Go's order.
	if initFn != nil {
		for _, f := range initFuncs(initFn) {
			in := false
			for k := range keep {
				if k.Parent() == f {
					in = true
					break
				}
			}
			if !in {
				continue
			}
			id := len(p.Funcs)
			out := &ir.Func{Name: f.Name(), GoSig: goSig(f), Init: true}
			p.Funcs = append(p.Funcs, out)
			t := &fnState{prog: st, f: f, out: out, vars: map[ssa.Value]ir.Var{}, blockID: map[*ssa.BasicBlock]int{}, keep: keep}
			if err := t.translate(); err != nil {
				rejectStub(out, err)
			} else {
				out.Sync = t.sync
			}
			p.Inits = append(p.Inits, id)
		}
	}
	// Pass 2: bodies.
	for _, f := range fns {
		out := p.Funcs[st.ids[f]]
		t := &fnState{prog: st, f: f, out: out, vars: map[ssa.Value]ir.Var{}, blockID: map[*ssa.BasicBlock]int{}}
		if err := t.translate(); err != nil {
			rejectStub(out, err)
		} else {
			out.Sync = t.sync
		}
	}
	markSync(p)
	for _, f := range p.Funcs {
		if f.Sync {
			p.SchedSite, p.HasSched = st.newHavoc(64, "schedule", "schedule", ""), true
			break
		}
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("internal error: generated program is malformed: %w", err)
	}
	p.JSONTable = st.json.lean(st.boxed)
	return p, nil
}

// markSync marks the functions that can reach a goroutine start or a
// synchronisation operation: the ones with one in their body, their callers
// in the package, and, when the package has any, every function that calls
// through a func value (its target may be one).
func markSync(p *ir.Prog) {
	any := false
	for _, f := range p.Funcs {
		any = any || f.Sync
	}
	if !any {
		return
	}
	for changed := true; changed; {
		changed = false
		for _, f := range p.Funcs {
			if f.Sync {
				continue
			}
			for _, c := range f.Calls {
				if c >= 0 && c < len(p.Funcs) && p.Funcs[c].Sync {
					f.Sync = true
				}
			}
			for _, b := range f.Blocks {
				if b.Term.Kind == ir.TermCallV {
					f.Sync = true
				}
			}
			changed = changed || f.Sync
		}
	}
}

// goSig renders a signature with its receiver, as the agent should read it.
func goSig(f *ssa.Function) string {
	if recv := f.Signature.Recv(); recv != nil {
		return "func (" + recv.Name() + " " + recv.Type().String() + ") " + f.Name() + strings.TrimPrefix(f.Signature.String(), "func")
	}
	return f.Signature.String()
}

// funcName is the model name of a function: its Go name, or Type.Method
// for a method (the same for value and pointer receivers).
func funcName(f *ssa.Function) string {
	if recv := f.Signature.Recv(); recv != nil {
		t := recv.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := t.(*types.Named); ok {
			return n.Obj().Name() + "." + f.Name()
		}
	}
	return f.Name()
}

// loadErrors collects the first few type/load errors of the packages (and
// their dependencies), so a broken module graph is reported with its cause
// rather than as "has errors".
func loadErrors(loaded []*packages.Package) []string {
	var errs []string
	seen := map[string]bool{}
	packages.Visit(loaded, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			msg := e.Error()
			if seen[msg] || len(errs) >= 8 {
				continue
			}
			seen[msg] = true
			errs = append(errs, msg)
		}
	})
	return errs
}

// selectVariant keeps the internal test variant of each package when one was
// loaded, else the plain package; test mains are dropped, and external test
// packages (package x_test) too unless their tests are targets.
func selectVariant(loaded []*packages.Package, xtests bool) []*packages.Package {
	best := map[string]*packages.Package{}
	for _, p := range loaded {
		if strings.HasSuffix(p.PkgPath, ".test") || (strings.HasSuffix(p.PkgPath, "_test") && !xtests) {
			continue
		}
		isTestVariant := strings.HasSuffix(p.ID, ".test]")
		if cur, ok := best[p.PkgPath]; !ok || (isTestVariant && !strings.HasSuffix(cur.ID, ".test]")) {
			best[p.PkgPath] = p
		}
	}
	var out []*packages.Package
	for _, p := range best {
		out = append(out, p)
	}
	return out
}

// isInit matches the package initialiser and the per-file init#N functions
// go/ssa synthesises for `func init()` bodies.
func isInit(name string) bool {
	return name == "init" || strings.HasPrefix(name, "init#")
}

// isTestEntry says whether f is a test function, TestXxx(t *testing.T).
func isTestEntry(f *ssa.Function) bool {
	sig := f.Signature
	return strings.HasPrefix(f.Name(), "Test") && f.Parent() == nil && sig.Recv() == nil &&
		sig.Params().Len() == 1 && sig.Results().Len() == 0 && isTestingType(sig.Params().At(0).Type())
}

func isTestFunc(name string) bool {
	return strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") ||
		strings.HasPrefix(name, "Fuzz") || strings.HasPrefix(name, "Example")
}

// progState is per-program translation state.
type progState struct {
	prog    *ir.Prog
	ids     map[*ssa.Function]int
	xrefs   map[string]contract.XCall // functions of sibling packages, by name
	specFor func(*ssa.Function) string
	// zeros holds the package-level variables nothing writes: a load of
	// one is its zero value.
	zeros map[*ssa.Global]ssa.Value
	sprog *ssa.Program
	// boxed lists the types modelled code boxes into interfaces.
	boxed []types.Type
	// notComparable: the tags of the boxed types == does not apply to.
	notComparable []uint32
	// json describes the types the package encodes or decodes.
	json *jsonTable
	// helpers are the synthetic functions of the program (conc.go), by name.
	helpers map[string]int
	// sharedGlobals: the package variables of a package that starts
	// goroutines that its functions (not its init) write (cellGlobal).
	sharedGlobals map[*ssa.Global]bool
	// initCells: the package variables of the package that are cells
	// starting at their zero value, set by the slice of the package
	// initializer the model runs before a search (#55).
	initCells map[*ssa.Global]bool
	// regexps caches packageRegexps.
	regexps map[*ssa.Package][]string
	// tables caches arrayTable; nil until first used.
	tables map[*ssa.Global]ir.Val
}

// arrayTable is the value of a package-level array that is a lookup
// table: its initializer sets it to a literal of constants (var table =
// [256]uint8{...}, or an array of struct literals, as utf8's
// acceptRanges), or sets elements with constant indices to constants, and
// everything else only reads it: loads of the array, of an element or of
// a field of one (*&table[i].f). Such reads index the constant array
// (tableLoad).
func (s *progState) arrayTable(g *ssa.Global) (ir.Val, bool) {
	if s.tables == nil {
		s.tables = map[*ssa.Global]ir.Val{}
		full := map[*ssa.Global]ir.Val{}
		elems := map[*ssa.Global]map[int64]*ssa.Const{}
		bad := map[*ssa.Global]bool{}
		for f := range ssautil.AllFunctions(s.sprog) {
			pkgInit := f.Name() == "init" && f.Synthetic != ""
			for _, b := range f.Blocks {
				for _, in := range b.Instrs {
					for _, op := range in.Operands(nil) {
						g, ok := (*op).(*ssa.Global)
						if !ok {
							continue
						}
						if st, ok := in.(*ssa.Store); ok && pkgInit && st.Addr == g {
							// var table = [N]T{...}: the literal is built in a
							// local array, element by element, then stored.
							v, ok := constData(st.Val)
							if _, arr := v.(ir.ArrVal); !ok || !arr || full[g] != nil {
								bad[g] = true
								continue
							}
							full[g] = v
							continue
						}
						if u, ok := in.(*ssa.UnOp); ok && u.Op == token.MUL && u.X == g {
							continue // a load of the whole array
						}
						ia, ok := in.(*ssa.IndexAddr)
						if !ok || ia.X != g {
							bad[g] = true
							continue
						}
						for _, r := range *ia.Referrers() {
							st, ok := r.(*ssa.Store)
							if !ok {
								if !addrRead(r) {
									bad[g] = true
								}
								continue
							}
							k, kok := ia.Index.(*ssa.Const)
							v, vok := st.Val.(*ssa.Const)
							if !pkgInit || st.Addr != ia || !kok || !vok || k.Value == nil {
								bad[g] = true
								continue
							}
							i, ok := constant.Int64Val(k.Value)
							if !ok {
								bad[g] = true
								continue
							}
							if elems[g] == nil {
								elems[g] = map[int64]*ssa.Const{}
							}
							elems[g][i] = v
						}
					}
				}
			}
		}
		for g, v := range full {
			if !bad[g] && elems[g] == nil && len(v.(ir.ArrVal).Elems) <= 4096 && !hasStr(v) {
				s.tables[g] = v
			}
		}
		for g, es := range elems {
			if bad[g] || full[g] != nil {
				continue
			}
			arr, ok := g.Type().(*types.Pointer).Elem().Underlying().(*types.Array)
			if !ok {
				continue
			}
			zero, ok := zeroVal(arr.Elem())
			if !ok || arr.Len() > 4096 {
				continue
			}
			vals := make([]ir.Val, arr.Len())
			for i := range vals {
				vals[i] = zero
			}
			okAll := true
			for i, c := range es {
				v, err := constVal(c)
				if _, str := v.(ir.StrConst); err != nil || str || i < 0 || i >= arr.Len() {
					okAll = false
					break
				}
				vals[i] = v
			}
			if okAll {
				s.tables[g] = ir.ArrVal{Elems: vals}
			}
		}
	}
	v, ok := s.tables[g]
	return v, ok
}

// tableRoot says whether addr is a lookup table (arrayTable), or the
// address of an element or a field inside one.
func (s *progState) tableRoot(addr ssa.Value) bool {
	for {
		switch a := addr.(type) {
		case *ssa.IndexAddr:
			addr = a.X
		case *ssa.FieldAddr:
			addr = a.X
		case *ssa.Global:
			_, ok := s.arrayTable(a)
			return ok
		default:
			return false
		}
	}
}

// addrRead says whether in only reads through the address it uses: a
// load, or the address of an element or field that is itself only read.
func addrRead(in ssa.Instruction) bool {
	switch in := in.(type) {
	case *ssa.UnOp:
		return in.Op == token.MUL
	case *ssa.IndexAddr, *ssa.FieldAddr:
		for _, r := range *in.(ssa.Value).Referrers() {
			if !addrRead(r) {
				return false
			}
		}
		return true
	}
	return false
}

// constData is the value v has when a package initializer computes it from
// constants only: a constant, or an array or struct literal of such
// values (go/ssa builds one in a local, element by element, then loads
// it).
func constData(v ssa.Value) (ir.Val, bool) {
	switch v := v.(type) {
	case *ssa.Const:
		c, err := constVal(v)
		return c, err == nil
	case *ssa.UnOp:
		if a, ok := v.X.(*ssa.Alloc); ok && v.Op == token.MUL {
			return literal(a, v)
		}
	}
	return nil, false
}

// literal is the array or struct value of the composite literal go/ssa
// builds in a: every use of a but the one that reads it out (use) is the
// address of an element or a field, with a constant index, into which a
// constant datum (constData) is stored once.
func literal(a *ssa.Alloc, use ssa.Instruction) (ir.Val, bool) {
	zero, ok := zeroVal(a.Type().(*types.Pointer).Elem())
	if !ok {
		return nil, false
	}
	var parts []ir.Val
	switch z := zero.(type) {
	case ir.ArrVal:
		parts = slices.Clone(z.Elems)
	case ir.StructVal:
		parts = slices.Clone(z.Fields)
	default:
		return nil, false
	}
	set := make([]bool, len(parts))
	for _, r := range *a.Referrers() {
		if r == use {
			continue
		}
		var i int64
		switch r := r.(type) {
		case *ssa.IndexAddr:
			k, ok := r.Index.(*ssa.Const)
			if !ok || k.Value == nil {
				return nil, false
			}
			if i, ok = constant.Int64Val(k.Value); !ok {
				return nil, false
			}
		case *ssa.FieldAddr:
			i = int64(r.Field)
		default:
			return nil, false
		}
		addr := r.(ssa.Value)
		refs := *addr.Referrers()
		if i < 0 || i >= int64(len(parts)) || set[i] || len(refs) != 1 {
			return nil, false
		}
		st, ok := refs[0].(*ssa.Store)
		if !ok || st.Addr != addr {
			return nil, false
		}
		val, ok := constData(st.Val)
		if !ok {
			return nil, false
		}
		parts[i], set[i] = val, true
	}
	if _, arr := zero.(ir.ArrVal); arr {
		return ir.ArrVal{Elems: parts}, true
	}
	return ir.StructVal{Fields: parts}, true
}

// constSlice is the elements of the slice v holds when a package
// initializer computes it from constants only: a slice literal, or an
// append of such slices (var b = append(a, x, y), where a is a package
// variable whose initializer, in inits, is one).
func constSlice(v ssa.Value, inits map[*ssa.Global]ssa.Value) ([]ir.Val, bool) {
	switch v := v.(type) {
	case *ssa.Slice:
		a, ok := v.X.(*ssa.Alloc)
		if !ok || v.Low != nil || v.High != nil || v.Max != nil {
			return nil, false
		}
		arr, ok := literal(a, v)
		if !ok {
			return nil, false
		}
		return arr.(ir.ArrVal).Elems, true
	case *ssa.UnOp:
		if g, ok := v.X.(*ssa.Global); ok && v.Op == token.MUL && inits[g] != nil {
			return constSlice(inits[g], inits)
		}
	case *ssa.Call:
		if b, ok := v.Call.Value.(*ssa.Builtin); ok && b.Name() == "append" && len(v.Call.Args) == 2 {
			x, ok := constSlice(v.Call.Args[0], inits)
			if !ok {
				return nil, false
			}
			y, ok := constSlice(v.Call.Args[1], inits)
			if !ok {
				return nil, false
			}
			return append(slices.Clone(x), y...), true
		}
	case *ssa.Convert:
		// []byte("constant"): redact's startRedactableBytes (#85).
		if c, ok := v.X.(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String && isByteSlice(v.Type()) {
			var elems []ir.Val
			for _, b := range []byte(constant.StringVal(c.Value)) {
				elems = append(elems, ir.IntVal{Width: 8, Bits: strconv.Itoa(int(b))})
			}
			return elems, true
		}
	}
	return nil, false
}

// kindTable lists the reflect.Kind of each type an interface value the
// model holds can carry: the types modelled code boxes, and those the
// search and the JSON model put in an any (string, bool, int, float64,
// map[string]any, []any).
func (s *progState) kindTable() [][2]uint32 {
	empty := types.NewInterfaceType(nil, nil)
	ts := append([]types.Type{types.Typ[types.String], types.Typ[types.Bool], types.Typ[types.Int], types.Typ[types.Float64],
		types.NewMap(types.Typ[types.String], empty), types.NewSlice(empty)}, s.boxed...)
	var out [][2]uint32
	seen := map[uint32]bool{}
	for _, t := range ts {
		tag := typeTag(t)
		if seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, [2]uint32{tag, uint32(reflectKindOf(t))})
	}
	return out
}

// reflectKindOf is the reflect.Kind of t (reflect's numbering).
func reflectKindOf(t types.Type) int {
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch u.Kind() {
		case types.Bool:
			return 1
		case types.Int:
			return 2
		case types.Int8:
			return 3
		case types.Int16:
			return 4
		case types.Int32:
			return 5
		case types.Int64:
			return 6
		case types.Uint:
			return 7
		case types.Uint8:
			return 8
		case types.Uint16:
			return 9
		case types.Uint32:
			return 10
		case types.Uint64:
			return 11
		case types.Uintptr:
			return 12
		case types.Float32:
			return 13
		case types.Float64:
			return 14
		case types.Complex64:
			return 15
		case types.Complex128:
			return 16
		case types.String:
			return 24
		case types.UnsafePointer:
			return 26
		}
	case *types.Array:
		return 17
	case *types.Chan:
		return 18
	case *types.Signature:
		return 19
	case *types.Interface:
		return 20
	case *types.Map:
		return 21
	case *types.Pointer:
		return 22
	case *types.Slice:
		return 23
	case *types.Struct:
		return 25
	}
	return 0
}

// maxImplementers bounds the boxed types an interface assertion or a
// method call on an interface considers; beyond it they stay unmodelled.
const maxImplementers = 64

// implementers returns the boxed types implementing iface, or false when
// there are more than maxImplementers.
func (s *progState) implementers(iface *types.Interface) ([]types.Type, bool) {
	var out []types.Type
	for _, t := range s.boxed {
		if types.Implements(t, iface) {
			out = append(out, t)
		}
	}
	return out, len(out) <= maxImplementers
}

func (s *progState) newHavoc(width int, site, reason, goHint string) int {
	idx := len(s.prog.HavocWidths)
	s.prog.HavocWidths = append(s.prog.HavocWidths, width)
	s.prog.Havoc = append(s.prog.Havoc, contract.ABIHavoc{Idx: idx, Site: site, Reason: reason, GoHint: goHint})
	return idx
}

// newProgState is the translation state of a package, with FuncIds
// assigned to fns (pass 1) so calls can be resolved.
func newProgState(pkgPath string, fns []*ssa.Function, xrefs map[string]contract.XCall, zeros map[*ssa.Global]ssa.Value, sprog *ssa.Program, boxed []types.Type, plan *cellPlan) *progState {
	p := &ir.Prog{Package: pkgPath, Frontend: Frontend}
	st := &progState{prog: p, ids: map[*ssa.Function]int{}, xrefs: xrefs, zeros: zeros, sprog: sprog, boxed: boxed, json: newJSONTable(sprog)}
	st.sharedGlobals, st.initCells = plan.shared, plan.init
	for _, ty := range boxed {
		if !types.Comparable(ty) && !slices.Contains(st.notComparable, typeTag(ty)) {
			st.notComparable = append(st.notComparable, typeTag(ty))
		}
	}
	byName := map[string]*ssa.Function{}
	for _, f := range fns {
		st.ids[f] = len(p.Funcs)
		p.Funcs = append(p.Funcs, &ir.Func{Name: funcName(f), GoSig: goSig(f)})
		byName[funcName(f)] = f
	}
	st.specFor = func(f *ssa.Function) string { return specTarget(f, byName) }
	return st
}

// rejectStub turns a function into a body that gets stuck, so callers still
// reference a valid FuncId.
func rejectStub(out *ir.Func, err error) {
	out.Status = contract.ModelRejected
	out.Issues = append(out.Issues, err.Error())
	out.Params = nil
	out.NVals = 0
	out.Entry = 0
	out.Blocks = []ir.Block{{Name: "rejected", Term: ir.Term{Kind: ir.TermPanic, Panic: contract.PanicOther}}}
}

type fnState struct {
	prog *progState
	// keep: for the package initializer's slice (#55), the instructions
	// translated (terminators and phis always are); nil otherwise.
	keep    map[ssa.Instruction]bool
	f       *ssa.Function
	out     *ir.Func
	vars    map[ssa.Value]ir.Var
	nvals   int
	blockID map[*ssa.BasicBlock]int
	cur     *ir.Block
	partial bool
	// quiet suppresses issues while the fallback block of a call is built:
	// its havoc is a contingency, not a gap in the model.
	quiet bool
	// defers are the deferred calls to modelled functions, in the order
	// their defer statements appear (deferCall).
	defers []deferred
	// deferStack, when the body has a defer statement in a loop: the cell
	// holding the frame's defer stack, which every defer of the function
	// pushes onto and runDefers pops (#62); dispatches are its pop sites,
	// whose calls are filled in once every defer is known.
	deferStack *ir.Var
	dispatches []deferPop
	// sync: the body starts a goroutine or synchronises (syncTerm).
	sync bool
	// setup, for a concurrent sequence harness: its entry block's
	// instructions reordered to end the setup with a setupEnd (setupCut).
	setup *setupOrder
	// pure caches readsOnly.
	pure *bool
}

func (t *fnState) fresh() ir.Var {
	v := ir.Var(t.nvals)
	t.nvals++
	return v
}

// assume notes an under-approximation like issue, and marks the point in
// the model so that a run through it records it (docs/limitations.md §2).
func (t *fnState) assume(format string, args ...any) {
	t.issue(format, args...)
	note := fmt.Sprintf(format, args...)
	if !strings.HasPrefix(note, t.f.Name()+": ") {
		note = t.f.Name() + ": " + note
	}
	t.emit(ir.Instr{Assume: note})
}

func (t *fnState) issue(format string, args ...any) {
	if t.quiet {
		return
	}
	t.partial = true
	t.out.Issues = append(t.out.Issues, fmt.Sprintf(format, args...))
}

func (t *fnState) emit(in ir.Instr) { t.cur.Instrs = append(t.cur.Instrs, in) }

// readVariant records that the body reads union variant field i (an
// `Of<Variant>` field) of struct type st: the search makes such variants
// the live one more often.
func (t *fnState) readVariant(st types.Type, i int) {
	s, ok := st.Underlying().(*types.Struct)
	if !ok || i >= s.NumFields() {
		return
	}
	name := s.Field(i).Name()
	if len(name) > 2 && strings.HasPrefix(name, "Of") && name[2] >= 'A' && name[2] <= 'Z' && !slices.Contains(t.out.Variants, name) {
		t.out.Variants = append(t.out.Variants, name)
	}
}

func (t *fnState) translate() error {
	f := t.f
	abi, err := abiOf(f, t.prog.json)
	if err != nil {
		return err
	}
	t.out.ABI = *abi
	t.out.SpecFor = t.prog.specFor(f)
	if decl, ok := f.Syntax().(*ast.FuncDecl); ok && decl.Doc != nil && t.out.SpecFor == "" &&
		f.Signature.Results().Len() == 1 && isBool(f.Signature.Results().At(0).Type()) {
		if m := seqRe.FindStringSubmatch(decl.Doc.Text()); m != nil {
			t.out.SequenceFor = m[1]
		}
	}
	if decl, ok := f.Syntax().(*ast.FuncDecl); ok && decl.Doc != nil && (t.out.SpecFor != "" || t.out.SequenceFor != "") {
		if m := maxLenRe.FindStringSubmatch(decl.Doc.Text()); m != nil {
			t.out.SpecMaxLen, _ = strconv.Atoi(m[1])
		}
	}
	if f.Blocks == nil {
		return fmt.Errorf("no body")
	}
	// A closure receives its captured values before its parameters, as
	// funcRef stores them.
	t.out.Closure = f.Parent() != nil
	t.out.TestFile = inTestFile(f) && !isTestEntry(f)
	for _, fv := range f.FreeVars {
		v := t.fresh()
		t.vars[fv] = v
		t.out.Params = append(t.out.Params, v)
	}
	for _, p := range f.Params {
		v := t.fresh()
		t.vars[p] = v
		t.out.Params = append(t.out.Params, v)
	}
	t.out.Blocks = make([]ir.Block, len(f.Blocks))
	if t.out.SequenceFor != "" {
		t.setup = setupCut(f)
	}
	if deferInLoop(f) {
		// The stack is empty on entry: a node is (more, tag, args, rest).
		stack, no, node := t.fresh(), t.fresh(), t.fresh()
		t.deferStack = &stack
		t.out.Blocks[0].Instrs = []ir.Instr{
			{Dst: stack, Op: ir.Alloc{Zero: ir.Opaque{}}},
			{Dst: no, Op: ir.Const{V: ir.BoolVal(false)}},
			{Dst: node, Op: ir.Simple("mkTuple"), Args: []ir.Var{no}},
			{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{stack, node}},
		}
	}
	for i, b := range f.Blocks {
		t.blockID[b] = i
		t.out.Blocks[i].Name = b.Comment
		for _, in := range b.Instrs {
			if phi, ok := in.(*ssa.Phi); ok {
				v := t.fresh()
				t.vars[phi] = v
				t.out.Blocks[i].Params = append(t.out.Blocks[i].Params, v)
			}
		}
	}
	for i, b := range f.Blocks {
		if err := t.block(i, b); err != nil {
			return fmt.Errorf("block %d (%s): %w", i, b.Comment, err)
		}
	}
	t.fillDeferPops()
	t.out.NVals = t.nvals
	t.out.Entry = 0
	if t.partial {
		t.out.Status = contract.ModelPartial
	} else {
		t.out.Status = contract.ModelComplete
	}
	markLastExit(t.out.Exits)
	return nil
}

// phiArgs are the values block `to` receives when entered from `from`.
func (t *fnState) phiArgs(from, to *ssa.BasicBlock) ([]ir.Var, error) {
	idx := -1
	for i, p := range to.Preds {
		if p == from {
			idx = i
		}
	}
	var args []ir.Var
	for _, in := range to.Instrs {
		phi, ok := in.(*ssa.Phi)
		if !ok {
			continue
		}
		if idx < 0 {
			return nil, fmt.Errorf("%s is not a predecessor of %s", from.Comment, to.Comment)
		}
		v, err := t.val(phi.Edges[idx])
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	return args, nil
}

// val returns the variable holding x, materialising constants.
func (t *fnState) val(x ssa.Value) (ir.Var, error) {
	if v, ok := t.vars[x]; ok {
		return v, nil
	}
	switch fn := x.(type) {
	case *ssa.Function:
		// A function used as a value: a reference a call through it follows
		// when the function is modelled. Not cached, like a constant: the
		// block that first used it need not dominate the next use.
		v := t.fresh()
		return v, t.funcValue(v, fn, nil)
	case *ssa.Builtin:
		v := t.fresh()
		t.emit(ir.Instr{Dst: v, Op: ir.Const{V: ir.OpaqueNonNil{}}})
		return v, nil
	case *ssa.Global:
		if key, zero, ok := t.prog.cellGlobal(fn); ok {
			// Not cached: the block that first used it need not dominate
			// the next use.
			v := t.fresh()
			t.emit(ir.Instr{Dst: v, Op: ir.GlobalAddr{Key: key, Zero: zero}})
			return v, nil
		}
	}
	c, ok := x.(*ssa.Const)
	if !ok {
		return 0, fmt.Errorf("unsupported value %s (%T)", x.Name(), x)
	}
	cv, err := constVal(c)
	if err != nil {
		return 0, err
	}
	if iv, ok := cv.(ir.IntVal); ok && !slices.Contains(t.out.Consts, iv.Bits) {
		t.out.Consts = append(t.out.Consts, iv.Bits)
	}
	v := t.fresh()
	if sv, ok := cv.(ir.StrConst); ok {
		if !slices.Contains(t.out.StrConsts, string(sv.Bytes)) {
			t.out.StrConsts = append(t.out.StrConsts, string(sv.Bytes))
		}
		// A string literal is an allocating instruction, not a constant value.
		t.emit(ir.Instr{Dst: v, Op: sv})
		return v, nil
	}
	t.emit(ir.Instr{Dst: v, Op: ir.Const{V: cv}})
	return v, nil
}

// mapKey records a string constant used as a map key (m["role"]): the
// generator builds input maps mostly from such keys.
func (t *fnState) mapKey(k ssa.Value) {
	c, ok := k.(*ssa.Const)
	if !ok || c.Value == nil || c.Value.Kind() != constant.String {
		return
	}
	if s := constant.StringVal(c.Value); !slices.Contains(t.out.MapKeys, s) {
		t.out.MapKeys = append(t.out.MapKeys, s)
	}
}

func constVal(c *ssa.Const) (ir.Val, error) {
	if c.Value == nil {
		switch {
		case isError(c.Type()):
			return ir.ErrVal{NonNil: false}, nil
		case isPointer(c.Type()):
			return ir.NilPtr{}, nil
		case isSlice(c.Type()):
			return ir.NilSlice{}, nil
		case isNilable(c.Type()):
			return ir.OpaqueNil{}, nil
		case isOpaqueType(c.Type()):
			return ir.Opaque{}, nil
		}
		// go/ssa writes the zero value of any type as a nil constant.
		if z, ok := zeroVal(c.Type()); ok {
			return z, nil
		}
		return nil, fmt.Errorf("nil constant of type %s", c.Type())
	}
	if isBool(c.Type()) {
		return ir.BoolVal(constant.BoolVal(c.Value)), nil
	}
	if isString(c.Type()) {
		return ir.StrConst{Bytes: []byte(constant.StringVal(c.Value))}, nil
	}
	if isOpaqueType(c.Type()) {
		return ir.Opaque{}, nil // a float literal: the model does not compute with it
	}
	w, signed, ok := intWidth(c.Type())
	if !ok {
		return nil, fmt.Errorf("constant of unsupported type %s", c.Type())
	}
	var bits uint64
	if signed {
		i, _ := constant.Int64Val(c.Value)
		bits = uint64(i)
	} else {
		bits, _ = constant.Uint64Val(c.Value)
	}
	if w < 64 {
		bits &= (1 << uint(w)) - 1
	}
	return ir.IntVal{Width: w, Bits: fmt.Sprint(bits)}, nil
}

func (t *fnState) block(i int, b *ssa.BasicBlock) error {
	t.cur = &t.out.Blocks[i]
	instrs := b.Instrs
	if i == 0 && t.setup != nil {
		for _, in := range t.setup.prefix {
			if err := t.instr(b, in); err != nil {
				return &instrError{in, err}
			}
		}
		t.syncOp(ir.SyncOp{Kind: "setupEnd"}, t.fresh())
		instrs = t.setup.rest
	}
	for i, in := range instrs {
		if t.keep != nil && !t.keep[in] && i < len(instrs)-1 {
			if _, phi := in.(*ssa.Phi); !phi {
				continue
			}
		}
		if u, ok := in.(*ssa.UnOp); ok && t.keep != nil && isGuard(u) {
			// The initializer runs once: its guard reads false.
			t.emit(ir.Instr{Dst: t.def(u), Op: ir.Const{V: ir.BoolVal(false)}})
			continue
		}
		if err := t.instr(b, in); err != nil {
			return &instrError{in, err}
		}
	}
	return nil
}

// instrError is an instruction the translator could not model.
type instrError struct {
	in  ssa.Instruction
	err error
}

func (e *instrError) Error() string { return fmt.Sprintf("%s: %v", e.in, e.err) }
func (e *instrError) Unwrap() error { return e.err }

// setupOrder is a harness's entry block split where its setup ends.
type setupOrder struct {
	prefix, rest []ssa.Instruction
}

// setupCut splits the entry block of a sequence harness that starts
// goroutines (#87): the prefix is what runs before anything reads a
// parameter, the setup the search then runs once for all trials. A store
// of a parameter into a local cell (a captured parameter) moves after the
// cut, and reads of that cell count as reading the parameter. The cut also
// comes before the first go statement, where the main goroutine stops being
// alone. Nil when the prefix calls nothing: no setup worth saving.
func setupCut(f *ssa.Function) *setupOrder {
	starts := false
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if _, ok := in.(*ssa.Go); ok {
				starts = true
			}
		}
	}
	if !starts || len(f.Blocks) == 0 {
		return nil
	}
	dep := map[ssa.Value]bool{}
	for _, p := range f.Params {
		dep[p] = true
	}
	for _, fv := range f.FreeVars {
		dep[fv] = true
	}
	var o setupOrder
	var deferred []ssa.Instruction
	calls := false
	instrs := f.Blocks[0].Instrs
	cut := len(instrs) - 1 // the terminator never goes in the prefix
	for i, in := range instrs[:len(instrs)-1] {
		if _, ok := in.(*ssa.DebugRef); ok {
			continue
		}
		reads := false
		for _, op := range in.Operands(nil) {
			if *op != nil && dep[*op] {
				reads = true
			}
		}
		if st, ok := in.(*ssa.Store); ok && reads && !dep[st.Addr] {
			if _, local := st.Addr.(*ssa.Alloc); local {
				if _, param := st.Val.(*ssa.Parameter); param {
					deferred = append(deferred, in)
					dep[st.Addr] = true
					continue
				}
			}
		}
		if _, ok := in.(*ssa.Go); ok || reads {
			cut = i
			break
		}
		if c, ok := in.(*ssa.Call); ok {
			if _, builtin := c.Call.Value.(*ssa.Builtin); !builtin {
				calls = true
			}
		}
		o.prefix = append(o.prefix, in)
	}
	if !calls {
		return nil
	}
	o.rest = append(deferred, instrs[cut:]...)
	return &o
}

func (t *fnState) binop(x *ssa.BinOp) error {
	if (x.Op == token.EQL || x.Op == token.NEQ) && isString(x.X.Type()) {
		// Strings compared against a constant: candidate JSON keys and values.
		for _, o := range []ssa.Value{x.X, x.Y} {
			if c, ok := o.(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
				if s := constant.StringVal(c.Value); !slices.Contains(t.out.CmpStrs, s) {
					t.out.CmpStrs = append(t.out.CmpStrs, s)
				}
			}
		}
	}
	w, signed, isInt := intWidth(x.X.Type())
	_, countSigned, _ := intWidth(x.Y.Type())
	_ = w
	var op ir.Op
	switch x.Op {
	case token.ADD:
		op = ir.Simple("add")
	case token.SUB:
		op = ir.Simple("sub")
	case token.MUL:
		op = ir.Simple("mul")
	case token.QUO:
		op = pick(signed, "sdiv", "udiv")
	case token.REM:
		op = pick(signed, "srem", "urem")
	case token.AND:
		op = ir.Simple("and")
	case token.OR:
		op = ir.Simple("or")
	case token.XOR:
		op = ir.Simple("xor")
	case token.AND_NOT:
		op = ir.Simple("andNot")
	case token.SHL:
		op = ir.Shl{CountSigned: countSigned}
	case token.SHR:
		op = ir.Shr{Signed: signed, CountSigned: countSigned}
	case token.EQL:
		op = ir.Simple("eq")
	case token.NEQ:
		op = ir.Simple("ne")
	case token.LSS:
		op = pick(signed, "slt", "ult")
	case token.LEQ:
		op = pick(signed, "sle", "ule")
	case token.GTR:
		op = pick(signed, "sgt", "ugt")
	case token.GEQ:
		op = pick(signed, "sge", "uge")
	default:
		return fmt.Errorf("unsupported binary operator %s", x.Op)
	}
	eqOp := x.Op == token.EQL || x.Op == token.NEQ
	if eqOp && isError(x.X.Type()) {
		for _, pair := range [][2]ssa.Value{{x.X, x.Y}, {x.Y, x.X}} {
			if s, ok := sentinelErr(pair[1]); ok {
				if _, isNil := pair[0].(*ssa.Const); !isNil {
					return t.sentinelCompare(t.def(x), pair[0], s, x.Op == token.NEQ, x.Op.String()+" "+pair[1].Name())
				}
			}
		}
	}
	if isString(x.X.Type()) {
		switch x.Op {
		case token.ADD:
			op = ir.Simple("strConcat")
		case token.EQL, token.NEQ:
		case token.LSS, token.LEQ, token.GTR, token.GEQ:
			// Byte-wise lexicographic order: the unsigned comparisons on
			// two strings (the dialect's string cases).
			op = ir.Simple(map[token.Token]string{token.LSS: "ult", token.LEQ: "ule", token.GTR: "ugt", token.GEQ: "uge"}[x.Op])
		default:
			return fmt.Errorf("string operator %s not supported", x.Op)
		}
	} else if isOpaqueType(x.X.Type()) {
		if eqOp && isNilable(x.X.Type()) {
			if v, ok := nilOperand(x); ok {
				return t.nilCompare(x, v)
			}
		}
		if _, iface := x.X.Type().Underlying().(*types.Interface); eqOp && iface {
			return t.ifaceCompare(x)
		}
		// Comparing maps, funcs or channels with anything but nil, or
		// arithmetic on floats: the model does not look inside them, so
		// the outcome is havoc (a bool, or an opaque number).
		return t.havoc(x, fmt.Sprintf("%s on %s", x.Op, x.X.Type()), "opaque-op", "")
	} else if !isInt && !(eqOp && (isBool(x.X.Type()) || isError(x.X.Type()) || isPointer(x.X.Type()) || isSlice(x.X.Type()) || flatComparable(x.X.Type()))) {
		return fmt.Errorf("binary operator on unsupported type %s", x.X.Type())
	}
	if eqOp && flatComparable(x.X.Type()) {
		op = pick(x.Op == token.EQL, "eqDeep", "neDeep")
	}
	a, err := t.val(x.X)
	if err != nil {
		return err
	}
	c, err := t.val(x.Y)
	if err != nil {
		return err
	}
	t.emit(ir.Instr{Dst: t.def(x), Op: op, Args: []ir.Var{a, c}})
	return nil
}

// flatComparable says whether t is an array or struct whose elements, all
// the way down, are integers, bools, pointers or strings: == on it compares
// element by element (eqDeep: strings by their bytes).
func flatComparable(t types.Type) bool {
	var leaf func(t types.Type) bool
	leaf = func(t types.Type) bool {
		if _, _, ok := intWidth(t); ok {
			return true
		}
		if isBool(t) || isPointer(t) || isString(t) {
			return true
		}
		switch u := t.Underlying().(type) {
		case *types.Array:
			return leaf(u.Elem())
		case *types.Struct:
			for i := 0; i < u.NumFields(); i++ {
				if !leaf(u.Field(i).Type()) {
					return false
				}
			}
			return true
		}
		return false
	}
	switch t.Underlying().(type) {
	case *types.Array, *types.Struct:
		return leaf(t)
	}
	return false
}

func pick(signed bool, s, u string) ir.Op {
	if signed {
		return ir.Simple(s)
	}
	return ir.Simple(u)
}

func (t *fnState) def(x ssa.Value) ir.Var {
	v := t.fresh()
	t.vars[x] = v
	return v
}

// havocKind maps a type to the havoc site encoding (see ir.HavocError).
func havocKind(t types.Type) (int, bool) {
	switch {
	case isError(t):
		return ir.HavocError, true
	case isBool(t):
		return ir.HavocBool, true
	case isString(t):
		return ir.HavocString, true
	}
	w, _, ok := intWidth(t)
	return w, ok
}

// assumedValue is what an unmodelled call's result becomes when it cannot
// be havoc'd: the zero value, or an opaque value for interfaces, maps and
// funcs. The function is marked partial with the assumption spelled out;
// this is an under-approximation M3 may expose as spurious results.
func assumedValue(t types.Type) (ir.Val, bool) {
	if isNilable(t) {
		return ir.Opaque{}, true // unknown, not the nil zero value
	}
	if z, ok := zeroVal(t); ok {
		return z, true
	}
	return nil, false
}

// isNilable says whether t is a func, map, channel, interface (other than
// error, modelled on its own) or unsafe.Pointer type: an opaque type whose
// values the model tells apart only as nil, non-nil or unknown.
func isNilable(t types.Type) bool {
	if isError(t) {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Interface, *types.Map, *types.Signature, *types.Chan:
		return true
	case *types.Basic:
		return u.Kind() == types.UnsafePointer
	}
	return false
}

func (t *fnState) havoc(x ssa.Value, site, reason, goHint string) error {
	if tu, ok := x.Type().(*types.Tuple); ok {
		// A multi-result call: havoc each component and pack them.
		var parts []ir.Var
		for i := 0; i < tu.Len(); i++ {
			v := t.fresh()
			if err := t.havocInto(v, tu.At(i).Type(), fmt.Sprintf("%s: %s #%d", t.f.Name(), site, i), reason, goHint); err != nil {
				return err
			}
			parts = append(parts, v)
		}
		parts, err := t.contractParts(x, parts)
		if err != nil {
			return err
		}
		t.issue("%s: %s is havoc (%s)", site, x.Name(), reason)
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("mkTuple"), Args: parts})
		return nil
	}
	if done, err := t.contractHavoc(t.def(x), x, t.f.Name()+": "+site, reason, goHint); done || err != nil {
		if err == nil {
			t.issue("%s: %s is havoc (%s)", site, x.Name(), reason)
		}
		return err
	}
	if err := t.havocInto(t.def(x), x.Type(), t.f.Name()+": "+site, reason, goHint); err != nil {
		return err
	}
	t.issue("%s: %s is havoc (%s)", site, x.Name(), reason)
	return nil
}

// havocInto binds dst to a havoc value of type ty, or to an assumed value
// when the type cannot be havoc'd: a pointer to a fresh zero object (nil
// would make every later dereference a spurious panic), a slice of a small
// havoc'd length holding zero values, else the zero or opaque value.
func (t *fnState) havocInto(dst ir.Var, ty types.Type, site, reason, goHint string) error {
	if k, ok := havocKind(ty); ok {
		idx := t.prog.newHavoc(k, site, reason, goHint)
		t.emit(ir.Instr{Dst: dst, HavocSite: idx})
		return nil
	}
	// A func, map, channel or interface the model knows nothing about:
	// unknown (every use of it is havoc), not an assumption.
	if isNilable(ty) {
		t.emit(ir.Instr{Dst: dst, Op: ir.Const{V: ir.Opaque{}}})
		return nil
	}
	// A struct, or a pointer to a fresh one: havoc field by field, so the
	// engines can vary its contents (see structHavoc).
	if st, ok := ty.Underlying().(*types.Struct); ok {
		if t.structHavoc(dst, st, site, reason, goHint, 3) {
			return nil
		}
	}
	if pt, ok := ty.Underlying().(*types.Pointer); ok {
		if st, ok := pt.Elem().Underlying().(*types.Struct); ok {
			if zero, ok := zeroVal(pt.Elem()); ok {
				val := t.fresh()
				if t.structHavoc(val, st, site, reason, goHint, 3) {
					t.assume("%s: assumed a pointer to a fresh %s with havoc fields (not modelled)", site, pt.Elem())
					t.emit(ir.Instr{Dst: dst, Op: ir.Alloc{Zero: zero}})
					t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{dst, val}})
					return nil
				}
			}
		}
	}
	// What is left is a fixed guess the engines cannot vary: logged when it
	// runs (a marker havoc read), except for byte slices of havoc length.
	marker := func() {
		t.emit(ir.Instr{Dst: t.fresh(), HavocSite: t.prog.newHavoc(ir.HavocAssumption, site+" (assumed)", "assumed", goHint)})
	}
	switch u := ty.Underlying().(type) {
	case *types.Pointer:
		if zero, ok := zeroVal(u.Elem()); ok {
			marker()
			t.assume("%s: assumed a pointer to a fresh zero %s (not modelled)", site, u.Elem())
			t.emit(ir.Instr{Dst: dst, Op: ir.Alloc{Zero: zero}})
			return nil
		}
	case *types.Slice:
		zero, ok := zeroVal(u.Elem())
		if !ok {
			zero, ok = assumedValue(u.Elem())
		}
		if ok {
			if w, _, isInt := intWidth(u.Elem()); !isInt || w != 8 {
				marker()
			}
			t.assume("%s: assumed a slice of 0..3 zero values of %s (not modelled)", site, u.Elem())
			raw := t.fresh()
			t.emit(ir.Instr{Dst: raw, HavocSite: t.prog.newHavoc(64, site+" (length)", reason, goHint)})
			four := t.fresh()
			t.emit(ir.Instr{Dst: four, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "4"}}})
			n := t.fresh()
			t.emit(ir.Instr{Dst: n, Op: ir.Simple("urem"), Args: []ir.Var{raw, four}})
			t.emit(ir.Instr{Dst: dst, Op: ir.MakeSlice{Zero: zero}, Args: []ir.Var{n, n}})
			return nil
		}
	}
	v, ok := assumedValue(ty)
	if !ok {
		return fmt.Errorf("cannot havoc a value of type %s", ty)
	}
	marker()
	t.assume("%s: assumed the zero value of %s (not modelled)", site, ty)
	t.emit(ir.Instr{Dst: dst, Op: ir.Const{V: v}})
	return nil
}

// structHavoc binds dst to a struct value of type st whose fields are
// havoc where they can be (integers, bools, strings, errors, and arrays of
// up to 16 of them, element by element), unknown for funcs, maps,
// channels and interfaces, havoc'd recursively for nested structs down to
// depth, and nil for pointers and slices. Any other field gets its zero
// value, a fixed guess that is recorded as an assumption, never silent: a
// version struct holding [4]uint read as all zeros made two versions
// compare equal (evaluation 7, k012). It reports false when some field has
// no zero value.
func (t *fnState) structHavoc(dst ir.Var, st *types.Struct, site, reason, goHint string, depth int) bool {
	var fields []ir.Var
	for i := 0; i < st.NumFields(); i++ {
		ft := st.Field(i).Type()
		v := t.fresh()
		fsite := site + "." + st.Field(i).Name()
		switch {
		case func() bool { _, ok := havocKind(ft); return ok }():
			k, _ := havocKind(ft)
			t.emit(ir.Instr{Dst: v, HavocSite: t.prog.newHavoc(k, fsite, reason, goHint)})
		case isNilable(ft):
			t.emit(ir.Instr{Dst: v, Op: ir.Const{V: ir.Opaque{}}})
		default:
			if inner, ok := ft.Underlying().(*types.Struct); ok && depth > 0 && !isStringsBuilder(ft) {
				if t.structHavoc(v, inner, fsite, reason, goHint, depth-1) {
					break
				}
			}
			if arr, ok := ft.Underlying().(*types.Array); ok && arr.Len() <= 16 {
				if k, ok := havocKind(arr.Elem()); ok {
					es := make([]ir.Var, arr.Len())
					for j := range es {
						es[j] = t.fresh()
						t.emit(ir.Instr{Dst: es[j], HavocSite: t.prog.newHavoc(k, fmt.Sprintf("%s[%d]", fsite, j), reason, goHint)})
					}
					t.emit(ir.Instr{Dst: v, Op: ir.Simple("mkArr"), Args: es})
					break
				}
			}
			zero, ok := zeroVal(ft)
			if !ok {
				return false
			}
			switch ft.Underlying().(type) {
			case *types.Pointer, *types.Slice:
			default:
				t.emit(ir.Instr{Dst: t.fresh(), HavocSite: t.prog.newHavoc(ir.HavocAssumption, fsite+" (assumed)", "assumed", goHint)})
				t.assume("%s: assumed the zero value of %s (not modelled)", fsite, ft)
			}
			t.emit(ir.Instr{Dst: v, Op: ir.Const{V: zero}})
		}
		fields = append(fields, v)
	}
	t.emit(ir.Instr{Dst: dst, Op: ir.Simple("mkStruct"), Args: fields})
	return true
}

func (t *fnState) instr(b *ssa.BasicBlock, in ssa.Instruction) error {
	switch x := in.(type) {
	case *ssa.Phi, *ssa.DebugRef:
		return nil
	case *ssa.BinOp:
		return t.binop(x)
	case *ssa.UnOp:
		return t.unop(x)
	case *ssa.Convert:
		return t.conv(x, x.X)
	case *ssa.ChangeType:
		return t.conv(x, x.X)
	case *ssa.IndexAddr:
		if t.prog.tableRoot(x) {
			return nil // read through by the load (tableLoad)
		}
		if !isSlice(x.X.Type()) && !isPointerToArray(x.X.Type()) {
			return fmt.Errorf("index into non-slice type %s", x.X.Type())
		}
		s, err := t.val(x.X)
		if err != nil {
			return err
		}
		i, err := t.index(x.Index)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("indexAddr"), Args: []ir.Var{s, i}})
		return nil
	case *ssa.Alloc:
		elem := x.Type().(*types.Pointer).Elem()
		zero, ok := zeroVal(elem)
		if !ok {
			if zero, ok = assumedValue(elem); !ok {
				return fmt.Errorf("alloc of unsupported type %s", elem)
			}
			t.issue("alloc of %s is opaque (not modelled)", elem)
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Alloc{Zero: zero}})
		return nil
	case *ssa.MakeInterface:
		if s, ok := sentinelErr(x); ok {
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: s}})
			return nil
		}
		// The value is boxed with a tag for its dynamic type, so a type
		// assertion back to that type gets it out again.
		if v, err := t.val(x.X); err == nil {
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Box{Tag: typeTag(x.X.Type())}, Args: []ir.Var{v}})
			return nil
		}
		t.issue("interface value %s is opaque (not modelled)", x.Name())
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.OpaqueNonNil{}}})
		return nil
	case *ssa.MakeMap:
		// A map the program builds: a list of key/value pairs on the heap.
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("makeMap")})
		return nil
	case *ssa.MakeClosure:
		// A closure is a func value holding the function and the values it
		// captured (free variables are pointers to the shared cells).
		fn, _ := x.Fn.(*ssa.Function)
		if fn == nil {
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.OpaqueNonNil{}}})
			return nil
		}
		return t.funcValue(t.def(x), fn, x.Bindings)
	case *ssa.MakeChan:
		elem := x.Type().Underlying().(*types.Chan).Elem()
		zero, ok := zeroVal(elem)
		if !ok {
			if zero, ok = assumedValue(elem); !ok {
				return fmt.Errorf("channel of unsupported type %s", elem)
			}
		}
		n, err := t.val(x.Size)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.ChanMake{Zero: zero}, Args: []ir.Var{n}})
		return nil
	case *ssa.Go:
		return t.goStmt(x)
	case *ssa.Send:
		ch, err := t.val(x.Chan)
		if err != nil {
			return err
		}
		v, err := t.val(x.X)
		if err != nil {
			return err
		}
		t.syncOp(ir.SyncOp{Kind: "send"}, t.fresh(), ch, v)
		return nil
	case *ssa.Select:
		return t.selectStmt(x)
	case *ssa.MapUpdate:
		t.mapKey(x.Key)
		m, err := t.val(x.Map)
		if err != nil {
			return err
		}
		k, err := t.val(x.Key)
		if err != nil {
			return err
		}
		v, err := t.val(x.Value)
		if err != nil {
			return err
		}
		// A map the model knows nothing about (an input field, a havoc
		// value): the update is ignored, as before.
		return t.onKnown(x.Map, m, func() error {
			t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("mapUpdate"), Args: []ir.Var{m, k, v}})
			return nil
		}, func() error {
			t.assume("map update %s[%s] on a map the model knows nothing about ignored", x.Map.Name(), x.Key.Name())
			return nil
		})
	case *ssa.Defer:
		// A deferred call to a function in the model (a closure included)
		// or to a modelled sync method (an unlock, wg.Done) runs at the
		// function's returns (deferCall); one the model does not look into
		// is an effect it does not model anyway.
		if b, ok := x.Call.Value.(*ssa.Builtin); ok && b.Name() == "close" {
			return t.deferCall(x, nil, "close")
		}
		if x.Call.IsInvoke() {
			if op, ok := lockerOrCtx(x.Call.Value.Type(), x.Call.Method.Name()); ok && (op == "lockL" || op == "unlockL") {
				return t.deferCall(x, nil, op)
			}
		} else if x.Call.StaticCallee() == nil {
			// A call through a func value (`defer cancel()`).
			return t.deferCall(x, nil, "dynamic")
		}
		if callee := x.Call.StaticCallee(); callee != nil {
			switch callee.String() {
			case "(*time.Ticker).Stop", "(*time.Timer).Stop":
				// A ticker is a daemon, and a timer's late send never
				// blocks (conc.go): stopping them changes nothing modelled.
				return nil
			}
			if op, ok := syncOps[callee.String()]; ok && op != "onceDo" && op != "yield" {
				return t.deferCall(x, callee, op)
			}
			if _, modelled := t.prog.ids[callee]; modelled {
				return t.deferCall(x, callee, "")
			}
		}
		t.assume("deferred call %s ignored (effects not modelled)", x.Call.String())
		return nil
	case *ssa.RunDefers:
		return t.runDefers()
	case *ssa.Range:
		if isString(x.X.Type()) {
			// The iterator is a heap cell (s, pos) that strNext advances.
			s, err := t.val(x.X)
			if err != nil {
				return err
			}
			zero := t.fresh()
			t.emit(ir.Instr{Dst: zero, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "0"}}})
			state := t.fresh()
			t.emit(ir.Instr{Dst: state, Op: ir.Simple("mkTuple"), Args: []ir.Var{s, zero}})
			cell := t.def(x)
			t.emit(ir.Instr{Dst: cell, Op: ir.Alloc{Zero: ir.Opaque{}}})
			t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{cell, state}})
			return nil
		}
		if _, isMap := x.X.Type().Underlying().(*types.Map); isMap {
			// Insertion order; a map the model knows nothing about has no
			// entries.
			if !t.knownBuilt(x.X) {
				t.issue("range over map %s: in insertion order; a map the model knows nothing about has no entries", x.X.Name())
			}
			m, err := t.val(x.X)
			if err != nil {
				return err
			}
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("mapRange"), Args: []ir.Var{m}})
			return nil
		}
		return fmt.Errorf("range over %s not supported", x.X.Type())
	case *ssa.Next:
		it, err := t.val(x.Iter)
		if err != nil {
			return err
		}
		if x.IsString {
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("strNext"), Args: []ir.Var{it}})
			return nil
		}
		// A map iterator: (ok, key, value), zeros when done.
		tu := x.Type().(*types.Tuple)
		zeros := make([]ir.Val, 2)
		for i := 1; i < tu.Len() && i < 3; i++ {
			z, ok := zeroVal(tu.At(i).Type())
			if !ok {
				if z, ok = assumedValue(tu.At(i).Type()); !ok {
					z = ir.Opaque{} // an unused component has the invalid type
				}
			}
			zeros[i-1] = z
		}
		for i := range zeros {
			if zeros[i] == nil {
				zeros[i] = ir.Opaque{}
			}
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.MapNext{ZeroK: zeros[0], ZeroV: zeros[1]}, Args: []ir.Var{it}})
		return nil
	case *ssa.ChangeInterface:
		// Converting between interface types keeps the value: nil stays
		// nil, a boxed value keeps its dynamic type.
		v, err := t.val(x.X)
		if err != nil {
			return err
		}
		t.vars[x] = v
		return nil
	case *ssa.TypeAssert:
		if zero, ok := zeroVal(x.AssertedType); ok && !types.IsInterface(x.AssertedType) {
			return t.typeAssert(x, zero)
		}
		if iface, ok := x.AssertedType.Underlying().(*types.Interface); ok {
			if impls, ok := t.prog.implementers(iface); ok {
				return t.ifaceAssert(x, impls)
			}
		}
		if x.CommaOk {
			return t.havoc(x, "type assertion", "opaque-assert", "")
		}
		// A failing single-result assertion panics in Go; the model assumes
		// it succeeds and havocs the result, so that panic is not found.
		return t.havoc(x, "type assertion (assumed to succeed)", "opaque-assert", "")
	case *ssa.Index:
		// Index covers arrays and, in recent go/ssa, strings.
		a, err := t.val(x.X)
		if err != nil {
			return err
		}
		i, err := t.index(x.Index)
		if err != nil {
			return err
		}
		op := ir.Simple("arrIndex")
		if isString(x.X.Type()) {
			op = ir.Simple("strIndex")
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: op, Args: []ir.Var{a, i}})
		return nil
	case *ssa.FieldAddr:
		if t.prog.tableRoot(x) {
			return nil // read through by the load (tableLoad)
		}
		if isTestingType(x.X.Type()) {
			// &t.common for a promoted method of the test handle (t.Error):
			// the handle is the harness's, opaque in the model, and the
			// calls on it are ignored, so its fields are a placeholder.
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.UnitVal{}}})
			return nil
		}
		p, err := t.val(x.X)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.FieldAddr{I: x.Field}, Args: []ir.Var{p}})
		t.readVariant(x.X.Type().Underlying().(*types.Pointer).Elem(), x.Field)
		return nil
	case *ssa.Field:
		s, err := t.val(x.X)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Field{I: x.Field}, Args: []ir.Var{s}})
		t.readVariant(x.X.Type(), x.Field)
		return nil
	case *ssa.MakeSlice:
		zero, ok := zeroVal(x.Type().Underlying().(*types.Slice).Elem())
		if !ok {
			return fmt.Errorf("make of unsupported slice type %s", x.Type())
		}
		l, err := t.val(x.Len)
		if err != nil {
			return err
		}
		c, err := t.val(x.Cap)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.MakeSlice{Zero: zero}, Args: []ir.Var{l, c}})
		return nil
	case *ssa.Lookup:
		if mt, isMap := x.X.Type().Underlying().(*types.Map); isMap {
			t.mapKey(x.Index)
			zero, ok := zeroVal(mt.Elem())
			if !ok {
				zero, ok = assumedValue(mt.Elem())
			}
			if !ok {
				return t.havoc(x, "map lookup", "opaque-map", "")
			}
			m, err := t.val(x.X)
			if err != nil {
				return err
			}
			k, err := t.val(x.Index)
			if err != nil {
				return err
			}
			dst := t.def(x)
			return t.onKnown(x.X, m, func() error {
				t.emit(ir.Instr{Dst: dst, Op: ir.MapLookup{CommaOk: x.CommaOk, Zero: zero}, Args: []ir.Var{m, k}})
				return nil
			}, func() error {
				t.issue("lookup in map %s the model knows nothing about is havoc", x.X.Name())
				return t.havocResult(dst, x, "map lookup", "opaque-map")
			})
		}
		if !isString(x.X.Type()) || x.CommaOk {
			return fmt.Errorf("lookup on %s not supported", x.X.Type())
		}
		s, err := t.val(x.X)
		if err != nil {
			return err
		}
		i, err := t.index(x.Index)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("strIndex"), Args: []ir.Var{s, i}})
		return nil
	case *ssa.Slice:
		return t.slice(x)
	case *ssa.Extract:
		tup, err := t.val(x.Tuple)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Extract{I: x.Index}, Args: []ir.Var{tup}})
		return nil
	case *ssa.Store:
		if g, isGlobal := x.Addr.(*ssa.Global); isGlobal {
			if _, _, cell := t.prog.cellGlobal(g); !cell {
				t.assume("store to global %s ignored", x.Addr.Name())
				return nil
			}
		}
		if t.intoHandle(x.Addr) {
			t.assume("store into handle %s ignored", x.Addr.Name())
			return nil
		}
		p, err := t.val(x.Addr)
		if err != nil {
			return err
		}
		v, err := t.val(x.Val)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{p, v}})
		return nil
	case *ssa.Call:
		return t.call(x)
	case *ssa.Return:
		t.recordExit(x)
		switch len(x.Results) {
		case 0:
			v := t.fresh()
			t.emit(ir.Instr{Dst: v, Op: ir.Const{V: ir.UnitVal{}}})
			t.cur.Term = ir.Term{Kind: ir.TermRet, V: v}
		case 1:
			v, err := t.val(x.Results[0])
			if err != nil {
				return err
			}
			t.cur.Term = ir.Term{Kind: ir.TermRet, V: v}
		default:
			var parts []ir.Var
			for _, r := range x.Results {
				v, err := t.val(r)
				if err != nil {
					return err
				}
				parts = append(parts, v)
			}
			tup := t.fresh()
			t.emit(ir.Instr{Dst: tup, Op: ir.Simple("mkTuple"), Args: parts})
			t.cur.Term = ir.Term{Kind: ir.TermRet, V: tup}
		}
		return nil
	case *ssa.Jump:
		args, err := t.phiArgs(b, b.Succs[0])
		if err != nil {
			return err
		}
		t.cur.Term = ir.Term{Kind: ir.TermJump, B: t.blockID[b.Succs[0]], Args: args}
		return nil
	case *ssa.If:
		c, err := t.val(x.Cond)
		if err != nil {
			return err
		}
		targs, err := t.phiArgs(b, b.Succs[0])
		if err != nil {
			return err
		}
		fargs, err := t.phiArgs(b, b.Succs[1])
		if err != nil {
			return err
		}
		t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: c, T: t.blockID[b.Succs[0]], TArgs: targs, F: t.blockID[b.Succs[1]], FArgs: fargs}
		return nil
	case *ssa.Panic:
		t.cur.Term = ir.Term{Kind: ir.TermPanic, Panic: contract.PanicExplicit}
		return nil
	default:
		return fmt.Errorf("unsupported instruction %T", in)
	}
}

// index is the value of v, an index or a slice bound, as the model's
// index operations read it: they sign-extend, so an unsigned index
// narrower than 64 bits (a byte indexing a [256]T table) is zero-extended
// first.
func (t *fnState) index(v ssa.Value) (ir.Var, error) {
	i, err := t.val(v)
	if err != nil {
		return 0, err
	}
	if w, signed, ok := intWidth(v.Type()); ok && !signed && w < 64 {
		z := t.fresh()
		t.emit(ir.Instr{Dst: z, Op: ir.Conv{Width: 64, SignExtend: false}, Args: []ir.Var{i}})
		return z, nil
	}
	return i, nil
}

// tableLoad is the value at addr, a lookup table (arrayTable) or an
// element or a field inside one: the constant array, indexed.
func (t *fnState) tableLoad(addr ssa.Value) (ir.Var, error) {
	v := t.fresh()
	switch a := addr.(type) {
	case *ssa.Global:
		tbl, _ := t.prog.arrayTable(a)
		t.emit(ir.Instr{Dst: v, Op: ir.Const{V: tbl}})
	case *ssa.IndexAddr:
		base, err := t.tableLoad(a.X)
		if err != nil {
			return 0, err
		}
		i, err := t.index(a.Index)
		if err != nil {
			return 0, err
		}
		t.emit(ir.Instr{Dst: v, Op: ir.Simple("arrIndex"), Args: []ir.Var{base, i}})
	case *ssa.FieldAddr:
		base, err := t.tableLoad(a.X)
		if err != nil {
			return 0, err
		}
		t.emit(ir.Instr{Dst: v, Op: ir.Field{I: a.Field}, Args: []ir.Var{base}})
	default:
		return 0, fmt.Errorf("%s is not in a lookup table", addr.Name())
	}
	return v, nil
}

// sliceOf makes x a fresh slice holding elems: the value of a package
// variable its initializer sets to a constant slice (constSlice). Each
// load gets its own copy, so a write through one load would not be seen
// by the next: zeroGlobals keeps only variables no code indexes to write.
func (t *fnState) sliceOf(x ssa.Value, elems []ir.Val) error {
	elem := x.Type().Underlying().(*types.Slice).Elem()
	zero, ok := zeroVal(elem)
	if !ok {
		return fmt.Errorf("slice of unsupported type %s", x.Type())
	}
	for _, e := range elems {
		if _, str := e.(ir.StrConst); !str && hasStr(e) {
			// Struct or array elements holding strings: not built here.
			return t.havoc(x, "load of a slice of "+elem.String()+" holding strings", "unmodeled-load", "")
		}
	}
	n := t.fresh()
	t.emit(ir.Instr{Dst: n, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: strconv.Itoa(len(elems))}}})
	s := t.def(x)
	t.emit(ir.Instr{Dst: s, Op: ir.MakeSlice{Zero: zero}, Args: []ir.Var{n, n}})
	for i, e := range elems {
		if e.Lean() == zero.Lean() {
			continue
		}
		k := t.fresh()
		t.emit(ir.Instr{Dst: k, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: strconv.Itoa(i)}}})
		p := t.fresh()
		t.emit(ir.Instr{Dst: p, Op: ir.Simple("indexAddr"), Args: []ir.Var{s, k}})
		c := t.fresh()
		if sv, ok := e.(ir.StrConst); ok {
			// A string literal is an allocating instruction (as in val).
			if !slices.Contains(t.out.StrConsts, string(sv.Bytes)) {
				t.out.StrConsts = append(t.out.StrConsts, string(sv.Bytes))
			}
			t.emit(ir.Instr{Dst: c, Op: sv})
		} else {
			t.emit(ir.Instr{Dst: c, Op: ir.Const{V: e}})
		}
		t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{p, c}})
	}
	return nil
}

// hasStr says whether v holds a string literal: a string is a heap object
// in the model, so such a value is not a constant.
func hasStr(v ir.Val) bool {
	switch v := v.(type) {
	case ir.StrConst:
		return true
	case ir.ArrVal:
		return slices.ContainsFunc(v.Elems, hasStr)
	case ir.StructVal:
		return slices.ContainsFunc(v.Fields, hasStr)
	}
	return false
}

func (t *fnState) unop(x *ssa.UnOp) error {
	switch x.Op {
	case token.MUL: // load through a pointer
		if g, ok := x.X.(*ssa.Global); ok && g.Pkg != nil && g.Pkg.Pkg.Path() == "time" {
			// The clock's origin (#59): readings count from it, so it is
			// 0; and the Local zone Now stores, which no modelled Time
			// method reads.
			switch g.Name() {
			case "startNano":
				t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "0"}}})
				return nil
			case "Local":
				t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.OpaqueNonNil{}}})
				return nil
			}
		}
		if g, ok := x.X.(*ssa.Global); ok {
			if _, _, cell := t.prog.cellGlobal(g); cell {
				p, err := t.val(g)
				if err != nil {
					return err
				}
				t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("loadPath"), Args: []ir.Var{p}})
				return nil
			}
		}
		if t.prog.tableRoot(x.X) {
			v, err := t.tableLoad(x.X)
			if err != nil {
				return err
			}
			t.vars[x] = v
			return nil
		}
		if t.intoHandle(x.X) {
			// A handle is opaque: what its fields hold is havoc.
			return t.havoc(x, "load "+x.X.Name()+" through a handle", "opaque-handle", "")
		}
		if s, ok := sentinelErr(x); ok {
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: s}})
			return nil
		}
		if zero, fields, ok := sentinelObject(x); ok {
			// The pointer itself, where it is not boxed as the error: a
			// fresh object holding the initializer's constants.
			p := t.def(x)
			t.emit(ir.Instr{Dst: p, Op: ir.Alloc{Zero: zero}})
			for i, c := range fields {
				if c == nil {
					continue
				}
				cv, err := t.val(c)
				if err != nil {
					return err
				}
				fa := t.fresh()
				t.emit(ir.Instr{Dst: fa, Op: ir.FieldAddr{I: i}, Args: []ir.Var{p}})
				t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{fa, cv}})
			}
			return nil
		}
		if _, ok := replacerPairs(x); ok {
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.OpaqueNonNil{}}})
			return nil
		}
		if _, ok := replacerFunc(x); ok {
			// A Replace function value: its calls are lowered in place.
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.OpaqueNonNil{}}})
			return nil
		}
		if re, ok := compiledPattern(x); ok {
			// A compiled constant regexp: only its modelled methods look
			// into it (regexpCall).
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: regexpValue(re.String())}})
			return nil
		}
		if g, isGlobal := x.X.(*ssa.Global); isGlobal {
			if g.Pkg != nil && g.Pkg.Pkg.Path() == "internal/bytealg" && g.Name() == "MaxLen" {
				// Set by an init from the CPU's features (63 with AVX2);
				// any value from 4 up picks an algorithm, not a result.
				t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "63"}}})
				return nil
			}
			if init, known := t.prog.zeros[g]; known {
				if mm, ok := init.(*ssa.MakeMap); ok {
					// A lookup table: built afresh from its constants.
					m := t.def(x)
					t.emit(ir.Instr{Dst: m, Op: ir.Simple("makeMap")})
					for _, up := range constMapEntries(mm) {
						t.mapKey(up.Key)
						k, err := t.val(up.Key)
						if err != nil {
							return err
						}
						val := up.Value
						if fn, ok := tableFunc(val); ok {
							val = fn // the entry is in the initializer, not here
						}
						v, err := t.val(val)
						if err != nil {
							return err
						}
						t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("mapUpdate"), Args: []ir.Var{m, k, v}})
					}
					return nil
				}
				if elems, ok := constSlice(init, t.prog.zeros); ok {
					return t.sliceOf(x, elems)
				}
				if c, ok := init.(*ssa.Call); ok && pureConstCall(c) {
					// var V = f(constants): the call again, here.
					if err := t.call(c); err != nil {
						return err
					}
					t.vars[x] = t.vars[c]
					return nil
				}
				if init != nil {
					v, err := t.val(init)
					if err != nil {
						return err
					}
					t.vars[x] = v
					return nil
				}
				if zero, ok := zeroVal(x.Type()); ok {
					t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: zero}})
					return nil
				}
			}
			return t.havoc(x, "load "+x.X.Name(), "unmodeled-load", "")
		}
		p, err := t.val(x.X)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("loadPath"), Args: []ir.Var{p}})
		return nil
	case token.ARROW: // receive
		ch, err := t.val(x.X)
		if err != nil {
			return err
		}
		t.syncOp(ir.SyncOp{Kind: "recv", WithOk: x.CommaOk}, t.def(x), ch)
		return nil
	case token.SUB, token.XOR:
		if isOpaqueType(x.X.Type()) {
			return t.havoc(x, fmt.Sprintf("%s on %s", x.Op, x.X.Type()), "opaque-op", "")
		}
		if _, _, ok := intWidth(x.X.Type()); !ok {
			return fmt.Errorf("unary %s on %s", x.Op, x.X.Type())
		}
		a, err := t.val(x.X)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: pick(x.Op == token.SUB, "neg", "bnot"), Args: []ir.Var{a}})
		return nil
	case token.NOT:
		a, err := t.val(x.X)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("not"), Args: []ir.Var{a}})
		return nil
	}
	return fmt.Errorf("unsupported unary operator %s", x.Op)
}

func (t *fnState) conv(dst ssa.Value, src ssa.Value) error {
	if isString(dst.Type()) && isByteSlice(src.Type()) || isByteSlice(dst.Type()) && isString(src.Type()) {
		a, err := t.val(src)
		if err != nil {
			return err
		}
		op := ir.Simple("bytesToStr")
		if isString(src.Type()) {
			op = ir.Simple("strToBytes")
		}
		t.emit(ir.Instr{Dst: t.def(dst), Op: op, Args: []ir.Var{a}})
		return nil
	}
	if isString(dst.Type()) && isString(src.Type()) || types.Identical(dst.Type().Underlying(), src.Type().Underlying()) || sameRepr(dst.Type(), src.Type()) {
		// A named string type, or a change of name only (json.RawMessage
		// <-> []byte): the value is unchanged.
		a, err := t.val(src)
		if err != nil {
			return err
		}
		t.vars[dst] = a
		return nil
	}
	if isRuneSlice(dst.Type()) && isString(src.Type()) || isString(dst.Type()) && isRuneSlice(src.Type()) {
		a, err := t.val(src)
		if err != nil {
			return err
		}
		op := ir.Simple("runesToStr")
		if isString(src.Type()) {
			op = ir.Simple("strToRunes")
		}
		t.emit(ir.Instr{Dst: t.def(dst), Op: op, Args: []ir.Var{a}})
		return nil
	}
	if _, _, isInt := intWidth(src.Type()); isInt && isString(dst.Type()) {
		a, err := t.val(src)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(dst), Op: ir.Simple("runeToStr"), Args: []ir.Var{a}})
		return nil
	}
	if isOpaqueType(dst.Type()) || isOpaqueType(src.Type()) {
		// int <-> float and the like: the model does not compute with floats.
		return t.havoc(dst, fmt.Sprintf("conversion %s -> %s", src.Type(), dst.Type()), "opaque-op", "")
	}
	w, _, ok := intWidth(dst.Type())
	_, srcSigned, ok2 := intWidth(src.Type())
	if !ok || !ok2 {
		return fmt.Errorf("conversion %s -> %s not supported", src.Type(), dst.Type())
	}
	a, err := t.val(src)
	if err != nil {
		return err
	}
	t.emit(ir.Instr{Dst: t.def(dst), Op: ir.Conv{Width: w, SignExtend: srcSigned}, Args: []ir.Var{a}})
	return nil
}

func (t *fnState) slice(x *ssa.Slice) error {
	str := isString(x.X.Type())
	if !isSlice(x.X.Type()) && !str && !isPointerToArray(x.X.Type()) {
		return fmt.Errorf("reslice of non-slice type %s", x.X.Type())
	}
	if x.Max != nil {
		if str {
			return fmt.Errorf("three-index slice of a string")
		}
		s, err := t.val(x.X)
		if err != nil {
			return err
		}
		lo := t.fresh()
		t.emit(ir.Instr{Dst: lo, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "0"}}})
		if x.Low != nil {
			if lo, err = t.index(x.Low); err != nil {
				return err
			}
		}
		hi, err := t.index(x.High)
		if err != nil {
			return err
		}
		mx, err := t.index(x.Max)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("reslice3"), Args: []ir.Var{s, lo, hi, mx}})
		return nil
	}
	lenOp, sliceOp := ir.Simple("len"), ir.Simple("reslice")
	if str {
		lenOp, sliceOp = ir.Simple("strLen"), ir.Simple("strSlice")
	}
	if isPointerToArray(x.X.Type()) {
		lenOp = ir.Simple("strLen") // the dialect reads an array's length through this op too
	}
	s, err := t.val(x.X)
	if err != nil {
		return err
	}
	var lo, hi ir.Var
	if x.Low != nil {
		if lo, err = t.index(x.Low); err != nil {
			return err
		}
	} else {
		lo = t.fresh()
		t.emit(ir.Instr{Dst: lo, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "0"}}})
	}
	if x.High != nil {
		if hi, err = t.index(x.High); err != nil {
			return err
		}
	} else {
		hi = t.fresh()
		t.emit(ir.Instr{Dst: hi, Op: lenOp, Args: []ir.Var{s}})
	}
	t.emit(ir.Instr{Dst: t.def(x), Op: sliceOp, Args: []ir.Var{s, lo, hi}})
	return nil
}

func (t *fnState) call(x *ssa.Call) error {
	hasResult := !isUnitTuple(x.Type())
	if bi, ok := x.Call.Value.(*ssa.Builtin); ok {
		switch bi.Name() {
		case "String", "Slice":
			if b, ok := unsafeView(x.Call); ok {
				return t.conv(x, b)
			}
		case "SliceData", "StringData":
			// Read only by the conversions unsafeView lowers: nothing to do.
			if refs := x.Referrers(); refs != nil && len(*refs) > 0 && !slices.ContainsFunc(*refs, func(r ssa.Instruction) bool {
				c, ok := r.(*ssa.Call)
				if !ok {
					return true
				}
				_, view := unsafeView(c.Call)
				return !view
			}) {
				return nil
			}
		case "close":
			ch, err := t.val(x.Call.Args[0])
			if err != nil {
				return err
			}
			t.syncOp(ir.SyncOp{Kind: "close"}, t.def(x), ch)
			return nil
		case "len", "cap":
			if _, isChan := x.Call.Args[0].Type().Underlying().(*types.Chan); isChan {
				ch, err := t.val(x.Call.Args[0])
				if err != nil {
					return err
				}
				t.emit(ir.Instr{Dst: t.def(x), Op: pick(bi.Name() == "len", "chanLen", "chanCap"), Args: []ir.Var{ch}})
				return nil
			}
			if _, isMap := x.Call.Args[0].Type().Underlying().(*types.Map); isMap && bi.Name() == "len" {
				m, err := t.val(x.Call.Args[0])
				if err != nil {
					return err
				}
				dst := t.def(x)
				return t.onKnown(x.Call.Args[0], m, func() error {
					t.emit(ir.Instr{Dst: dst, Op: ir.Simple("mapLen"), Args: []ir.Var{m}})
					return nil
				}, func() error {
					t.opaqueLen(dst, bi.Name(), x.Call.Args[0].Type())
					return nil
				})
			}
			if isSlice(x.Call.Args[0].Type()) {
				a, err := t.val(x.Call.Args[0])
				if err != nil {
					return err
				}
				t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple(bi.Name()), Args: []ir.Var{a}})
				return nil
			}
			if bi.Name() == "len" && isPointerToArray(x.Call.Args[0].Type()) {
				a, err := t.val(x.Call.Args[0])
				if err != nil {
					return err
				}
				t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("strLen"), Args: []ir.Var{a}})
				return nil
			}
			if bi.Name() == "len" && isString(x.Call.Args[0].Type()) {
				a, err := t.val(x.Call.Args[0])
				if err != nil {
					return err
				}
				t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("strLen"), Args: []ir.Var{a}})
				return nil
			}
		case "delete":
			if _, isMap := x.Call.Args[0].Type().Underlying().(*types.Map); isMap {
				m, err := t.val(x.Call.Args[0])
				if err != nil {
					return err
				}
				k, err := t.val(x.Call.Args[1])
				if err != nil {
					return err
				}
				t.def(x)
				return t.onKnown(x.Call.Args[0], m, func() error {
					t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("mapDelete"), Args: []ir.Var{m, k}})
					return nil
				}, func() error { return nil })
			}
		case "append", "copy":
			// append(s, t...) and copy(dst, src) on slices of modelled
			// elements; `append([]byte, string...)` and `copy([]byte, string)`
			// go through a byte-slice copy of the string.
			if !isSlice(x.Call.Args[0].Type()) {
				break
			}
			a, err := t.val(x.Call.Args[0])
			if err != nil {
				return err
			}
			b, err := t.val(x.Call.Args[1])
			if err != nil {
				return err
			}
			if isString(x.Call.Args[1].Type()) {
				bs := t.fresh()
				t.emit(ir.Instr{Dst: bs, Op: ir.Simple("strToBytes"), Args: []ir.Var{b}})
				b = bs
			}
			if bi.Name() == "copy" {
				t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("copySlice"), Args: []ir.Var{a, b}})
				return nil
			}
			zero, ok := zeroVal(x.Type().Underlying().(*types.Slice).Elem())
			if !ok {
				return fmt.Errorf("append to unsupported slice type %s", x.Type())
			}
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Append{Zero: zero}, Args: []ir.Var{a, b}})
			return nil
		}
		if _, signed, isInt := intWidth(x.Type()); (bi.Name() == "min" || bi.Name() == "max") && isInt {
			// min(a, b, ...) and max: a comparison and a choice per argument
			// (min(a) compares a with itself).
			acc, err := t.val(x.Call.Args[0])
			if err != nil {
				return err
			}
			rest := x.Call.Args[1:]
			if len(rest) == 0 {
				rest = x.Call.Args[:1]
			}
			for i, a := range rest {
				v, err := t.val(a)
				if err != nil {
					return err
				}
				less := t.fresh()
				t.emit(ir.Instr{Dst: less, Op: pick(signed, "slt", "ult"), Args: []ir.Var{v, acc}})
				next := t.fresh()
				if i == len(rest)-1 {
					next = t.def(x)
				}
				if bi.Name() == "min" {
					t.emit(ir.Instr{Dst: next, Op: ir.Simple("ite"), Args: []ir.Var{less, v, acc}})
				} else {
					t.emit(ir.Instr{Dst: next, Op: ir.Simple("ite"), Args: []ir.Var{less, acc, v}})
				}
				acc = next
			}
			return nil
		}
		if hasResult {
			if bi.Name() == "len" || bi.Name() == "cap" {
				t.opaqueLen(t.def(x), bi.Name(), x.Call.Args[0].Type())
				return nil
			}
			return t.havoc(x, "builtin "+bi.Name(), "unmodeled-call", "")
		}
		t.assume("call to builtin %s ignored", bi.Name())
		return nil
	}
	callee := x.Call.StaticCallee()
	if callee != nil && callee.Pkg != nil && loggingPkg(callee.Pkg.Pkg.Path()) {
		// A logger (logrus.WithContext, WithField, Debugf): its results only
		// feed more logging calls. Not followed, even where its package is
		// modelled (its internals get stuck on locks and formatting).
		if _, tuple := x.Type().(*types.Tuple); hasResult && !tuple {
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.Opaque{}}})
			return nil
		}
		if !hasResult {
			return nil
		}
	}
	if pairs, ok := replacerFunc(x.Call.Value); ok && len(x.Call.Args) == 1 {
		return t.replace(t.def(x), pairs, x.Call.Args[0])
	}
	if callee != nil && callee.String() == "(*strings.Replacer).Replace" && len(x.Call.Args) == 2 {
		if pairs, ok := replacerPairs(x.Call.Args[0]); ok {
			return t.replace(t.def(x), pairs, x.Call.Args[1])
		}
	}
	if callee != nil && callee.String() == "strings.NewReplacer" {
		if _, ok := replacerPairs(x); ok {
			// Only Replace looks into it (above).
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.OpaqueNonNil{}}})
			return nil
		}
	}
	if callee != nil && callee.Name() == "go2leanProbe" && len(x.Call.Args) == 2 {
		// A spec's probe: record the value, run nothing.
		if c, ok := x.Call.Args[0].(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
			v, err := t.val(x.Call.Args[1])
			if err != nil {
				return err
			}
			t.emit(ir.Instr{Probe: constant.StringVal(c.Value), Args: []ir.Var{v}})
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.UnitVal{}}})
			return nil
		}
	}
	if callee != nil && callee.String() == "fmt.Sprintf" && len(x.Call.Args) == 2 {
		return t.sprintf(x)
	}
	if callee != nil && callee.String() == "fmt.Fprintf" && len(x.Call.Args) == 3 {
		if done, err := t.fprintf(x); done || err != nil {
			return err
		}
	}
	if callee != nil && callee.String() == "encoding/json.Unmarshal" && len(x.Call.Args) == 2 {
		if done, err := t.jsonUnmarshal(x); done || err != nil {
			return err
		}
		if done, err := t.jsonDecode(x); done || err != nil {
			return err
		}
	}
	if callee != nil && callee.Pkg != nil && sdkPkg(callee.Pkg.Pkg.Path()) && strings.HasSuffix(callee.String(), "/internal/apijson.UnmarshalRoot") && len(x.Call.Args) == 2 {
		// The SDKs decode their own types with apijson (UnmarshalJSON,
		// the As* methods of unions): the JSON model's SDK decoding.
		if done, err := t.jsonDecode(x); done || err != nil {
			return err
		}
	}
	if callee != nil && (callee.String() == "(reflect.Value).Kind" || callee.String() == "(reflect.Value).IsNil") && len(x.Call.Args) == 1 {
		v, err := t.val(x.Call.Args[0])
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.ReflectKind{Table: t.prog.kindTable(), IsNil: callee.Name() == "IsNil"}, Args: []ir.Var{v}})
		return nil
	}
	if callee != nil && callee.String() == "errors.Unwrap" && len(x.Call.Args) == 1 {
		if done, err := t.errorsUnwrap(x); done || err != nil {
			return err
		}
	}
	if callee != nil && callee.String() == "errors.Is" && len(x.Call.Args) == 2 {
		if done, err := t.errorsIs(x); done || err != nil {
			return err
		}
		if s, ok := sentinelErr(x.Call.Args[1]); ok {
			return t.sentinelCompare(t.def(x), x.Call.Args[0], s, false, "errors.Is")
		}
	}
	if callee != nil && callee.String() == "errors.As" && len(x.Call.Args) == 2 {
		if done, err := t.errorsAs(x); done || err != nil {
			return err
		}
	}
	if callee != nil {
		if done := t.clockCall(x, callee); done {
			return nil
		}
		if re, ok := compiledPattern(x); ok {
			// regexp.MustCompile of a valid constant pattern: no panic, and
			// a value only the modelled methods look into.
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: regexpValue(re.String())}})
			return nil
		}
		if done, err := t.randBounded(x, callee); done || err != nil {
			return err
		}
		if done, err := t.bufferGrow(x, callee); done || err != nil {
			return err
		}
		if done, err := t.atomicFuncCall(x, callee); done || err != nil {
			return err
		}
		if done, err := t.atomicCall(x, callee); done || err != nil {
			return err
		}
		if done, err := t.regexpCall(x, callee); done || err != nil {
			return err
		}
	}
	if callee != nil && callee.String() == "encoding/json.Marshal" && len(x.Call.Args) == 1 {
		return t.jsonMarshal(x)
	}
	if callee != nil {
		if done, err := t.jsonEncoder(x, callee); done || err != nil {
			return err
		}
	}
	if callee != nil && callee.String() == "strconv.ParseFloat" && len(x.Call.Args) == 2 {
		if k, ok := x.Call.Args[1].(*ssa.Const); ok && k.Value != nil && k.Int64() == 64 {
			return t.parseFloat(x)
		}
	}
	if callee != nil && len(x.Call.Args) == 2 {
		// DecodeString and EncodeToString of the four encodings package
		// base64 defines (others, from NewEncoding, stay unmodelled).
		op := map[string]string{
			"(*encoding/base64.Encoding).DecodeString":   "base64Decode",
			"(*encoding/base64.Encoding).EncodeToString": "base64Encode",
		}[callee.String()]
		for k, name := range []string{"StdEncoding", "URLEncoding", "RawStdEncoding", "RawURLEncoding"} {
			if op == "" || !isGlobalLoad(x.Call.Args[0], "encoding/base64", name) {
				continue
			}
			s, err := t.val(x.Call.Args[1])
			if err != nil {
				return err
			}
			kv := t.fresh()
			t.emit(ir.Instr{Dst: kv, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: fmt.Sprint(k)}}})
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple(op), Args: []ir.Var{s, kv}})
			return nil
		}
	}
	if g := poolGet(x.Call); g != nil {
		if nf := poolNew(g); nf != nil {
			if id, ok := t.prog.ids[nf]; ok {
				// An empty pool's Get calls New, which Go may do at any
				// Get; an object Put back is the behaviour left out (#105).
				t.assume("sync.Pool.Get of %s returns a new object from its New: objects Put back are not modelled", g.Name())
				if !slices.Contains(t.out.Calls, id) {
					t.out.Calls = append(t.out.Calls, id)
				}
				return t.emitCallArgs(x, ir.Term{Kind: ir.TermCall, Callee: id}, nf.String(), nil)
			}
		}
	}
	if done, err := t.concCall(x); done || err != nil {
		return err
	}
	if done, err := t.syncCall(x, callee); done || err != nil {
		return err
	}
	if callee != nil {
		// Ops without a result (Builder.Reset) return the unit value.
		if done, err := t.bytealgCall(x, callee); done || err != nil {
			return err
		}
		if op, ok := stdlibOp(callee.String()); ok {
			var args []ir.Var
			for _, a := range x.Call.Args {
				v, err := t.val(a)
				if err != nil {
					return err
				}
				args = append(args, v)
			}
			t.emit(ir.Instr{Dst: t.def(x), Op: op, Args: args})
			return nil
		}
	}
	if id, ok := t.prog.ids[callee]; ok && callee != nil {
		if !slices.Contains(t.out.Calls, id) {
			t.out.Calls = append(t.out.Calls, id)
		}
		return t.emitCall(x, ir.Term{Kind: ir.TermCall, Callee: id}, callee.String())
	}
	if x.Call.IsInvoke() {
		if done, err := t.invoke(x); done || err != nil {
			return err
		}
	}
	if callee == nil && !x.Call.IsInvoke() {
		// A call through a func value: the model follows the function it
		// names, if it knows it.
		fv, err := t.val(x.Call.Value)
		if err != nil {
			return err
		}
		return t.emitCall(x, ir.Term{Kind: ir.TermCallV, FV: fv}, x.Call.Value.Name())
	}
	if callee == nil {
		// fall through to the unmodelled cases below
	} else if xr, ok := t.prog.xrefs[callee.String()]; ok && xr.Package != t.prog.prog.Package {
		if !slices.Contains(t.out.XCalls, xr) {
			t.out.XCalls = append(t.out.XCalls, xr)
		}
		return t.emitCall(x, ir.Term{Kind: ir.TermXCall, Callee: xr.ID, CalleePkg: xr.Package}, callee.String())
	}
	name := x.Call.Value.Name()
	if callee != nil {
		name = callee.String()
	}
	if hasResult {
		if callee != nil && callee.Pkg != nil && callee.Name() == "Perm" && strings.HasPrefix(callee.Pkg.Pkg.Path(), "math/rand") && isSlice(x.Type()) {
			return t.randPerm(x, x.Call.Args[len(x.Call.Args)-1])
		}
		return t.havoc(x, "call "+name, "unmodeled-call", "")
	}
	// The sync operations the concurrent machine models are lowered before
	// this (conc.go); one that reaches here (sync.Map, sync.Pool) is not
	// modelled, so ignoring it is an under-approximation like any other.
	t.assume("call to %s ignored (no result, effects not modelled)", name)
	return nil
}

// isGlobalLoad says whether v loads the package-level variable pkg.name.
func isGlobalLoad(v ssa.Value, pkg, name string) bool {
	u, ok := v.(*ssa.UnOp)
	if !ok || u.Op != token.MUL {
		return false
	}
	g, ok := u.X.(*ssa.Global)
	return ok && g.Pkg != nil && g.Pkg.Pkg.Path() == pkg && g.Name() == name
}

// loggingPkg says whether path is a logging package: its calls have no
// effect the model follows.
func loggingPkg(path string) bool {
	switch path {
	case "github.com/sirupsen/logrus", "log", "log/slog", "go.uber.org/zap":
		return true
	}
	return false
}

// recordExit notes a return statement: its block (the current one), its
// source line, and whether it returns the constant true.
// markLastExit flags the return statement with the highest source line.
func markLastExit(exits []contract.ExitSite) {
	last := -1
	for i, e := range exits {
		if last < 0 || e.Line > exits[last].Line {
			last = i
		}
	}
	if last >= 0 {
		exits[last].Last = true
	}
}

func (t *fnState) recordExit(x *ssa.Return) {
	block := -1
	for i := range t.out.Blocks {
		if &t.out.Blocks[i] == t.cur {
			block = i
		}
	}
	line := 0
	if t.f.Prog != nil && x.Pos().IsValid() {
		line = t.f.Prog.Fset.Position(x.Pos()).Line
	}
	constTrue := false
	if len(x.Results) == 1 {
		if c, ok := x.Results[0].(*ssa.Const); ok && c.Value != nil && isBool(c.Type()) && constant.BoolVal(c.Value) {
			constTrue = true
		}
	}
	t.out.Exits = append(t.out.Exits, contract.ExitSite{Block: block, Line: line, ConstTrue: constTrue})
}

// stringEnum returns the string constants declared with type t in t's
// package, for a named string type (at most 64, sorted); nil otherwise.
func stringEnum(t types.Type) []string {
	n, ok := t.(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return nil
	}
	scope := n.Obj().Pkg().Scope()
	var out []string
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok || !types.Identical(c.Type(), t) || c.Val().Kind() != constant.String {
			continue
		}
		v := constant.StringVal(c.Val())
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// typeTag names a dynamic type in boxed interface values: a hash of the
// type's fully qualified name, so every package of a linked model agrees.
func typeTag(t types.Type) uint32 {
	h := fnv.New32a()
	h.Write([]byte(tagName(t)))
	return h.Sum32()
}

var anyAliasRe = regexp.MustCompile(`\bany\b`)

// tagName spells t for its tag the same way whether the source wrote an
// alias or the type it stands for (any or interface{}, type B = A or A):
// they are one type at run time.
func tagName(t types.Type) string {
	return anyAliasRe.ReplaceAllString(types.TypeString(unaliasDeep(t), nil), "interface{}")
}

// unaliasDeep is t with the aliases in it, and in the types it is built
// from (pointer, slice, array, map and channel elements), replaced by the
// types they stand for.
func unaliasDeep(t types.Type) types.Type {
	switch u := types.Unalias(t).(type) {
	case *types.Pointer:
		return types.NewPointer(unaliasDeep(u.Elem()))
	case *types.Slice:
		return types.NewSlice(unaliasDeep(u.Elem()))
	case *types.Array:
		return types.NewArray(unaliasDeep(u.Elem()), u.Len())
	case *types.Map:
		return types.NewMap(unaliasDeep(u.Key()), unaliasDeep(u.Elem()))
	case *types.Chan:
		return types.NewChan(u.Dir(), unaliasDeep(u.Elem()))
	default:
		return u
	}
}

// typeAssert lowers x.(T) for a concrete T. A value the model boxed itself
// is unboxed, a nil interface fails; a failure gives the zero value and
// false, or panics. An interface value the model knows nothing about (the
// result of an unmodelled call) gives a havoc result.
func (t *fnState) typeAssert(x *ssa.TypeAssert, zero ir.Val) error {
	v, err := t.val(x.X)
	if err != nil {
		return err
	}
	dst := t.def(x)
	isBoxed := t.fresh()
	t.emit(ir.Instr{Dst: isBoxed, Op: ir.Simple("nilKnown"), Args: []ir.Var{v}})
	boxed := len(t.out.Blocks)
	other, join := boxed+1, boxed+2
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: isBoxed, T: boxed, F: other}
	name := t.cur.Name
	t.out.Blocks = append(t.out.Blocks,
		ir.Block{Name: name + ".assert-boxed"},
		ir.Block{Name: name + ".assert-unknown"},
		ir.Block{Name: name + ".assert-join"})
	t.cur = &t.out.Blocks[boxed]
	t.emit(ir.Instr{Dst: dst, Op: ir.Unbox{Tag: typeTag(x.AssertedType), CommaOk: x.CommaOk, Zero: zero}, Args: []ir.Var{v}})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[other]
	if !t.knownBuilt(x.X) {
		t.issue("type assertion %s on an interface value the model did not build is havoc", x.Name())
	}
	if err := t.havocResult(dst, x, "type assertion", "opaque-assert"); err != nil {
		return err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return nil
}

// errorsAs lowers errors.As(err, &v): as Go runs it on the errors the
// model built (errorsAsWalk), havoc on the others (errorsAsHavoc).
func (t *fnState) errorsAs(x *ssa.Call) (bool, error) {
	mi, ok := x.Call.Args[1].(*ssa.MakeInterface)
	if !ok {
		return false, nil
	}
	pt, ok := mi.X.Type().Underlying().(*types.Pointer)
	if !ok {
		return false, nil
	}
	p, err := t.val(mi.X)
	if err != nil {
		return true, err
	}
	dst := t.def(x)
	if ok, err := t.errorsAsWalk(x, p, pt.Elem(), dst); ok || err != nil {
		return true, err
	}
	return true, t.errorsAsHavoc(p, pt.Elem(), dst)
}

// errorsAsHavoc is errors.As on an error the model does not know: a havoc
// result, and when it holds, a havoc value of the target's type.
func (t *fnState) errorsAsHavoc(p ir.Var, elem types.Type, dst ir.Var) error {
	site := t.f.Name() + ": call errors.As"
	if err := t.havocInto(dst, types.Typ[types.Bool], site, "unmodeled-call", ""); err != nil {
		return err
	}
	set := len(t.out.Blocks)
	join := set + 1
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: dst, T: set, F: join}
	name := t.cur.Name
	t.out.Blocks = append(t.out.Blocks, ir.Block{Name: name + ".as-set"}, ir.Block{Name: name + ".as-join"})
	t.cur = &t.out.Blocks[set]
	v := t.fresh()
	if err := t.havocInto(v, elem, site+" (target)", "unmodeled-call", ""); err != nil {
		return err
	}
	t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{p, v}})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return nil
}

// addBlocks appends blocks with these names, keeping t.cur on its block
// (an append can move the slice t.cur points into).
func (t *fnState) addBlocks(names ...string) {
	ci := -1
	for i := range t.out.Blocks {
		if &t.out.Blocks[i] == t.cur {
			ci = i
		}
	}
	for _, n := range names {
		t.out.Blocks = append(t.out.Blocks, ir.Block{Name: n})
	}
	if ci >= 0 {
		t.cur = &t.out.Blocks[ci]
	}
}

// methodIface is interface{ name(params) results } with its method.
func methodIface(name string, params, results []types.Type) (*types.Interface, *types.Func) {
	vars := func(ts []types.Type) *types.Tuple {
		var vs []*types.Var
		for _, ty := range ts {
			vs = append(vs, types.NewVar(token.NoPos, nil, "", ty))
		}
		return types.NewTuple(vs...)
	}
	m := types.NewFunc(token.NoPos, nil, name, types.NewSignatureType(nil, nil, nil, vars(params), vars(results), false))
	return types.NewInterfaceType([]*types.Func{m}, nil).Complete(), m
}

// tagsOf are the tags of the boxed types implementing iface; not ok when
// there are too many to list.
func (t *fnState) tagsOf(iface *types.Interface) ([]uint32, bool) {
	impls, ok := t.prog.implementers(iface)
	if !ok {
		return nil, false
	}
	tags := make([]uint32, len(impls))
	for i, ty := range impls {
		tags[i] = typeTag(ty)
	}
	return tags, true
}

// errChain walks the chain of err0's Unwrap() error, as errors.Is and
// errors.As do, for the errors the model built: visit tests each non-nil
// error e in the current block, jumping to found (which sets dst true) on
// a match and leaving the current block for the next test otherwise. The
// walk sets dst false at the chain's end; an error the model does not know
// met on the way, or one of a type in stop (whose chain it does not
// follow), runs unknown instead. Not ok when the types to test are too
// many to list.
func (t *fnState) errChain(err0, dst ir.Var, stop []*types.Interface, visit func(e ir.Var, found, unknown int) error, unknown func() error) (bool, error) {
	errT := types.Universe.Lookup("error").Type()
	unwrapI, unwrapM := methodIface("Unwrap", nil, []types.Type{errT})
	unwrapTags, ok := t.tagsOf(unwrapI)
	if !ok {
		return false, nil
	}
	var stopTags []uint32
	for _, iface := range stop {
		tags, ok := t.tagsOf(iface)
		if !ok {
			return false, nil
		}
		stopTags = append(stopTags, tags...)
	}
	// The error the walk is at lives in a cell.
	cur := t.fresh()
	t.emit(ir.Instr{Dst: cur, Op: ir.Alloc{Zero: ir.Opaque{}}})
	t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{cur, err0}})
	n := len(t.out.Blocks)
	loop, known, notNil, found, unk, no, join := n, n+1, n+2, n+3, n+4, n+5, n+6
	name := t.cur.Name
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: loop}
	t.addBlocks(name+".chain", name+".chain-known", name+".chain-err", name+".chain-found", name+".chain-unknown", name+".chain-end", name+".chain-join")
	t.cur = &t.out.Blocks[loop]
	e, isKnown := t.fresh(), t.fresh()
	t.emit(ir.Instr{Dst: e, Op: ir.Simple("loadPath"), Args: []ir.Var{cur}})
	t.emit(ir.Instr{Dst: isKnown, Op: ir.Simple("errKnown"), Args: []ir.Var{e}})
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: isKnown, T: known, F: unk}
	t.cur = &t.out.Blocks[known]
	isNil := t.fresh()
	t.emit(ir.Instr{Dst: isNil, Op: ir.Simple("isNil"), Args: []ir.Var{e}})
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: isNil, T: no, F: notNil}
	t.cur = &t.out.Blocks[notNil]
	if err := visit(e, found, unk); err != nil {
		return true, err
	}
	// assertTo branches on whether e's type is one of tags.
	assertTo := func(tags []uint32, yes, other int) {
		r, is := t.fresh(), t.fresh()
		t.emit(ir.Instr{Dst: r, Op: ir.AssertIface{Tags: tags, CommaOk: true}, Args: []ir.Var{e}})
		t.emit(ir.Instr{Dst: is, Op: ir.Extract{I: 1}, Args: []ir.Var{r}})
		t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: is, T: yes, F: other}
	}
	if len(stopTags) > 0 {
		cont := len(t.out.Blocks)
		t.addBlocks(name + ".chain-plain")
		assertTo(stopTags, unk, cont)
		t.cur = &t.out.Blocks[cont]
	}
	if len(unwrapTags) == 0 {
		t.cur.Term = ir.Term{Kind: ir.TermJump, B: no}
	} else {
		fv, ok, err := t.dispatchOn(e, unwrapI, unwrapM)
		if err != nil {
			return true, err
		}
		if !ok {
			// Only the standard library's types have an Unwrap: havoc
			// for those, the end of the chain for the others.
			assertTo(unwrapTags, unk, no)
		} else {
			unwrap, after := len(t.out.Blocks), len(t.out.Blocks)+1
			t.addBlocks(name+".chain-unwrap", name+".chain-next")
			assertTo(unwrapTags, unwrap, no)
			t.cur = &t.out.Blocks[unwrap]
			// An Unwrap the model gets stuck in leaves the walk to unknown.
			inner := t.fresh()
			t.cur.Term = ir.Term{Kind: ir.TermCallV, FV: fv, Dst: inner, Next: after, Fallback: unk}
			t.cur = &t.out.Blocks[after]
			t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{cur, inner}})
			t.cur.Term = ir.Term{Kind: ir.TermJump, B: loop}
		}
	}
	t.cur = &t.out.Blocks[found]
	t.emit(ir.Instr{Dst: dst, Op: ir.Const{V: ir.BoolVal(true)}})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[no]
	t.emit(ir.Instr{Dst: dst, Op: ir.Const{V: ir.BoolVal(false)}})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[unk]
	if err := unknown(); err != nil {
		return true, err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return true, nil
}

// errorsAsWalk lowers errors.As(err, p) as Go runs it, on the errors the
// model built (errChain): the first error in err's chain whose dynamic
// type is the target type (or implements it, for an interface) is stored
// through p. Errors with an As method or an Unwrap() []error stop the walk
// (errorsAsHavoc). Not ok when the target type cannot be tested.
func (t *fnState) errorsAsWalk(x *ssa.Call, p ir.Var, elem types.Type, dst ir.Var) (bool, error) {
	var match ir.Op
	if iface, ok := elem.Underlying().(*types.Interface); ok {
		tags, ok := t.tagsOf(iface)
		if !ok {
			return false, nil
		}
		match = ir.AssertIface{Tags: tags, CommaOk: true}
	} else if zero, ok := zeroVal(elem); ok {
		match = ir.Unbox{Tag: typeTag(elem), CommaOk: true, Zero: zero}
	} else {
		return false, nil
	}
	errT := types.Universe.Lookup("error").Type()
	asI, _ := methodIface("As", []types.Type{types.NewInterfaceType(nil, nil)}, []types.Type{types.Typ[types.Bool]})
	joinI, _ := methodIface("Unwrap", nil, []types.Type{types.NewSlice(errT)})
	err0, err := t.val(x.Call.Args[0])
	if err != nil {
		return true, err
	}
	return t.errChain(err0, dst, []*types.Interface{asI, joinI}, func(e ir.Var, found, _ int) error {
		r, v, ok := t.fresh(), t.fresh(), t.fresh()
		t.emit(ir.Instr{Dst: r, Op: match, Args: []ir.Var{e}})
		t.emit(ir.Instr{Dst: v, Op: ir.Extract{I: 0}, Args: []ir.Var{r}})
		t.emit(ir.Instr{Dst: ok, Op: ir.Extract{I: 1}, Args: []ir.Var{r}})
		set, cont := len(t.out.Blocks), len(t.out.Blocks)+1
		name := t.cur.Name
		t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: ok, T: set, F: cont}
		t.addBlocks(name+".as-set", name+".as-next")
		t.cur = &t.out.Blocks[set]
		t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{p, v}})
		t.cur.Term = ir.Term{Kind: ir.TermJump, B: found}
		t.cur = &t.out.Blocks[cont]
		return nil
	}, func() error { return t.errorsAsHavoc(p, elem, dst) })
}

// errorsUnwrap lowers errors.Unwrap(err) on an error the model built: its
// Unwrap() error, or nil when its type has none; havoc on another error.
func (t *fnState) errorsUnwrap(x *ssa.Call) (bool, error) {
	errT := types.Universe.Lookup("error").Type()
	unwrapI, unwrapM := methodIface("Unwrap", nil, []types.Type{errT})
	tags, ok := t.tagsOf(unwrapI)
	if !ok {
		return false, nil
	}
	e, err := t.val(x.Call.Args[0])
	if err != nil {
		return true, err
	}
	dst := t.def(x)
	n := len(t.out.Blocks)
	known, call, none, unknown, join := n, n+1, n+2, n+3, n+4
	name := t.cur.Name
	isKnown := t.fresh()
	t.emit(ir.Instr{Dst: isKnown, Op: ir.Simple("errKnown"), Args: []ir.Var{e}})
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: isKnown, T: known, F: unknown}
	t.addBlocks(name+".unwrap-known", name+".unwrap-call", name+".unwrap-none", name+".unwrap-unknown", name+".unwrap-join")
	t.cur = &t.out.Blocks[known]
	fv, ok, err := t.dispatchOn(e, unwrapI, unwrapM)
	if err != nil {
		return true, err
	}
	if len(tags) == 0 || !ok {
		// No Unwrap the model can call: a type with one (the standard
		// library's) gives havoc, any other has none.
		t.cur.Term = ir.Term{Kind: ir.TermJump, B: none}
		if len(tags) > 0 {
			r, has := t.fresh(), t.fresh()
			t.emit(ir.Instr{Dst: r, Op: ir.AssertIface{Tags: tags, CommaOk: true}, Args: []ir.Var{e}})
			t.emit(ir.Instr{Dst: has, Op: ir.Extract{I: 1}, Args: []ir.Var{r}})
			t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: has, T: unknown, F: none}
		}
		t.out.Blocks[call].Term = ir.Term{Kind: ir.TermJump, B: unknown}
	} else {
		r, has := t.fresh(), t.fresh()
		t.emit(ir.Instr{Dst: r, Op: ir.AssertIface{Tags: tags, CommaOk: true}, Args: []ir.Var{e}})
		t.emit(ir.Instr{Dst: has, Op: ir.Extract{I: 1}, Args: []ir.Var{r}})
		t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: has, T: call, F: none}
		t.cur = &t.out.Blocks[call]
		t.cur.Term = ir.Term{Kind: ir.TermCallV, FV: fv, Dst: dst, Next: join, Fallback: unknown}
	}
	t.cur = &t.out.Blocks[none]
	t.emit(ir.Instr{Dst: dst, Op: ir.Const{V: ir.ErrVal{NonNil: false}}})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[unknown]
	if err := t.havocResult(dst, x, "errors.Unwrap", "unmodeled-call"); err != nil {
		return true, err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return true, nil
}

// errorsIs lowers errors.Is(err, target) as Go runs it when target is an
// error the model built (errChain): an error in err's chain equal to
// target (when target's type is comparable), or whose Is method says so.
// Errors with an Unwrap() []error stop the walk; with an unknown error,
// the result is havoc (it may be, or wrap, target).
func (t *fnState) errorsIs(x *ssa.Call) (bool, error) {
	errT := types.Universe.Lookup("error").Type()
	isI, isM := methodIface("Is", []types.Type{errT}, []types.Type{types.Typ[types.Bool]})
	isTags, ok := t.tagsOf(isI)
	if !ok {
		return false, nil
	}
	joinI, _ := methodIface("Unwrap", nil, []types.Type{types.NewSlice(errT)})
	err0, err := t.val(x.Call.Args[0])
	if err != nil {
		return true, err
	}
	target, err := t.val(x.Call.Args[1])
	if err != nil {
		return true, err
	}
	dst := t.def(x)
	havoc := func() error {
		t.emit(ir.Instr{Dst: dst, HavocSite: t.prog.newHavoc(ir.HavocBool, t.f.Name()+": errors.Is", "opaque-op", "")})
		return nil
	}
	// errors.Is(err, nil) is err == nil; an unknown target: havoc.
	known, tnil, walk, unknown, join := len(t.out.Blocks), len(t.out.Blocks)+1, len(t.out.Blocks)+2, len(t.out.Blocks)+3, len(t.out.Blocks)+4
	name := t.cur.Name
	t.addBlocks(name+".is-target", name+".is-nil-target", name+".is-walk", name+".is-unknown", name+".is-join")
	tKnown := t.fresh()
	t.emit(ir.Instr{Dst: tKnown, Op: ir.Simple("errKnown"), Args: []ir.Var{target}})
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: tKnown, T: known, F: unknown}
	t.cur = &t.out.Blocks[known]
	isNil := t.fresh()
	t.emit(ir.Instr{Dst: isNil, Op: ir.Simple("isNil"), Args: []ir.Var{target}})
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: isNil, T: tnil, F: walk}
	t.cur = &t.out.Blocks[tnil]
	eKnown := t.fresh()
	t.emit(ir.Instr{Dst: eKnown, Op: ir.Simple("errKnown"), Args: []ir.Var{err0}})
	nilKnown := len(t.out.Blocks)
	t.addBlocks(name + ".is-nil-known")
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: eKnown, T: nilKnown, F: unknown}
	t.cur = &t.out.Blocks[nilKnown]
	t.emit(ir.Instr{Dst: dst, Op: ir.Simple("isNil"), Args: []ir.Var{err0}})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[unknown]
	if err := havoc(); err != nil {
		return true, err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[walk]
	comparable := t.fresh()
	t.emit(ir.Instr{Dst: comparable, Op: ir.IfaceComparable{NotComparable: t.prog.notComparable}, Args: []ir.Var{target}})
	ok, err = t.errChain(err0, dst, []*types.Interface{joinI}, func(e ir.Var, found, unk int) error {
		cmp, method := len(t.out.Blocks), len(t.out.Blocks)+1
		name := t.cur.Name
		t.addBlocks(name+".is-eq", name+".is-method")
		t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: comparable, T: cmp, F: method}
		t.cur = &t.out.Blocks[cmp]
		eq := t.fresh()
		t.emit(ir.Instr{Dst: eq, Op: ir.Simple("ifaceEq"), Args: []ir.Var{e, target}})
		t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: eq, T: found, F: method}
		t.cur = &t.out.Blocks[method]
		if len(isTags) == 0 {
			return nil
		}
		fv, ok, err := t.dispatchOn(e, isI, isM)
		if err != nil {
			return err
		}
		call, after, cont := len(t.out.Blocks), len(t.out.Blocks)+1, len(t.out.Blocks)+2
		t.addBlocks(name+".is-call", name+".is-said", name+".is-next")
		r, has := t.fresh(), t.fresh()
		t.emit(ir.Instr{Dst: r, Op: ir.AssertIface{Tags: isTags, CommaOk: true}, Args: []ir.Var{e}})
		t.emit(ir.Instr{Dst: has, Op: ir.Extract{I: 1}, Args: []ir.Var{r}})
		t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: has, T: call, F: cont}
		t.cur = &t.out.Blocks[call]
		if !ok {
			t.cur.Term = ir.Term{Kind: ir.TermJump, B: unk}
			t.out.Blocks[after].Term = ir.Term{Kind: ir.TermJump, B: unk}
		} else {
			said := t.fresh()
			t.cur.Term = ir.Term{Kind: ir.TermCallV, FV: fv, CArgs: []ir.Var{target}, Dst: said, Next: after, Fallback: unk}
			t.cur = &t.out.Blocks[after]
			t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: said, T: found, F: cont}
		}
		t.cur = &t.out.Blocks[cont]
		return nil
	}, havoc)
	if err != nil {
		return true, err
	}
	if !ok {
		// Too many error types to list: havoc, as for an unknown error.
		t.cur.Term = ir.Term{Kind: ir.TermJump, B: unknown}
	} else {
		t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	}
	t.cur = &t.out.Blocks[join]
	return true, nil
}

// sprintf lowers fmt.Sprintf: the formatted string when the model can
// format it (plain %s %d %v %t %% on strings, integers and bools the
// program boxed, %x on strings and byte slices), else the havoc string an unmodelled call gives.
// jsonUnmarshal lowers json.Unmarshal(data, &v) for v of a JSON-shaped
// type (string, bool, interface{}, map[string]interface{}, []interface{}):
// the model parses the bytes. Other targets (structs) stay unmodelled.
func (t *fnState) jsonUnmarshal(x *ssa.Call) (bool, error) {
	mi, ok := x.Call.Args[1].(*ssa.MakeInterface)
	if !ok {
		return false, nil
	}
	pt, ok := mi.X.Type().Underlying().(*types.Pointer)
	if !ok {
		return false, nil
	}
	kind := -1
	elem := pt.Elem()
	switch u := elem.Underlying().(type) {
	case *types.Basic:
		if isString(elem) {
			kind = 0
		} else if isBool(elem) {
			kind = 1
		}
	case *types.Interface:
		if isEmptyInterface(elem) {
			kind = 2
		}
	case *types.Map:
		if isString(u.Key()) && isEmptyInterface(u.Elem()) {
			kind = 3
		}
	case *types.Slice:
		if isEmptyInterface(u.Elem()) {
			kind = 4
		}
	}
	if kind < 0 {
		return false, nil
	}
	data, err := t.val(x.Call.Args[0])
	if err != nil {
		return true, err
	}
	p, err := t.val(mi.X)
	if err != nil {
		return true, err
	}
	empty := types.NewInterfaceType(nil, nil)
	op := ir.JSONUnmarshal{Kind: kind, Tags: [5]uint32{
		typeTag(types.Typ[types.String]), typeTag(types.Typ[types.Bool]), typeTag(types.Typ[types.Float64]),
		typeTag(types.NewMap(types.Typ[types.String], empty)), typeTag(types.NewSlice(empty)),
	}}
	t.emit(ir.Instr{Dst: t.def(x), Op: op, Args: []ir.Var{data, p}})
	t.out.UsesJSON = true
	return true, nil
}

// jsonMarshal lowers json.Marshal(v) by the JSON description of v's type
// (the type stored in the interface argument, when the call site shows it).
func (t *fnState) jsonMarshal(x *ssa.Call) error {
	arg := x.Call.Args[0]
	ty := arg.Type()
	if mi, ok := arg.(*ssa.MakeInterface); ok {
		arg, ty = mi.X, mi.X.Type()
	}
	v, err := t.val(arg)
	if err != nil {
		return err
	}
	t.emit(ir.Instr{Dst: t.def(x), Op: ir.JSONMarshal{Root: t.prog.json.desc(ty)}, Args: []ir.Var{v}})
	t.out.UsesJSON = true
	return nil
}

// jsonEncoder lowers a json.Encoder that writes to a strings.Builder and
// is used only for SetEscapeHTML with a constant and Encode (the
// encode-a-string idiom): NewEncoder(&b) stands for the builder's pointer,
// SetEscapeHTML does nothing, and Encode appends the JSON to the builder.
func (t *fnState) jsonEncoder(x *ssa.Call, callee *ssa.Function) (bool, error) {
	switch callee.String() {
	case "encoding/json.NewEncoder":
		b, _, ok := builderEncoder(x)
		if !ok {
			return false, nil
		}
		p, err := t.val(b)
		if err != nil {
			return true, err
		}
		t.vars[x] = p
		return true, nil
	case "(*encoding/json.Encoder).SetEscapeHTML":
		nc, ok := x.Call.Args[0].(*ssa.Call)
		if !ok {
			return false, nil
		}
		_, _, ok = builderEncoder(nc)
		return ok, nil
	case "(*encoding/json.Encoder).Encode":
		nc, ok := x.Call.Args[0].(*ssa.Call)
		if !ok {
			return false, nil
		}
		b, html, ok := builderEncoder(nc)
		if !ok {
			return false, nil
		}
		arg := x.Call.Args[1]
		ty := arg.Type()
		if mi, ok := arg.(*ssa.MakeInterface); ok {
			arg, ty = mi.X, mi.X.Type()
		}
		p, err := t.val(b)
		if err != nil {
			return true, err
		}
		v, err := t.val(arg)
		if err != nil {
			return true, err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.JSONEncode{Root: t.prog.json.desc(ty), HTML: html}, Args: []ir.Var{p, v}})
		t.out.UsesJSON = true
		return true, nil
	}
	return false, nil
}

// builderEncoder says whether nc is json.NewEncoder(&b) for a
// strings.Builder b whose result only SetEscapeHTML(constant) and Encode
// use, and returns &b and the HTML escaping its Encode calls get (Go's
// default, true, unless every SetEscapeHTML call says false).
func builderEncoder(nc *ssa.Call) (ssa.Value, bool, bool) {
	f := nc.Call.StaticCallee()
	if f == nil || f.String() != "encoding/json.NewEncoder" || len(nc.Call.Args) != 1 {
		return nil, false, false
	}
	mi, ok := nc.Call.Args[0].(*ssa.MakeInterface)
	if !ok {
		return nil, false, false
	}
	pt, ok := mi.X.Type().(*types.Pointer)
	if !ok || !isStringsBuilder(pt.Elem()) {
		return nil, false, false
	}
	html, set := true, false
	var sets, encodes []*ssa.Call
	for _, r := range *nc.Referrers() {
		c, ok := r.(*ssa.Call)
		if !ok || len(c.Call.Args) == 0 || c.Call.Args[0] != nc || c.Call.StaticCallee() == nil {
			return nil, false, false
		}
		switch c.Call.StaticCallee().String() {
		case "(*encoding/json.Encoder).Encode":
			encodes = append(encodes, c)
		case "(*encoding/json.Encoder).SetEscapeHTML":
			sets = append(sets, c)
			k, ok := c.Call.Args[1].(*ssa.Const)
			if !ok || k.Value == nil {
				return nil, false, false
			}
			v := constant.BoolVal(k.Value)
			if set && v != html {
				return nil, false, false
			}
			html, set = v, true
		default:
			return nil, false, false
		}
	}
	// Every Encode must come after every SetEscapeHTML.
	for _, st := range sets {
		for _, e := range encodes {
			if !precedes(st, e) {
				return nil, false, false
			}
		}
	}
	return mi.X, html, true
}

// precedes says whether instruction a runs before b on every path to b:
// earlier in the same block, or in a block that dominates b's.
func precedes(a, b ssa.Instruction) bool {
	if a.Block() != b.Block() {
		return a.Block().Dominates(b.Block())
	}
	for _, in := range a.Block().Instrs {
		switch in {
		case a:
			return true
		case b:
			return false
		}
	}
	return false
}

// jsonDecode lowers json.Unmarshal(data, &v) for a v of any type by its JSON
// description (structs, slices, maps, pointers, SDK types).
func (t *fnState) jsonDecode(x *ssa.Call) (bool, error) {
	mi, ok := x.Call.Args[1].(*ssa.MakeInterface)
	if !ok {
		return false, nil
	}
	pt, ok := mi.X.Type().Underlying().(*types.Pointer)
	if !ok {
		return false, nil
	}
	data, err := t.val(x.Call.Args[0])
	if err != nil {
		return true, err
	}
	p, err := t.val(mi.X)
	if err != nil {
		return true, err
	}
	t.emit(ir.Instr{Dst: t.def(x), Op: ir.JSONDecode{Root: t.prog.json.desc(pt.Elem())}, Args: []ir.Var{data, p}})
	t.out.UsesJSON = true
	// The keys the decoded type reads, nearest first, ahead of the other
	// keys: JSON inputs are built from them.
	root := t.prog.json.desc(pt.Elem())
	var keys []string
	for _, k := range append(t.prog.json.keysOf(root), t.prog.json.keys...) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, k := range t.out.MapKeys {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	t.out.MapKeys = keys
	return true, nil
}

// parseFloat lowers strconv.ParseFloat(s, 64): decided by the model for
// decimal syntax away from the float64 limit, havoc otherwise.
func (t *fnState) parseFloat(x *ssa.Call) error {
	s, err := t.val(x.Call.Args[0])
	if err != nil {
		return err
	}
	dst := t.def(x)
	known := t.fresh()
	t.emit(ir.Instr{Dst: known, Op: ir.Simple("strParseFloatKnown"), Args: []ir.Var{s}})
	yes := len(t.out.Blocks)
	no, join := yes+1, yes+2
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: known, T: yes, F: no}
	name := t.cur.Name
	t.out.Blocks = append(t.out.Blocks,
		ir.Block{Name: name + ".parsefloat"},
		ir.Block{Name: name + ".parsefloat-unknown"},
		ir.Block{Name: name + ".parsefloat-join"})
	t.cur = &t.out.Blocks[yes]
	t.emit(ir.Instr{Dst: dst, Op: ir.Simple("strParseFloat"), Args: []ir.Var{s}})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[no]
	if err := t.havocResult(dst, x, t.f.Name()+": call strconv.ParseFloat", "unmodeled-call"); err != nil {
		return err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return nil
}

func (t *fnState) sprintf(x *ssa.Call) error {
	return t.sprintfTo(t.def(x), x.Call.Args[0], x.Call.Args[1], "call fmt.Sprintf")
}

// sprintfTo binds dst to fmt.Sprintf(format, args...) (args the variadic
// slice), havoc when the model cannot format them.
func (t *fnState) sprintfTo(dst ir.Var, format, argSlice ssa.Value, site string) error {
	f, err := t.val(format)
	if err != nil {
		return err
	}
	args, err := t.val(argSlice)
	if err != nil {
		return err
	}
	// %x formats a byte slice; the model tells one by its boxed type tag
	// (the tag of []byte as the program spells it: byte, not uint8).
	bytesTag := t.fresh()
	t.emit(ir.Instr{Dst: bytesTag, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: fmt.Sprint(typeTag(types.NewSlice(types.Universe.Lookup("byte").Type())))}}})
	// %d and %v print unsigned for the unsigned integer types the
	// program boxes (the dialect knows a boxed value by its type tag only).
	var us []ir.Val
	for _, bt := range t.prog.boxed {
		if b, ok := bt.Underlying().(*types.Basic); ok && b.Info()&types.IsUnsigned != 0 {
			us = append(us, ir.IntVal{Width: 64, Bits: fmt.Sprint(typeTag(bt))})
		}
	}
	unsigned := t.fresh()
	t.emit(ir.Instr{Dst: unsigned, Op: ir.Const{V: ir.ArrVal{Elems: us}}})
	ok := t.fresh()
	t.emit(ir.Instr{Dst: ok, Op: ir.Simple("sprintfOk"), Args: []ir.Var{f, args, bytesTag, unsigned}})
	yes := len(t.out.Blocks)
	no, join := yes+1, yes+2
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: ok, T: yes, F: no}
	name := t.cur.Name
	t.out.Blocks = append(t.out.Blocks,
		ir.Block{Name: name + ".sprintf"},
		ir.Block{Name: name + ".sprintf-unknown"},
		ir.Block{Name: name + ".sprintf-join"})
	t.cur = &t.out.Blocks[yes]
	t.emit(ir.Instr{Dst: dst, Op: ir.Simple("sprintf"), Args: []ir.Var{f, args, bytesTag, unsigned}})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[no]
	if err := t.havocInto(dst, types.Typ[types.String], t.f.Name()+": "+site, "unmodeled-call", ""); err != nil {
		return err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return nil
}

// fprintf lowers fmt.Fprintf into a *strings.Builder or a *bytes.Buffer:
// the text fmt.Sprintf would make, written with the writer's WriteString
// (which returns what Fprintf does: the byte count and a nil error). fmt
// itself is not modelled (it formats through reflect); other writers stay
// unmodelled calls.
func (t *fnState) fprintf(x *ssa.Call) (bool, error) {
	mi, ok := x.Call.Args[0].(*ssa.MakeInterface)
	if !ok {
		return false, nil
	}
	pt, ok := mi.X.Type().(*types.Pointer)
	if !ok {
		return false, nil
	}
	var xr contract.XCall
	builder := isStringsBuilder(pt.Elem())
	if !builder {
		n, ok := pt.Elem().(*types.Named)
		if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != "bytes" || n.Obj().Name() != "Buffer" {
			return false, nil
		}
		// The model of bytes.Buffer is its source (-deps).
		if xr, ok = t.prog.xrefs["(*bytes.Buffer).WriteString"]; !ok || xr.Package == t.prog.prog.Package {
			return false, nil
		}
	}
	w, err := t.val(mi.X)
	if err != nil {
		return true, err
	}
	s := t.fresh()
	if err := t.sprintfTo(s, x.Call.Args[1], x.Call.Args[2], "call fmt.Fprintf"); err != nil {
		return true, err
	}
	if builder {
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("sbWriteString"), Args: []ir.Var{w, s}})
		return true, nil
	}
	if !slices.Contains(t.out.XCalls, xr) {
		t.out.XCalls = append(t.out.XCalls, xr)
	}
	return true, t.emitCallArgs(x, ir.Term{Kind: ir.TermXCall, Callee: xr.ID, CalleePkg: xr.Package}, "(*bytes.Buffer).WriteString", []ir.Var{w, s})
}

// ifaceAssert lowers x.(I) for an interface I: a value the model boxed
// passes when its type is among impls, the boxed types implementing I.
func (t *fnState) ifaceAssert(x *ssa.TypeAssert, impls []types.Type) error {
	v, err := t.val(x.X)
	if err != nil {
		return err
	}
	tags := make([]uint32, len(impls))
	for i, ty := range impls {
		tags[i] = typeTag(ty)
	}
	dst := t.def(x)
	return t.onKnown(x.X, v, func() error {
		t.emit(ir.Instr{Dst: dst, Op: ir.AssertIface{Tags: tags, CommaOk: x.CommaOk}, Args: []ir.Var{v}})
		return nil
	}, func() error {
		t.issue("type assertion %s on an interface value the model did not build is havoc", x.Name())
		return t.havocResult(dst, x, "type assertion", "opaque-assert")
	})
}

// invoke lowers a method call on an interface value: the method of the
// boxed value's type, among the boxed types implementing the interface
// whose method the model holds; a nil interface panics, any other value
// makes the call unmodelled. It reports false when no type qualifies.
func (t *fnState) invoke(x *ssa.Call) (bool, error) {
	fv, ok, err := t.dispatch(&x.Call)
	if !ok || err != nil {
		return ok, err
	}
	return true, t.emitCall(x, ir.Term{Kind: ir.TermCallV, FV: fv}, x.Call.Method.Name())
}

// dispatch is the func value an interface method call c calls: the method
// of the receiver's dynamic type among the modelled implementers (a Dispatch
// on its type tag). Not ok when no implementer is modelled.
func (t *fnState) dispatch(c *ssa.CallCommon) (ir.Var, bool, error) {
	iface, ok := c.Value.Type().Underlying().(*types.Interface)
	if !ok {
		return 0, false, nil
	}
	if _, ok := t.prog.implementers(iface); !ok {
		return 0, false, nil
	}
	recv, err := t.val(c.Value)
	if err != nil {
		return 0, false, err
	}
	return t.dispatchOn(recv, iface, c.Method)
}

// dispatchOn is the func value calling method m of interface iface on
// receiver recv (see dispatch).
func (t *fnState) dispatchOn(recv ir.Var, iface *types.Interface, m *types.Func) (ir.Var, bool, error) {
	impls, ok := t.prog.implementers(iface)
	if !ok {
		return 0, false, nil
	}
	var tags []uint32
	var fns []*ssa.Function
	var derefs []int // a helper's FuncId where fns has nil
	for _, ty := range impls {
		sel := t.prog.sprog.MethodSets.MethodSet(ty).Lookup(m.Pkg(), m.Name())
		if sel == nil {
			continue
		}
		fn := t.prog.sprog.MethodValue(sel)
		if fn == nil {
			continue
		}
		if _, ok := t.prog.ids[fn]; !ok {
			if xr, ok := t.prog.xrefs[fn.String()]; !ok || xr.Package == t.prog.prog.Package {
				// *T's method declared on T: go/ssa's wrapper is not
				// translated, but T's method may be.
				if id, ok := t.prog.derefMethod(ty, m); ok {
					tags = append(tags, typeTag(ty))
					fns = append(fns, nil)
					derefs = append(derefs, id)
				}
				continue
			}
		}
		tags = append(tags, typeTag(ty))
		fns = append(fns, fn)
		derefs = append(derefs, -1)
	}
	if len(fns) == 0 {
		return 0, false, nil
	}
	args := []ir.Var{recv}
	for i, fn := range fns {
		v := t.fresh()
		if fn == nil {
			if !slices.Contains(t.out.Calls, derefs[i]) {
				t.out.Calls = append(t.out.Calls, derefs[i])
			}
			t.emit(ir.Instr{Dst: v, Op: ir.FuncRef{ID: derefs[i]}})
		} else if err := t.funcValue(v, fn, nil); err != nil {
			return 0, false, err
		}
		args = append(args, v)
	}
	fv := t.fresh()
	t.emit(ir.Instr{Dst: fv, Op: ir.Dispatch{Tags: tags}, Args: args})
	return fv, true, nil
}

// derefMethod is a helper for method m of pointer type ty when m is
// declared on the element type T: it loads the receiver and calls T's
// method, as go/ssa's wrapper for (*T).m does. Not ok when ty is not a
// pointer or T's method is not modelled.
func (s *progState) derefMethod(ty types.Type, m *types.Func) (int, bool) {
	p, ok := ty.(*types.Pointer)
	if !ok {
		return 0, false
	}
	sel := s.sprog.MethodSets.MethodSet(p.Elem()).Lookup(m.Pkg(), m.Name())
	if sel == nil {
		return 0, false
	}
	vm := s.sprog.MethodValue(sel)
	if vm == nil {
		return 0, false
	}
	name := "$deref:" + vm.String()
	if id, ok := s.helpers[name]; ok {
		return id, true
	}
	n := vm.Signature.Params().Len()
	params := make([]ir.Var, n+1) // the pointer receiver, then m's parameters
	for i := range params {
		params[i] = ir.Var(i)
	}
	load, dst := ir.Var(n+1), ir.Var(n+2)
	call := ir.Term{CArgs: append([]ir.Var{load}, params[1:]...), Dst: dst, Next: 1}
	var calls []int
	var xcalls []contract.XCall
	if id, ok := s.ids[vm]; ok {
		call.Kind, call.Callee = ir.TermCall, id
		calls = []int{id}
	} else if xr, ok := s.xrefs[vm.String()]; ok && xr.Package != s.prog.Package {
		call.Kind, call.Callee, call.CalleePkg = ir.TermXCall, xr.ID, xr.Package
		xcalls = []contract.XCall{xr}
	} else {
		return 0, false
	}
	id := s.helper(name, false, false, n+3, params, []ir.Block{
		{Name: "deref", Instrs: []ir.Instr{{Dst: load, Op: ir.Simple("loadPath"), Args: []ir.Var{0}}}, Term: call},
		{Name: "ret", Term: ir.Term{Kind: ir.TermRet, V: dst}},
	})
	s.prog.Funcs[id].Calls, s.prog.Funcs[id].XCalls = calls, xcalls
	return id, true
}

// regexpCall lowers the methods of a *regexp.Regexp compiled from a
// constant pattern (MatchString, FindStringSubmatch, FindString,
// FindStringIndex), and regexp.MatchString of a constant pattern, to the
// dialect's matcher, which runs the program regexp/syntax compiles the
// pattern to (Go's own instructions and leftmost-first semantics).
func (t *fnState) regexpCall(x *ssa.Call, callee *ssa.Function) (bool, error) {
	kinds := map[string]int{"MatchString": 0, "FindStringSubmatch": 1, "FindString": 2, "FindStringIndex": 3,
		"FindAllString": 4, "FindAllStringSubmatch": 5, "FindAllStringIndex": 6}
	var pat string
	var subject ssa.Value
	pkgFunc := false
	switch name := callee.String(); {
	case name == "regexp.MatchString" && len(x.Call.Args) == 2:
		k, ok := x.Call.Args[0].(*ssa.Const)
		if !ok || k.Value == nil || k.Value.Kind() != constant.String {
			return false, nil
		}
		pat, subject, pkgFunc = constant.StringVal(k.Value), x.Call.Args[1], true
	case strings.HasPrefix(name, "(*regexp.Regexp).") && len(x.Call.Args) == 2+boolInt(strings.Contains(callee.Name(), "All")):
		if _, ok := kinds[callee.Name()]; !ok {
			return false, nil
		}
		p, ok := regexpPattern(x.Call.Args[0])
		if !ok {
			// A regexp passed in (a helper's parameter): one of the
			// package's constant patterns, picked at run time.
			return t.regexpDynamic(x, callee, kinds[callee.Name()])
		}
		pat, subject = p, x.Call.Args[1]
	default:
		return false, nil
	}
	prog, ok := compileRegexp(pat)
	if !ok {
		return false, nil
	}
	// Strings the pattern matches join the search's pool: random strings
	// rarely match, and the code behind a match is what to explore.
	for _, sample := range regexpSamples(pat) {
		if !slices.Contains(t.out.StrConsts, sample) {
			t.out.StrConsts = append(t.out.StrConsts, sample)
		}
	}
	sv, err := t.val(subject)
	if err != nil {
		return true, err
	}
	kind := kinds[callee.Name()]
	if pkgFunc {
		kind = 0
	}
	prog.Kind = kind
	if !pkgFunc {
		args := []ir.Var{sv}
		if len(x.Call.Args) == 3 { // FindAll*: the limit n
			nv, err := t.val(x.Call.Args[2])
			if err != nil {
				return true, err
			}
			args = append(args, nv)
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: prog, Args: args})
		return true, nil
	}
	// regexp.MatchString(pattern, s): (matched, nil) for a valid pattern.
	m := t.fresh()
	t.emit(ir.Instr{Dst: m, Op: prog, Args: []ir.Var{sv}})
	nilErr := t.fresh()
	t.emit(ir.Instr{Dst: nilErr, Op: ir.Const{V: ir.ErrVal{}}})
	t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("mkTuple"), Args: []ir.Var{m, nilErr}})
	return true, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// regexpSamples writes up to two strings the pattern matches: the
// shortest (repetitions their minimum, the first alternative, the low end
// of a class: "0" for \d), and one taking each optional part once, the
// second alternative and the high end of a class.
func regexpSamples(pat string) []string {
	re, err := syntax.Parse(pat, syntax.Perl)
	if err != nil {
		return nil
	}
	re = re.Simplify()
	compiled, err := regexp.Compile(pat)
	if err != nil {
		return nil
	}
	var out []string
	for _, more := range []bool{false, true} {
		var b strings.Builder
		var gen func(r *syntax.Regexp)
		gen = func(r *syntax.Regexp) {
			if b.Len() > 64 {
				return
			}
			switch r.Op {
			case syntax.OpLiteral:
				for _, c := range r.Rune {
					b.WriteRune(c)
				}
			case syntax.OpCharClass:
				if len(r.Rune) >= 2 {
					c := r.Rune[0]
					if more {
						c = r.Rune[1]
					}
					b.WriteRune(c)
				}
			case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
				b.WriteByte('a')
			case syntax.OpCapture, syntax.OpPlus:
				gen(r.Sub[0])
			case syntax.OpStar, syntax.OpQuest:
				if more {
					gen(r.Sub[0])
				}
			case syntax.OpRepeat:
				n := r.Min
				if more && (r.Max < 0 || r.Max > n) {
					n++
				}
				for i := 0; i < n && i < 8; i++ {
					gen(r.Sub[0])
				}
			case syntax.OpConcat:
				for _, sub := range r.Sub {
					gen(sub)
				}
			case syntax.OpAlternate:
				if more && len(r.Sub) > 1 {
					gen(r.Sub[1])
				} else {
					gen(r.Sub[0])
				}
			}
		}
		gen(re)
		if s := b.String(); compiled.MatchString(s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// replacerPairs is the (old, new) pairs of a strings.Replacer made by
// strings.NewReplacer with constant arguments: v is that call, or a load of
// a package variable its initializer sets to it. A pair with an empty old
// string (Go's special case) is not taken.
func replacerPairs(v ssa.Value) ([][2]string, bool) {
	if init, ok := loadedInit(v); ok {
		v = init
	}
	c, ok := v.(*ssa.Call)
	if !ok || len(c.Call.Args) != 1 {
		return nil, false
	}
	if f := c.Call.StaticCallee(); f == nil || f.String() != "strings.NewReplacer" {
		return nil, false
	}
	sl, ok := c.Call.Args[0].(*ssa.Slice)
	if !ok {
		return nil, false
	}
	a, ok := sl.X.(*ssa.Alloc)
	if !ok {
		return nil, false
	}
	args := map[int64]string{}
	for _, r := range *a.Referrers() {
		if r == sl {
			continue
		}
		ia, ok := r.(*ssa.IndexAddr)
		if !ok {
			return nil, false
		}
		k, ok := ia.Index.(*ssa.Const)
		if !ok || k.Value == nil {
			return nil, false
		}
		i, _ := constant.Int64Val(k.Value)
		for _, rr := range *ia.Referrers() {
			st, ok := rr.(*ssa.Store)
			if !ok {
				return nil, false
			}
			sc, ok := st.Val.(*ssa.Const)
			if !ok || sc.Value == nil || sc.Value.Kind() != constant.String {
				return nil, false
			}
			args[i] = constant.StringVal(sc.Value)
		}
	}
	n := len(args)
	if n == 0 || n%2 != 0 {
		return nil, false
	}
	var pairs [][2]string
	for i := 0; i < n; i += 2 {
		o, ok1 := args[int64(i)]
		nw, ok2 := args[int64(i+1)]
		if !ok1 || !ok2 || o == "" {
			return nil, false
		}
		pairs = append(pairs, [2]string{o, nw})
	}
	return pairs, true
}

// replacerFunc is the pairs of a function value bound to a constant
// Replacer's Replace (var Escape = strings.NewReplacer(...).Replace),
// loaded from a package variable or made in place.
func replacerFunc(v ssa.Value) ([][2]string, bool) {
	if init, ok := loadedInit(v); ok {
		v = init
	}
	mc, ok := v.(*ssa.MakeClosure)
	if !ok || len(mc.Bindings) != 1 {
		return nil, false
	}
	f, ok := mc.Fn.(*ssa.Function)
	if !ok || f.Name() != "Replace$bound" || !strings.Contains(f.String(), "strings.Replacer") {
		return nil, false
	}
	return replacerPairs(mc.Bindings[0])
}

// replace emits the strReplacer op for s.
func (t *fnState) replace(dst ir.Var, pairs [][2]string, s ssa.Value) error {
	sv, err := t.val(s)
	if err != nil {
		return err
	}
	u := t.fresh()
	t.emit(ir.Instr{Dst: u, Op: ir.Const{V: ir.UnitVal{}}})
	t.emit(ir.Instr{Dst: dst, Op: ir.StrReplacer{Pairs: pairs}, Args: []ir.Var{u, sv}})
	return nil
}

// regexpTag tags the model of a *regexp.Regexp.
var regexpTag = func() uint32 {
	h := fnv.New32a()
	h.Write([]byte("*regexp.Regexp"))
	return h.Sum32()
}()

// regexpID identifies a constant pattern.
func regexpID(pat string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(pat))
	return h.Sum32()
}

// regexpValue is the model of a regexp compiled from a constant pattern:
// boxed with the identity of its pattern, so that a method called where
// the pattern is not known statically can pick its program at run time.
func regexpValue(pat string) ir.Val {
	return ir.Boxed{Tag: regexpTag, V: ir.IntVal{Width: 64, Bits: fmt.Sprint(regexpID(pat))}}
}

// maxRegexps bounds the programs a call on an unknown regexp embeds.
const maxRegexps = 12

// packageRegexps lists the constant patterns the package compiles with
// regexp.MustCompile (in its functions and its initializer).
func (s *progState) packageRegexps(pkg *ssa.Package) []string {
	if pats, ok := s.regexps[pkg]; ok {
		return pats
	}
	var pats []string
	for f := range ssautil.AllFunctions(s.sprog) {
		if f.Pkg != pkg {
			continue
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if c, ok := in.(*ssa.Call); ok {
					if p, ok := regexpPattern(c); ok && !slices.Contains(pats, p) {
						pats = append(pats, p)
					}
				}
			}
		}
	}
	sort.Strings(pats)
	if s.regexps == nil {
		s.regexps = map[*ssa.Package][]string{}
	}
	s.regexps[pkg] = pats
	return pats
}

// regexpDynamic lowers a method call on a regexp the call site does not
// know: when it is one of the package's constant patterns (at most
// maxRegexps), that pattern's program runs; otherwise (a pattern built at
// run time) the result is havoc.
func (t *fnState) regexpDynamic(x *ssa.Call, callee *ssa.Function, kind int) (bool, error) {
	if t.f.Pkg == nil {
		return false, nil
	}
	pats := t.prog.packageRegexps(t.f.Pkg)
	if len(pats) == 0 || len(pats) > maxRegexps {
		return false, nil
	}
	op := ir.ReFindIn{Kind: kind}
	var ids []string
	for _, p := range pats {
		prog, ok := compileRegexp(p)
		if !ok {
			return false, nil
		}
		prog.Kind = kind
		op.Progs = append(op.Progs, ir.ReProg{ID: regexpID(p), Prog: prog})
		ids = append(ids, fmt.Sprint(regexpID(p)))
		for _, sample := range regexpSamples(p) {
			if !slices.Contains(t.out.StrConsts, sample) {
				t.out.StrConsts = append(t.out.StrConsts, sample)
			}
		}
	}
	re, err := t.val(x.Call.Args[0])
	if err != nil {
		return true, err
	}
	sv, err := t.val(x.Call.Args[1])
	if err != nil {
		return true, err
	}
	dst := t.def(x)
	known := t.fresh()
	t.emit(ir.Instr{Dst: known, Op: ir.ReIn{IDs: ids}, Args: []ir.Var{re}})
	yes := len(t.out.Blocks)
	no, join := yes+1, yes+2
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: known, T: yes, F: no}
	name := t.cur.Name
	t.out.Blocks = append(t.out.Blocks, ir.Block{Name: name + ".re-known"}, ir.Block{Name: name + ".re-unknown"}, ir.Block{Name: name + ".re-join"})
	t.cur = &t.out.Blocks[yes]
	args := []ir.Var{re, sv}
	if len(x.Call.Args) == 3 { // FindAll*: the limit n
		nv, err := t.val(x.Call.Args[2])
		if err != nil {
			return true, err
		}
		args = append(args, nv)
	}
	t.emit(ir.Instr{Dst: dst, Op: op, Args: args})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[no]
	t.issue("%s on a regexp that may not be one of the package's constant patterns is havoc", callee.Name())
	if err := t.havocResult(dst, x, callee.Name(), "unmodeled-call"); err != nil {
		return true, err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return true, nil
}

// compileRegexp compiles a pattern as regexp.Compile does (Perl syntax,
// simplified) into the dialect's instructions; a rune with case folding
// lists its whole fold orbit.
func compileRegexp(pat string) (ir.ReFind, bool) {
	re, err := syntax.Parse(pat, syntax.Perl)
	if err != nil {
		return ir.ReFind{}, false
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		return ir.ReFind{}, false
	}
	out := ir.ReFind{Start: prog.Start, NCap: prog.NumCap}
	for _, in := range prog.Inst {
		var li string
		switch in.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			li = fmt.Sprintf(".alt %d %d", in.Out, in.Arg)
		case syntax.InstCapture:
			li = fmt.Sprintf(".cap %d %d", in.Arg, in.Out)
		case syntax.InstEmptyWidth:
			li = fmt.Sprintf(".empty %d %d", in.Arg, in.Out)
		case syntax.InstMatch:
			li = ".match"
		case syntax.InstFail:
			li = ".fail"
		case syntax.InstNop:
			li = fmt.Sprintf(".nop %d", in.Out)
		case syntax.InstRuneAny:
			li = fmt.Sprintf(".any true %d", in.Out)
		case syntax.InstRuneAnyNotNL:
			li = fmt.Sprintf(".any false %d", in.Out)
		case syntax.InstRune, syntax.InstRune1:
			var ranges []string
			if len(in.Rune) == 1 {
				r0 := in.Rune[0]
				rs := []rune{r0}
				if in.Op == syntax.InstRune && syntax.Flags(in.Arg)&syntax.FoldCase != 0 {
					for r := unicode.SimpleFold(r0); r != r0; r = unicode.SimpleFold(r) {
						rs = append(rs, r)
					}
				}
				for _, r := range rs {
					ranges = append(ranges, fmt.Sprintf("(%d, %d)", r, r))
				}
			} else {
				for i := 0; i+1 < len(in.Rune); i += 2 {
					ranges = append(ranges, fmt.Sprintf("(%d, %d)", in.Rune[i], in.Rune[i+1]))
				}
			}
			li = fmt.Sprintf(".rune [%s] %d", strings.Join(ranges, ", "), in.Out)
		default:
			return ir.ReFind{}, false
		}
		out.Insts = append(out.Insts, li)
	}
	return out, true
}

// compiledPattern is the regexp v holds when regexpPattern finds its
// constant pattern and the pattern compiles.
func compiledPattern(v ssa.Value) (*regexp.Regexp, bool) {
	pat, ok := regexpPattern(v)
	if !ok {
		return nil, false
	}
	re, err := regexp.Compile(pat)
	return re, err == nil
}

// cellGlobal says whether package variable g is a cell (ir.GlobalAddr): its
// type holds synchronisation state by value (a mutex, read-write mutex, wait
// group, once or channel, also in a struct field or array element) and no
// initializer or init function sets it, so it starts at its zero value (or
// at the one constant its initializer stores, or, for an interface its
// initializer builds, at an opaque non-nil value). The
// goroutines of a run share it, so loads and stores go through the cell; any
// other package variable keeps the rules of docs/semantics.md §4.7.
//
// In a package that starts goroutines, a package variable the package's
// functions write is a cell too: the goroutines share it, and its races
// are the race detector's (C2).
func (s *progState) cellGlobal(g *ssa.Global) (uint32, ir.Val, bool) {
	pt, ok := g.Type().(*types.Pointer)
	if !ok || g.Pkg == nil || !(holdsSync(pt.Elem()) || s.sharedGlobals[g] || s.initCells[g]) {
		return 0, nil, false
	}
	var start ir.Val
	if initFn := g.Pkg.Func("init"); initFn != nil && !s.initCells[g] {
		for _, f := range initFuncs(initFn) {
			for _, b := range f.Blocks {
				for _, in := range b.Instrs {
					st, ok := in.(*ssa.Store)
					if !ok || st.Addr != g {
						continue
					}
					if start != nil {
						return 0, nil, false
					}
					if _, iface := pt.Elem().Underlying().(*types.Interface); iface && builtIface(st.Val, 2) {
						// An interface the initializer builds (var logger
						// Logger = &std{}, or = NewStd() returning one):
						// non-nil, of a value the model does not know;
						// calls on it are havoc, but writes and reads are
						// the cell's.
						start = ir.OpaqueNonNil{}
						continue
					}
					// One constant initializer (var d = 10 * time.Second):
					// the cell starts at it.
					c, ok := st.Val.(*ssa.Const)
					if !ok {
						return 0, nil, false
					}
					v, err := constVal(c)
					if _, str := v.(ir.StrConst); err != nil || str {
						return 0, nil, false
					}
					start = v
				}
			}
		}
	}
	if start == nil {
		zero, ok := zeroVal(pt.Elem())
		if !ok {
			return 0, nil, false
		}
		start = zero
	}
	h := fnv.New32a()
	h.Write([]byte(g.Pkg.Pkg.Path() + "." + g.Name()))
	return h.Sum32(), start, true
}

// builtIface says whether v is an interface value built from a concrete
// one, so never nil: a MakeInterface, or a call of a function each of whose
// returns is one (up to depth calls deep).
func builtIface(v ssa.Value, depth int) bool {
	switch x := v.(type) {
	case *ssa.MakeInterface:
		return true
	case *ssa.Call:
		fn := x.Call.StaticCallee()
		if depth == 0 || fn == nil || len(fn.Blocks) == 0 || fn.Signature.Results().Len() != 1 {
			return false
		}
		returns := 0
		for _, b := range fn.Blocks {
			if r, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok {
				if !builtIface(r.Results[0], depth-1) {
					return false
				}
				returns++
			}
		}
		return returns > 0
	}
	return false
}

// unit is a package the compile stage translates, with its functions.
type unit struct {
	path string
	fns  []*ssa.Function
}

// cellPlan is which package variables are cells, decided once for every
// linked package so that they all agree (#55).
type cellPlan struct {
	// concurrent: the packages whose code runs in goroutines: the ones
	// whose functions start one, and, in a program whose compiled
	// packages start one, every dependency package.
	concurrent map[string]bool
	// used: the package variables some linked package's functions use.
	used map[*ssa.Global]bool
	// shared: the variables of a concurrent package that some linked
	// package's functions write.
	shared map[*ssa.Global]bool
	// init: the variables of a concurrent package its initializer builds
	// (initCells).
	init map[*ssa.Global]bool
	// noInit: the variables whose part of the initializer's slice does not
	// translate (dropUntranslatable); they are not init cells.
	noInit map[*ssa.Global]bool
}

// initUsed is used without the variables in noInit: the candidates for
// init cells.
func (plan *cellPlan) initUsed() map[*ssa.Global]bool {
	if len(plan.noInit) == 0 {
		return plan.used
	}
	out := map[*ssa.Global]bool{}
	for g := range plan.used {
		if !plan.noInit[g] {
			out[g] = true
		}
	}
	return out
}

func planCells(units []unit, mainPaths map[string]bool) *cellPlan {
	plan := &cellPlan{concurrent: map[string]bool{}, used: map[*ssa.Global]bool{},
		shared: map[*ssa.Global]bool{}, init: map[*ssa.Global]bool{}, noInit: map[*ssa.Global]bool{}}
	mainSpawns := false
	for _, u := range units {
		if sharedGlobals(u.fns) != nil {
			plan.concurrent[u.path] = true
			mainSpawns = mainSpawns || mainPaths[u.path]
		}
	}
	for _, u := range units {
		if mainSpawns && !mainPaths[u.path] {
			plan.concurrent[u.path] = true
		}
	}
	written := map[*ssa.Global]bool{}
	for _, u := range units {
		for _, f := range u.fns {
			if isInit(f.Name()) && f.Signature.Recv() == nil {
				continue
			}
			for _, b := range f.Blocks {
				for _, in := range b.Instrs {
					if st, ok := in.(*ssa.Store); ok {
						if g, ok := st.Addr.(*ssa.Global); ok {
							written[g] = true
						}
					}
					for _, op := range in.Operands(nil) {
						if g, ok := (*op).(*ssa.Global); ok {
							plan.used[g] = true
						}
					}
				}
			}
		}
	}
	for g := range written {
		if g.Pkg != nil && plan.concurrent[g.Pkg.Pkg.Path()] {
			plan.shared[g] = true
		}
	}
	return plan
}

// collectInits records the variables each concurrent package's
// initializer builds, once its functions are final.
func (plan *cellPlan) collectInits(units []unit) {
	plan.init = map[*ssa.Global]bool{}
	for _, u := range units {
		if !plan.concurrent[u.path] {
			continue
		}
		_, keep := initCells(u.path, u.fns, plan.initUsed())
		for in := range keep {
			if st, ok := in.(*ssa.Store); ok {
				if g, ok := st.Addr.(*ssa.Global); ok {
					plan.init[g] = true
				}
			}
		}
	}
}

// dropUntranslatable translates each package initializer's slice ahead
// of time. Where an instruction of it does not translate, the variables
// whose own slice holds that instruction leave the init cells (noInit),
// all of the package's when none does, and the slice is taken again
// without them: one variable the model cannot build (a large array)
// does not leave the package's other cells at their zero values.
func (plan *cellPlan) dropUntranslatable(units []unit, xrefs map[string]contract.XCall, zeros map[*ssa.Global]ssa.Value, sprog *ssa.Program, boxed []types.Type) {
	changed := false
	for _, u := range units {
		if !plan.concurrent[u.path] {
			continue
		}
		for {
			initFn, keep := initCells(u.path, u.fns, plan.initUsed())
			if initFn == nil {
				break
			}
			bad, ok := trialInit(u.path, u.fns, initFn, keep, xrefs, zeros, sprog, boxed, plan)
			if ok {
				break
			}
			var cells []*ssa.Global
			for in := range keep {
				if st, ok := in.(*ssa.Store); ok {
					if g, ok := st.Addr.(*ssa.Global); ok && plan.init[g] && !plan.noInit[g] && !slices.Contains(cells, g) {
						cells = append(cells, g)
					}
				}
			}
			dropped := false
			for _, g := range cells {
				if _, own := initCells(u.path, u.fns, map[*ssa.Global]bool{g: true}); bad == nil || own[bad] {
					plan.noInit[g], dropped = true, true
				}
			}
			if !dropped {
				for _, g := range cells {
					plan.noInit[g] = true
				}
			}
			changed = true
			if len(cells) == 0 {
				break
			}
		}
	}
	if changed {
		plan.collectInits(units)
	}
}

// trialInit translates a package initializer's slice and says whether it
// translates; when not, which instruction failed (nil if it cannot say).
func trialInit(pkgPath string, fns []*ssa.Function, initFn *ssa.Function, keep map[ssa.Instruction]bool, xrefs map[string]contract.XCall, zeros map[*ssa.Global]ssa.Value, sprog *ssa.Program, boxed []types.Type, plan *cellPlan) (ssa.Instruction, bool) {
	st := newProgState(pkgPath, fns, xrefs, zeros, sprog, boxed, plan)
	for _, f := range initFuncs(initFn) {
		out := &ir.Func{Name: f.Name(), GoSig: goSig(f), Init: true}
		st.prog.Funcs = append(st.prog.Funcs, out)
		t := &fnState{prog: st, f: f, out: out, vars: map[ssa.Value]ir.Var{}, blockID: map[*ssa.BasicBlock]int{}, keep: keep}
		if err := t.translate(); err != nil {
			var ie *instrError
			if errors.As(err, &ie) && keep[ie.in] {
				return ie.in, false
			}
			return nil, false
		}
	}
	return nil, true
}

// sharedGlobals are the package variables written by fns (and the
// closures in them) when one of them starts a goroutine; none otherwise.
func sharedGlobals(fns []*ssa.Function) map[*ssa.Global]bool {
	written := map[*ssa.Global]bool{}
	spawns := false
	var visit func(f *ssa.Function)
	visit = func(f *ssa.Function) {
		if isInit(f.Name()) && f.Signature.Recv() == nil {
			return
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch x := in.(type) {
				case *ssa.Go:
					spawns = true
				case *ssa.Store:
					if g, ok := x.Addr.(*ssa.Global); ok {
						written[g] = true
					}
				}
			}
		}
		for _, a := range f.AnonFuncs {
			visit(a)
		}
	}
	for _, f := range fns {
		visit(f)
	}
	if !spawns {
		return nil
	}
	return written
}

// isGuard says whether u loads the package initializer's init$guard.
func isGuard(u *ssa.UnOp) bool {
	g, ok := u.X.(*ssa.Global)
	return ok && u.Op == token.MUL && g.Name() == "init$guard"
}

// initCells finds, in a package whose functions start goroutines, or a
// dependency of a program whose compiled packages do (concurrent), the
// package variables its functions use that the package
// initializer sets, once, to a value it builds: a new object, map, slice
// or channel, made in place or by a call of a function of the package (var
// reg = &registry{m: map[string]int{}}, var reg = newRegistry()). The
// goroutines share what the initializer built, so each such variable is a
// cell that starts at its zero value, and the search runs the part of the
// package initializer that sets it before the trials (#55). It returns the
// package initializer and that part, its slice: the stores to those
// variables, every instruction of the initializer they depend on, the
// instructions that write into what those build (a field, an element, a
// map entry, a call that takes it), and the initializer's control flow.
// Nil when there is no such variable.
func initCells(pkgPath string, fns []*ssa.Function, used map[*ssa.Global]bool) (*ssa.Function, map[ssa.Instruction]bool) {
	var spkg *ssa.Package
	for _, f := range fns {
		if f.Pkg != nil && f.Pkg.Pkg.Path() == pkgPath {
			spkg = f.Pkg
			break
		}
	}
	if spkg == nil {
		return nil, nil
	}
	initFn := spkg.Func("init")
	if initFn == nil {
		return nil, nil
	}
	stores := map[*ssa.Global][]*ssa.Store{}
	for _, f := range initFuncs(initFn) {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if st, ok := in.(*ssa.Store); ok {
					if g, ok := st.Addr.(*ssa.Global); ok && used[g] {
						stores[g] = append(stores[g], st)
					}
				}
			}
		}
	}
	var roots []ssa.Instruction
	cells := map[*ssa.Global]bool{}
	for g, sts := range stores {
		if _, iface := g.Type().(*types.Pointer).Elem().Underlying().(*types.Interface); iface {
			continue
		}
		if len(sts) == 1 && builtValue(sts[0].Val, spkg) {
			roots = append(roots, sts[0])
			cells[g] = true
		}
	}
	if len(roots) == 0 {
		return nil, nil
	}
	inits := initFuncs(initFn)
	// What the init functions do with a variable after setting it
	// (reg = make(map[string]int); reg["x"] = 1 in a func init()).
	for _, f := range inits {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if u, ok := in.(*ssa.UnOp); ok && u.Op == token.MUL {
					if g, ok := u.X.(*ssa.Global); ok && cells[g] {
						roots = append(roots, in)
					}
				}
			}
		}
	}
	keep := map[ssa.Instruction]bool{}
	var work []ssa.Instruction
	add := func(in ssa.Instruction) {
		if slices.Contains(inits, in.Parent()) && !keep[in] {
			keep[in] = true
			work = append(work, in)
		}
	}
	for _, r := range roots {
		add(r)
	}
	closure := func() {
		for len(work) > 0 {
			in := work[len(work)-1]
			work = work[:len(work)-1]
			for _, op := range in.Operands(nil) {
				if d, ok := (*op).(ssa.Instruction); ok {
					add(d)
				}
			}
			v, ok := in.(ssa.Value)
			if !ok || v.Referrers() == nil {
				continue
			}
			for _, r := range *v.Referrers() {
				switch x := r.(type) {
				case *ssa.FieldAddr, *ssa.IndexAddr, *ssa.Slice, ssa.CallInstruction:
					add(r)
				case *ssa.Store:
					if x.Addr == v {
						add(r)
					}
				case *ssa.MapUpdate:
					if x.Map == v {
						add(r)
					}
				}
			}
		}
	}
	closure()
	// The control flow of each init function the slice is in.
	for _, f := range inits {
		in := false
		for k := range keep {
			if k.Parent() == f {
				in = true
				break
			}
		}
		if !in {
			continue
		}
		for _, b := range f.Blocks {
			for _, i := range b.Instrs {
				if _, phi := i.(*ssa.Phi); phi {
					add(i)
				}
			}
			add(b.Instrs[len(b.Instrs)-1])
		}
	}
	closure()
	return initFn, keep
}

// withInitCallees adds to fns the functions of their package that the
// package initializer's slice (initCells) calls, a generic constructor's
// instance included, so that the slice runs them.
func withInitCallees(pkgPath string, fns []*ssa.Function, used map[*ssa.Global]bool) []*ssa.Function {
	initFn, keep := initCells(pkgPath, fns, used)
	if initFn == nil {
		return fns
	}
	seen := map[*ssa.Function]bool{}
	for _, f := range fns {
		seen[f] = true
	}
	for _, b := range slices.Concat(func() [][]*ssa.BasicBlock {
		var bs [][]*ssa.BasicBlock
		for _, f := range initFuncs(initFn) {
			bs = append(bs, f.Blocks)
		}
		return bs
	}()...) {
		for _, in := range b.Instrs {
			c, ok := in.(*ssa.Call)
			if !ok || !keep[in] {
				continue
			}
			fn := c.Call.StaticCallee()
			if fn == nil || seen[fn] || len(fn.Blocks) == 0 || isInit(fn.Name()) {
				continue
			}
			if o := fn.Origin(); fn.Pkg != initFn.Pkg && (o == nil || o.Pkg != initFn.Pkg) {
				continue
			}
			seen[fn] = true
			fns = append(fns, fn)
		}
	}
	return fns
}

// builtValue says whether v is a value an initializer builds: a new
// object, map, slice or channel, or the result of a call of a function of
// package spkg (or of an instance of a generic one).
func builtValue(v ssa.Value, spkg *ssa.Package) bool {
	switch x := v.(type) {
	case *ssa.Alloc:
		return x.Heap
	case *ssa.MakeMap, *ssa.MakeSlice, *ssa.MakeChan:
		return true
	case *ssa.Slice:
		return builtValue(x.X, spkg)
	case *ssa.Call:
		fn := x.Call.StaticCallee()
		if fn != nil && fn.Origin() != nil {
			fn = fn.Origin()
		}
		return fn != nil && fn.Pkg == spkg && fn.Parent() == nil && !isInit(fn.Name())
	}
	return false
}

// holdsSync says whether a value of type t holds synchronisation state
// itself (not through a pointer).
func holdsSync(t types.Type) bool {
	if n, ok := t.(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "sync" {
		switch n.Obj().Name() {
		case "Mutex", "RWMutex", "WaitGroup", "Once":
			return true
		}
	}
	switch u := t.Underlying().(type) {
	case *types.Chan:
		return true
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if holdsSync(u.Field(i).Type()) {
				return true
			}
		}
	case *types.Array:
		return holdsSync(u.Elem())
	}
	return false
}

// poolGet is the package variable a call is (*sync.Pool).Get on, or nil.
func poolGet(c ssa.CallCommon) *ssa.Global {
	if fn := c.StaticCallee(); fn == nil || fn.String() != "(*sync.Pool).Get" || len(c.Args) != 1 {
		return nil
	}
	g, _ := c.Args[0].(*ssa.Global)
	return g
}

// poolNew is the function the initializer of g, a package variable of
// type sync.Pool, stores in its New field (var ppFree = sync.Pool{New:
// func() any { return new(pp) }}), or nil (#105).
func poolNew(g *ssa.Global) *ssa.Function {
	if g.Pkg == nil {
		return nil
	}
	initFn := g.Pkg.Func("init")
	if initFn == nil {
		return nil
	}
	// The literal is either stored field by field into g, or built in a
	// local (complit) that is then copied into g.
	bases := map[ssa.Value]bool{g: true}
	for _, f := range initFuncs(initFn) {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if st, ok := in.(*ssa.Store); ok && st.Addr == g {
					if u, ok := st.Val.(*ssa.UnOp); ok && u.Op == token.MUL {
						if a, ok := u.X.(*ssa.Alloc); ok {
							bases[a] = true
						}
					}
				}
			}
		}
	}
	for _, f := range initFuncs(initFn) {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				st, ok := in.(*ssa.Store)
				if !ok {
					continue
				}
				fa, ok := st.Addr.(*ssa.FieldAddr)
				if !ok || !bases[fa.X] {
					continue
				}
				if s, ok := fa.X.Type().(*types.Pointer).Elem().Underlying().(*types.Struct); !ok || s.Field(fa.Field).Name() != "New" {
					continue
				}
				switch v := st.Val.(type) {
				case *ssa.Function:
					return v
				case *ssa.MakeClosure:
					if fn, ok := v.Fn.(*ssa.Function); ok && len(v.Bindings) == 0 {
						return fn
					}
				}
			}
		}
	}
	return nil
}

// withPoolNews adds to fns the New functions of the pools of their own
// package that they Get from (#105).
func withPoolNews(fns []*ssa.Function) []*ssa.Function {
	seen := map[*ssa.Function]bool{}
	for _, f := range fns {
		seen[f] = true
	}
	out := fns
	for _, f := range fns {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				c, ok := in.(ssa.CallInstruction)
				if !ok {
					continue
				}
				g := poolGet(*c.Common())
				if g == nil || g.Pkg != f.Pkg {
					continue
				}
				if nf := poolNew(g); nf != nil && !seen[nf] {
					seen[nf] = true
					out = append(out, nf)
				}
			}
		}
	}
	return out
}

// loadedInit is the value a package initializer, or an init function on
// every path through it, stores in the package variable v loads, when they
// store one exactly once.
func loadedInit(v ssa.Value) (ssa.Value, bool) {
	u, ok := v.(*ssa.UnOp)
	if !ok || u.Op != token.MUL {
		return nil, false
	}
	g, ok := u.X.(*ssa.Global)
	if !ok || g.Pkg == nil {
		return nil, false
	}
	initFn := g.Pkg.Func("init")
	if initFn == nil {
		return nil, false
	}
	var init ssa.Value
	for _, f := range initFuncs(initFn) {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if st, ok := in.(*ssa.Store); ok && st.Addr == g {
					if init != nil || (f != initFn && !initStore(b)) {
						return nil, false // written twice, or on some paths only
					}
					init = st.Val
				}
			}
		}
	}
	return init, init != nil
}

// initFuncs is the package initializer and the init functions it calls
// (init#1, init#2, ...: the bodies of the package's `func init()`).
func initFuncs(initFn *ssa.Function) []*ssa.Function {
	out := []*ssa.Function{initFn}
	for _, b := range initFn.Blocks {
		for _, in := range b.Instrs {
			if c, ok := in.(*ssa.Call); ok {
				if f := c.Call.StaticCallee(); f != nil && userInit(f) && f.Pkg == initFn.Pkg {
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// userInit says whether f is the body of a `func init()`.
func userInit(f *ssa.Function) bool {
	return strings.HasPrefix(f.Name(), "init#") && f.Parent() == nil && f.Synthetic == ""
}

// initStore says whether block b of an init function runs on every path
// that returns from it: it dominates every returning block. (A path that
// panics ends the program before any other code runs.)
func initStore(b *ssa.BasicBlock) bool {
	for _, r := range b.Parent().Blocks {
		if len(r.Instrs) == 0 {
			continue
		}
		if _, ok := r.Instrs[len(r.Instrs)-1].(*ssa.Return); ok && !b.Dominates(r) {
			return false
		}
	}
	return true
}

// sentinelErr is the identity of a sentinel error: v loads a package
// variable its initializer sets to errors.New or fmt.Errorf of a constant
// (io.EOF, context.Canceled, ErrNotFound), assumed not to be reassigned.
// Its model is a boxed *errors.errorString whose payload is the identity.
func sentinelErr(v ssa.Value) (ir.Val, bool) {
	if mi, ok := v.(*ssa.MakeInterface); ok {
		// var errX = &T{"..."} boxed as an error: tagged with *T, its
		// payload the identity.
		if _, _, ok := sentinelObject(mi.X); !ok || !isError(mi.Type()) {
			return nil, false
		}
		h := fnv.New32a()
		h.Write([]byte(mi.X.(*ssa.UnOp).X.(*ssa.Global).String()))
		return ir.Boxed{Tag: typeTag(mi.X.Type()), V: ir.IntVal{Width: 64, Bits: fmt.Sprint(h.Sum32())}}, true
	}
	init, ok := loadedInit(v)
	if !ok {
		return nil, false
	}
	g := v.(*ssa.UnOp).X.(*ssa.Global)
	// var B = get() where get only returns package variable A
	// (os.ErrDeadlineExceeded = errDeadlineExceeded(), which returns
	// poll.ErrDeadlineExceeded): B is A, with A's identity.
	for range 4 {
		c, ok := init.(*ssa.Call)
		if !ok {
			break
		}
		ld := getterLoad(c)
		if ld == nil {
			break
		}
		if init, ok = loadedInit(ld); !ok {
			return nil, false
		}
		g = ld.X.(*ssa.Global)
	}
	identity := func() ir.Val {
		h := fnv.New32a()
		h.Write([]byte(g.String()))
		return ir.IntVal{Width: 64, Bits: fmt.Sprint(h.Sum32())}
	}
	// var ErrX error = &T{} of a struct the initializer sets no field of
	// (poll.ErrDeadlineExceeded): tagged with *T, its payload the identity.
	if mi, ok := init.(*ssa.MakeInterface); ok && isError(mi.Type()) {
		a, ok := mi.X.(*ssa.Alloc)
		if !ok || !a.Heap {
			return nil, false
		}
		if _, ok := a.Type().(*types.Pointer).Elem().Underlying().(*types.Struct); !ok {
			return nil, false
		}
		for _, r := range *a.Referrers() {
			if r != ssa.Instruction(mi) {
				return nil, false
			}
		}
		return ir.Boxed{Tag: typeTag(a.Type()), V: identity()}, true
	}
	c, ok := init.(*ssa.Call)
	if !ok {
		return nil, false
	}
	// errors.New of a constant, or fmt.Errorf of a constant format and
	// arguments that wraps nothing (with %w, errors.Is would see through
	// it to the wrapped error).
	if f := c.Call.StaticCallee(); f != nil && f.String() == "errors.New" && len(c.Call.Args) == 1 {
		if k, ok := c.Call.Args[0].(*ssa.Const); !ok || k.Value == nil || k.Value.Kind() != constant.String {
			return nil, false
		}
	} else if format, ok := constFormat(c, "fmt.Errorf"); !ok || strings.Contains(format, "%w") {
		return nil, false
	}
	return ir.Boxed{Tag: sentinelTag, V: identity()}, true
}

// getterLoad is the load of a package variable that a call's static
// callee, with no parameters, only returns (func errDeadlineExceeded()
// error { return poll.ErrDeadlineExceeded }); nil otherwise.
func getterLoad(c *ssa.Call) *ssa.UnOp {
	f := c.Call.StaticCallee()
	if f == nil || len(f.Params) != 0 || len(f.Blocks) != 1 || len(c.Call.Args) != 0 {
		return nil
	}
	ins := f.Blocks[0].Instrs
	if len(ins) != 2 {
		return nil
	}
	ld, ok := ins[0].(*ssa.UnOp)
	if !ok || ld.Op != token.MUL {
		return nil
	}
	if _, ok := ld.X.(*ssa.Global); !ok {
		return nil
	}
	if ret, ok := ins[1].(*ssa.Return); !ok || len(ret.Results) != 1 || ret.Results[0] != ld {
		return nil
	}
	return ld
}

// sentinelObject is the object a pointer sentinel points to: v loads a
// package variable its initializer sets to &T{...} of a struct whose
// fields the initializer only sets to constants (sjson's errNoChange),
// assumed not to be reassigned: the struct's zero value and the constant
// of each field set (nil for the others). An array field set element by
// element to constants (grpc's bdpPing, &ping{data: [8]byte{...}}) is in
// the zero value returned (#71).
func sentinelObject(v ssa.Value) (ir.Val, []*ssa.Const, bool) {
	init, ok := loadedInit(v)
	if !ok {
		return nil, nil, false
	}
	a, ok := init.(*ssa.Alloc)
	if !ok || !a.Heap {
		return nil, nil, false
	}
	elem := a.Type().(*types.Pointer).Elem()
	st, ok := elem.Underlying().(*types.Struct)
	if !ok {
		return nil, nil, false
	}
	zero, ok := zeroVal(elem)
	if !ok {
		return nil, nil, false
	}
	fields := make([]*ssa.Const, st.NumFields())
	for _, r := range *a.Referrers() {
		switch r := r.(type) {
		case *ssa.Store:
			if r.Val != a {
				return nil, nil, false
			}
		case *ssa.FieldAddr:
			for _, fr := range *r.Referrers() {
				w, ok := fr.(*ssa.Store)
				if !ok || w.Addr != r {
					return nil, nil, false
				}
				if constArrayField(zero, r.Field, w.Val) {
					continue
				}
				k, ok := w.Val.(*ssa.Const)
				if !ok {
					return nil, nil, false
				}
				if _, err := constVal(k); err != nil {
					return nil, nil, false
				}
				fields[r.Field] = k
			}
		default:
			return nil, nil, false
		}
	}
	return zero, fields, true
}

// constArrayField sets, in struct value zero, array field f to v when v
// is an array literal of constants: go/ssa builds [8]byte{2, 4, ...} in a
// local, stores each element, and loads the local.
func constArrayField(zero ir.Val, f int, v ssa.Value) bool {
	sv, ok := zero.(ir.StructVal)
	if !ok || f >= len(sv.Fields) {
		return false
	}
	arr, ok := sv.Fields[f].(ir.ArrVal)
	load, isLoad := v.(*ssa.UnOp)
	if !ok || !isLoad || load.Op != token.MUL {
		return false
	}
	local, ok := load.X.(*ssa.Alloc)
	if !ok || local.Heap || local.Referrers() == nil {
		return false
	}
	elems := slices.Clone(arr.Elems)
	for _, r := range *local.Referrers() {
		if r == load {
			continue
		}
		ia, ok := r.(*ssa.IndexAddr)
		if !ok || ia.Referrers() == nil || len(*ia.Referrers()) != 1 {
			return false
		}
		ic, ok := ia.Index.(*ssa.Const)
		if !ok || ic.Value == nil {
			return false
		}
		i, exact := constant.Int64Val(constant.ToInt(ic.Value))
		w, ok := (*ia.Referrers())[0].(*ssa.Store)
		if !exact || i < 0 || int(i) >= len(elems) || !ok || w.Addr != ia {
			return false
		}
		k, ok := w.Val.(*ssa.Const)
		if !ok {
			return false
		}
		c, err := constVal(k)
		if _, str := c.(ir.StrConst); err != nil || str {
			return false
		}
		elems[i] = c
	}
	sv.Fields[f] = ir.ArrVal{Elems: elems}
	return true
}

// sentinelTag tags sentinel errors: errors.New returns an *errors.errorString.
var sentinelTag = func() uint32 {
	h := fnv.New32a()
	h.Write([]byte("*errors.errorString"))
	return h.Sum32()
}()

// sentinelCompare lowers err == S, err != S and errors.Is(err, S) for a
// sentinel S: decided when err is known (nil, a sentinel, or for == another
// boxed error; errors.Is would call an error type's Unwrap or Is), havoc
// when it is an error the standard library made or the model knows nothing
// about, which may be, or wrap, S.
func (t *fnState) sentinelCompare(dst ir.Var, errV ssa.Value, s ir.Val, neg bool, what string) error {
	e, err := t.val(errV)
	if err != nil {
		return err
	}
	sv := t.fresh()
	t.emit(ir.Instr{Dst: sv, Op: ir.Const{V: s}})
	known := t.fresh()
	var check ir.Op = ir.Simple("errKnown")
	if what == "errors.Is" {
		check = ir.ErrTagKnown{Tag: sentinelTag}
	}
	t.emit(ir.Instr{Dst: known, Op: check, Args: []ir.Var{e}})
	yes := len(t.out.Blocks)
	no, join := yes+1, yes+2
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: known, T: yes, F: no}
	name := t.cur.Name
	t.out.Blocks = append(t.out.Blocks, ir.Block{Name: name + ".err-known"}, ir.Block{Name: name + ".err-unknown"}, ir.Block{Name: name + ".err-join"})
	t.cur = &t.out.Blocks[yes]
	t.emit(ir.Instr{Dst: dst, Op: pick(!neg, "eq", "ne"), Args: []ir.Var{e, sv}})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[no]
	t.emit(ir.Instr{Dst: dst, HavocSite: t.prog.newHavoc(ir.HavocBool, t.f.Name()+": "+what, "opaque-op", "")})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return nil
}

// regexpPattern is the constant pattern a *regexp.Regexp was compiled
// from by regexp.MustCompile: v is that call, or a load of a package
// variable its package initializer sets to that call (assumed not to be
// reassigned: a compiled pattern is a constant in all but name).
func regexpPattern(v ssa.Value) (string, bool) {
	if init, ok := loadedInit(v); ok {
		v = init
	}
	c, ok := v.(*ssa.Call)
	if !ok || len(c.Call.Args) != 1 {
		return "", false
	}
	if f := c.Call.StaticCallee(); f == nil || f.String() != "regexp.MustCompile" {
		return "", false
	}
	if pat, ok := constSprintf(c.Call.Args[0]); ok {
		return pat, true
	}
	k, ok := c.Call.Args[0].(*ssa.Const)
	if !ok || k.Value == nil || k.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(k.Value), true
}

// constSprintf evaluates fmt.Sprintf of a constant format and constant
// string, integer and bool arguments (a pattern assembled from constant
// pieces), as Go would.
func constSprintf(v ssa.Value) (string, bool) {
	c, ok := v.(*ssa.Call)
	if !ok {
		return "", false
	}
	format, ok := constFormat(c, "fmt.Sprintf")
	if !ok {
		return "", false
	}
	return fmt.Sprintf(format, constArgs(c)...), true
}

// constFormat is the format of a call to fn (fmt.Sprintf, fmt.Errorf)
// whose format and arguments are all constants (see constArgs).
func constFormat(c *ssa.Call, fn string) (string, bool) {
	if f := c.Call.StaticCallee(); f == nil || f.String() != fn || len(c.Call.Args) != 2 {
		return "", false
	}
	format, ok := c.Call.Args[0].(*ssa.Const)
	if !ok || format.Value == nil || format.Value.Kind() != constant.String {
		return "", false
	}
	if constArgs(c) == nil {
		return "", false
	}
	return constant.StringVal(format.Value), true
}

// constArgs is the variadic arguments of a printf-like call c as Go
// values, when each is a constant string, integer or bool (an empty,
// non-nil slice for none); nil otherwise.
func constArgs(c *ssa.Call) []any {
	args, ok := constArgList(c)
	if !ok {
		return nil
	}
	if args == nil {
		args = []any{}
	}
	return args
}

func constArgList(c *ssa.Call) ([]any, bool) {
	var args []any
	switch a := c.Call.Args[1].(type) {
	case *ssa.Const: // nil: no arguments
		if a.Value != nil {
			return nil, false
		}
	case *ssa.Slice:
		arr, ok := a.X.(*ssa.Alloc)
		if !ok {
			return nil, false
		}
		n := int(arr.Type().(*types.Pointer).Elem().(*types.Array).Len())
		args = make([]any, n)
		set := 0
		for _, r := range *arr.Referrers() {
			ia, ok := r.(*ssa.IndexAddr)
			if !ok {
				if r == a {
					continue
				}
				return nil, false
			}
			i, ok := ia.Index.(*ssa.Const)
			if !ok {
				return nil, false
			}
			for _, w := range *ia.Referrers() {
				st, ok := w.(*ssa.Store)
				if !ok {
					return nil, false
				}
				mi, ok := st.Val.(*ssa.MakeInterface)
				if !ok {
					return nil, false
				}
				k, ok := mi.X.(*ssa.Const)
				if !ok || k.Value == nil {
					return nil, false
				}
				switch k.Value.Kind() {
				case constant.String:
					args[i.Int64()] = constant.StringVal(k.Value)
				case constant.Int:
					x, exact := constant.Int64Val(k.Value)
					if !exact {
						return nil, false
					}
					args[i.Int64()] = x
				case constant.Bool:
					args[i.Int64()] = constant.BoolVal(k.Value)
				default:
					return nil, false
				}
				set++
			}
		}
		if set != n {
			return nil, false
		}
	default:
		return nil, false
	}
	return args, true
}

// sameRepr: both are channels of the same element type (a change of
// direction only, chan T -> <-chan T), or pointers to types of the same
// underlying type ((*uint32)(&status) for a `type Status uint32`).
func sameRepr(a, b types.Type) bool {
	if pa, ok := a.Underlying().(*types.Pointer); ok {
		pb, ok2 := b.Underlying().(*types.Pointer)
		return ok2 && types.Identical(pa.Elem().Underlying(), pb.Elem().Underlying())
	}
	ca, ok := a.Underlying().(*types.Chan)
	cb, ok2 := b.Underlying().(*types.Chan)
	return ok && ok2 && types.Identical(ca.Elem(), cb.Elem())
}

// atomicFuncCall lowers sync/atomic's functions on an integer pointer
// (LoadInt32(&x), StoreInt32, AddInt32, SwapInt32, CompareAndSwapInt32 and
// the 64-bit and unsigned ones). The model runs a goroutine's instructions
// between two synchronisation terms atomically, so these are plain
// accesses.
// clockKey is the cell of the model's clock (#59).
var clockKey = func() uint32 {
	h := fnv.New32a()
	h.Write([]byte("go2lean.clock"))
	return h.Sum32()
}()

// clockCall lowers package time's runtime hooks, which have no Go body,
// onto the model's clock (#59): one cell per run, read atomically (the
// race detector does not report it), that only virtual time moves on: a
// sleep or a timer that ends (GoConc.advanceClock). Code runs in no time,
// so two readings with no sleep between them are equal, as Go's monotonic
// clock may give. runtimeNano returns the reading (from 1); runtimeNow
// returns (sec, nsec, mono), a wall clock that moves with it from a fixed
// start.
// With these, Now, Since and the Time methods are translated from source
// (modelledTime) and agree with each other as Go's do.
func (t *fnState) clockCall(x *ssa.Call, callee *ssa.Function) bool {
	name := callee.String()
	if name == "time.runtimeIsBubbled" {
		// Not inside a testing/synctest bubble.
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.BoolVal(false)}})
		return true
	}
	if name != "time.runtimeNano" && name != "time.runtimeNow" {
		return false
	}
	c := func(bits string, w int) ir.Var {
		v := t.fresh()
		t.emit(ir.Instr{Dst: v, Op: ir.Const{V: ir.IntVal{Width: w, Bits: bits}}})
		return v
	}
	op := func(name string, args ...ir.Var) ir.Var {
		v := t.fresh()
		t.emit(ir.Instr{Dst: v, Op: ir.Simple(name), Args: args})
		return v
	}
	clk := t.fresh()
	t.emit(ir.Instr{Dst: clk, Op: ir.GlobalAddr{Key: clockKey, Zero: ir.IntVal{Width: 64, Bits: "0"}}})
	now := t.fresh()
	t.emit(ir.Instr{Dst: now, Op: ir.Conc("atomicLoad"), Args: []ir.Var{clk}})
	mono := op("add", now, c("1", 64))
	if name == "time.runtimeNano" {
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("add"), Args: []ir.Var{now, c("1", 64)}})
		return true
	}
	// 2023-11-14 22:13:20 UTC, and the reading's seconds after it.
	sec := op("add", c("1700000000", 64), op("udiv", mono, c("1000000000", 64)))
	nsec := t.fresh()
	t.emit(ir.Instr{Dst: nsec, Op: ir.Conv{Width: 32}, Args: []ir.Var{op("urem", mono, c("1000000000", 64))}})
	t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("mkTuple"), Args: []ir.Var{sec, nsec, mono}})
	return true
}

func (t *fnState) atomicFuncCall(x *ssa.Call, callee *ssa.Function) (bool, error) {
	if callee.Signature.Recv() != nil || callee.Pkg == nil || callee.Pkg.Pkg.Path() != "sync/atomic" {
		return false, nil
	}
	var op string
	for _, o := range []string{"CompareAndSwap", "Load", "Store", "Add", "Swap"} {
		if strings.HasPrefix(callee.Name(), o) {
			op = o
			break
		}
	}
	switch strings.TrimPrefix(callee.Name(), op) {
	case "Int32", "Int64", "Uint32", "Uint64":
	default:
		return false, nil
	}
	args := make([]ir.Var, len(x.Call.Args))
	for i, a := range x.Call.Args {
		v, err := t.val(a)
		if err != nil {
			return true, err
		}
		args[i] = v
	}
	addr := args[0]
	load := func() ir.Var {
		v := t.fresh()
		t.emit(ir.Instr{Dst: v, Op: ir.Conc("atomicLoad"), Args: []ir.Var{addr}})
		return v
	}
	store := func(v ir.Var) {
		t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Conc("atomicStore"), Args: []ir.Var{addr, v}})
	}
	switch op {
	case "Load":
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Conc("atomicLoad"), Args: []ir.Var{addr}})
	case "Store":
		store(args[1])
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.UnitVal{}}})
	case "Add":
		r := t.def(x)
		t.emit(ir.Instr{Dst: r, Op: ir.Simple("add"), Args: []ir.Var{load(), args[1]}})
		store(r)
	case "Swap":
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Conc("atomicLoad"), Args: []ir.Var{addr}})
		store(args[1])
	case "CompareAndSwap":
		cur, r := load(), t.def(x)
		t.emit(ir.Instr{Dst: r, Op: ir.Simple("eq"), Args: []ir.Var{cur, args[1]}})
		nv := t.fresh()
		t.emit(ir.Instr{Dst: nv, Op: ir.Simple("ite"), Args: []ir.Var{r, args[2], cur}})
		store(nv)
	}
	return true, nil
}

// atomicCall lowers the methods of sync/atomic's Bool, Int32, Int64,
// Uint32 and Uint64 (Load, Store, Add, Swap, CompareAndSwap, And, Or):
// reads and writes of the integer field v the type wraps (Bool holds 0 or
// 1). The model runs a goroutine's instructions between two
// synchronisation terms atomically, so a read and a write in one method
// are one atomic step (#67).
func (t *fnState) atomicCall(x *ssa.Call, callee *ssa.Function) (bool, error) {
	recv := callee.Signature.Recv()
	if recv == nil || callee.Pkg == nil || callee.Pkg.Pkg.Path() != "sync/atomic" {
		return false, nil
	}
	pt, ok := recv.Type().(*types.Pointer)
	if !ok {
		return false, nil
	}
	named, ok := pt.Elem().(*types.Named)
	if !ok {
		return false, nil
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return false, nil
	}
	field := -1
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Name() == "v" {
			field = i
		}
	}
	isBool := named.Obj().Name() == "Bool"
	switch named.Obj().Name() {
	case "Bool", "Int32", "Int64", "Uint32", "Uint64":
	default:
		return false, nil
	}
	w, _, isInt := intWidth(st.Field(max(field, 0)).Type())
	if field < 0 || !isInt {
		return false, nil
	}
	switch callee.Name() {
	case "Load", "Store", "Add", "Swap", "CompareAndSwap", "And", "Or":
	default:
		return false, nil
	}
	p, err := t.val(x.Call.Args[0])
	if err != nil {
		return true, err
	}
	addr := t.fresh()
	t.emit(ir.Instr{Dst: addr, Op: ir.FieldAddr{I: field}, Args: []ir.Var{p}})
	konst := func(bits string) ir.Var {
		v := t.fresh()
		t.emit(ir.Instr{Dst: v, Op: ir.Const{V: ir.IntVal{Width: w, Bits: bits}}})
		return v
	}
	switch callee.Name() {
	case "Load":
		if isBool {
			raw := t.fresh()
			t.emit(ir.Instr{Dst: raw, Op: ir.Conc("atomicLoad"), Args: []ir.Var{addr}})
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("ne"), Args: []ir.Var{raw, konst("0")}})
		} else {
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Conc("atomicLoad"), Args: []ir.Var{addr}})
		}
	case "Store":
		v, err := t.val(x.Call.Args[1])
		if err != nil {
			return true, err
		}
		if isBool {
			b := t.fresh()
			t.emit(ir.Instr{Dst: b, Op: ir.Simple("ite"), Args: []ir.Var{v, konst("1"), konst("0")}})
			v = b
		}
		t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Conc("atomicStore"), Args: []ir.Var{addr, v}})
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.UnitVal{}}})
	case "Add": // Bool has no Add
		d, err := t.val(x.Call.Args[1])
		if err != nil {
			return true, err
		}
		old, r := t.fresh(), t.def(x)
		t.emit(ir.Instr{Dst: old, Op: ir.Conc("atomicLoad"), Args: []ir.Var{addr}})
		t.emit(ir.Instr{Dst: r, Op: ir.Simple("add"), Args: []ir.Var{old, d}})
		t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Conc("atomicStore"), Args: []ir.Var{addr, r}})
	case "Swap", "CompareAndSwap", "And", "Or":
		// The argument as the field holds it: Bool's true is 1.
		arg := func(i int) (ir.Var, error) {
			v, err := t.val(x.Call.Args[i])
			if err != nil || !isBool {
				return v, err
			}
			b := t.fresh()
			t.emit(ir.Instr{Dst: b, Op: ir.Simple("ite"), Args: []ir.Var{v, konst("1"), konst("0")}})
			return b, nil
		}
		cur := t.fresh()
		t.emit(ir.Instr{Dst: cur, Op: ir.Conc("atomicLoad"), Args: []ir.Var{addr}})
		var nv ir.Var
		switch callee.Name() {
		case "Swap": // returns the old value
			v, err := arg(1)
			if err != nil {
				return true, err
			}
			nv = v
			if isBool {
				t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("ne"), Args: []ir.Var{cur, konst("0")}})
			} else {
				t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("add"), Args: []ir.Var{cur, konst("0")}})
			}
		case "CompareAndSwap": // returns whether it swapped
			old, err := arg(1)
			if err != nil {
				return true, err
			}
			v, err := arg(2)
			if err != nil {
				return true, err
			}
			r := t.def(x)
			t.emit(ir.Instr{Dst: r, Op: ir.Simple("eq"), Args: []ir.Var{cur, old}})
			nv = t.fresh()
			t.emit(ir.Instr{Dst: nv, Op: ir.Simple("ite"), Args: []ir.Var{r, v, cur}})
		default: // And, Or (Go 1.23): the old value; Bool has neither
			m, err := arg(1)
			if err != nil {
				return true, err
			}
			nv = t.fresh()
			t.emit(ir.Instr{Dst: nv, Op: ir.Simple(strings.ToLower(callee.Name())), Args: []ir.Var{cur, m}})
			t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("add"), Args: []ir.Var{cur, konst("0")}})
		}
		t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Conc("atomicStore"), Args: []ir.Var{addr, nv}})
	}
	return true, nil
}

// bufferGrow lowers (*strings.Builder).Grow(n) and (*bytes.Buffer).Grow(n).
// Grow changes only the capacity, which the model does not track, and
// panics for n < 0 as in Go. Ignoring the call would be an
// under-approximation: it would drop that panic.
func (t *fnState) bufferGrow(x *ssa.Call, callee *ssa.Function) (bool, error) {
	switch callee.String() {
	case "(*strings.Builder).Grow", "(*bytes.Buffer).Grow":
	default:
		return false, nil
	}
	args := x.Call.Args
	if len(args) != 2 {
		return false, nil
	}
	n, err := t.val(args[1])
	if err != nil {
		return true, err
	}
	zero := t.fresh()
	t.emit(ir.Instr{Dst: zero, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "0"}}})
	neg := t.fresh()
	t.emit(ir.Instr{Dst: neg, Op: ir.Simple("slt"), Args: []ir.Var{n, zero}})
	panicB := len(t.out.Blocks)
	okB := panicB + 1
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: neg, T: panicB, F: okB}
	name0 := t.cur.Name
	t.out.Blocks = append(t.out.Blocks, ir.Block{Name: name0 + ".grow-neg"}, ir.Block{Name: name0 + ".grow-ok"})
	t.out.Blocks[panicB].Term = ir.Term{Kind: ir.TermPanic, Panic: contract.PanicExplicit}
	t.cur = &t.out.Blocks[okB]
	return true, nil
}

// randBounded lowers the bounded draws of math/rand and math/rand/v2
// (Intn(n) and the like): any value in [0, n), a havoc the search picks,
// and the panic Go raises for n <= 0. The bound is what a caller relies on
// (indexing a table of n entries), so a plain havoc would refute it.
func (t *fnState) randBounded(x *ssa.Call, callee *ssa.Function) (bool, error) {
	name := callee.String()
	if strings.HasPrefix(name, "(*math/rand.Rand).") || strings.HasPrefix(name, "(*math/rand/v2.Rand).") {
		name = name[strings.Index(name, ".Rand).")+len(".Rand)."):]
	} else if p := callee.Pkg; p != nil && (p.Pkg.Path() == "math/rand" || p.Pkg.Path() == "math/rand/v2") {
		name = callee.Name()
	} else {
		return false, nil
	}
	switch name {
	case "Intn", "Int63n", "Int31n", "IntN", "Int64N", "Int32N", "UintN", "Uint64N", "Uint32N":
	default:
		return false, nil
	}
	args := x.Call.Args
	if len(args) == 0 {
		return false, nil
	}
	nArg := args[len(args)-1]
	width, signed, ok := intWidth(nArg.Type())
	if !ok {
		return false, nil
	}
	n, err := t.val(nArg)
	if err != nil {
		return true, err
	}
	zero := t.fresh()
	t.emit(ir.Instr{Dst: zero, Op: ir.Const{V: ir.IntVal{Width: width, Bits: "0"}}})
	bad := t.fresh()
	t.emit(ir.Instr{Dst: bad, Op: pick(signed, "sle", "ule"), Args: []ir.Var{n, zero}})
	panicB := len(t.out.Blocks)
	okB := panicB + 1
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: bad, T: panicB, F: okB}
	name0 := t.cur.Name
	t.out.Blocks = append(t.out.Blocks, ir.Block{Name: name0 + ".rand-bad"}, ir.Block{Name: name0 + ".rand-ok"})
	t.out.Blocks[panicB].Term = ir.Term{Kind: ir.TermPanic, Panic: contract.PanicExplicit}
	t.cur = &t.out.Blocks[okB]
	raw := t.fresh()
	t.emit(ir.Instr{Dst: raw, HavocSite: t.prog.newHavoc(width, fmt.Sprintf("%s: %s", t.f.Name(), callee.Name()), "unmodeled-call", "")})
	t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("urem"), Args: []ir.Var{raw, n}})
	return true, nil
}

// opaqueLen binds dst to the length of a map or channel the model does
// not look into: havoc, but kept below 16 so that `make([]T, 0, len(m))`
// stays something the model materialises. Recorded as an assumption.
func (t *fnState) opaqueLen(dst ir.Var, name string, ty types.Type) {
	t.assume("%s of %s assumed below 16 (not modelled)", name, ty)
	raw := t.fresh()
	t.emit(ir.Instr{Dst: raw, HavocSite: t.prog.newHavoc(64, fmt.Sprintf("%s: %s", t.f.Name(), name), "unmodeled-call", "")})
	sixteen := t.fresh()
	t.emit(ir.Instr{Dst: sixteen, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "16"}}})
	t.emit(ir.Instr{Dst: dst, Op: ir.Simple("urem"), Args: []ir.Var{raw, sixteen}})
}

// onKnown runs known in a block taken when the func, map, channel or
// interface value v is one the model knows (nil, or built by the program)
// and unknown otherwise; both continue in a join block.
func (t *fnState) onKnown(sv ssa.Value, v ir.Var, known, unknown func() error) error {
	isKnown := t.fresh()
	t.emit(ir.Instr{Dst: isKnown, Op: ir.Simple("nilKnown"), Args: []ir.Var{v}})
	yes := len(t.out.Blocks)
	no, join := yes+1, yes+2
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: isKnown, T: yes, F: no}
	name := t.cur.Name
	t.out.Blocks = append(t.out.Blocks,
		ir.Block{Name: name + ".known"},
		ir.Block{Name: name + ".unknown"},
		ir.Block{Name: name + ".known-join"})
	t.cur = &t.out.Blocks[yes]
	if err := known(); err != nil {
		return err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[no]
	// A value known by construction never takes this branch: build it
	// without recording its havoc as a gap in the model.
	quiet := t.quiet
	t.quiet = quiet || t.knownBuilt(sv)
	err := unknown()
	t.quiet = quiet
	if err != nil {
		return err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return nil
}

// knownBuilt reports whether v is a func, map or interface value the model
// always knows at run time (nilKnown holds): one the function builds (make,
// a boxed value, a closure, a func, nil), an input (the search generates
// inputs concretely, all the way down), a phi or conversion of those, or a
// value read out of a known one (a lookup, an assertion, a field, an
// element) in a function that writes nothing unknown into memory. Other
// values (loads of locals, call results) may be havoc.
func (t *fnState) knownBuilt(v ssa.Value) bool {
	return t.known(v, map[ssa.Value]bool{})
}

func (t *fnState) known(v ssa.Value, seen map[ssa.Value]bool) bool {
	if seen[v] {
		return true // a cycle through phis: known if every way in is
	}
	seen[v] = true
	switch v := v.(type) {
	case *ssa.MakeMap, *ssa.MakeInterface, *ssa.MakeClosure, *ssa.Function, *ssa.Const, *ssa.Parameter:
		return true
	case *ssa.ChangeType:
		return t.known(v.X, seen)
	case *ssa.ChangeInterface:
		return t.known(v.X, seen)
	case *ssa.Phi:
		for _, e := range v.Edges {
			if !t.known(e, seen) {
				return false
			}
		}
		return true
	case *ssa.Extract:
		return t.known(v.Tuple, seen)
	case *ssa.Lookup:
		return t.readsOnly() && t.known(v.X, seen)
	case *ssa.TypeAssert:
		return t.readsOnly() && t.known(v.X, seen)
	case *ssa.Next:
		if r, ok := v.Iter.(*ssa.Range); ok {
			return t.readsOnly() && t.known(r.X, seen)
		}
	case *ssa.Field:
		return t.readsOnly() && t.known(v.X, seen)
	case *ssa.Index:
		return t.readsOnly() && t.known(v.X, seen)
	case *ssa.Slice:
		return t.readsOnly() && t.known(v.X, seen)
	case *ssa.FieldAddr:
		return t.known(v.X, seen)
	case *ssa.IndexAddr:
		return t.known(v.X, seen)
	case *ssa.UnOp:
		if v.Op == token.MUL {
			switch v.X.(type) {
			case *ssa.FieldAddr, *ssa.IndexAddr:
				return t.readsOnly() && t.known(v.X, seen)
			}
		}
	}
	return false
}

// readsOnly reports whether the function (with its closures) stores into
// memory or maps only values known by construction, so that what it reads
// out of an input is what the search generated. Stores into its own
// locals do not count. Callees may still write; their havoc stays a
// run-time branch, only its report is left out.
func (t *fnState) readsOnly() bool {
	if t.pure != nil {
		return *t.pure
	}
	pure := true
	t.pure = &pure
	var walk func(f *ssa.Function)
	walk = func(f *ssa.Function) {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				var val, addr ssa.Value
				switch in := in.(type) {
				case *ssa.Store:
					val, addr = in.Val, in.Addr
				case *ssa.MapUpdate:
					val, addr = in.Value, in.Map
				default:
					continue
				}
				if _, local := addr.(*ssa.Alloc); local {
					continue
				}
				if typeMayHoldUnknown(val.Type()) && !t.known(val, map[ssa.Value]bool{}) {
					pure = false
					return
				}
			}
		}
		for _, af := range f.AnonFuncs {
			walk(af)
		}
	}
	walk(t.f)
	return pure
}

// typeMayHoldUnknown reports whether a value of type ty can be (or hold) a
// func, map, interface or pointer the model may not know.
func typeMayHoldUnknown(ty types.Type) bool {
	switch u := ty.Underlying().(type) {
	case *types.Basic:
		return false
	case *types.Array:
		return typeMayHoldUnknown(u.Elem())
	}
	return true
}

// nilOperand returns the operand compared with a nil constant in x, if
// one side of x is that constant.
func nilOperand(x *ssa.BinOp) (ssa.Value, bool) {
	if c, ok := x.Y.(*ssa.Const); ok && c.Value == nil {
		return x.X, true
	}
	if c, ok := x.X.(*ssa.Const); ok && c.Value == nil {
		return x.Y, true
	}
	return nil, false
}

// nilCompare lowers v == nil or v != nil for a func, map, channel or
// interface value: decided when the model knows the value's nil-ness (nil
// inputs, values the program built), havoc otherwise.
func (t *fnState) nilCompare(x *ssa.BinOp, v ssa.Value) error {
	val, err := t.val(v)
	if err != nil {
		return err
	}
	dst := t.def(x)
	known := t.fresh()
	t.emit(ir.Instr{Dst: known, Op: ir.Simple("nilKnown"), Args: []ir.Var{val}})
	yes := len(t.out.Blocks)
	no, join := yes+1, yes+2
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: known, T: yes, F: no}
	name := t.cur.Name
	t.out.Blocks = append(t.out.Blocks,
		ir.Block{Name: name + ".nil-known"},
		ir.Block{Name: name + ".nil-unknown"},
		ir.Block{Name: name + ".nil-join"})
	t.cur = &t.out.Blocks[yes]
	if x.Op == token.EQL {
		t.emit(ir.Instr{Dst: dst, Op: ir.Simple("isNilOpaque"), Args: []ir.Var{val}})
	} else {
		isNil := t.fresh()
		t.emit(ir.Instr{Dst: isNil, Op: ir.Simple("isNilOpaque"), Args: []ir.Var{val}})
		t.emit(ir.Instr{Dst: dst, Op: ir.Simple("not"), Args: []ir.Var{isNil}})
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[no]
	if !t.knownBuilt(v) {
		t.issue("nil comparison %s on a value of unknown nil-ness is havoc", x.Name())
	}
	idx := t.prog.newHavoc(ir.HavocBool, t.f.Name()+": nil comparison", "opaque-op", "")
	t.emit(ir.Instr{Dst: dst, HavocSite: idx})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return nil
}

// ifaceCompare lowers == or != on two interface values: exact when both
// are values the model built (their dynamic types' tags and values, or
// nil), havoc otherwise (#60).
func (t *fnState) ifaceCompare(x *ssa.BinOp) error {
	a, err := t.val(x.X)
	if err != nil {
		return err
	}
	b, err := t.val(x.Y)
	if err != nil {
		return err
	}
	dst := t.def(x)
	known := t.fresh()
	t.emit(ir.Instr{Dst: known, Op: ir.Simple("ifaceEqKnown"), Args: []ir.Var{a, b}})
	yes := len(t.out.Blocks)
	no, join := yes+1, yes+2
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: known, T: yes, F: no}
	name := t.cur.Name
	t.out.Blocks = append(t.out.Blocks,
		ir.Block{Name: name + ".iface-known"},
		ir.Block{Name: name + ".iface-unknown"},
		ir.Block{Name: name + ".iface-join"})
	t.cur = &t.out.Blocks[yes]
	if x.Op == token.EQL {
		t.emit(ir.Instr{Dst: dst, Op: ir.Simple("ifaceEq"), Args: []ir.Var{a, b}})
	} else {
		eq := t.fresh()
		t.emit(ir.Instr{Dst: eq, Op: ir.Simple("ifaceEq"), Args: []ir.Var{a, b}})
		t.emit(ir.Instr{Dst: dst, Op: ir.Simple("not"), Args: []ir.Var{eq}})
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[no]
	idx := t.prog.newHavoc(ir.HavocBool, t.f.Name()+": "+x.Op.String()+" on an interface value the model did not build", "opaque-op", "")
	t.emit(ir.Instr{Dst: dst, HavocSite: idx})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return nil
}

// emitCall terminates the current block with the call term (its callee
// already set), continuing in a fresh block. A second fresh block is the
// call's fallback: run when the callee gets stuck, it binds the result to
// a havoc value (as an unmodelled call would) and joins the continuation.
// A call without a result binds the unit value, so the callee's effects on
// the heap are modelled too.
func (t *fnState) emitCall(x *ssa.Call, term ir.Term, name string) error {
	var args []ir.Var
	callArgs := x.Call.Args
	if mc, ok := x.Call.Value.(*ssa.MakeClosure); ok && term.Kind != ir.TermCallV {
		// A closure called directly: its captured values come first.
		callArgs = append(append([]ssa.Value(nil), mc.Bindings...), callArgs...)
	}
	for _, a := range callArgs {
		v, err := t.val(a)
		if err != nil {
			return err
		}
		args = append(args, v)
	}
	return t.emitCallArgs(x, term, name, args)
}

// deferred is a deferred call to a modelled function: whether its defer
// statement ran and the arguments it evaluated live in two cells the
// function allocates on entry.
type deferred struct {
	callee, nargs int
	name          string
	flag, args    ir.Var
	// sync is the synchronisation operation a deferred sync method makes
	// (syncOps), "" for a call.
	sync string
	lock ir.SyncOp // lockL, unlockL: the op with its tags
	// dynamic: a call through the func value that is the first argument.
	dynamic bool
}

// deferCall lowers x, a defer of modelled function callee, or of a
// synchronisation operation syncOp (callee nil for the builtin close): the
// arguments are evaluated now and kept, and runDefers makes the call at
// each return. In a function with a defer statement in a loop, the
// arguments go onto the frame's defer stack instead (deferStack).
func (t *fnState) deferCall(x *ssa.Defer, callee *ssa.Function, syncOp string) error {
	name := syncOp
	if callee != nil {
		name = callee.String()
	}
	var args []ir.Var
	callArgs := x.Call.Args
	dynamic := syncOp == "dynamic"
	if dynamic || x.Call.IsInvoke() {
		// The func value, or the receiver, comes first.
		callArgs = append([]ssa.Value{x.Call.Value}, callArgs...)
		if dynamic {
			syncOp = ""
		}
	} else if mc, ok := x.Call.Value.(*ssa.MakeClosure); ok {
		// A closure: its captured values come first.
		callArgs = append(append([]ssa.Value(nil), mc.Bindings...), callArgs...)
	}
	for _, a := range callArgs {
		v, err := t.val(a)
		if err != nil {
			return err
		}
		args = append(args, v)
	}
	if syncOp == "wgDone" {
		d := t.fresh()
		t.emit(ir.Instr{Dst: d, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "18446744073709551615"}}})
		args, syncOp = append(args, d), "wgAdd"
	}
	d := deferred{callee: t.prog.ids[callee], nargs: len(args), name: name, flag: t.fresh(), args: t.fresh(), sync: syncOp, dynamic: dynamic}
	if syncOp == "lockL" || syncOp == "unlockL" {
		d.lock = t.prog.lockerOp(syncOp)
	}
	if syncOp != "" {
		t.sync = true
	} else if !dynamic && !slices.Contains(t.out.Calls, d.callee) {
		t.out.Calls = append(t.out.Calls, d.callee)
	}
	if t.deferStack != nil {
		// Push (true, tag, args, rest): rest is a new cell with the old stack.
		tag, tup, old, rest, tr, node := t.fresh(), t.fresh(), t.fresh(), t.fresh(), t.fresh(), t.fresh()
		t.emit(ir.Instr{Dst: tag, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: strconv.Itoa(len(t.defers))}}})
		t.emit(ir.Instr{Dst: tup, Op: ir.Simple("mkTuple"), Args: args})
		t.emit(ir.Instr{Dst: old, Op: ir.Simple("loadPath"), Args: []ir.Var{*t.deferStack}})
		t.emit(ir.Instr{Dst: rest, Op: ir.Alloc{Zero: ir.Opaque{}}})
		t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{rest, old}})
		t.emit(ir.Instr{Dst: tr, Op: ir.Const{V: ir.BoolVal(true)}})
		t.emit(ir.Instr{Dst: node, Op: ir.Simple("mkTuple"), Args: []ir.Var{tr, tag, tup, rest}})
		t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{*t.deferStack, node}})
		t.defers = append(t.defers, d)
		return nil
	}
	// The cells, on entry: a return before the defer statement ran sees
	// the flag false.
	f, tr := t.fresh(), t.fresh()
	entry := []ir.Instr{
		{Dst: d.flag, Op: ir.Alloc{Zero: ir.BoolVal(false)}},
		{Dst: d.args, Op: ir.Alloc{Zero: ir.Opaque{}}},
		{Dst: f, Op: ir.Const{V: ir.BoolVal(false)}},
		{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{d.flag, f}},
	}
	t.out.Blocks[0].Instrs = append(entry, t.out.Blocks[0].Instrs...)
	t.emit(ir.Instr{Dst: tr, Op: ir.Const{V: ir.BoolVal(true)}})
	t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{d.flag, tr}})
	tup := t.fresh()
	t.emit(ir.Instr{Dst: tup, Op: ir.Simple("mkTuple"), Args: args})
	t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{d.args, tup}})
	t.defers = append(t.defers, d)
	return nil
}

// runDefers makes the deferred calls whose defer statements ran, last
// first, before a return. A call that panics ends the run with that panic
// (Go would run the remaining deferred calls first; their effects are not
// seen). The calls on the way out of a panic are not made.
func (t *fnState) runDefers() error {
	if t.deferStack != nil {
		t.popDefers()
		return nil
	}
	for i := len(t.defers) - 1; i >= 0; i-- {
		d := t.defers[i]
		ran := t.fresh()
		t.emit(ir.Instr{Dst: ran, Op: ir.Simple("loadPath"), Args: []ir.Var{d.flag}})
		call, join := len(t.out.Blocks), len(t.out.Blocks)+1
		name := t.cur.Name
		t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: ran, T: call, F: join}
		t.out.Blocks = append(t.out.Blocks, ir.Block{Name: name + ".defer"}, ir.Block{Name: name + ".defer-done"})
		t.cur = &t.out.Blocks[call]
		tup := t.fresh()
		t.emit(ir.Instr{Dst: tup, Op: ir.Simple("loadPath"), Args: []ir.Var{d.args}})
		t.callDeferred(d, tup, join)
		t.cur = &t.out.Blocks[join]
	}
	return nil
}

// deferPop is a pop site of the defer stack: block, which holds the popped
// tag and args, gets the dispatch on tag to the deferred calls; each goes
// back to loop, the next pop.
type deferPop struct {
	block, loop int
	tag, args   ir.Var
}

// popDefers lowers RunDefers on the defer stack: pop nodes until the empty
// one, making each node's call.
func (t *fnState) popDefers() {
	loop, body, done := len(t.out.Blocks), len(t.out.Blocks)+1, len(t.out.Blocks)+2
	name := t.cur.Name
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: loop}
	t.out.Blocks = append(t.out.Blocks,
		ir.Block{Name: name + ".defer-pop"},
		ir.Block{Name: name + ".defer"},
		ir.Block{Name: name + ".defer-done"})
	t.cur = &t.out.Blocks[loop]
	node, more := t.fresh(), t.fresh()
	t.emit(ir.Instr{Dst: node, Op: ir.Simple("loadPath"), Args: []ir.Var{*t.deferStack}})
	t.emit(ir.Instr{Dst: more, Op: ir.Extract{I: 0}, Args: []ir.Var{node}})
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: more, T: body, F: done}
	t.cur = &t.out.Blocks[body]
	tag, args, rest, old := t.fresh(), t.fresh(), t.fresh(), t.fresh()
	t.emit(ir.Instr{Dst: tag, Op: ir.Extract{I: 1}, Args: []ir.Var{node}})
	t.emit(ir.Instr{Dst: args, Op: ir.Extract{I: 2}, Args: []ir.Var{node}})
	t.emit(ir.Instr{Dst: rest, Op: ir.Extract{I: 3}, Args: []ir.Var{node}})
	t.emit(ir.Instr{Dst: old, Op: ir.Simple("loadPath"), Args: []ir.Var{rest}})
	t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{*t.deferStack, old}})
	t.dispatches = append(t.dispatches, deferPop{block: body, loop: loop, tag: tag, args: args})
	t.cur = &t.out.Blocks[done]
}

// fillDeferPops ends each pop site with the dispatch on its tag: a test per
// defer statement of the function, then its call.
func (t *fnState) fillDeferPops() {
	for _, p := range t.dispatches {
		t.cur = &t.out.Blocks[p.block]
		for k, d := range t.defers {
			kv, is := t.fresh(), t.fresh()
			t.emit(ir.Instr{Dst: kv, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: strconv.Itoa(k)}}})
			t.emit(ir.Instr{Dst: is, Op: ir.Simple("eq"), Args: []ir.Var{p.tag, kv}})
			call, next := len(t.out.Blocks), len(t.out.Blocks)+1
			name := t.cur.Name
			t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: is, T: call, F: next}
			t.out.Blocks = append(t.out.Blocks, ir.Block{Name: name + ".call"}, ir.Block{Name: name + ".next"})
			t.cur = &t.out.Blocks[call]
			t.callDeferred(d, p.args, p.loop)
			t.cur = &t.out.Blocks[next]
		}
		t.cur.Term = ir.Term{Kind: ir.TermJump, B: p.loop}
	}
}

// callDeferred makes deferred call d with the arguments in tuple tup, then
// goes to join.
func (t *fnState) callDeferred(d deferred, tup ir.Var, join int) {
	args := make([]ir.Var, d.nargs)
	for k := range args {
		args[k] = t.fresh()
		t.emit(ir.Instr{Dst: args[k], Op: ir.Extract{I: k}, Args: []ir.Var{tup}})
	}
	if d.sync != "" {
		op := ir.SyncOp{Kind: d.sync}
		if d.lock.Kind != "" {
			op = d.lock
		}
		t.syncOp(op, t.fresh(), args...)
		t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
		return
	}
	next, fb := len(t.out.Blocks), len(t.out.Blocks)+1
	name := t.cur.Name
	if d.dynamic {
		t.cur.Term = ir.Term{Kind: ir.TermCallV, FV: args[0], CArgs: args[1:], Dst: t.fresh(), Next: next, Fallback: fb}
	} else {
		t.cur.Term = ir.Term{Kind: ir.TermCall, Callee: d.callee, CArgs: args, Dst: t.fresh(), Next: next, Fallback: fb}
	}
	// A deferred call the model gets stuck in: its effects are dropped.
	t.out.Blocks = append(t.out.Blocks,
		ir.Block{Name: name + "-after", Term: ir.Term{Kind: ir.TermJump, B: join}},
		ir.Block{Name: name + "-fallback", Term: ir.Term{Kind: ir.TermJump, B: join}})
}

// deferInLoop says whether f has a defer statement that can run more than
// once in a call.
func deferInLoop(f *ssa.Function) bool {
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if _, ok := in.(*ssa.Defer); ok && inCycle(b) {
				return true
			}
		}
	}
	return false
}

// inCycle says whether b can reach itself.
func inCycle(b *ssa.BasicBlock) bool {
	seen := map[*ssa.BasicBlock]bool{}
	stack := slices.Clone(b.Succs)
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if c == b {
			return true
		}
		if !seen[c] {
			seen[c] = true
			stack = append(stack, c.Succs...)
		}
	}
	return false
}

// syncTerm ends the current block with a spawn or sync terminator; the
// code after it goes on in a new block, the terminator's successor.
func (t *fnState) syncTerm(term ir.Term) {
	next := len(t.out.Blocks)
	term.Next = next
	t.cur.Term = term
	t.out.Blocks = append(t.out.Blocks, ir.Block{Name: t.cur.Name + ".after-sync"})
	t.cur = &t.out.Blocks[next]
	t.sync = true
}

func (t *fnState) syncOp(op ir.SyncOp, dst ir.Var, args ...ir.Var) {
	t.syncTerm(ir.Term{Kind: ir.TermSync, Sync: op, CArgs: args, Dst: dst})
}

// goStmt lowers `go f(args)`: a goroutine running the func value.
func (t *fnState) goStmt(x *ssa.Go) error {
	var fv ir.Var
	switch v := x.Call.Value.(type) {
	case *ssa.Builtin:
		if v.Name() == "close" {
			fv = t.fresh()
			t.emit(ir.Instr{Dst: fv, Op: ir.FuncRef{ID: t.prog.closeHelper()}})
			break
		}
		t.assume("go %s: a goroutine running a builtin is not modelled", v.Name())
		return nil
	case *ssa.Function:
		fv = t.fresh()
		if err := t.funcValue(fv, v, nil); err != nil {
			return err
		}
	default:
		var err error
		if x.Call.IsInvoke() {
			// The receiver's method, by its dynamic type (see invoke).
			ok := false
			if fv, ok, err = t.dispatch(&x.Call); err != nil {
				return err
			} else if !ok {
				t.assume("go %s: a goroutine running an interface method no modelled type implements is not modelled", x.Call.Method.Name())
				return nil
			}
		} else if fv, err = t.val(v); err != nil {
			return err
		}
	}
	var args []ir.Var
	for _, a := range x.Call.Args {
		v, err := t.val(a)
		if err != nil {
			return err
		}
		args = append(args, v)
	}
	t.syncTerm(ir.Term{Kind: ir.TermSpawn, FV: fv, CArgs: args, Dst: t.fresh()})
	return nil
}

// selectStmt lowers a select: each case's channel, then its value for a
// send; the result is go/ssa's tuple (index, recvOk, received values).
func (t *fnState) selectStmt(x *ssa.Select) error {
	var sends []bool
	var args []ir.Var
	for _, st := range x.States {
		ch, err := t.val(st.Chan)
		if err != nil {
			return err
		}
		args = append(args, ch)
		if st.Dir == types.SendOnly {
			v, err := t.val(st.Send)
			if err != nil {
				return err
			}
			args = append(args, v)
		}
		sends = append(sends, st.Dir == types.SendOnly)
	}
	t.syncOp(ir.SyncOp{Kind: "select", Sends: sends, Blocking: x.Blocking}, t.def(x), args...)
	return nil
}

// syncOps are the methods of package sync (and the yields of time and
// runtime) the concurrent machine models.
var syncOps = map[string]string{
	"(*sync.Mutex).Lock":      "lock",
	"(*sync.Mutex).Unlock":    "unlock",
	"(*sync.Mutex).TryLock":   "tryLock",
	"(*sync.RWMutex).Lock":    "wLock",
	"(*sync.RWMutex).Unlock":  "wUnlock",
	"(*sync.RWMutex).RLock":   "rLock",
	"(*sync.RWMutex).RUnlock": "rUnlock",
	"(*sync.WaitGroup).Add":   "wgAdd",
	"(*sync.WaitGroup).Done":  "wgDone",
	"(*sync.WaitGroup).Wait":  "wgWait",
	"(*sync.Once).Do":         "onceDo",
	"time.Sleep":              "yield",
	"runtime.Gosched":         "yield",
}

// syncCall lowers a call of one of syncOps.
func (t *fnState) syncCall(x *ssa.Call, callee *ssa.Function) (bool, error) {
	if callee == nil {
		return false, nil
	}
	op, ok := syncOps[callee.String()]
	if !ok {
		return false, nil
	}
	if op == "yield" {
		if callee.String() == "time.Sleep" {
			// A sleep of its duration (#59): the goroutine wakes once the
			// other sleepers with less left have, see GoConc.fire.
			d, err := t.val(x.Call.Args[0])
			if err != nil {
				return true, err
			}
			t.syncOp(ir.SyncOp{Kind: "yield"}, t.def(x), d)
			return true, nil
		}
		t.syncOp(ir.SyncOp{Kind: "yield"}, t.def(x))
		return true, nil
	}
	p, err := t.val(x.Call.Args[0])
	if err != nil {
		return true, err
	}
	switch op {
	case "onceDo":
		return true, t.onceDo(x, p)
	case "wgAdd":
		d, err := t.val(x.Call.Args[1])
		if err != nil {
			return true, err
		}
		t.syncOp(ir.SyncOp{Kind: "wgAdd"}, t.def(x), p, d)
	case "wgDone":
		d := t.fresh()
		t.emit(ir.Instr{Dst: d, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "18446744073709551615"}}})
		t.syncOp(ir.SyncOp{Kind: "wgAdd"}, t.def(x), p, d)
	default:
		t.syncOp(ir.SyncOp{Kind: op}, t.def(x), p)
	}
	return true, nil
}

// onceDo lowers once.Do(f): f runs when this caller enters first; another
// caller blocks until it is done.
func (t *fnState) onceDo(x *ssa.Call, p ir.Var) error {
	f, err := t.val(x.Call.Args[1])
	if err != nil {
		return err
	}
	run := t.fresh()
	t.syncOp(ir.SyncOp{Kind: "onceEnter"}, run, p)
	name := t.cur.Name
	body := len(t.out.Blocks)
	after, fb, done := body+1, body+2, body+3
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: run, T: body, F: done}
	t.out.Blocks = append(t.out.Blocks,
		ir.Block{Name: name + ".once-do"},
		ir.Block{Name: name + ".once-after"},
		ir.Block{Name: name + ".once-fallback"},
		ir.Block{Name: name + ".once-done"})
	res := t.fresh()
	t.out.Blocks[body].Term = ir.Term{Kind: ir.TermCallV, FV: f, Dst: res, Next: after, Fallback: fb}
	t.out.Blocks[fb].Instrs = []ir.Instr{{Dst: res, Op: ir.Const{V: ir.UnitVal{}}}}
	t.out.Blocks[fb].Term = ir.Term{Kind: ir.TermJump, B: after}
	t.out.Blocks[after].Instrs = []ir.Instr{{Dst: t.fresh(), Op: ir.Simple("onceExit"), Args: []ir.Var{p}}}
	t.out.Blocks[after].Term = ir.Term{Kind: ir.TermJump, B: done}
	t.cur = &t.out.Blocks[done]
	t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.UnitVal{}}})
	return nil
}

// emitCallArgs is emitCall with the argument variables given.
func (t *fnState) emitCallArgs(x *ssa.Call, term ir.Term, name string, args []ir.Var) error {
	dst := t.def(x)
	next := len(t.out.Blocks)
	fb := next + 1
	term.CArgs, term.Dst, term.Next, term.Fallback = args, dst, next, fb
	// Terminate the current block before appending: append may move the
	// backing array and t.cur would then point at a stale copy.
	t.cur.Term = term
	t.out.Blocks = append(t.out.Blocks,
		ir.Block{Name: t.cur.Name + ".after-call"},
		ir.Block{Name: t.cur.Name + ".call-fallback"})
	t.cur = &t.out.Blocks[fb]
	t.quiet = true
	err := t.havocResult(dst, x, "call "+name+" (callee stuck)", "callee-stuck")
	t.quiet = false
	if err != nil {
		return err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: next}
	t.cur = &t.out.Blocks[next]
	return nil
}

// funcValue binds dst to the func value of fn capturing bindings: a
// reference into the model when fn is modelled, here or in a sibling
// package, else a func value the model knows nothing about (non-nil).
func (t *fnState) funcValue(dst ir.Var, fn *ssa.Function, bindings []ssa.Value) error {
	if m, ok := fn.Object().(*types.Func); ok && strings.HasSuffix(fn.Name(), "$bound") && len(bindings) == 1 {
		// A method value x.M: go/ssa's wrapper calls M with the bound
		// receiver first, as a closure gets its captured values, so it is
		// M itself capturing x.
		if method := fn.Prog.FuncValue(m); method != nil {
			fn = method
		}
	}
	var args []ir.Var
	for _, b := range bindings {
		v, err := t.val(b)
		if err != nil {
			return err
		}
		args = append(args, v)
	}
	if id, ok := t.prog.ids[fn]; ok {
		if !slices.Contains(t.out.Calls, id) {
			t.out.Calls = append(t.out.Calls, id)
		}
		t.emit(ir.Instr{Dst: dst, Op: ir.FuncRef{ID: id}, Args: args})
		return nil
	}
	if xr, ok := t.prog.xrefs[fn.String()]; ok && xr.Package != t.prog.prog.Package {
		if !slices.Contains(t.out.XCalls, xr) {
			t.out.XCalls = append(t.out.XCalls, xr)
		}
		t.emit(ir.Instr{Dst: dst, Op: ir.XFuncRef{Key: ssaout.RegistryKey(Frontend, xr.Package), ID: xr.ID}, Args: args})
		return nil
	}
	t.issue("func value %s is opaque (not modelled)", fn.Name())
	t.emit(ir.Instr{Dst: dst, Op: ir.Const{V: ir.OpaqueNonNil{}}})
	return nil
}

// havocResult binds dst to a havoc value of the call's result type (a
// tuple for several results, unit for none).
func (t *fnState) havocResult(dst ir.Var, x ssa.Value, site, reason string) error {
	if isUnitTuple(x.Type()) {
		t.emit(ir.Instr{Dst: dst, Op: ir.Const{V: ir.UnitVal{}}})
		return nil
	}
	if tu, ok := x.Type().(*types.Tuple); ok {
		var parts []ir.Var
		for i := 0; i < tu.Len(); i++ {
			v := t.fresh()
			if err := t.havocInto(v, tu.At(i).Type(), fmt.Sprintf("%s: %s #%d", t.f.Name(), site, i), reason, ""); err != nil {
				return err
			}
			parts = append(parts, v)
		}
		parts, err := t.contractParts(x, parts)
		if err != nil {
			return err
		}
		t.emit(ir.Instr{Dst: dst, Op: ir.Simple("mkTuple"), Args: parts})
		return nil
	}
	if done, err := t.contractHavoc(dst, x, t.f.Name()+": "+site, reason, ""); done || err != nil {
		return err
	}
	return t.havocInto(dst, x.Type(), t.f.Name()+": "+site, reason, "")
}

// contractHavoc binds dst to a havoc result of a single-result call kept
// within its callee's documented contract (#85): a Len or Cap is from 0 to
// the model's slice bound; runtime.Callers fills at most len(pc).
func (t *fnState) contractHavoc(dst ir.Var, x ssa.Value, site, reason, goHint string) (bool, error) {
	c, ok := x.(*ssa.Call)
	if !ok {
		return false, nil
	}
	if w, signed, isInt := intWidth(x.Type()); !isInt || w != 64 || !signed {
		return false, nil
	}
	var bound ir.Var
	bounded := false
	name, nargs := "", len(c.Call.Args)
	if c.Call.IsInvoke() {
		name = c.Call.Method.Name()
	} else if f := c.Call.StaticCallee(); f != nil {
		name = f.Name()
		if f.Signature.Recv() != nil {
			nargs--
		}
		if f.Pkg != nil && f.Pkg.Pkg.Path() == "runtime" && name == "Callers" && nargs == 2 {
			pc, err := t.val(c.Call.Args[1])
			if err != nil {
				return true, err
			}
			n, one := t.fresh(), t.fresh()
			t.emit(ir.Instr{Dst: n, Op: ir.Simple("len"), Args: []ir.Var{pc}})
			t.emit(ir.Instr{Dst: one, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "1"}}})
			bound, bounded = t.fresh(), true
			t.emit(ir.Instr{Dst: bound, Op: ir.Simple("add"), Args: []ir.Var{n, one}})
		}
	}
	if !bounded && (name == "Len" || name == "Cap") && nargs == 0 {
		bound, bounded = t.fresh(), true
		t.emit(ir.Instr{Dst: bound, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "16385"}}})
	}
	if !bounded {
		return false, nil
	}
	raw := t.fresh()
	if err := t.havocInto(raw, x.Type(), site, reason, goHint); err != nil {
		return true, err
	}
	t.emit(ir.Instr{Dst: dst, Op: ir.Simple("urem"), Args: []ir.Var{raw, bound}})
	return true, nil
}

// randPerm lowers an unmodelled rand.Perm(n) to the identity permutation,
// one of its results, where a havoc slice of any length would break its
// contract (len n, the values 0..n-1) and index past what it permutes.
func (t *fnState) randPerm(x *ssa.Call, n ssa.Value) error {
	nv, err := t.val(n)
	if err != nil {
		return err
	}
	t.assume("%s: call %s: assumed the identity permutation (one of its results; not modelled)", t.f.Name(), x.Call.Value.Name())
	s := t.def(x)
	t.emit(ir.Instr{Dst: s, Op: ir.MakeSlice{Zero: ir.IntVal{Width: 64, Bits: "0"}}, Args: []ir.Var{nv, nv}})
	i, one := t.fresh(), t.fresh()
	t.emit(ir.Instr{Dst: i, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "0"}}})
	t.emit(ir.Instr{Dst: one, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "1"}}})
	head := len(t.out.Blocks)
	body, done := head+1, head+2
	name := t.cur.Name
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: head}
	t.addBlocks(name+".perm-head", name+".perm-body", name+".perm-done")
	t.cur = &t.out.Blocks[head]
	more := t.fresh()
	t.emit(ir.Instr{Dst: more, Op: ir.Simple("slt"), Args: []ir.Var{i, nv}})
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: more, T: body, F: done}
	t.cur = &t.out.Blocks[body]
	a := t.fresh()
	t.emit(ir.Instr{Dst: a, Op: ir.Simple("indexAddr"), Args: []ir.Var{s, i}})
	t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{a, i}})
	t.emit(ir.Instr{Dst: i, Op: ir.Simple("add"), Args: []ir.Var{i, one}})
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: head}
	t.cur = &t.out.Blocks[done]
	return nil
}

// ioCall splits a call to a method of an io-style signature, (p []byte)
// or (s string) to (n int, err error): its name, receiver type and p.
func ioCall(x ssa.Value) (name string, recv types.Type, p ssa.Value, ok bool) {
	c, isCall := x.(*ssa.Call)
	if !isCall {
		return "", nil, nil, false
	}
	var sig *types.Signature
	args := c.Call.Args
	switch {
	case c.Call.IsInvoke():
		name, recv, sig = c.Call.Method.Name(), c.Call.Value.Type(), c.Call.Signature()
	case c.Call.StaticCallee() != nil && c.Call.StaticCallee().Signature.Recv() != nil && len(args) > 0:
		f := c.Call.StaticCallee()
		name, recv, sig, args = f.Name(), args[0].Type(), f.Signature, args[1:]
	default:
		return "", nil, nil, false
	}
	if sig.Params().Len() != 1 || len(args) != 1 || sig.Results().Len() != 2 {
		return "", nil, nil, false
	}
	pt := sig.Params().At(0).Type()
	if !isByteSlice(pt) && !isString(pt) {
		return "", nil, nil, false
	}
	if w, signed, isInt := intWidth(sig.Results().At(0).Type()); !isInt || w != 64 || !signed || !isError(sig.Results().At(1).Type()) {
		return "", nil, nil, false
	}
	return name, recv, args[0], true
}

// isHash says whether t has hash.Hash's methods; its Write "never returns
// an error" (package hash).
func isHash(t types.Type) bool {
	ms := types.NewMethodSet(t)
	if _, ptr := t.Underlying().(*types.Pointer); !ptr && !types.IsInterface(t) {
		ms = types.NewMethodSet(types.NewPointer(t))
	}
	for _, m := range []string{"Write", "Sum", "Reset", "Size", "BlockSize"} {
		if ms.Lookup(nil, m) == nil {
			return false
		}
	}
	return true
}

// contractParts narrows the havoc results of an unmodelled call to what
// its interface's documented contract allows (#85): a Read or Write
// (io.Reader, io.Writer) returns 0 <= n <= len(p); a hash.Hash's Write and
// WriteString take all of p and never fail. Outside the contract the
// caller is right to panic (bufio's "negative read"), so a value there
// only makes a spurious counterexample.
func (t *fnState) contractParts(x ssa.Value, parts []ir.Var) ([]ir.Var, error) {
	name, recv, p, ok := ioCall(x)
	if !ok || len(parts) != 2 || (name != "Read" && name != "Write" && name != "WriteString") {
		return parts, nil
	}
	pv, err := t.val(p)
	if err != nil {
		return nil, err
	}
	n := t.fresh()
	t.emit(ir.Instr{Dst: n, Op: pick(isString(p.Type()), "strLen", "len"), Args: []ir.Var{pv}})
	if name != "Read" && isHash(recv) {
		nilErr := t.fresh()
		t.emit(ir.Instr{Dst: nilErr, Op: ir.Const{V: ir.ErrVal{}}})
		return []ir.Var{n, nilErr}, nil
	}
	if name == "WriteString" {
		return parts, nil
	}
	one, n1, k := t.fresh(), t.fresh(), t.fresh()
	t.emit(ir.Instr{Dst: one, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "1"}}})
	t.emit(ir.Instr{Dst: n1, Op: ir.Simple("add"), Args: []ir.Var{n, one}})
	t.emit(ir.Instr{Dst: k, Op: ir.Simple("urem"), Args: []ir.Var{parts[0], n1}})
	return []ir.Var{k, parts[1]}, nil
}

// unsafeView matches the zero-copy conversions unsafe.String(unsafe.SliceData(b),
// len(b)) and unsafe.Slice(unsafe.StringData(s), len(s)) (prometheus'
// yoloString and yoloBytes, #87), giving b or s: the model converts it as
// string(b) or []byte(s). Exact for a valid program: package unsafe forbids
// writing the bytes while the result is in use.
func unsafeView(c ssa.CallCommon) (ssa.Value, bool) {
	if len(c.Args) != 2 {
		return nil, false
	}
	data, ok := c.Args[0].(*ssa.Call)
	if !ok || len(data.Call.Args) != 1 {
		return nil, false
	}
	db, ok := data.Call.Value.(*ssa.Builtin)
	if !ok || (db.Name() != "SliceData" && db.Name() != "StringData") {
		return nil, false
	}
	src := data.Call.Args[0]
	n, ok := c.Args[1].(*ssa.Call)
	if !ok || len(n.Call.Args) != 1 || n.Call.Args[0] != src {
		return nil, false
	}
	if lb, ok := n.Call.Value.(*ssa.Builtin); !ok || lb.Name() != "len" {
		return nil, false
	}
	if db.Name() == "SliceData" && !isByteSlice(src.Type()) || db.Name() == "StringData" && !isString(src.Type()) {
		return nil, false
	}
	return src, true
}

// ---- types -----------------------------------------------------------------

func intWidth(t types.Type) (w int, signed bool, ok bool) {
	b, isBasic := t.Underlying().(*types.Basic)
	if !isBasic {
		return 0, false, false
	}
	switch b.Kind() {
	case types.Int, types.Int64, types.UntypedInt:
		return 64, true, true
	case types.Uint, types.Uint64, types.Uintptr:
		return 64, false, true
	case types.Int32, types.UntypedRune:
		return 32, true, true
	case types.Uint32:
		return 32, false, true
	case types.Int16:
		return 16, true, true
	case types.Uint16:
		return 16, false, true
	case types.Int8:
		return 8, true, true
	case types.Uint8:
		return 8, false, true
	}
	return 0, false, false
}

func isBool(t types.Type) bool {
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&types.IsBoolean != 0
}

var errorType = types.Universe.Lookup("error").Type()

func isError(t types.Type) bool { return types.Identical(t, errorType) }

func isString(t types.Type) bool {
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&types.IsString != 0
}

func isRuneSlice(t types.Type) bool {
	s, ok := t.Underlying().(*types.Slice)
	if !ok {
		return false
	}
	b, ok := s.Elem().Underlying().(*types.Basic)
	return ok && (b.Kind() == types.Int32 || b.Kind() == types.Rune)
}

func isByteSlice(t types.Type) bool {
	s, ok := t.Underlying().(*types.Slice)
	if !ok {
		return false
	}
	b, ok := s.Elem().Underlying().(*types.Basic)
	return ok && (b.Kind() == types.Uint8 || b.Kind() == types.Byte)
}

func isPointer(t types.Type) bool {
	_, ok := t.Underlying().(*types.Pointer)
	return ok
}

func isPointerToArray(t types.Type) bool {
	p, ok := t.Underlying().(*types.Pointer)
	if !ok {
		return false
	}
	_, ok = p.Elem().Underlying().(*types.Array)
	return ok
}

func isSlice(t types.Type) bool {
	_, ok := t.Underlying().(*types.Slice)
	return ok
}

// isOpaqueType says whether t is modelled as an opaque value: interfaces,
// maps, funcs, channels, and the basic types the dialect has no arithmetic
// for (floats, complex numbers, unsafe pointers).
func isOpaqueType(t types.Type) bool {
	if isError(t) {
		return false // the error interface is modelled precisely (nil or not)
	}
	switch u := t.Underlying().(type) {
	case *types.Interface, *types.Map, *types.Signature, *types.Chan:
		return true
	case *types.Basic:
		switch u.Kind() {
		case types.Float32, types.Float64, types.Complex64, types.Complex128, types.UnsafePointer,
			types.UntypedFloat, types.UntypedComplex:
			return true
		}
	}
	return false
}

// isStringsBuilder says whether t is strings.Builder, which the model keeps
// as a string cell (its methods are dialect ops).
func isStringsBuilder(t types.Type) bool {
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "strings" && n.Obj().Name() == "Builder"
}

// isForeignUnexported says whether t is an unexported named type of
// another package (gin's `errorMsgs`): a test in the target package cannot
// name it, so values of it are opaque parameters with the zero value.
func isForeignUnexported(pkg *types.Package, t types.Type) bool {
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg() != pkg && !n.Obj().Exported()
}

// isForeignOpaqueStruct says whether t is a struct of another package with
// no exported field (sync.Mutex, strings.Builder): a test cannot build one
// with contents, so it is an opaque parameter with the zero value.
func isForeignOpaqueStruct(pkg *types.Package, t types.Type) bool {
	n, ok := t.(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg() == pkg {
		return false
	}
	st, ok := n.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Exported() {
			return false
		}
	}
	return true
}

// intoHandle says whether addr points into a handle (isHandle): a field
// of one, or of a struct or array inside one. The model holds a handle as
// an opaque pointer, so such a load is havoc, not a read of its memory.
func (t *fnState) intoHandle(addr ssa.Value) bool {
	switch a := addr.(type) {
	case *ssa.FieldAddr:
		if pt, ok := a.X.Type().Underlying().(*types.Pointer); ok && isHandle(funcPkg(t.f), pt.Elem()) {
			return true
		}
		return t.intoHandle(a.X)
	case *ssa.IndexAddr:
		return t.intoHandle(a.X)
	}
	return false
}

// isHandle says whether t is a struct type of another package, outside
// the standard library, with state of its own (an unexported field), whose
// package exports a function returning *t (a constructor: gorm.Open,
// NewStore). Pointers to it are handles the constructor builds, not data a
// caller fills in. A struct of exported fields is data even when its
// package has such a function (one-api's meta.GetByContext returns the
// *Meta a request carries; callers also build one field by field).
func isHandle(pkg *types.Package, t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg() == pkg || isStdlib(n.Obj().Pkg().Path()) {
		return false
	}
	st, ok := n.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	private := false
	for i := 0; i < st.NumFields(); i++ {
		private = private || !st.Field(i).Exported()
	}
	if !private {
		return false
	}
	if v, ok := handleCache[n]; ok {
		return v
	}
	scope := n.Obj().Pkg().Scope()
	found := false
	for _, name := range scope.Names() {
		fn, ok := scope.Lookup(name).(*types.Func)
		if !ok || !fn.Exported() {
			continue
		}
		res := fn.Type().(*types.Signature).Results()
		if res.Len() == 0 {
			continue
		}
		if pt, ok := res.At(0).Type().(*types.Pointer); ok && types.Identical(pt.Elem(), n) {
			found = true
			break
		}
	}
	handleCache[n] = found
	return found
}

var handleCache = map[*types.Named]bool{}

// zeroLit is the Go literal of t's zero value where the harness needs one
// for an opaque element: "0" for numbers, "nil" for everything else.
func zeroLit(t types.Type, src string) string {
	if b, ok := t.Underlying().(*types.Basic); ok && b.Info()&types.IsNumeric != 0 {
		return "0"
	}
	if _, ok := t.Underlying().(*types.Struct); ok {
		return src + "{}"
	}
	return "nil"
}

// abiArrayLimit bounds the arrays described element by element in the
// ABI; longer ones are opaque parameters (zero in M3, `.unit` in the model).
const abiArrayLimit = 64

// isTestingType says whether t is (a pointer to) a type of package testing:
// a function taking one is a test helper, not a target.
func isTestingType(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "testing"
}

// valueOK says whether values of type t are representable in the model:
// integers, bools, errors, strings, and pointers to, structs of and slices
// of such values. Interfaces, maps, funcs and channels are representable
// as opaque values: the model never looks inside them, every read through
// them is havoc, and M3 passes their zero value. Recursive types are fine: a pointer
// cycle is cut by treating the type under construction as representable.
func valueOK(t types.Type) bool { return valueOKVisiting(t, map[types.Type]bool{}) }

func valueOKVisiting(t types.Type, visiting map[types.Type]bool) bool {
	if _, _, ok := intWidth(t); ok || isBool(t) || isError(t) || isString(t) || isOpaqueType(t) {
		return true
	}
	if visiting[t] {
		return true
	}
	visiting[t] = true
	defer delete(visiting, t)
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return valueOKVisiting(u.Elem(), visiting)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if !valueOKVisiting(u.Field(i).Type(), visiting) {
				return false
			}
		}
		return true
	case *types.Slice:
		return valueOKVisiting(u.Elem(), visiting)
	case *types.Array:
		return u.Len() == 0 || valueOKVisiting(u.Elem(), visiting)
	}
	return false
}

// zeroVal is the model's zero value of t.
func zeroVal(t types.Type) (ir.Val, bool) {
	if w, _, ok := intWidth(t); ok {
		return ir.IntVal{Width: w, Bits: "0"}, true
	}
	if isStringsBuilder(t) {
		return ir.EmptyStr{}, true // a Builder is a string cell in the model
	}
	if isBool(t) {
		return ir.BoolVal(false), true
	}
	if isError(t) {
		return ir.ErrVal{}, true
	}
	if isString(t) {
		return ir.EmptyStr{}, true
	}
	if isNilable(t) {
		return ir.OpaqueNil{}, true
	}
	if isOpaqueType(t) {
		return ir.Opaque{}, true
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return ir.NilPtr{}, true
	case *types.Slice:
		return ir.NilSlice{}, true
	case *types.Struct:
		var fields []ir.Val
		for i := 0; i < u.NumFields(); i++ {
			z, ok := zeroVal(u.Field(i).Type())
			if !ok {
				return nil, false
			}
			fields = append(fields, z)
		}
		return ir.StructVal{Fields: fields}, true
	case *types.Array:
		if u.Len() > 4096 {
			return nil, false
		}
		z, ok := zeroVal(u.Elem())
		if !ok {
			// Arrays of interfaces (varargs) hold opaque values.
			if z, ok = assumedValue(u.Elem()); !ok {
				return nil, false
			}
		}
		elems := make([]ir.Val, u.Len())
		for i := range elems {
			elems[i] = z
		}
		return ir.ArrVal{Elems: elems}, true
	}
	return nil, false
}

// resultOK says whether a result type can be returned by a modelled function.
func resultOK(t types.Type) bool { return valueOK(t) }

func sliceElem(t types.Type) (w int, signed bool, ok bool) {
	s, isSlice := t.Underlying().(*types.Slice)
	if !isSlice {
		return 0, false, false
	}
	return intWidth(s.Elem())
}

func isUnitTuple(t types.Type) bool {
	tu, ok := t.(*types.Tuple)
	return ok && tu.Len() == 0
}

// spell renders a type as source in package pkg would: unqualified for
// pkg's own types, `pkgname.T` otherwise. It also returns the import path
// of a named type from another package.
func spell(pkg *types.Package, t types.Type) (src, importPath string) {
	src, paths := spellAll(pkg, t)
	if n, ok := t.(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg() != pkg {
		importPath = n.Obj().Pkg().Path()
	} else if len(paths) > 0 {
		importPath = paths[0]
	}
	return src, importPath
}

// spellAll is spell with every import path the spelling mentions, in
// order of first appearance: a func or map type names other packages'
// types inside it (`func(http.RoundTripper) http.RoundTripper`).
func spellAll(pkg *types.Package, t types.Type) (src string, paths []string) {
	qual := func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		if !slices.Contains(paths, p.Path()) {
			paths = append(paths, p.Path())
		}
		if recordImport != nil {
			return recordImport(p.Path(), p.Name())
		}
		return p.Name()
	}
	src = types.TypeString(t, qual)
	return src, paths
}

// recordImport, while an ABI is being built, notes every package a
// spelling mentions and returns the qualifier to spell it with: the
// package name, or an alias when another package of that name is already
// in use (two packages called `anthropic`).
var recordImport func(path, name string) string

// funcPkg is the package a function belongs to; an instance of a generic
// function has none of its own and belongs to its origin's.
func funcPkg(f *ssa.Function) *types.Package {
	if f.Pkg == nil && f.Origin() != nil && f.Origin().Pkg != nil {
		return f.Origin().Pkg.Pkg
	}
	if f.Pkg == nil {
		return nil
	}
	return f.Pkg.Pkg
}

func abiOf(f *ssa.Function, json *jsonTable) (*contract.ABI, error) {
	abi := &contract.ABI{GoFunc: f.String(), GoSig: f.Signature.String(), Frontend: Frontend, Package: funcPkg(f).Path(), Variadic: f.Signature.Variadic(), Imports: map[string]string{}}
	used := map[string]string{} // qualifier -> path
	recordImport = func(path, name string) string {
		alias := name
		for i := 2; ; i++ {
			if p, taken := used[alias]; !taken || p == path {
				break
			}
			alias = fmt.Sprintf("%s%d", name, i)
		}
		used[alias] = path
		if alias != filepath.Base(path) {
			abi.Imports[path] = alias
		}
		return alias
	}
	defer func() { recordImport = nil }()
	if f.Signature.Recv() != nil {
		abi.Method = f.Name()
	}
	pkg := funcPkg(f)
	ab := &abiBuilder{pkg: pkg, abi: abi, index: map[*types.Named]int{}, fset: f.Prog.Fset, json: json, asserted: assertedTypes(f)}
	for _, p := range f.Params {
		if isTestEntry(f) {
			// The test's t: opaque in the model, the harness's own t in M3.
			abi.Params = append(abi.Params, contract.ABIParam{Go: p.Name(), GoType: "*testing.T", Kind: contract.ABIOpaque,
				Model: []string{p.Name()}, Zero: "go2leanT"})
			continue
		}
		if isTestingType(p.Type()) {
			return nil, fmt.Errorf("test helper: parameter %s has type %s", p.Name(), p.Type())
		}
		ap, err := ab.param(p.Name(), p.Type(), nil)
		if err != nil {
			return nil, fmt.Errorf("parameter %s has unsupported type %s: %w", p.Name(), p.Type(), err)
		}
		abi.Params = append(abi.Params, ap)
	}
	res := f.Signature.Results()
	for i := 0; i < res.Len(); i++ {
		rt := res.At(i).Type()
		if !resultOK(rt) {
			return nil, fmt.Errorf("result type %s not supported", rt)
		}
		var r contract.ABIResult
		r.GoType, r.ExtraPkgs = spellAll(pkg, rt)
		if len(r.ExtraPkgs) > 0 {
			r.TypePkg = r.ExtraPkgs[0]
		}
		_, r.Func = rt.Underlying().(*types.Signature)
		abi.Results = append(abi.Results, r)
	}
	return abi, nil
}

// abiBuilder describes parameters, moving named struct types that refer
// to themselves into the ABI's type table so the description stays finite.
type abiBuilder struct {
	pkg   *types.Package
	abi   *contract.ABI
	index map[*types.Named]int
	// anyDepth counts the empty interfaces being described around the
	// current value: an interface inside an interface's map or slice holds
	// scalars only, so the description stays finite.
	anyDepth int
	fset     *token.FileSet
	// json is the package's JSON table, for SDK response types (nil: no
	// JSON descriptions).
	json *jsonTable
	// asserted are the concrete types the function's call tree asserts
	// empty interfaces to: more values an interface input may hold.
	asserted []types.Type
}

// register adds a recursive named struct type to the table.
func (ab *abiBuilder) register(n *types.Named, src, ipath string) int {
	if idx, ok := ab.index[n]; ok {
		return idx
	}
	idx := len(ab.abi.Types)
	ab.index[n] = idx
	ab.abi.Types = append(ab.abi.Types, contract.ABIType{GoType: src, TypePkg: ipath})
	return idx
}

// hasRespMeta says whether st is an SDK response type: it keeps JSON
// metadata (raw text, per-member fields) in a field named JSON.
func hasRespMeta(st *types.Struct) bool {
	for i := 0; i < st.NumFields(); i++ {
		if f := st.Field(i); f.Name() == "JSON" {
			if mst, ok := f.Type().Underlying().(*types.Struct); ok && isRespJSONMeta(mst) {
				return true
			}
		}
	}
	return false
}

// fields describes the fields of a struct; unexported fields of a foreign
// struct are opaque, since a test in the target package cannot set them.
func (ab *abiBuilder) fields(u *types.Struct, foreign bool, stack []*types.Named) ([]contract.ABIParam, error) {
	var fields []contract.ABIParam
	meta := isRespJSONMeta(u)
	for i := 0; i < u.NumFields(); i++ {
		fld := u.Field(i)
		if foreign && meta && fld.Name() == "raw" {
			// The raw JSON behind RawJSON(): a JSON text of extra fields.
			fields = append(fields, contract.ABIParam{Go: "raw", GoType: "string", Model: []string{"raw"}, Kind: contract.ABIString, JSONText: true})
			continue
		}
		if foreign && meta && fld.Name() == "ExtraFields" {
			// Decoding sets it from the raw JSON; a test cannot set it on
			// its own (the metadata struct cannot be spelled), so it stays
			// nil in the model.
			fields = append(fields, contract.ABIParam{Go: fld.Name(), GoType: types.TypeString(fld.Type(), nil), Model: []string{fld.Name()}, Kind: contract.ABIOpaque, Zero: "nil", ZeroModel: map[string]any{"k": "nil"}})
			continue
		}
		if foreign && !fld.Exported() {
			p := contract.ABIParam{Go: fld.Name(), GoType: types.TypeString(fld.Type(), nil), Model: []string{fld.Name()}, Kind: contract.ABIOpaque, Zero: zeroLit(fld.Type(), types.TypeString(fld.Type(), nil))}
			if z, ok := zeroVal(fld.Type()); ok {
				p.ZeroModel = zeroModel(z)
			}
			fields = append(fields, p)
			continue
		}
		f, err := ab.param(fld.Name(), fld.Type(), stack)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", fld.Name(), err)
		}
		if f.Kind == contract.ABIString && len(f.Enum) == 0 && ab.fset != nil {
			f.Enum = docEnum(ab.fset, fld)
		}
		f.JSONName, f.JSONInline = jsonName(fld, u.Tag(i))
		fields = append(fields, f)
	}
	return fields, nil
}

var (
	docLines   = map[string][]string{}
	anyOfRe    = regexp.MustCompile(`^\s*//\s*Any of (.*)$`)
	quotedRe   = regexp.MustCompile(`"([^"\\]*)"`)
	docLinesMu sync.Mutex
)

// docEnum reads the values a string field documents as `// Any of "a",
// "b".` in the comment above it (the convention of the Stainless-generated
// OpenAI and Anthropic SDKs, whose enum-like fields are plain strings).
func docEnum(fset *token.FileSet, fld *types.Var) []string {
	pos := fset.Position(fld.Pos())
	if pos.Filename == "" || pos.Line < 2 {
		return nil
	}
	docLinesMu.Lock()
	lines, ok := docLines[pos.Filename]
	if !ok {
		if b, err := os.ReadFile(pos.Filename); err == nil {
			lines = strings.Split(string(b), "\n")
		}
		docLines[pos.Filename] = lines
	}
	docLinesMu.Unlock()
	for i := pos.Line - 2; i >= 0 && i < len(lines); i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "//") {
			break
		}
		if m := anyOfRe.FindStringSubmatch(l); m != nil {
			// The list goes on over the next comment lines while a line
			// ends with a comma.
			var out []string
			list := m[1]
			for j := i + 1; ; j++ {
				for _, q := range quotedRe.FindAllStringSubmatch(list, -1) {
					out = append(out, q[1])
				}
				if !strings.HasSuffix(strings.TrimSpace(list), ",") || j >= pos.Line-1 {
					break
				}
				next := strings.TrimSpace(lines[j])
				if !strings.HasPrefix(next, "//") {
					break
				}
				list = strings.TrimPrefix(next, "//")
			}
			return out
		}
	}
	return nil
}

// jsonName is the key encoding/json uses for a struct field, and whether
// the SDKs' `json:",inline"` option puts the field's encoding in place of
// the struct's.
func jsonName(fld *types.Var, tag string) (string, bool) {
	name, opts, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
	inline := name == "" && slices.Contains(strings.Split(opts, ","), "inline")
	if name == "" {
		return fld.Name(), inline
	}
	return name, inline
}

// isRespJSONMeta says whether u is the JSON metadata struct of an OpenAI or
// Anthropic SDK response type: fields of type respjson.Field (and
// ExtraFields) next to an unexported raw string.
func isRespJSONMeta(u *types.Struct) bool {
	hasRaw, hasField := false, false
	for i := 0; i < u.NumFields(); i++ {
		f := u.Field(i)
		if f.Name() == "raw" && isString(f.Type()) {
			hasRaw = true
		}
		if n, ok := types.Unalias(f.Type()).(*types.Named); ok && n.Obj().Name() == "Field" && n.Obj().Pkg() != nil &&
			strings.HasSuffix(n.Obj().Pkg().Path(), "/packages/respjson") {
			hasField = true
		}
	}
	return hasRaw && hasField
}

// zeroModel describes a zero value for the generator (see valOfJson in
// lean/Go2Lean/Driver.lean).
func zeroModel(v ir.Val) any {
	switch x := v.(type) {
	case ir.IntVal:
		return map[string]any{"k": "int", "w": x.Width}
	case ir.BoolVal:
		return map[string]any{"k": "bool"}
	case ir.ErrVal:
		return map[string]any{"k": "err"}
	case ir.EmptyStr:
		return map[string]any{"k": "str"}
	case ir.OpaqueNil:
		return map[string]any{"k": "nil"}
	case ir.NilPtr:
		return map[string]any{"k": "ptr"}
	case ir.NilSlice:
		return map[string]any{"k": "slice"}
	case ir.StructVal:
		fs := make([]any, len(x.Fields))
		for i, f := range x.Fields {
			fs[i] = zeroModel(f)
		}
		return map[string]any{"k": "struct", "fields": fs}
	case ir.ArrVal:
		es := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			es[i] = zeroModel(e)
		}
		return map[string]any{"k": "arr", "elems": es}
	}
	return map[string]any{"k": "unit"}
}

// param describes one value of type t named name. `stack` holds the named
// struct types under construction; meeting one again means recursion.
func (ab *abiBuilder) param(name string, t types.Type, stack []*types.Named) (contract.ABIParam, error) {
	pkg := ab.pkg
	src, ipath := spell(pkg, t)
	// An alias (`type PairingManager = core.PairingManager`) is spelled by
	// its own name but described as the type it stands for.
	t = types.Unalias(t)
	if n, ok := t.(*types.Named); ok {
		if st, isStruct := n.Underlying().(*types.Struct); isStruct && !isForeignOpaqueStruct(pkg, t) && !isForeignUnexported(pkg, t) {
			// Every named struct lives in the type table once and parameters
			// and fields refer to it: recursion resolves through the index,
			// and a type nested many times (gin.Context) is described once.
			if idx, done := ab.index[n]; done {
				return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Model: []string{name}, Kind: contract.ABIStruct, TypeRef: ab.abi.Types[idx].GoType}, nil
			}
			idx := ab.register(n, src, ipath)
			fields, err := ab.fields(st, ipath != "", stack)
			if err != nil {
				return contract.ABIParam{}, err
			}
			ab.abi.Types[idx].Fields = fields
			if ab.json != nil && hasRespMeta(st) {
				ab.abi.Types[idx].JSONDesc = ab.json.desc(n) + 1
			}
			return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Model: []string{name}, Kind: contract.ABIStruct, TypeRef: ab.abi.Types[idx].GoType}, nil
		}
	}
	_, allPaths := spellAll(pkg, t)
	extra := []string{}
	for _, ip := range allPaths {
		if ip != ipath {
			extra = append(extra, ip)
		}
	}
	switch {
	case isStringsBuilder(t):
		// An empty Builder (a copied non-empty one panics in Go): a string of
		// length zero in the model, `strings.Builder{}` in the test.
		return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Model: []string{name}, Kind: contract.ABIString, MaxLen: -1}, nil
	case !isForeignUnexported(pkg, t) && isEmptyInterface(t):
		return ab.anyParam(name, src, ipath, extra, stack), nil
	case !isForeignUnexported(pkg, t) && isGeneratedMap(t):
		if mp, ok := ab.mapParam(name, t.Underlying().(*types.Map), src, ipath, extra, stack); ok {
			return mp, nil
		}
		return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, ExtraPkgs: extra, Model: []string{name}, Kind: contract.ABIOpaque, Zero: zeroLit(t, src)}, nil
	case isOpaqueType(t) || isForeignOpaqueStruct(pkg, t) || isForeignUnexported(pkg, t):
		return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, ExtraPkgs: extra, Model: []string{name}, Kind: contract.ABIOpaque, Zero: zeroLit(t, src)}, nil
	case isBool(t):
		return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Model: []string{name}, Kind: contract.ABIBool}, nil
	case isError(t):
		return contract.ABIParam{Go: name, GoType: "error", Model: []string{name}, Kind: contract.ABIError}, nil
	case isString(t):
		return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Model: []string{name}, Kind: contract.ABIString, Enum: stringEnum(t)}, nil
	}
	if w, signed, ok := intWidth(t); ok {
		return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Width: w, Signed: signed, Model: []string{name}, Kind: contract.ABIInt}, nil
	}
	// A slice, array or pointer of an unexported type of another package
	// cannot be written down by the test either.
	switch u := t.Underlying().(type) {
	case *types.Array, *types.Slice, *types.Pointer:
		if isForeignUnexported(pkg, u.(interface{ Elem() types.Type }).Elem()) {
			return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Model: []string{name}, Kind: contract.ABIOpaque, Zero: "nil"}, nil
		}
	}
	switch u := t.Underlying().(type) {
	case *types.Array:
		esrc, epath := spell(pkg, u.Elem())
		if u.Len() > abiArrayLimit {
			return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Model: []string{name}, Kind: contract.ABIOpaque}, nil
		}
		ap := contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Kind: contract.ABIArray, Model: []string{name}, Elem: esrc, ElemPkg: epath, Len: int(u.Len())}
		if ew, esigned, ok := intWidth(u.Elem()); ok {
			ap.ElemWidth, ap.ElemSigned = ew, esigned
			return ap, nil
		}
		elem, err := ab.param("elem", u.Elem(), stack)
		if err != nil {
			return contract.ABIParam{}, fmt.Errorf("array of %s: %w", u.Elem(), err)
		}
		ap.ElemParam = &elem
		return ap, nil
	case *types.Slice:
		esrc, epath := spell(pkg, u.Elem())
		ap := contract.ABIParam{
			Go: name, GoType: src, TypePkg: ipath, Kind: contract.ABISlice,
			Model: []string{name + "_ref", name + "_off", name + "_len", name + "_cap"},
			Elem:  esrc, ElemPkg: epath,
		}
		if ew, esigned, ok := intWidth(u.Elem()); ok {
			ap.ElemWidth, ap.ElemSigned = ew, esigned
			return ap, nil
		}
		elem, err := ab.param("elem", u.Elem(), stack)
		if err != nil {
			return contract.ABIParam{}, fmt.Errorf("slice of %s: %w", u.Elem(), err)
		}
		ap.ElemParam = &elem
		return ap, nil
	case *types.Struct:
		// An anonymous struct (or a named one of an opaque kind handled
		// above): described inline.
		fields, err := ab.fields(u, ipath != "", stack)
		if err != nil {
			return contract.ABIParam{}, err
		}
		return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Model: []string{name}, Kind: contract.ABIStruct, Fields: fields}, nil
	case *types.Pointer:
		if isHandle(pkg, u.Elem()) {
			// A handle another package builds (gorm.Open, NewClient): a
			// non-nil pointer to a value the model does not look into, not
			// one assembled field by field (whose internals a real one
			// never has nil).
			esrc, epath := spell(pkg, u.Elem())
			pointee := contract.ABIParam{Go: name, GoType: esrc, TypePkg: epath, Model: []string{name}, Kind: contract.ABIOpaque, Zero: zeroLit(u.Elem(), esrc)}
			return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Model: []string{name}, Kind: contract.ABIPtr, Pointee: &pointee}, nil
		}
		pointee, err := ab.param(name, u.Elem(), stack)
		if err != nil {
			return contract.ABIParam{}, err
		}
		return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, Model: []string{name}, Kind: contract.ABIPtr, Pointee: &pointee}, nil
	}
	return contract.ABIParam{}, fmt.Errorf("unsupported type %s", t)
}

// isEmptyInterface says whether t is interface{} (any, or a named type
// for it): the inputs the generator fills with values of a few dynamic
// types. Interfaces with methods stay opaque.
func isEmptyInterface(t types.Type) bool {
	it, ok := t.Underlying().(*types.Interface)
	return ok && it.NumMethods() == 0 && !isError(t)
}

// isGeneratedMap says whether t is a map the generator builds: keys of a
// string, integer or bool kind (they compare by value in the model).
func isGeneratedMap(t types.Type) bool {
	m, ok := t.Underlying().(*types.Map)
	if !ok {
		return false
	}
	if isString(m.Key()) || isBool(m.Key()) {
		return true
	}
	_, _, ok = intWidth(m.Key())
	return ok
}

// mapParam describes a map input; false when its key or value cannot be
// described (the map then stays opaque).
func (ab *abiBuilder) mapParam(name string, m *types.Map, src, ipath string, extra []string, stack []*types.Named) (contract.ABIParam, bool) {
	key, err := ab.param("key", m.Key(), stack)
	if err != nil {
		return contract.ABIParam{}, false
	}
	val, err := ab.param("value", m.Elem(), stack)
	if err != nil {
		return contract.ABIParam{}, false
	}
	return contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, ExtraPkgs: extra, Model: []string{name},
		Kind: contract.ABIMap, Key: &key, Value: &val, Zero: "nil"}, true
}

// assertedTypes lists the concrete types (at most 6) that f and the
// functions it calls, three calls deep, assert empty interfaces to: x.(T)
// and the cases of type switches. Interface types, floats and channels are
// left out, as are types the model cannot generate (anyParam skips them).
func assertedTypes(f *ssa.Function) []types.Type {
	var out []types.Type
	seen := map[*ssa.Function]bool{}
	var walk func(f *ssa.Function, depth int)
	walk = func(f *ssa.Function, depth int) {
		if f == nil || seen[f] || depth > 3 || len(out) >= 6 || (f.Pkg != nil && isStdlib(f.Pkg.Pkg.Path())) {
			return
		}
		seen[f] = true
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch in := in.(type) {
				case *ssa.TypeAssert:
					it, ok := in.X.Type().Underlying().(*types.Interface)
					if !ok || !it.Empty() || types.IsInterface(in.AssertedType) {
						continue
					}
					switch u := in.AssertedType.Underlying().(type) {
					case *types.Basic:
						if u.Info()&(types.IsInteger|types.IsString|types.IsBoolean) == 0 {
							continue
						}
					case *types.Chan, *types.Signature:
						continue
					}
					if len(out) < 6 && !slices.ContainsFunc(out, func(t types.Type) bool { return types.Identical(t, in.AssertedType) }) {
						out = append(out, in.AssertedType)
					}
				case ssa.CallInstruction:
					walk(in.Common().StaticCallee(), depth+1)
				}
			}
		}
		for _, af := range f.AnonFuncs {
			walk(af, depth)
		}
	}
	walk(f, 0)
	return out
}

// anyParam describes an empty interface input: nil, or a string, bool or
// int, or (outside another interface's contents) a map[string]interface{}
// or []interface{} of such values, the shapes decoded JSON has, or a value
// of a type the function's call tree asserts to (ab.asserted).
func (ab *abiBuilder) anyParam(name, src, ipath string, extra []string, stack []*types.Named) contract.ABIParam {
	alts := []types.Type{types.Typ[types.String], types.Typ[types.Bool], types.Typ[types.Int]}
	if ab.anyDepth == 0 {
		empty := types.NewInterfaceType(nil, nil)
		alts = append(alts, types.NewMap(types.Typ[types.String], empty), types.NewSlice(empty))
		// The types the code asserts to (a type switch's int64, uint8 or
		// json.Number cases), so that every case can be taken.
		for _, at := range ab.asserted {
			if !slices.ContainsFunc(alts, func(t types.Type) bool { return types.Identical(t, at) }) {
				alts = append(alts, at)
			}
		}
	}
	ab.anyDepth++
	defer func() { ab.anyDepth-- }()
	p := contract.ABIParam{Go: name, GoType: src, TypePkg: ipath, ExtraPkgs: extra, Model: []string{name}, Kind: contract.ABIAny, Zero: "nil"}
	for _, at := range alts {
		a, err := ab.param("v", at, stack)
		if err != nil {
			continue
		}
		a.Tag = typeTag(at)
		p.Alts = append(p.Alts, a)
	}
	return p
}

// specTarget names the function f is a spec for. A `go2lean:spec X` doc
// comment wins; otherwise a function named Spec<X>... whose parameters are
// X's parameters followed by X's result is taken as a spec for X, so a
// misplaced marker does not turn a spec into a target.
func specTarget(f *ssa.Function, byName map[string]*ssa.Function) string {
	if decl, ok := f.Syntax().(*ast.FuncDecl); ok && decl.Doc != nil {
		if m := specRe.FindStringSubmatch(decl.Doc.Text()); m != nil {
			return m[1]
		}
	}
	name := f.Name()
	if !strings.HasPrefix(name, "Spec") || f.Signature.Results().Len() != 1 || !isBool(f.Signature.Results().At(0).Type()) {
		return ""
	}
	rest := name[len("Spec"):]
	// Longest target name that is a prefix of the rest and whose signature
	// fits; a method Type.Method matches as TypeMethod.
	best, bestFlat := "", ""
	for tname, target := range byName {
		flat := strings.ReplaceAll(tname, ".", "")
		if target == f || !strings.HasPrefix(rest, flat) || len(flat) <= len(bestFlat) {
			continue
		}
		if specSignatureFits(f.Signature, target.Signature) {
			best, bestFlat = tname, flat
		}
	}
	return best
}

// specSignatureFits checks spec(receiver?, params of target..., results of
// target...) bool.
func specSignatureFits(spec, target *types.Signature) bool {
	var want []types.Type
	if recv := target.Recv(); recv != nil {
		want = append(want, recv.Type())
	}
	for i := 0; i < target.Params().Len(); i++ {
		want = append(want, target.Params().At(i).Type())
	}
	for i := 0; i < target.Results().Len(); i++ {
		want = append(want, target.Results().At(i).Type())
	}
	sp := spec.Params()
	if spec.Results().Len() != 1 || sp.Len() != len(want) {
		return false
	}
	for i, t := range want {
		if !types.Identical(sp.At(i).Type(), t) {
			return false
		}
	}
	return true
}

// GoName strips the package qualifier from an ssa function name.
func GoName(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}
