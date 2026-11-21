// Package symexec is a bounded symbolic executor for Go functions over
// go/ssa. It looks for inputs that reach a run-time panic (index out of
// range, division by zero, a slice bound, an explicit panic) in functions of
// integers, booleans and slices of integers, by collecting the path condition
// of every path (loops unrolled to a bound, calls inlined to a depth) and
// asking Z3 whether the panic condition is satisfiable on it.
//
// The executor is approximate on purpose. Anything it does not model (a call
// without a body, a struct field, a map, an allocation) yields a fresh
// unconstrained value, so a finding can be spurious; the replay stage runs
// the inputs on the real code and is the judge. It never claims that a
// function is free of panics.
package symexec

import (
	"bufio"
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/ssa"
)

// Options bound the search.
type Options struct {
	LoopBound  int           // visits of one block on one path (default MaxLen+2)
	CallDepth  int           // inlined call depth (default 3)
	MaxLen     int           // length bound of slice parameters (default 12)
	MaxPaths   int           // paths explored (default 2000)
	SolverTime time.Duration // per solver query (default 5s)
	FuncTime   time.Duration // per function (default 30s)
	Root       string        // report only panics in files under this directory
}

func (o *Options) defaults() {

	if o.CallDepth == 0 {
		o.CallDepth = 3
	}
	if o.MaxLen == 0 {
		o.MaxLen = 12
	}
	if o.LoopBound == 0 {
		o.LoopBound = o.MaxLen + 2 // a loop over a slice of the longest length runs to its end
	}
	if o.MaxPaths == 0 {
		o.MaxPaths = 2000
	}
	if o.FuncTime == 0 {
		o.FuncTime = 30 * time.Second
	}
	if o.SolverTime == 0 {
		o.SolverTime = 5 * time.Second
	}
}

// Input is a value of one parameter in a finding.
type Input struct {
	Name string
	Type string
	Go   string // a Go expression for the value
}

// Finding is a panic reachable on some path.
type Finding struct {
	Func    string
	Imports []string // import paths the Go expressions of Inputs need
	Kind    string   // "index out of range", "division by zero", "slice bounds", "explicit panic"
	Pos     token.Position
	Inputs  []Input
	// Approximate says that the path used a value the executor did not model.
	Approximate bool
}

// sval is a symbolic value.
type sval interface{}

type bv struct { // an integer of Width bits, an SMT bitvector expression
	E      string
	Width  int
	Signed bool
}

type bl struct{ E string } // an SMT Bool expression

type slc struct { // a slice of integers: elements mem[Arr][Off+i], i < Len
	Arr    int    // id of the SMT array (Array (_ BitVec 64) (_ BitVec EW)) in state.mem
	Off    string // BV64
	Len    string // BV64
	Cap    string // BV64
	EW     int
	Signed bool
}

type arrObj struct{ S slc } // a pointer to a local array of integers

type addr struct { // &s[i] of a slice
	S slc
	I string // BV64 absolute index
}

type tuple []sval

type unknown struct{} // an unmodelled value

// state of one path.
type state struct {
	stack  []*frame
	cons   []string
	approx bool
	mem    map[int]string // the current SMT term of every array
}

type frame struct {
	fn    *ssa.Function
	env   map[ssa.Value]sval
	block *ssa.BasicBlock
	prev  *ssa.BasicBlock
	pc    int
	visit map[*ssa.BasicBlock]int
	ret   ssa.Value // the call instruction in the caller that receives the result
}

func (s *state) clone() *state {
	n := &state{cons: append([]string(nil), s.cons...), approx: s.approx, mem: make(map[int]string, len(s.mem))}
	for k, v := range s.mem {
		n.mem[k] = v
	}
	for _, f := range s.stack {
		nf := *f
		nf.env = make(map[ssa.Value]sval, len(f.env))
		for k, v := range f.env {
			nf.env[k] = v
		}
		nf.visit = make(map[*ssa.BasicBlock]int, len(f.visit))
		for k, v := range f.visit {
			nf.visit[k] = v
		}
		n.stack = append(n.stack, &nf)
	}
	return n
}

type engine struct {
	opts     Options
	decls    []string // SMT declarations, in order
	nfresh   int
	params   []paramInfo
	finds    []Finding
	seen     map[string]bool
	fn       *ssa.Function
	paths    int
	queries  int
	sv       *solver
	deadline time.Time
	imports  map[string]bool
	root     string // only panics in files under this directory are reported
}

