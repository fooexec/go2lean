package gossa

import (
	"fmt"
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/andreasfoo/go2lean/internal/contract"
	"github.com/andreasfoo/go2lean/internal/ir"
)

// The parts of `context`, `time` and `sync.Cond` the concurrent machine
// models (docs/concurrency.md): a context is a heap object with a done
// channel (the GoSSA ops ctx*), a timer a goroutine that fires at any
// scheduling point, a ticker one that fires forever (a daemon), a Cond
// a ticket queue in its notify field.

// helper returns the id of a synthetic function of the program, built once
// per package by build: a cancel func, a timer's goroutine. Helpers are
// closures to M2 (never targets).
func (s *progState) helper(name string, sync, daemon bool, nvals int, params []ir.Var, blocks []ir.Block) int {
	if id, ok := s.helpers[name]; ok {
		return id
	}
	if s.helpers == nil {
		s.helpers = map[string]int{}
	}
	id := len(s.prog.Funcs)
	s.prog.Funcs = append(s.prog.Funcs, &ir.Func{Name: name, GoSig: "func()", Params: params, NVals: nvals,
		Blocks: blocks, Status: contract.ModelComplete, Closure: true, Sync: sync, Daemon: daemon})
	s.helpers[name] = id
	return id
}

func unitRet(dst ir.Var) ir.Block {
	return ir.Block{Name: "ret", Instrs: []ir.Instr{{Dst: dst, Op: ir.Const{V: ir.UnitVal{}}}}, Term: ir.Term{Kind: ir.TermRet, V: dst}}
}

// sleepFor is a block that sleeps the duration in variable d (a
// `time.Sleep`, #59), then goes on to block next.
func sleepFor(d, dst ir.Var, next int) ir.Block {
	return ir.Block{Name: "sleep", Term: ir.Term{Kind: ir.TermSync, Sync: ir.SyncOp{Kind: "yield"}, CArgs: []ir.Var{d}, Dst: dst, Next: next}}
}

// cancelHelper: func(ctx) ends ctx with code 1 (canceled); with code 2,
// func(ctx, d) ends it as its deadline after sleeping d (#59).
func (s *progState) cancelHelper(code int) int {
	if code == 2 {
		b := unitRet(4)
		b.Instrs = append([]ir.Instr{{Dst: 3, Op: ir.Raw("(.conc (.ctxCancel 2))"), Args: []ir.Var{0}}}, b.Instrs...)
		return s.helper("$context.deadline", true, false, 5, []ir.Var{0, 1}, []ir.Block{sleepFor(1, 2, 1), b})
	}
	b := unitRet(2)
	b.Instrs = append([]ir.Instr{{Dst: 1, Op: ir.Raw(fmt.Sprintf("(.conc (.ctxCancel %d))", code)), Args: []ir.Var{0}}}, b.Instrs...)
	return s.helper("$context.cancel", false, false, 3, []ir.Var{0}, []ir.Block{b})
}

// fireHelper: func(ch, d) sleeps d, then sends on a timer's channel unless
// its buffer is full (a select with a default), so a timer never blocks.
func (s *progState) fireHelper() int {
	return s.helper("$time.fire", true, false, 6, []ir.Var{0, 1}, []ir.Block{
		sleepFor(1, 2, 1),
		{Name: "fire", Instrs: []ir.Instr{{Dst: 3, Op: ir.Const{V: ir.UnitVal{}}}},
			Term: ir.Term{Kind: ir.TermSync, Sync: ir.SyncOp{Kind: "select", Sends: []bool{true}}, CArgs: []ir.Var{0, 3}, Dst: 4, Next: 2}},
		unitRet(5),
	})
}

// tickHelper: func(ch, d) fires a ticker's channel every d, forever, a
// daemon.
func (s *progState) tickHelper() int {
	return s.helper("$time.tick", true, true, 5, []ir.Var{0, 1}, []ir.Block{
		sleepFor(1, 2, 1),
		{Name: "tick", Instrs: []ir.Instr{{Dst: 3, Op: ir.Const{V: ir.UnitVal{}}}},
			Term: ir.Term{Kind: ir.TermSync, Sync: ir.SyncOp{Kind: "select", Sends: []bool{true}}, CArgs: []ir.Var{0, 3}, Dst: 4, Next: 0}},
	})
}

// afterFuncHelper: func(f, d) sleeps d, then starts f in a goroutine of
// its own (time.AfterFunc).
func (s *progState) afterFuncHelper() int {
	return s.helper("$time.afterfunc", true, false, 5, []ir.Var{0, 1}, []ir.Block{
		sleepFor(1, 2, 1),
		{Name: "start", Term: ir.Term{Kind: ir.TermSpawn, FV: 0, Dst: 3, Next: 2}},
		unitRet(4),
	})
}

// closeHelper: func(ch) closes ch, for `go close(ch)`.
func (s *progState) closeHelper() int {
	return s.helper("$close", true, false, 3, []ir.Var{0}, []ir.Block{
		{Name: "close", Term: ir.Term{Kind: ir.TermSync, Sync: ir.SyncOp{Kind: "close"}, CArgs: []ir.Var{0}, Dst: 1, Next: 1}},
		unitRet(2),
	})
}

