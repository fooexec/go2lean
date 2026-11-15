package gossa

import (
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/fooexec/go2lean/internal/ir"
)

// The older zero-copy conversions between strings and byte slices, which
// cast pointers through unsafe.Pointer instead of calling unsafe.String
// and unsafe.Slice (unsafeView): tidwall/gjson's and sjson's
//
//	*(*string)(unsafe.Pointer(&b))
//	*(*[]byte)(unsafe.Pointer(&sliceHeader{data: (*stringHeader)(unsafe.Pointer(&s)).data, len: len(s), cap: len(s)}))
//
// with their own header structs, or reflect.StringHeader and SliceHeader.
// A string and a byte slice share one heap representation in the model, so
// each is a view of the same bytes (the unsafeStr and unsafeBytes ops); a
// header struct is a real object whose data field holds the string or
// slice it describes. Exact for a valid program: package unsafe forbids
// writing a string's bytes, and a header's data is only read back.

// headerShape reports whether t is a string or slice header: a struct of
// a data word (unsafe.Pointer or uintptr) and one or two ints (len, cap).
func headerShape(t types.Type) (fields int, ok bool) {
	st, isStruct := t.Underlying().(*types.Struct)
	if !isStruct || st.NumFields() < 2 || st.NumFields() > 3 {
		return 0, false
	}
	b, isBasic := st.Field(0).Type().Underlying().(*types.Basic)
	if !isBasic || b.Kind() != types.UnsafePointer && b.Kind() != types.Uintptr {
		return 0, false
	}
	for i := 1; i < st.NumFields(); i++ {
		if b, ok := st.Field(i).Type().Underlying().(*types.Basic); !ok || b.Kind() != types.Int {
			return 0, false
		}
	}
	return st.NumFields(), true
}

func isBytesOrString(t types.Type) bool { return isString(t) || isByteSlice(t) }

// unsafeCastOf matches dst = (*T)(unsafe.Pointer(p)) for the conversions
// above: p a pointer to a string, a byte slice or a header, and T one of
// them too (not header to header). It returns p.
func unsafeCastOf(dst ssa.Value, src ssa.Value) (ssa.Value, bool) {
	inner, ok := src.(*ssa.Convert)
	if !ok || !isUnsafePointerType(src.Type()) {
		return nil, false
	}
	pt, ok := dst.Type().Underlying().(*types.Pointer)
	if !ok {
		return nil, false
	}
	pu, ok := inner.X.Type().Underlying().(*types.Pointer)
	if !ok {
		return nil, false
	}
	_, toHeader := headerShape(pt.Elem())
	_, fromHeader := headerShape(pu.Elem())
	switch {
	case isBytesOrString(pt.Elem()) && (isBytesOrString(pu.Elem()) || fromHeader):
	case toHeader && isBytesOrString(pu.Elem()):
	default:
		return nil, false
	}
	return inner.X, true
}

func isUnsafePointerType(t types.Type) bool {
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Kind() == types.UnsafePointer
}

// onlyUnsafeCasts reports whether every use of x, an unsafe.Pointer made
// from a pointer, is a conversion unsafeCastOf takes: x itself then needs
// no value.
func onlyUnsafeCasts(x ssa.Value) bool {
	refs := x.Referrers()
	if refs == nil || len(*refs) == 0 {
		return false
	}
	for _, r := range *refs {
		c, ok := r.(*ssa.Convert)
		if !ok {
			return false
		}
		if _, ok := unsafeCastOf(c, x); !ok {
			return false
		}
	}
	return true
}

// unsafeCast lowers dst = (*T)(unsafe.Pointer(p)) when unsafeCastOf takes
// it: a fresh object holding the view of what p points to.
func (t *fnState) unsafeCast(dst ssa.Value, src ssa.Value) (bool, error) {
	p, ok := unsafeCastOf(dst, src)
	if !ok {
		return false, nil
	}
	pv, err := t.val(p)
	if err != nil {
		return true, err
	}
	target := dst.Type().Underlying().(*types.Pointer).Elem()
	from := p.Type().Underlying().(*types.Pointer).Elem()
	var data, n, c ir.Var
	if _, hdr := headerShape(from); hdr {
		data, n = t.fieldLoad(pv, 0), t.fieldLoad(pv, 1)
		c = n
		if k, _ := headerShape(from); k == 3 {
			c = t.fieldLoad(pv, 2)
		}
	} else {
		data = t.fresh()
		t.emit(ir.Instr{Dst: data, Op: ir.Simple("loadPath"), Args: []ir.Var{pv}})
		n, c = t.fresh(), t.fresh()
		if isString(from) {
			t.emit(ir.Instr{Dst: n, Op: ir.Simple("strLen"), Args: []ir.Var{data}})
			c = n
		} else {
			t.emit(ir.Instr{Dst: n, Op: ir.Simple("len"), Args: []ir.Var{data}})
			t.emit(ir.Instr{Dst: c, Op: ir.Simple("cap"), Args: []ir.Var{data}})
		}
	}
	zero, ok := zeroVal(target)
	if !ok {
		return false, nil
	}
	out := t.def(dst)
	t.emit(ir.Instr{Dst: out, Op: ir.Alloc{Zero: zero}})
	if k, hdr := headerShape(target); hdr {
		// The header of a string or slice: its data, length and capacity.
		vals := []ir.Var{data, n, c}
		for i := 0; i < k; i++ {
			fa := t.fresh()
			t.emit(ir.Instr{Dst: fa, Op: ir.FieldAddr{I: i}, Args: []ir.Var{out}})
			t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{fa, vals[i]}})
		}
		return true, nil
	}
	view := t.fresh()
	if isString(target) {
		t.emit(ir.Instr{Dst: view, Op: ir.Simple("unsafeStr"), Args: []ir.Var{data, n}})
	} else {
		t.emit(ir.Instr{Dst: view, Op: ir.Simple("unsafeBytes"), Args: []ir.Var{data, n, c}})
	}
	t.emit(ir.Instr{Dst: t.fresh(), Op: ir.Simple("storePath"), Args: []ir.Var{out, view}})
	return true, nil
}

// fieldLoad reads field i of the struct p points to.
func (t *fnState) fieldLoad(p ir.Var, i int) ir.Var {
	fa, v := t.fresh(), t.fresh()
	t.emit(ir.Instr{Dst: fa, Op: ir.FieldAddr{I: i}, Args: []ir.Var{p}})
	t.emit(ir.Instr{Dst: v, Op: ir.Simple("loadPath"), Args: []ir.Var{fa}})
	return v
}