type paramInfo struct {
	name string
	typ  types.Type
	val  sval
	lenN string // slice length symbol
	arrN string
}

// Explore runs the executor on fn and returns the panics it can reach.
func Explore(fn *ssa.Function, opts Options) ([]Finding, error) {
	opts.defaults()
	e := &engine{opts: opts, seen: map[string]bool{}, imports: map[string]bool{}, root: opts.Root, fn: fn, deadline: time.Now().Add(opts.FuncTime)}
	if err := e.start(); err != nil {
		return nil, fmt.Errorf("symexec: z3: %v", err)
	}
	defer e.stop()
	f0 := &frame{fn: fn, env: map[ssa.Value]sval{}, visit: map[*ssa.BasicBlock]int{}}
	st := &state{mem: map[int]string{}}
	for _, p := range fn.Params {
		v, ok := e.param(p, st)
		if !ok {
			return nil, fmt.Errorf("symexec: parameter %s of type %s is not supported", p.Name(), p.Type())
		}
		f0.env[p] = v
	}
	if len(fn.Blocks) == 0 {
		return nil, fmt.Errorf("symexec: %s has no body", fn.Name())
	}
	f0.block = fn.Blocks[0]
	st.stack = []*frame{f0}
	work := []*state{st}
	for len(work) > 0 && e.paths < e.opts.MaxPaths && time.Now().Before(e.deadline) {
		s := work[len(work)-1]
		work = work[:len(work)-1]
		next := e.run(s)
		work = append(work, next...)
	}
	if os.Getenv("SYMEXEC_STATS") != "" {
		fmt.Fprintf(os.Stderr, "symexec: %s: %d paths, %d solver calls\n", fn.Name(), e.paths, e.queries)
	}
	sort.SliceStable(e.finds, func(i, j int) bool { return e.finds[i].Pos.Line < e.finds[j].Pos.Line })
	return e.finds, nil
}

func (e *engine) newArr(s *state, init string) int {
	e.nfresh++
	s.mem[e.nfresh] = init
	return e.nfresh
}

func (e *engine) fresh(prefix string) string {
	e.nfresh++
	return fmt.Sprintf("%s_%d", prefix, e.nfresh)
}

func (e *engine) declare(name, sort string) {
	e.decls = append(e.decls, fmt.Sprintf("(declare-const %s %s)", name, sort))
}

func bvSort(w int) string { return fmt.Sprintf("(_ BitVec %d)", w) }

func intInfo(t types.Type) (width int, signed bool, ok bool) {
	b, isB := t.Underlying().(*types.Basic)
	if !isB {
		return 0, false, false
	}
	switch b.Kind() {
	case types.Int, types.Int64:
		return 64, true, true
	case types.Int8:
		return 8, true, true
	case types.Int16:
		return 16, true, true
	case types.Int32:
		return 32, true, true
	case types.Uint, types.Uint64, types.Uintptr:
		return 64, false, true
	case types.Uint8:
		return 8, false, true
	case types.Uint16:
		return 16, false, true
	case types.Uint32:
		return 32, false, true
	}
	return 0, false, false
}

func (e *engine) param(p *ssa.Parameter, st *state) (sval, bool) {
	t := p.Type()
	name := "p_" + p.Name()
	if w, sg, ok := intInfo(t); ok {
		e.declare(name, bvSort(w))
		v := &bv{E: name, Width: w, Signed: sg}
		e.params = append(e.params, paramInfo{name: name, typ: t, val: v})
		return v, true
	}
	if b, ok := t.Underlying().(*types.Basic); ok && b.Kind() == types.Bool {
		e.declare(name, "Bool")
		v := &bl{E: name}
		e.params = append(e.params, paramInfo{name: name, typ: t, val: v})
		return v, true
	}
	if sl, ok := t.Underlying().(*types.Slice); ok {
		if w, sg, ok := intInfo(sl.Elem()); ok {
			ln, an := name+"_len", name+"_arr"
			e.declare(ln, bvSort(64))
			e.declare(an, fmt.Sprintf("(Array %s %s)", bvSort(64), bvSort(w)))
			st.cons = append(st.cons, fmt.Sprintf("(bvule %s (_ bv%d 64))", ln, e.opts.MaxLen))
			v := &slc{Arr: e.newArr(st, an), Off: "(_ bv0 64)", Len: ln, Cap: ln, EW: w, Signed: sg}
			e.params = append(e.params, paramInfo{name: name, typ: t, val: v, lenN: ln, arrN: an})
			return v, true
		}
	}
	return nil, false
}