// syncTag is the type tag of *sync.<name> (RWMutex, rlocker), which a
// sync.Locker holding one is boxed with (0 when the program does not use
// package sync).
func (s *progState) syncTag(name string) uint32 {
	for _, p := range s.sprog.AllPackages() {
		if p.Pkg.Path() == "sync" {
			if obj := p.Pkg.Scope().Lookup(name); obj != nil {
				return typeTag(types.NewPointer(obj.Type()))
			}
		}
	}
	return 0
}

// lockerOp is Lock or Unlock (kind lockL, unlockL) through a sync.Locker.
func (s *progState) lockerOp(kind string) ir.SyncOp {
	return ir.SyncOp{Kind: kind, Tag: s.syncTag("RWMutex"), RTag: s.syncTag("rlocker")}
}

// havocDuration is a duration of zero or more nanoseconds, below 2^40, the
// model chooses (#59).
func (t *fnState) havocDuration(site string) ir.Var {
	raw, bound, d := t.fresh(), t.fresh(), t.fresh()
	t.emit(ir.Instr{Dst: raw, HavocSite: t.prog.newHavoc(64, t.f.Name()+": "+site, "clock", "")})
	t.emit(ir.Instr{Dst: bound, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "1099511627776"}}})
	t.emit(ir.Instr{Dst: d, Op: ir.Simple("urem"), Args: []ir.Var{raw, bound}})
	return d
}

// spawnHelper starts a goroutine running helper id on args.
func (t *fnState) spawnHelper(id int, args ...ir.Var) {
	fv := t.fresh()
	t.emit(ir.Instr{Dst: fv, Op: ir.FuncRef{ID: id}})
	t.syncTerm(ir.Term{Kind: ir.TermSpawn, FV: fv, CArgs: args, Dst: t.fresh()})
}

// timerObj allocates a time.Timer or time.Ticker (type named) whose
// channel C is ch.
func (t *fnState) timerObj(named types.Type, ch ir.Var) (ir.Var, bool) {
	zero, ok := zeroVal(named)
	if !ok {
		return 0, false
	}
	p := t.fresh()
	t.emit(ir.Instr{Dst: p, Op: ir.Alloc{Zero: zero}})
	fa := t.fresh()
	t.emit(ir.Instr{Dst: fa, Op: ir.FieldAddr{I: 0}, Args: []ir.Var{p}})
	t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{fa, ch}})
	return p, true
}

// timerChan makes a timer's channel (buffered, one value).
func (t *fnState) timerChan() ir.Var {
	one, ch := t.fresh(), t.fresh()
	t.emit(ir.Instr{Dst: one, Op: ir.Const{V: ir.IntVal{Width: 64, Bits: "1"}}})
	t.emit(ir.Instr{Dst: ch, Op: ir.ChanMake{Zero: ir.UnitVal{}}, Args: []ir.Var{one}})
	return ch
}

