package gossa

import (
	"fmt"
	"go/token"

	"github.com/andreasfoo/go2lean/internal/ir"
	"golang.org/x/tools/go/ssa"
)

// Floats (#148): a w-bit float is `.int w bits`, its IEEE 754 encoding, and
// the dialect's float ops compute with Lean's Float and Float32. A float the
// model does not know (an opaque input, a float64 a JSON decoder holds as
// decimal bytes it cannot read) makes the op's result havoc, as before.

var floatBinKinds = map[token.Token]int{token.ADD: 0, token.SUB: 1, token.MUL: 2, token.QUO: 3}

var floatCmpKinds = map[token.Token]int{
	token.EQL: 0, token.NEQ: 1, token.LSS: 2, token.LEQ: 3, token.GTR: 4, token.GEQ: 5,
}

// floatBinOp lowers x op y on w-bit floats: + - * / and the comparisons.
func (t *fnState) floatBinOp(x *ssa.BinOp, w int) error {
	var op ir.Op
	if k, ok := floatBinKinds[x.Op]; ok {
		op = ir.Raw(fmt.Sprintf("(.fbin %d %d)", k, w))
	} else if k, ok := floatCmpKinds[x.Op]; ok {
		op = ir.Raw(fmt.Sprintf("(.fcmp %d %d)", k, w))
	} else {
		return fmt.Errorf("float operator %s not supported", x.Op)
	}
	a, err := t.val(x.X)
	if err != nil {
		return err
	}
	b, err := t.val(x.Y)
	if err != nil {
		return err
	}
	return t.floatGuarded(x, w, op, []ir.Var{a, b}, fmt.Sprintf("%s on %s", x.Op, x.X.Type()))
}

// floatGuarded binds x to op(args) when every argument is a w-bit float the
// model knows (fknown), and to a havoc value of x's type otherwise.
func (t *fnState) floatGuarded(x ssa.Value, w int, op ir.Op, args []ir.Var, what string) error {
	dst := t.def(x)
	known := t.fresh()
	t.emit(ir.Instr{Dst: known, Op: ir.Raw(fmt.Sprintf("(.fknown %d)", w)), Args: args})
	return t.guarded(known, func() error {
		t.emit(ir.Instr{Dst: dst, Op: op, Args: args})
		return nil
	}, func() error {
		t.issue("%s on a float the model does not know is havoc", what)
		return t.havocInto(dst, x.Type(), t.f.Name()+": "+what, "opaque-op", "")
	})
}

// guarded branches on cond: known() in one block, unknown() in the other,
// both joining after.
func (t *fnState) guarded(cond ir.Var, known, unknown func() error) error {
	yes := len(t.out.Blocks)
	no, join := yes+1, yes+2
	t.cur.Term = ir.Term{Kind: ir.TermBr, Cond: cond, T: yes, F: no}
	name := t.cur.Name
	t.out.Blocks = append(t.out.Blocks, ir.Block{Name: name + ".float"}, ir.Block{Name: name + ".float-unknown"}, ir.Block{Name: name + ".float-join"})
	t.cur = &t.out.Blocks[yes]
	if err := known(); err != nil {
		return err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[no]
	if err := unknown(); err != nil {
		return err
	}
	t.cur.Term = ir.Term{Kind: ir.TermJump, B: join}
	t.cur = &t.out.Blocks[join]
	return nil
}

// floatConv lowers a conversion with a float on either side: integer to
// float (rounded to nearest even), float to integer (truncated; out of range
// or NaN, which Go leaves to the machine, havoc), and float32 <-> float64.
func (t *fnState) floatConv(dst, src ssa.Value) (bool, error) {
	dw, dstFloat := floatWidth(dst.Type())
	sw, srcFloat := floatWidth(src.Type())
	if !dstFloat && !srcFloat {
		return false, nil
	}
	what := fmt.Sprintf("conversion %s -> %s", src.Type(), dst.Type())
	switch {
	case dstFloat && srcFloat:
		a, err := t.val(src)
		if err != nil {
			return true, err
		}
		return true, t.floatGuarded(dst, sw, ir.Raw(fmt.Sprintf("(.fconv %d %d)", sw, dw)), []ir.Var{a}, what)
	case dstFloat:
		_, signed, ok := intWidth(src.Type())
		if !ok {
			return false, nil
		}
		a, err := t.val(src)
		if err != nil {
			return true, err
		}
		t.emit(ir.Instr{Dst: t.def(dst), Op: ir.Raw(fmt.Sprintf("(.itof %d %v)", dw, signed)), Args: []ir.Var{a}})
		return true, nil
	default:
		iw, signed, ok := intWidth(dst.Type())
		if !ok {
			return false, nil
		}
		a, err := t.val(src)
		if err != nil {
			return true, err
		}
		inRange := t.fresh()
		t.emit(ir.Instr{Dst: inRange, Op: ir.Raw(fmt.Sprintf("(.ftoiOk %d %d %v)", sw, iw, signed)), Args: []ir.Var{a}})
		res := t.def(dst)
		return true, t.guarded(inRange, func() error {
			t.emit(ir.Instr{Dst: res, Op: ir.Raw(fmt.Sprintf("(.ftoi %d %d %v)", sw, iw, signed)), Args: []ir.Var{a}})
			return nil
		}, func() error {
			t.issue("%s out of range, NaN or unknown is havoc", what)
			return t.havocInto(res, dst.Type(), t.f.Name()+": "+what, "opaque-op", "")
		})
	}
}

// floatBitsCall lowers math.Float64bits, Float64frombits, Float32bits and
// Float32frombits: a float is its bits in the model, so they convert
// nothing; Float64bits of a float64 decoded from JSON reads its decimal
// bytes (fconv to the same width).
func (t *fnState) floatBitsCall(x *ssa.Call, callee *ssa.Function) (bool, error) {
	var w int
	toBits := false
	switch callee.String() {
	case "math.Float64bits":
		w, toBits = 64, true
	case "math.Float32bits":
		w, toBits = 32, true
	case "math.Float64frombits":
		w = 64
	case "math.Float32frombits":
		w = 32
	default:
		return false, nil
	}
	if len(x.Call.Args) != 1 {
		return false, nil
	}
	a, err := t.val(x.Call.Args[0])
	if err != nil {
		return true, err
	}
	if !toBits {
		// The bits are the float.
		t.vars[x] = a
		return true, nil
	}
	return true, t.floatGuarded(x, w, ir.Raw(fmt.Sprintf("(.fconv %d %d)", w, w)), []ir.Var{a}, callee.Name())
}