// run executes s until it ends or forks; it returns the states to continue.
func (e *engine) run(s *state) []*state {
	var out []*state
	for steps := 0; steps < 200000; steps++ {
		f := s.stack[len(s.stack)-1]
		if f.pc >= len(f.block.Instrs) {
			e.paths++
			return out
		}
		ins := f.block.Instrs[f.pc]
		f.pc++
		switch ins := ins.(type) {
		case *ssa.Jump:
			if !e.enter(s, f, ins.Block().Succs[0]) {
				e.paths++
				return out
			}
		case *ssa.If:
			c := e.boolOf(s, f, ins.Cond)
			t, el := ins.Block().Succs[0], ins.Block().Succs[1]
			ts := s.clone()
			ts.cons = append(ts.cons, c)
			if e.sat(ts.cons) {
				tf := ts.stack[len(ts.stack)-1]
				if e.enter(ts, tf, t) {
					out = append(out, ts)
				}
			}
			s.cons = append(s.cons, fmt.Sprintf("(not %s)", c))
			if !e.sat(s.cons) {
				e.paths++
				return out
			}
			if !e.enter(s, f, el) {
				e.paths++
				return out
			}
		case *ssa.Return:
			if len(s.stack) == 1 {
				e.paths++
				return out
			}
			var rv sval = unknown{}
			switch len(ins.Results) {
			case 0:
			case 1:
				rv = e.val(s, f, ins.Results[0])
			default:
				tp := make(tuple, len(ins.Results))
				for i, r := range ins.Results {
					tp[i] = e.val(s, f, r)
				}
				rv = tp
			}
			ret := f.ret
			s.stack = s.stack[:len(s.stack)-1]
			caller := s.stack[len(s.stack)-1]
			if ret != nil {
				caller.env[ret] = rv
			}
		case *ssa.Panic:
			e.report(s, f, ins, "explicit panic", "true")
			e.paths++
			return out
		default:
			if !e.exec(s, f, ins) {
				e.paths++
				return out
			}
		}
	}
	e.paths++
	return out
}

// enter moves f to block b, resolving phis against the old environment.
func (e *engine) enter(s *state, f *frame, b *ssa.BasicBlock) bool {
	if f.visit[b] >= e.opts.LoopBound {
		return false
	}
	f.visit[b]++
	pred := f.block
	idx := -1
	for i, p := range b.Preds {
		if p == pred {
			idx = i
		}
	}
	if idx >= 0 {
		var vals []sval
		var phis []*ssa.Phi
		for _, ins := range b.Instrs {
			phi, ok := ins.(*ssa.Phi)
			if !ok {
				break
			}
			phis = append(phis, phi)
			vals = append(vals, e.val(s, f, phi.Edges[idx]))
		}
		for i, phi := range phis {
			f.env[phi] = vals[i]
		}
	}
	f.prev = pred
	f.block = b
	f.pc = 0
	// skip the phis, already bound
	for f.pc < len(b.Instrs) {
		if _, ok := b.Instrs[f.pc].(*ssa.Phi); !ok {
			break
		}
		f.pc++
	}
	return true
}

func lit(v uint64, w int) string { return fmt.Sprintf("(_ bv%d %d)", v, w) }

func (e *engine) val(s *state, f *frame, v ssa.Value) sval {
	if x, ok := f.env[v]; ok {
		return x
	}
	switch c := v.(type) {
	case *ssa.Const:
		if c.Value == nil { // nil
			if _, ok := c.Type().Underlying().(*types.Slice); ok {
				return &slc{Arr: 0, Off: lit(0, 64), Len: lit(0, 64), Cap: lit(0, 64), EW: 8}
			}
			return unknown{}
		}
		if w, sg, ok := intInfo(c.Type()); ok {
			if c.Value.Kind() == constant.Int {
				if sg {
					n, _ := constant.Int64Val(c.Value)
					return &bv{E: lit(uint64(n)&mask(w), w), Width: w, Signed: sg}
				}
				n, _ := constant.Uint64Val(c.Value)
				return &bv{E: lit(n&mask(w), w), Width: w, Signed: sg}
			}
		}
		if c.Value.Kind() == constant.Bool {
			return &bl{E: fmt.Sprint(constant.BoolVal(c.Value))}
		}
		return unknown{}
	}
	return unknown{}
}