// concCall lowers the calls of context, time and sync.Cond the machine
// models, and Lock/Unlock through a sync.Locker.
func (t *fnState) concCall(x *ssa.Call) (bool, error) {
	if x.Call.IsInvoke() {
		return t.concInvoke(x)
	}
	callee := x.Call.StaticCallee()
	if callee == nil {
		return false, nil
	}
	args := func() ([]ir.Var, error) {
		var out []ir.Var
		for _, a := range x.Call.Args {
			v, err := t.val(a)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	resType := func() types.Type {
		if r := x.Call.Signature().Results(); r.Len() > 0 {
			if p, ok := r.At(0).Type().(*types.Pointer); ok {
				return p.Elem()
			}
		}
		return nil
	}
	switch callee.String() {
	case "context.Background", "context.TODO":
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Conc("ctxBackground")})
	case "context.WithCancel", "context.WithTimeout", "context.WithDeadline":
		as, err := args()
		if err != nil {
			return true, err
		}
		ctx, cancel := t.fresh(), t.fresh()
		t.emit(ir.Instr{Dst: ctx, Op: ir.Conc("ctxNew"), Args: as[:1]})
		t.emit(ir.Instr{Dst: cancel, Op: ir.FuncRef{ID: t.prog.cancelHelper(1)}, Args: []ir.Var{ctx}})
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Simple("mkTuple"), Args: []ir.Var{ctx, cancel}})
		switch callee.Name() {
		case "WithTimeout":
			// The deadline passes after the timeout (#59).
			t.spawnHelper(t.prog.cancelHelper(2), ctx, as[1])
		case "WithDeadline":
			// A deadline is a time: it passes after a havoc duration.
			t.spawnHelper(t.prog.cancelHelper(2), ctx, t.havocDuration("context.WithDeadline"))
		}
	case "time.After":
		as, err := args()
		if err != nil {
			return true, err
		}
		ch := t.timerChan()
		t.vars[x] = ch
		t.spawnHelper(t.prog.fireHelper(), ch, as[0])
	case "time.NewTimer", "time.NewTicker":
		as, err := args()
		if err != nil {
			return true, err
		}
		ch := t.timerChan()
		p, ok := t.timerObj(resType(), ch)
		if !ok {
			return false, nil
		}
		t.vars[x] = p
		if callee.Name() == "NewTimer" {
			t.spawnHelper(t.prog.fireHelper(), ch, as[0])
		} else {
			t.spawnHelper(t.prog.tickHelper(), ch, as[0])
		}
	case "time.AfterFunc":
		as, err := args()
		if err != nil {
			return true, err
		}
		nilCh := t.fresh()
		t.emit(ir.Instr{Dst: nilCh, Op: ir.Const{V: ir.OpaqueNil{}}})
		p, ok := t.timerObj(resType(), nilCh)
		if !ok {
			return false, nil
		}
		t.vars[x] = p
		t.spawnHelper(t.prog.afterFuncHelper(), as[1], as[0])
	case "(*time.Timer).Reset":
		// Fires again at any point; whether it was active is not modelled.
		as, err := args()
		if err != nil {
			return true, err
		}
		fa, ch := t.fresh(), t.fresh()
		t.emit(ir.Instr{Dst: fa, Op: ir.FieldAddr{I: 0}, Args: as[:1]})
		t.emit(ir.Instr{Dst: ch, Op: ir.Simple("loadPath"), Args: []ir.Var{fa}})
		if err := t.havoc(x, "(*time.Timer).Reset", "unmodeled-call", ""); err != nil {
			return true, err
		}
		t.spawnHelper(t.prog.fireHelper(), ch, as[1])
	case "(*time.Ticker).Stop", "(*time.Ticker).Reset":
		// The ticker goroutine is a daemon: it does not keep a run alive.
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Const{V: ir.UnitVal{}}})
	case "sync.NewCond":
		as, err := args()
		if err != nil {
			return true, err
		}
		zero, ok := zeroVal(resType())
		if !ok {
			return false, nil
		}
		p, fa := t.def(x), t.fresh()
		t.emit(ir.Instr{Dst: p, Op: ir.Alloc{Zero: zero}})
		t.emit(ir.Instr{Dst: fa, Op: ir.FieldAddr{I: 1}, Args: []ir.Var{p}}) // L
		t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{fa, as[0]}})
	case "(*sync.Cond).Wait":
		as, err := args()
		if err != nil {
			return true, err
		}
		ticket, fa, l := t.fresh(), t.fresh(), t.fresh()
		t.emit(ir.Instr{Dst: ticket, Op: ir.Conc("condEnter"), Args: as})
		t.emit(ir.Instr{Dst: fa, Op: ir.FieldAddr{I: 1}, Args: as})
		t.emit(ir.Instr{Dst: l, Op: ir.Simple("loadPath"), Args: []ir.Var{fa}})
		t.syncOp(t.prog.lockerOp("unlockL"), t.fresh(), l)
		t.syncOp(ir.SyncOp{Kind: "condWait"}, t.fresh(), as[0], ticket)
		t.syncOp(t.prog.lockerOp("lockL"), t.def(x), l)
	case "(*sync.RWMutex).RLocker":
		// A Locker whose Lock is RLock: the mutex boxed under rlocker's tag.
		as, err := args()
		if err != nil {
			return true, err
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Box{Tag: t.prog.syncTag("rlocker")}, Args: as})
	case "(*sync.Cond).Signal", "(*sync.Cond).Broadcast":
		as, err := args()
		if err != nil {
			return true, err
		}
		op := "(.conc (.condSignal false))"
		if callee.Name() == "Broadcast" {
			op = "(.conc (.condSignal true))"
		}
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Raw(op), Args: as})
	default:
		return false, nil
	}
	return true, nil
}

// concInvoke lowers ctx.Done(), ctx.Err() on a context.Context and
// Lock/Unlock on a sync.Locker.
func (t *fnState) concInvoke(x *ssa.Call) (bool, error) {
	op, ok := lockerOrCtx(x.Call.Value.Type(), x.Call.Method.Name())
	if !ok {
		return false, nil
	}
	recv, err := t.val(x.Call.Value)
	if err != nil {
		return true, err
	}
	switch op {
	case "ctxDone", "ctxErr":
		t.emit(ir.Instr{Dst: t.def(x), Op: ir.Conc(op), Args: []ir.Var{recv}})
	default:
		t.syncOp(t.prog.lockerOp(op), t.def(x), recv)
	}
	return true, nil
}

// lockerOrCtx names the model of method m on interface type typ: ctxDone,
// ctxErr on a context.Context, lockL, unlockL on a sync.Locker.
func lockerOrCtx(typ types.Type, m string) (string, bool) {
	n, ok := typ.(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return "", false
	}
	switch n.Obj().Pkg().Path() + "." + n.Obj().Name() + "." + m {
	case "context.Context.Done":
		return "ctxDone", true
	case "context.Context.Err":
		return "ctxErr", true
	case "sync.Locker.Lock":
		return "lockL", true
	case "sync.Locker.Unlock":
		return "unlockL", true
	}
	return "", false
}