func mask(w int) uint64 {
	if w >= 64 {
		return ^uint64(0)
	}
	return (1 << uint(w)) - 1
}

func (e *engine) boolOf(s *state, f *frame, v ssa.Value) string {
	if b, ok := e.val(s, f, v).(*bl); ok {
		return b.E
	}
	s.approx = true
	n := e.fresh("hb")
	e.declare(n, "Bool")
	return n
}

func (e *engine) havoc(s *state, t types.Type) sval {
	s.approx = true
	if w, sg, ok := intInfo(t); ok {
		n := e.fresh("h")
		e.declare(n, bvSort(w))
		return &bv{E: n, Width: w, Signed: sg}
	}
	if b, ok := t.Underlying().(*types.Basic); ok && b.Kind() == types.Bool {
		n := e.fresh("hb")
		e.declare(n, "Bool")
		return &bl{E: n}
	}
	if sl, ok := t.Underlying().(*types.Slice); ok {
		if w, sg, ok := intInfo(sl.Elem()); ok {
			ln, an := e.fresh("hl"), e.fresh("ha")
			e.declare(ln, bvSort(64))
			e.declare(an, fmt.Sprintf("(Array %s %s)", bvSort(64), bvSort(w)))
			s.cons = append(s.cons, fmt.Sprintf("(bvule %s (_ bv%d 64))", ln, e.opts.MaxLen))
			return &slc{Arr: e.newArr(s, an), Off: lit(0, 64), Len: ln, Cap: ln, EW: w, Signed: sg}
		}
	}
	return unknown{}
}

func (e *engine) intOf(s *state, f *frame, v ssa.Value) *bv {
	if x, ok := e.val(s, f, v).(*bv); ok {
		return x
	}
	h := e.havoc(s, v.Type())
	if x, ok := h.(*bv); ok {
		return x
	}
	s.approx = true
	w := 64
	n := e.fresh("hi")
	e.declare(n, bvSort(w))
	return &bv{E: n, Width: w}
}

func ext(x *bv, w int) string { // x as a bitvector of width w (w >= x.Width)
	if w == x.Width {
		return x.E
	}
	if x.Signed {
		return fmt.Sprintf("((_ sign_extend %d) %s)", w-x.Width, x.E)
	}
	return fmt.Sprintf("((_ zero_extend %d) %s)", w-x.Width, x.E)
}

// exec runs one straight-line instruction; false ends the path.
func (e *engine) exec(s *state, f *frame, ins ssa.Instruction) bool {
	switch ins := ins.(type) {
	case *ssa.BinOp:
		return e.binop(s, f, ins)
	case *ssa.UnOp:
		switch ins.Op {
		case token.NOT:
			f.env[ins] = &bl{E: fmt.Sprintf("(not %s)", e.boolOf(s, f, ins.X))}
		case token.SUB:
			x := e.intOf(s, f, ins.X)
			f.env[ins] = &bv{E: fmt.Sprintf("(bvneg %s)", x.E), Width: x.Width, Signed: x.Signed}
		case token.XOR:
			x := e.intOf(s, f, ins.X)
			f.env[ins] = &bv{E: fmt.Sprintf("(bvnot %s)", x.E), Width: x.Width, Signed: x.Signed}
		case token.MUL: // load
			if a, ok := e.val(s, f, ins.X).(*addr); ok {
				f.env[ins] = &bv{E: fmt.Sprintf("(select %s %s)", s.mem[a.S.Arr], a.I), Width: a.S.EW, Signed: a.S.Signed}
			} else {
				f.env[ins] = e.havoc(s, ins.Type())
			}
		default:
			f.env[ins] = e.havoc(s, ins.Type())
		}
	case *ssa.Convert:
		x, ok := e.val(s, f, ins.X).(*bv)
		w, sg, ok2 := intInfo(ins.Type())
		if !ok || !ok2 {
			f.env[ins] = e.havoc(s, ins.Type())
			break
		}
		var r string
		switch {
		case w == x.Width:
			r = x.E
		case w < x.Width:
			r = fmt.Sprintf("((_ extract %d 0) %s)", w-1, x.E)
		default:
			r = ext(x, w)
		}
		f.env[ins] = &bv{E: r, Width: w, Signed: sg}
	case *ssa.ChangeType:
		f.env[ins] = e.val(s, f, ins.X)
	case *ssa.IndexAddr, *ssa.Index:
		var x, i ssa.Value
		if ia, ok := ins.(*ssa.IndexAddr); ok {
			x, i = ia.X, ia.Index
		} else {
			x, i = ins.(*ssa.Index).X, ins.(*ssa.Index).Index
		}
		sl, ok := asSlice(e.val(s, f, x))
		idx := e.intOf(s, f, i)
		if !ok {
			f.env[ins.(ssa.Value)] = e.havoc(s, ins.(ssa.Value).Type())
			break
		}
		ix := ext64(idx)
		if !e.check(s, f, ins, "index out of range", fmt.Sprintf("(bvult %s %s)", ix, sl.Len)) {
			return false
		}
		abs := fmt.Sprintf("(bvadd %s %s)", sl.Off, ix)
		if _, isAddr := ins.(*ssa.IndexAddr); isAddr {
			f.env[ins.(ssa.Value)] = &addr{S: *sl, I: abs}
		} else {
			f.env[ins.(ssa.Value)] = &bv{E: fmt.Sprintf("(select %s %s)", s.mem[sl.Arr], abs), Width: sl.EW, Signed: sl.Signed}
		}
	case *ssa.Slice:
		sl, ok := asSlice(e.val(s, f, ins.X))
		if !ok {
			f.env[ins] = e.havoc(s, ins.Type())
			break
		}
		lo := lit(0, 64)
		hi := sl.Len
		if ins.Low != nil {
			lo = ext64(e.intOf(s, f, ins.Low))
		}
		if ins.High != nil {
			hi = ext64(e.intOf(s, f, ins.High))
		}
		if !e.check(s, f, ins, "slice bounds", fmt.Sprintf("(and (bvule %s %s) (bvule %s %s))", lo, hi, hi, sl.Cap)) {
			return false
		}
		f.env[ins] = &slc{Arr: sl.Arr, Off: fmt.Sprintf("(bvadd %s %s)", sl.Off, lo), Len: fmt.Sprintf("(bvsub %s %s)", hi, lo),
			Cap: fmt.Sprintf("(bvsub %s %s)", sl.Cap, lo), EW: sl.EW, Signed: sl.Signed}
	case *ssa.Extract:
		if t, ok := e.val(s, f, ins.Tuple).(tuple); ok && ins.Index < len(t) {
			f.env[ins] = t[ins.Index]
		} else {
			f.env[ins] = e.havoc(s, ins.Type())
		}
	case *ssa.Call:
		return e.call(s, f, ins)
	case *ssa.Alloc:
		if pt, ok := ins.Type().(*types.Pointer); ok {
			if at, ok := pt.Elem().Underlying().(*types.Array); ok {
				if w, sg, ok := intInfo(at.Elem()); ok {
					id := e.newArr(s, fmt.Sprintf("((as const (Array %s %s)) %s)", bvSort(64), bvSort(w), lit(0, w)))
					n := lit(uint64(at.Len()), 64)
					f.env[ins] = &arrObj{S: slc{Arr: id, Off: lit(0, 64), Len: n, Cap: n, EW: w, Signed: sg}}
					break
				}
			}
		}
		f.env[ins] = e.havoc(s, ins.Type())
	case *ssa.Store:
		a, ok := e.val(s, f, ins.Addr).(*addr)
		v, ok2 := e.val(s, f, ins.Val).(*bv)
		if ok && ok2 {
			s.mem[a.S.Arr] = fmt.Sprintf("(store %s %s %s)", s.mem[a.S.Arr], a.I, v.E)
		} else {
			s.approx = true
		}
	case *ssa.DebugRef:
	default:
		if v, ok := ins.(ssa.Value); ok {
			f.env[v] = e.havoc(s, v.Type())
		}
	}
	return true
}

func ext64(x *bv) string { return ext(x, 64) }

func (e *engine) binop(s *state, f *frame, ins *ssa.BinOp) bool {
	if _, ok := e.val(s, f, ins.X).(*bl); ok {
		a, b := e.boolOf(s, f, ins.X), e.boolOf(s, f, ins.Y)
		switch ins.Op {
		case token.EQL:
			f.env[ins] = &bl{E: fmt.Sprintf("(= %s %s)", a, b)}
		case token.NEQ:
			f.env[ins] = &bl{E: fmt.Sprintf("(not (= %s %s))", a, b)}
		default:
			f.env[ins] = e.havoc(s, ins.Type())
		}
		return true
	}
	x, ok1 := e.val(s, f, ins.X).(*bv)
	y, ok2 := e.val(s, f, ins.Y).(*bv)
	if !ok1 || !ok2 {
		f.env[ins] = e.havoc(s, ins.Type())
		return true
	}
	cmp := func(signedOp, unsignedOp string) sval {
		op := unsignedOp
		if x.Signed {
			op = signedOp
		}
		return &bl{E: fmt.Sprintf("(%s %s %s)", op, x.E, y.E)}
	}
	arith := func(op string) sval {
		return &bv{E: fmt.Sprintf("(%s %s %s)", op, x.E, y.E), Width: x.Width, Signed: x.Signed}
	}
	switch ins.Op {
	case token.ADD:
		f.env[ins] = arith("bvadd")
	case token.SUB:
		f.env[ins] = arith("bvsub")
	case token.MUL:
		f.env[ins] = arith("bvmul")
	case token.AND:
		f.env[ins] = arith("bvand")
	case token.OR:
		f.env[ins] = arith("bvor")
	case token.XOR:
		f.env[ins] = arith("bvxor")
	case token.AND_NOT:
		f.env[ins] = &bv{E: fmt.Sprintf("(bvand %s (bvnot %s))", x.E, y.E), Width: x.Width, Signed: x.Signed}
	case token.QUO, token.REM:
		if !e.check(s, f, ins, "division by zero", fmt.Sprintf("(not (= %s %s))", y.E, lit(0, y.Width))) {
			return false
		}
		op := map[bool]map[token.Token]string{true: {token.QUO: "bvsdiv", token.REM: "bvsrem"}, false: {token.QUO: "bvudiv", token.REM: "bvurem"}}[x.Signed][ins.Op]
		f.env[ins] = arith(op)
	case token.SHL, token.SHR:
		// the count is any integer type; a count of at least the width gives 0 (or the sign)
		c := ext64(y)
		if y.Width > 64 {
			c = y.E
		}
		small := fmt.Sprintf("((_ extract %d 0) %s)", x.Width-1, c)
		over := fmt.Sprintf("(bvuge %s %s)", c, lit(uint64(x.Width), 64))
		var r, fill string
		if ins.Op == token.SHL {
			r, fill = fmt.Sprintf("(bvshl %s %s)", x.E, small), lit(0, x.Width)
		} else if x.Signed {
			r, fill = fmt.Sprintf("(bvashr %s %s)", x.E, small), fmt.Sprintf("(ite (bvslt %s %s) %s %s)", x.E, lit(0, x.Width), fmt.Sprintf("(bvnot %s)", lit(0, x.Width)), lit(0, x.Width))
		} else {
			r, fill = fmt.Sprintf("(bvlshr %s %s)", x.E, small), lit(0, x.Width)
		}
		f.env[ins] = &bv{E: fmt.Sprintf("(ite %s %s %s)", over, fill, r), Width: x.Width, Signed: x.Signed}
	case token.EQL:
		f.env[ins] = &bl{E: fmt.Sprintf("(= %s %s)", x.E, y.E)}
	case token.NEQ:
		f.env[ins] = &bl{E: fmt.Sprintf("(not (= %s %s))", x.E, y.E)}
	case token.LSS:
		f.env[ins] = cmp("bvslt", "bvult")
	case token.LEQ:
		f.env[ins] = cmp("bvsle", "bvule")
	case token.GTR:
		f.env[ins] = cmp("bvsgt", "bvugt")
	case token.GEQ:
		f.env[ins] = cmp("bvsge", "bvuge")
	default:
		f.env[ins] = e.havoc(s, ins.Type())
	}
	return true
}

func (e *engine) call(s *state, f *frame, ins *ssa.Call) bool {
	cc := ins.Common()
	if b, ok := cc.Value.(*ssa.Builtin); ok {
		switch b.Name() {
		case "len", "cap":
			if sl, ok := asSlice(e.val(s, f, cc.Args[0])); ok {
				l := sl.Len
				if b.Name() == "cap" {
					l = sl.Cap
				}
				f.env[ins] = &bv{E: l, Width: 64, Signed: true}
				return true
			}
		case "panic":
		}
		f.env[ins] = e.havoc(s, ins.Type())
		return true
	}
	callee := cc.StaticCallee()
	if callee == nil || len(callee.Blocks) == 0 || len(s.stack) > e.opts.CallDepth {
		f.env[ins] = e.havocResult(s, ins.Type())
		return true
	}
	nf := &frame{fn: callee, env: map[ssa.Value]sval{}, visit: map[*ssa.BasicBlock]int{}, ret: ins}
	for i, p := range callee.Params {
		if i < len(cc.Args) {
			nf.env[p] = e.val(s, f, cc.Args[i])
		}
	}
	nf.block = callee.Blocks[0]
	s.stack = append(s.stack, nf)
	return true
}

func (e *engine) havocResult(s *state, t types.Type) sval {
	if tt, ok := t.(*types.Tuple); ok {
		out := make(tuple, tt.Len())
		for i := 0; i < tt.Len(); i++ {
			out[i] = e.havoc(s, tt.At(i).Type())
		}
		return out
	}
	return e.havoc(s, t)
}

// check records a finding if the path can violate safe, and continues the
// path under safe; false when the path cannot continue.
func (e *engine) check(s *state, f *frame, ins ssa.Instruction, kind, safe string) bool {
	e.report(s, f, ins, kind, fmt.Sprintf("(not %s)", safe))
	s.cons = append(s.cons, safe)
	return e.sat(s.cons)
}

func (e *engine) report(s *state, f *frame, ins ssa.Instruction, kind, bad string) {
	pos := f.fn.Prog.Fset.Position(ins.Pos())
	if e.root != "" && !strings.HasPrefix(pos.Filename, e.root) {
		return // inside the standard library or a dependency
	}
	key := fmt.Sprintf("%s:%d:%s", pos.Filename, pos.Line, kind)
	if e.seen[key] {
		return
	}
	cons := append(append([]string(nil), s.cons...), bad)
	model, ok := e.model(cons)
	if !ok {
		return
	}
	e.seen[key] = true
	in := e.inputs(model)
	e.finds = append(e.finds, Finding{Func: e.fn.Name(), Imports: e.importList(), Kind: kind, Pos: pos, Inputs: in, Approximate: s.approx})
}

// solver: one z3 process per function, driven incrementally.

type solver struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader
	ndecl int // declarations already sent
}

func (e *engine) start() error {
	cmd := exec.Command("z3", "-in", "-smt2")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	e.sv = &solver{cmd: cmd, in: in, out: bufio.NewReader(out)}
	fmt.Fprintf(in, "(set-option :timeout %d)\n(set-logic ALL)\n", e.opts.SolverTime.Milliseconds())
	return nil
}

func (e *engine) stop() {
	if e.sv != nil {
		e.sv.in.Close()
		e.sv.cmd.Process.Kill()
		e.sv.cmd.Wait()
		e.sv = nil
	}
}

// ask asserts cons in a scope, checks, optionally reads values, and leaves
// the scope. It returns the check result and the raw value text.
func (e *engine) ask(cons []string, tail string) (string, string) {
	e.queries++
	sv := e.sv
	var b strings.Builder
	for ; sv.ndecl < len(e.decls); sv.ndecl++ {
		b.WriteString(e.decls[sv.ndecl] + "\n")
	}
	b.WriteString("(push)\n")
	for _, c := range cons {
		b.WriteString("(assert " + c + ")\n")
	}
	b.WriteString("(check-sat)\n")
	if tail != "" {
		b.WriteString(tail + "\n")
	}
	b.WriteString("(pop)\n(echo \"END\")\n")
	if _, err := io.WriteString(sv.in, b.String()); err != nil {
		return "error", ""
	}
	var res, vals strings.Builder
	first := true
	for {
		line, err := sv.out.ReadString('\n')
		if err != nil {
			return "error", ""
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "END" || line == "\"END\"" {
			break
		}
		if first {
			res.WriteString(line)
			first = false
			continue
		}
		vals.WriteString(line + " ")
	}
	return strings.TrimSpace(res.String()), vals.String()
}

func (e *engine) sat(cons []string) bool {
	r, _ := e.ask(cons, "")
	return r == "sat"
}

// model returns z3's values for the parameters when cons is satisfiable.
func (e *engine) model(cons []string) (map[string]string, bool) {
	var terms []string
	for _, p := range e.params {
		if p.lenN == "" {
			terms = append(terms, p.name)
			continue
		}
		terms = append(terms, p.lenN)
		for i := 0; i < e.opts.MaxLen; i++ {
			terms = append(terms, fmt.Sprintf("(select %s (_ bv%d 64))", p.arrN, i))
		}
	}
	r, vals := e.ask(cons, "(get-value ("+strings.Join(terms, " ")+"))")
	if r != "sat" {
		return nil, false
	}
	return parseValues(vals, terms), true
}

// parseValues reads z3's ((term value) ...) answer.
func parseValues(s string, terms []string) map[string]string {
	res := map[string]string{}
	s = strings.TrimSpace(s)
	for _, t := range terms {
		i := strings.Index(s, "("+t+" ")
		if i < 0 {
			continue
		}
		rest := s[i+len(t)+2:]
		// the value runs to the matching close paren of the pair
		depth := 1
		j := 0
		for ; j < len(rest) && depth > 0; j++ {
			switch rest[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		res[t] = strings.TrimSpace(rest[:j-1])
		s = s[i+1:]
	}
	return res
}

func bitsOf(v string) (uint64, bool) {
	v = strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(v, "#x"):
		var n uint64
		_, err := fmt.Sscanf(v[2:], "%x", &n)
		return n, err == nil
	case strings.HasPrefix(v, "#b"):
		var n uint64
		for _, c := range v[2:] {
			n = n<<1 | uint64(c-'0')
		}
		return n, true
	case v == "true":
		return 1, true
	case v == "false":
		return 0, true
	}
	return 0, false
}

func (e *engine) qual(p *types.Package) string {
	if p == e.fn.Pkg.Pkg {
		return ""
	}
	e.imports[p.Path()] = true
	return p.Name()
}

func (e *engine) goInt(n uint64, w int, signed bool, t types.Type) string {
	name := types.TypeString(t, e.qual)
	if signed {
		sh := uint(64 - w)
		return fmt.Sprintf("%s(%d)", name, int64(n<<sh)>>sh)
	}
	return fmt.Sprintf("%s(%d)", name, n&mask(w))
}

func (e *engine) inputs(m map[string]string) []Input {
	var out []Input
	for i, p := range e.params {
		name := strings.TrimPrefix(p.name, "p_")
		_ = i
		switch v := p.val.(type) {
		case *bv:
			n, _ := bitsOf(m[p.name])
			out = append(out, Input{Name: name, Type: p.typ.String(), Go: e.goInt(n, v.Width, v.Signed, p.typ)})
		case *bl:
			n, _ := bitsOf(m[p.name])
			out = append(out, Input{Name: name, Type: "bool", Go: fmt.Sprint(n == 1)})
		case *slc:
			ln, _ := bitsOf(m[p.lenN])
			if ln > uint64(e.opts.MaxLen) {
				ln = uint64(e.opts.MaxLen)
			}
			el := p.typ.Underlying().(*types.Slice).Elem()
			var elems []string
			for k := uint64(0); k < ln; k++ {
				n, _ := bitsOf(m[fmt.Sprintf("(select %s (_ bv%d 64))", p.arrN, k)])
				elems = append(elems, strings.TrimPrefix(goIntBare(n, v.EW, v.Signed), ""))
			}
			out = append(out, Input{Name: name, Type: p.typ.String(), Go: fmt.Sprintf("%s{%s}", types.TypeString(p.typ, e.qual), strings.Join(elems, ", "))})
			_ = el
		}
	}
	return out
}

func goIntBare(n uint64, w int, signed bool) string {
	if signed {
		sh := uint(64 - w)
		return fmt.Sprint(int64(n<<sh) >> sh)
	}
	return fmt.Sprint(n & mask(w))
}

// asSlice views a slice or a pointer to a local array as a slice.
func asSlice(v sval) (*slc, bool) {
	switch x := v.(type) {
	case *slc:
		return x, true
	case *arrObj:
		return &x.S, true
	}
	return nil, false
}

func (e *engine) importList() []string {
	var out []string
	for p := range e.imports {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
