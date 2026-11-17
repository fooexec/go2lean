//go:build llvm15 || llvm16 || llvm17 || llvm18 || llvm19 || llvm20 || llvm21 || llvm22 || llvm23

// Package llvm is go2lean's LLVM frontend: it reads LLVM IR (bitcode or
// text, as clang emits it at -O0 with mem2reg applied) with go-llvm and
// prints each defined function as a Lean model over the LLVM dialect of
// lean-llvm/, whose instruction semantics are VeIR's.
//
// It is built only with one of go-llvm's version tags (-tags llvm18), since
// it links libLLVM through cgo; without one, Available is false.
package llvm

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"tinygo.org/x/go-llvm"
)

// Available says whether this binary was built with the LLVM frontend.
const Available = true

// Gen is how the search generates one parameter; the shape mirrors
// Go2Lean.LLVM.Search.Gen.
type Gen struct {
	K      string `json:"k"`
	Name   string `json:"name"`
	W      int    `json:"w,omitempty"`
	EB     int    `json:"eb,omitempty"`
	MaxLen int    `json:"maxLen,omitempty"`
	Of     int    `json:"of"`
	// Random: an object's bytes are random (it holds no pointers).
	Random bool `json:"random,omitempty"`
	// Ty is the type of an aggregate passed by value (k "agg").
	Ty any `json:"ty,omitempty"`
}

// Func is one lowered function: its index in the model, how its
// parameters are generated, and what the lowering did not model.
type Func struct {
	Name   string
	Index  int
	Gens   []Gen
	Issues []string
	// Internal is a function only its own file can call (C's static,
	// internal or private linkage): the search's default leaves it out,
	// as the Go line leaves out unexported functions, since its callers'
	// invariants are not in its parameters.
	Internal bool
	// Consts are the integer constants of the function, which the search
	// tries as values (a magic number guarding a branch).
	Consts []int64
}

// Model is a lowered module: its functions and the Lean definitions.
type Model struct {
	Funcs []Func
	// Lean is the body of the model's module: one definition per function
	// and `prog`.
	Lean string
}

// maxBuf bounds the elements of a generated buffer.
const maxBuf = 8

// Options say what to lower and how parameters are passed.
type Options struct {
	// Keep selects the functions to model (all defined functions when nil);
	// a call to any other function is havoc.
	Keep func(name string) bool
	// GoABI reads parameters as TinyGo passes Go values: `x.data`, `x.len`,
	// `x.cap` are a slice (a string has no cap), and the trailing `context`
	// pointer is nil for a plain function.
	GoABI bool
	// Callees also lowers the functions the kept ones call or take the
	// address of, transitively (up to maxCallees), so a call into the
	// standard library or TinyGo's runtime runs its code instead of being
	// havoc.
	Callees bool
	// Types are the C types of each function's parameters, from the debug
	// information (ParamTypes): a parameter that is a pointer to a struct
	// is generated as that struct (#269). Used only where the function's
	// parameter count is the IR's.
	Types map[string][]any
}

// maxCallees bounds the functions Options.Callees adds.
const maxCallees = 400

// panics are TinyGo's runtime panic helpers, by the core panic they raise.
var panics = map[string]string{
	"runtime.lookupPanic":              "indexOutOfRange",
	"runtime.slicePanic":               "sliceBounds",
	"runtime.sliceToArrayPointerPanic": "sliceBounds",
	"runtime.unsafeSlicePanic":         "sliceBounds",
	"runtime.nilPanic":                 "nilDereference",
	"runtime.nilMapPanic":              "nilMapWrite",
	"runtime.divideByZeroPanic":        "divideByZero",
	"runtime.negativeShiftPanic":       "negativeShift",
	"runtime._panic":                   "explicit",
	"runtime.runtimePanic":             "explicit",
}

// Compile turns a C file into the bitcode the frontend reads: clang at -O0
// without optnone, then mem2reg, so values are SSA registers and memory is
// what the source addresses. A .ll or .bc file is used as it is.
func Compile(src, dir string) (string, error) {
	switch filepath.Ext(src) {
	case ".ll", ".bc":
		return src, nil
	case ".c":
	default:
		return "", fmt.Errorf("%s: want a .c, .ll or .bc file", src)
	}
	base := filepath.Join(dir, strings.TrimSuffix(filepath.Base(src), ".c"))
	run := func(name string, args ...string) error {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %v\n%s", name, err, out)
		}
		return nil
	}
	if err := run("clang", "-O0", "-Xclang", "-disable-O0-optnone", "-g0", "-emit-llvm", "-c", src, "-o", base+".O0.bc"); err != nil {
		return "", err
	}
	if err := run("opt", "-passes=mem2reg", base+".O0.bc", "-o", base+".bc"); err != nil {
		return "", err
	}
	return base + ".bc", nil
}

// Load reads bitcode or IR text and lowers the defined functions opts keeps.
func Load(path string, opts Options) (*Model, error) {
	ctx := llvm.NewContext()
	buf, err := llvm.NewMemoryBufferFromFile(path)
	if err != nil {
		return nil, err
	}
	m, err := ctx.ParseIR(buf)
	if err != nil {
		return nil, err
	}
	l := &lowerer{td: llvm.NewTargetData(m.DataLayout()), fids: map[string]int{}, opts: opts, globalIdx: map[string]int{}}
	var defs []llvm.Value
	for f := m.FirstFunction(); !f.IsNil(); f = llvm.NextFunction(f) {
		if !f.IsDeclaration() && (opts.Keep == nil || opts.Keep(f.Name())) {
			l.fids[f.Name()] = len(defs)
			defs = append(defs, f)
		}
	}
	if opts.Callees {
		// Breadth first, so the functions closest to the kept ones come
		// first when the bound cuts.
		roots := len(defs)
		for i := 0; i < len(defs) && len(defs)-roots < maxCallees; i++ {
			for _, g := range referenced(defs[i]) {
				name := g.Name()
				if _, ok := l.fids[name]; ok || g.IsDeclaration() || len(defs)-roots >= maxCallees {
					continue
				}
				if _, ok := panics[name]; ok || name == "runtime.alloc" || strings.HasPrefix(name, "llvm.") {
					continue
				}
				l.fids[name] = len(defs)
				defs = append(defs, g)
			}
		}
	}
	model := &Model{}
	var b strings.Builder
	var names []string
	for i, f := range defs {
		text, fn := l.function(f, i)
		model.Funcs = append(model.Funcs, fn)
		b.WriteString(text)
		names = append(names, fmt.Sprintf("f%d", i))
	}
	tbl := make([]string, len(l.fnTable))
	for i, e := range l.fnTable {
		tbl[i] = fmt.Sprintf("(%d, %d)", e[0], e[1])
	}
	model.Lean = fmt.Sprintf("def fnTable : List (Nat × Nat) := [%s]\n\n", strings.Join(tbl, ", ")) + b.String()
	b.Reset()
	widths := make([]string, len(l.havoc))
	for i, w := range l.havoc {
		widths[i] = fmt.Sprint(w)
	}
	fmt.Fprintf(&b, "def prog : Go2Lean.LLVM.Prog := { funcs := #[%s], havocWidths := #[%s] }\n\n",
		strings.Join(names, ", "), strings.Join(widths, ", "))
	// One definition per global, its bytes as hex: the module of a
	// package that pulls in the standard library has hundreds.
	var gs []string
	for i, g := range l.globals {
		end := len(g.bytes)
		for end > 0 && g.bytes[end-1] == 0 {
			end-- // trailing zeros are implied by the size
		}
		rs := make([]string, len(g.relocs))
		for k, r := range g.relocs {
			rs[k] = fmt.Sprintf("(%d, %d, %s)", r[0], r[1], lint(r[2]))
		}
		fmt.Fprintf(&b, "def g%d : Go2Lean.LLVM.GlobalInit := { size := %d, bytes := Go2Lean.LLVM.hexBytes %q, relocs := [%s] }\n",
			i, g.size, hex.EncodeToString(g.bytes[:end]), strings.Join(rs, ", "))
		gs = append(gs, fmt.Sprintf("g%d", i))
	}
	fmt.Fprintf(&b, "\ndef globals : List Go2Lean.LLVM.GlobalInit := #[%s].toList\n", strings.Join(gs, ", "))
	model.Lean += b.String()
	return model, nil
}

// WriteModule writes the model as module `Go2Lean.Generated.llvm.<key>`.
func WriteModule(model *Model, leanDir, key string) error {
	dir := filepath.Join(leanDir, "Go2Lean", "Generated", "llvm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mod := "Go2Lean.Generated.llvm." + key
	// Functions of the standard library and the runtime are long array
	// literals: raise the elaborator's depth and step limits for them.
	src := fmt.Sprintf("import Go2LeanLLVM.Dialect\n\nnamespace %s\n\nopen Go2Lean.SSA Go2Lean.LLVM\n\nset_option maxRecDepth 100000\nset_option maxHeartbeats 2000000\n\n%s\nend %s\n", mod, model.Lean, mod)
	return os.WriteFile(filepath.Join(dir, key+".lean"), []byte(src), 0o644)
}

type lowerer struct {
	td    llvm.TargetData
	fids  map[string]int
	opts  Options
	havoc []int // width of each havoc site
	// globals are the module's global variables the lowered code uses, in
	// the order of their indices; globalIdx maps a name to its index, or
	// -1 when its initializer is not modelled.
	globals   []*globalInit
	globalIdx map[string]int
	// partial are the globals whose initial value is modelled only in part.
	partial []string
	// fnTable pairs the heap slot of each modelled function's object with
	// the function.
	fnTable [][2]int
}

// globalInit is a global variable's initial memory: its bytes and the
// pointers among them (offset, target global, addend).
type globalInit struct {
	size   int
	bytes  []byte
	relocs [][3]int64
}

type block struct {
	params []int
	instrs []string
	term   string
}

type fnState struct {
	l       *lowerer
	vars    map[llvm.Value]int
	next    int
	blocks  []*block
	bbIndex map[llvm.BasicBlock]int
	cur     *block
	curBB   llvm.BasicBlock
	issues  []string
	consts  []int64
	// dead: the rest of the LLVM block follows a call that never returns
	// (a runtime panic).
	dead bool
}

func (t *fnState) fresh() int { t.next++; return t.next - 1 }

func (t *fnState) emit(dst int, op string, args ...int) {
	t.cur.instrs = append(t.cur.instrs, fmt.Sprintf(".op %d %s %s", dst, op, list(args)))
}

func (t *fnState) issue(format string, args ...any) {
	t.issues = append(t.issues, fmt.Sprintf(format, args...))
}

func list(vs []int) string {
	s := make([]string, len(vs))
	for i, v := range vs {
		s[i] = fmt.Sprint(v)
	}
	return "[" + strings.Join(s, ", ") + "]"
}

func lint(v int64) string {
	if v < 0 {
		return fmt.Sprintf("(%d)", v)
	}
	return fmt.Sprint(v)
}

func (t *fnState) newBlock() int {
	t.blocks = append(t.blocks, &block{})
	return len(t.blocks) - 1
}

// val is the variable holding operand v, materialising constants.
func (t *fnState) val(v llvm.Value) int {
	if id, ok := t.vars[v]; ok {
		return id
	}
	d := t.fresh()
	ty := v.Type()
	switch {
	case !v.IsAConstantInt().IsNil() && ty.IntTypeWidth() <= 64:
		c := v.SExtValue()
		t.emit(d, fmt.Sprintf("(.const %d %s)", ty.IntTypeWidth(), lint(c)))
		if (c < -2 || c > 2) && len(t.consts) < 32 && !slices.Contains(t.consts, c) {
			t.consts = append(t.consts, c, c-1, c+1)
		}
	case ty.TypeKind() == llvm.PointerTypeKind && (v.IsNull() || !v.IsAUndefValue().IsNil()):
		// undef may be any pointer; null is one of its values.
		t.emit(d, ".null")
	case !v.IsAConstantExpr().IsNil() && v.Opcode() == llvm.IntToPtr:
		// TinyGo passes GC layout tags as inttoptr constants; nothing the
		// model runs dereferences them.
		t.emit(d, ".opaque")
	case !v.IsAUndefValue().IsNil() && ty.TypeKind() == llvm.IntegerTypeKind:
		t.emit(d, fmt.Sprintf("(.poison %d)", ty.IntTypeWidth()))
	case !v.IsAFunction().IsNil():
		// A function pointer points to the function's object.
		t.emit(d, fmt.Sprintf("(.global %d)", t.l.global(v)))
	case !v.IsAGlobalVariable().IsNil():
		if i := t.l.global(v); i >= 0 {
			if slices.Contains(t.l.partial, v.Name()) {
				t.issue("global %s: initial value modelled in part (the rest is zero)", v.Name())
				t.cur.instrs = append(t.cur.instrs, fmt.Sprintf(".assume %q", "global "+v.Name()+": initial value modelled in part (the rest is zero)"))
			}
			t.emit(d, fmt.Sprintf("(.global %d)", i))
		} else {
			t.issue("global %s: initializer not modelled", v.Name())
			t.emit(d, fmt.Sprintf("(.unsupported %q)", "global "+v.Name()))
		}
	case !v.IsAConstantExpr().IsNil() && v.Opcode() == llvm.GetElementPtr:
		base, off, ok := t.l.constPtr(v)
		i := -1
		if ok {
			i = t.l.global(base)
		}
		if !ok || i < 0 {
			t.issue("constant %s not modelled", strings.TrimSpace(v.String()))
			t.emit(d, `(.unsupported "constant getelementptr")`)
			break
		}
		g := t.fresh()
		t.emit(g, fmt.Sprintf("(.global %d)", i))
		t.emit(d, fmt.Sprintf("(.gep %s [])", lint(off)), g)
	case isAgg(ty) && (v.IsNull() || !v.IsAUndefValue().IsNil()):
		// zeroinitializer; undef is the base of insertvalue chains.
		t.emit(d, fmt.Sprintf("(.zero %s)", tyLean(ty)))
	case isAgg(ty) && v.IsConstant() && aggLen(ty) <= 64:
		// A constant struct or array (TinyGo's interface constants: a
		// typecode and a pointer): zero, then each element inserted.
		acc := t.fresh()
		t.emit(acc, fmt.Sprintf("(.zero %s)", tyLean(ty)))
		n := aggLen(ty)
		for i := 0; i < n; i++ {
			e := t.val(aggElem(v, i))
			next := d
			if i < n-1 {
				next = t.fresh()
			}
			t.emit(next, fmt.Sprintf("(.insert %s)", path([]uint32{uint32(i)})), acc, e)
			acc = next
		}
		if n == 0 {
			t.emit(d, fmt.Sprintf("(.zero %s)", tyLean(ty)))
		}
	default:
		what := strings.TrimSpace(v.String())
		t.issue("operand %s not modelled", what)
		t.emit(d, fmt.Sprintf("(.unsupported %q)", "operand "+what))
	}
	return d
}

// aggLen is the number of elements of a struct or array type.
func aggLen(ty llvm.Type) int {
	if ty.TypeKind() == llvm.StructTypeKind {
		return ty.StructElementTypesCount()
	}
	return ty.ArrayLength()
}

// flags reads nsw, nuw and exact from the instruction's text (go-llvm has
// no accessors for them).
func flags(in llvm.Value) (nsw, nuw, exact bool) {
	s := in.String()
	if i := strings.Index(s, "="); i >= 0 {
		s = s[i+1:]
	}
	fs := strings.Fields(s)
	for _, f := range fs[1:] {
		switch f {
		case "nsw":
			nsw = true
		case "nuw":
			nuw = true
		case "exact":
			exact = true
		default:
			return
		}
	}
	return
}

func bool2(b bool) string { return fmt.Sprint(b) }

// referenced are the functions f calls directly or takes the address of.
func referenced(f llvm.Value) []llvm.Value {
	var out []llvm.Value
	seen := map[llvm.Value]bool{}
	for b := f.FirstBasicBlock(); !b.IsNil(); b = llvm.NextBasicBlock(b) {
		for in := b.FirstInstruction(); !in.IsNil(); in = llvm.NextInstruction(in) {
			for i := 0; i < in.OperandsCount(); i++ {
				out = constFuncs(in.Operand(i), seen, out)
			}
		}
	}
	return out
}

// constFuncs adds the functions a constant names: itself, the elements of
// a constant aggregate or expression, and what a global's initializer holds
// (a map TinyGo builds at compile time keeps its key-equality function
// there).
func constFuncs(v llvm.Value, seen map[llvm.Value]bool, out []llvm.Value) []llvm.Value {
	if v.IsNil() || seen[v] || !v.IsConstant() {
		return out
	}
	seen[v] = true
	switch {
	case !v.IsAFunction().IsNil():
		return append(out, v)
	case !v.IsAGlobalVariable().IsNil():
		if init := v.Initializer(); !init.IsNil() {
			out = constFuncs(init, seen, out)
		}
	case !v.IsAConstantExpr().IsNil():
		for i := 0; i < v.OperandsCount(); i++ {
			out = constFuncs(v.Operand(i), seen, out)
		}
	case isAgg(v.Type()) && !v.IsNull() && v.IsAUndefValue().IsNil():
		if n := aggLen(v.Type()); n <= 4096 {
			for i := 0; i < n; i++ {
				out = constFuncs(aggElem(v, i), seen, out)
			}
		}
	}
	return out
}

// tyJSON is the dialect's Ty of a register type, as the search decodes it.
func tyJSON(t llvm.Type) any {
	switch t.TypeKind() {
	case llvm.IntegerTypeKind:
		return map[string]any{"k": "int", "w": t.IntTypeWidth()}
	case llvm.StructTypeKind:
		var fs []any
		for _, f := range t.StructElementTypes() {
			fs = append(fs, tyJSON(f))
		}
		return map[string]any{"k": "agg", "fields": fs}
	case llvm.ArrayTypeKind:
		fs := make([]any, t.ArrayLength())
		for i := range fs {
			fs[i] = tyJSON(t.ElementType())
		}
		return map[string]any{"k": "agg", "fields": fs}
	}
	return map[string]any{"k": "ptr"}
}

// typeName names a type for messages (go-llvm's Type.String recurses
// without end on some of TinyGo's struct types).
func typeName(t llvm.Type) string {
	switch t.TypeKind() {
	case llvm.IntegerTypeKind:
		return fmt.Sprintf("i%d", t.IntTypeWidth())
	case llvm.PointerTypeKind:
		return "ptr"
	case llvm.StructTypeKind:
		if n := t.StructName(); n != "" {
			return "%" + n
		}
		return "struct"
	case llvm.ArrayTypeKind:
		return fmt.Sprintf("[%d x %s]", t.ArrayLength(), typeName(t.ElementType()))
	case llvm.VoidTypeKind:
		return "void"
	}
	return fmt.Sprintf("type kind %d", t.TypeKind())
}

// global is the index of global variable v, adding it (and the globals its
// initializer points to); -1 when its initializer is not modelled.
func (l *lowerer) global(v llvm.Value) int {
	if i, ok := l.globalIdx[v.Name()]; ok {
		return i
	}
	if !v.IsAFunction().IsNil() {
		// A function's object: one byte, and an entry of the table that
		// resolves pointers to it (a function not modelled resolves to
		// nothing: calls through it take their fallback).
		i := len(l.globals)
		l.globalIdx[v.Name()] = i
		l.globals = append(l.globals, &globalInit{size: 1, bytes: []byte{0}})
		if fid, ok := l.fids[v.Name()]; ok {
			l.fnTable = append(l.fnTable, [2]int{2 + i, fid})
		}
		return i
	}
	init := v.Initializer()
	if init.IsNil() {
		l.globalIdx[v.Name()] = -1
		return -1
	}
	ty := v.GlobalValueType()
	g := &globalInit{size: int(l.td.TypeAllocSize(ty))}
	g.bytes = make([]byte, g.size)
	i := len(l.globals)
	l.globalIdx[v.Name()] = i
	l.globals = append(l.globals, g)
	if !l.constBytes(init, ty, g, 0) {
		// Its identity is what matters most often (a type descriptor);
		// what could not be written stays zero.
		l.partial = append(l.partial, v.Name())
	}
	return i
}

// constBytes writes constant c of type ty at offset off of g; false when
// some of it could not be written (it stays zero).
func (l *lowerer) constBytes(c llvm.Value, ty llvm.Type, g *globalInit, off int64) bool {
	switch {
	case c.IsNull() || !c.IsAUndefValue().IsNil():
		return true // zero
	case !c.IsAConstantInt().IsNil():
		n := int(l.td.TypeAllocSize(ty))
		x := c.ZExtValue()
		for k := 0; k < n && k < 8; k++ {
			g.bytes[off+int64(k)] = byte(x >> (8 * k))
		}
		return true
	case !c.IsAGlobalVariable().IsNil():
		j := l.global(c)
		if j < 0 {
			return false
		}
		g.relocs = append(g.relocs, [3]int64{off, int64(j), 0})
		return true
	case !c.IsAFunction().IsNil():
		g.relocs = append(g.relocs, [3]int64{off, int64(l.global(c)), 0})
		return true
	}
	switch ty.TypeKind() {
	case llvm.StructTypeKind:
		ok := true
		for k, f := range ty.StructElementTypes() {
			ok = l.constBytes(aggElem(c, k), f, g, off+int64(l.td.ElementOffset(ty, k))) && ok
		}
		return ok
	case llvm.ArrayTypeKind:
		ok := true
		stride := int64(l.td.TypeAllocSize(ty.ElementType()))
		for k := 0; k < ty.ArrayLength(); k++ {
			ok = l.constBytes(aggElem(c, k), ty.ElementType(), g, off+int64(k)*stride) && ok
		}
		return ok
	}
	if !c.IsAConstantExpr().IsNil() && c.Opcode() == llvm.GetElementPtr {
		// A pointer into a global: base global plus constant offset.
		base, addend, ok := l.constPtr(c)
		if !ok {
			return false
		}
		j := l.global(base)
		if j < 0 {
			return false
		}
		g.relocs = append(g.relocs, [3]int64{off, int64(j), addend})
		return true
	}
	return false
}

// constPtr resolves a constant pointer to the global it points into and
// the byte offset, through nested constant getelementptrs (TinyGo writes a
// type code as gep(gep(@type, -8), 8)).
func (l *lowerer) constPtr(c llvm.Value) (llvm.Value, int64, bool) {
	if !c.IsAGlobalVariable().IsNil() {
		return c, 0, true
	}
	if c.IsAConstantExpr().IsNil() || c.Opcode() != llvm.GetElementPtr {
		return llvm.Value{}, 0, false
	}
	base, off0, ok := l.constPtr(c.Operand(0))
	if !ok {
		return llvm.Value{}, 0, false
	}
	off, ok := l.constGEPOffset(c)
	return base, off0 + off, ok
}

// constGEPOffset is the byte offset of a constant getelementptr.
func (l *lowerer) constGEPOffset(c llvm.Value) (int64, bool) {
	cur := c.GEPSourceElementType()
	var offset int64
	for i := 1; i < c.OperandsCount(); i++ {
		idx := c.Operand(i)
		if idx.IsAConstantInt().IsNil() {
			return 0, false
		}
		if i == 1 {
			offset += idx.SExtValue() * int64(l.td.TypeAllocSize(cur))
			continue
		}
		switch cur.TypeKind() {
		case llvm.StructTypeKind:
			k := int(idx.ZExtValue())
			offset += int64(l.td.ElementOffset(cur, k))
			cur = cur.StructElementTypes()[k]
		case llvm.ArrayTypeKind:
			cur = cur.ElementType()
			offset += idx.SExtValue() * int64(l.td.TypeAllocSize(cur))
		default:
			return 0, false
		}
	}
	return offset, true
}

func isAgg(t llvm.Type) bool {
	k := t.TypeKind()
	return k == llvm.StructTypeKind || k == llvm.ArrayTypeKind
}

// tyLean is the dialect's Ty of a register type.
func tyLean(t llvm.Type) string {
	switch t.TypeKind() {
	case llvm.IntegerTypeKind:
		return fmt.Sprintf("(.int %d)", t.IntTypeWidth())
	case llvm.PointerTypeKind:
		return ".ptr"
	case llvm.StructTypeKind:
		var fs []string
		for _, f := range t.StructElementTypes() {
			fs = append(fs, tyLean(f))
		}
		return "(.agg [" + strings.Join(fs, ", ") + "])"
	case llvm.ArrayTypeKind:
		fs := make([]string, t.ArrayLength())
		for i := range fs {
			fs[i] = tyLean(t.ElementType())
		}
		return "(.agg [" + strings.Join(fs, ", ") + "])"
	}
	return "(.agg [])"
}

func path(idx []uint32) string {
	s := make([]string, len(idx))
	for i, v := range idx {
		s[i] = fmt.Sprint(v)
	}
	return "[" + strings.Join(s, ", ") + "]"
}

var preds = map[llvm.IntPredicate]string{
	llvm.IntEQ: "eq", llvm.IntNE: "ne", llvm.IntUGT: "ugt", llvm.IntUGE: "uge", llvm.IntULT: "ult",
	llvm.IntULE: "ule", llvm.IntSGT: "sgt", llvm.IntSGE: "sge", llvm.IntSLT: "slt", llvm.IntSLE: "sle",
}

// edgeArgs are the values block succ's phis take on the edge from pred.
func (t *fnState) edgeArgs(pred, succ llvm.BasicBlock) []int {
	var args []int
	for in := succ.FirstInstruction(); !in.IsNil() && in.InstructionOpcode() == llvm.PHI; in = llvm.NextInstruction(in) {
		for i := 0; i < in.IncomingCount(); i++ {
			if in.IncomingBlock(i) == pred {
				args = append(args, t.val(in.IncomingValue(i)))
				break
			}
		}
	}
	return args
}

func (t *fnState) jumpTo(succ llvm.BasicBlock) string {
	return fmt.Sprintf(".jump %d %s", t.bbIndex[succ], list(t.edgeArgs(t.curBB, succ)))
}

// goParam reads parameter i as TinyGo passes Go values: `x.data` is a
// buffer whose `x.len` (and a slice's `x.cap`) follow it, and the trailing
// `context` pointer is nil.
func goParam(ps []llvm.Value, i int, gens []Gen) (Gen, bool) {
	name := ps[i].Name()
	switch {
	case name == "context" && i == len(ps)-1 && ps[i].Type().TypeKind() == llvm.PointerTypeKind:
		return Gen{K: "null", Name: name}, true
	case strings.HasSuffix(name, ".len") || strings.HasSuffix(name, ".cap"):
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".len"), ".cap")
		for j := i - 1; j >= 0 && j >= i-2; j-- {
			if gens[j].K == "buf" && gens[j].Name == base {
				return Gen{K: "len", Name: name, W: ps[i].Type().IntTypeWidth(), Of: j}, true
			}
		}
	}
	return Gen{}, false
}

// elemBytes guesses the element size of what pointer parameter p points
// to: the stride of the first getelementptr on it, else the width of the
// first load or store through it, else 1.
func (l *lowerer) elemBytes(f, p llvm.Value) int {
	for b := f.FirstBasicBlock(); !b.IsNil(); b = llvm.NextBasicBlock(b) {
		for in := b.FirstInstruction(); !in.IsNil(); in = llvm.NextInstruction(in) {
			switch in.InstructionOpcode() {
			case llvm.GetElementPtr:
				if in.Operand(0) == p {
					return int(l.td.TypeAllocSize(in.GEPSourceElementType()))
				}
			case llvm.Load:
				if in.Operand(0) == p && in.Type().TypeKind() == llvm.IntegerTypeKind {
					return max(1, in.Type().IntTypeWidth()/8)
				}
			case llvm.Store:
				if in.Operand(1) == p && in.Operand(0).Type().TypeKind() == llvm.IntegerTypeKind {
					return max(1, in.Operand(0).Type().IntTypeWidth()/8)
				}
			}
		}
	}
	return 1
}

func (l *lowerer) function(f llvm.Value, index int) (string, Func) {
	t := &fnState{l: l, vars: map[llvm.Value]int{}, bbIndex: map[llvm.BasicBlock]int{}}
	fn := Func{Name: f.Name(), Index: index, Internal: f.Linkage() == llvm.InternalLinkage || f.Linkage() == llvm.PrivateLinkage}
	var params []int
	ps := f.Params()
	for i, p := range ps {
		t.vars[p] = t.fresh()
		params = append(params, t.vars[p])
		name := p.Name()
		if name == "" {
			name = fmt.Sprintf("arg%d", i)
		}
		if l.opts.GoABI {
			if g, ok := goParam(ps, i, fn.Gens); ok {
				fn.Gens = append(fn.Gens, g)
				continue
			}
		}
		switch ty := p.Type(); ty.TypeKind() {
		case llvm.IntegerTypeKind:
			g := Gen{K: "int", Name: name, W: ty.IntTypeWidth()}
			// An integer right after a pointer is that buffer's length.
			if i > 0 && len(fn.Gens) == i && fn.Gens[i-1].K == "buf" {
				g = Gen{K: "len", Name: name, W: ty.IntTypeWidth(), Of: i - 1}
			}
			fn.Gens = append(fn.Gens, g)
		case llvm.PointerTypeKind:
			if l.opts.GoABI {
				name = strings.TrimSuffix(name, ".data")
			}
			if ts := l.opts.Types[f.Name()]; len(ts) == len(ps) && ts[i] != nil {
				fn.Gens = append(fn.Gens, Gen{K: "typed", Name: name, Ty: ts[i]})
				continue
			}
			g := Gen{K: "buf", Name: name, EB: l.elemBytes(f, p), MaxLen: maxBuf}
			if g.EB != 1 && g.EB != 2 && g.EB != 4 && g.EB != 8 {
				g.K = "zbuf"
			}
			fn.Gens = append(fn.Gens, g)
		case llvm.StructTypeKind, llvm.ArrayTypeKind:
			fn.Gens = append(fn.Gens, Gen{K: "agg", Name: name, Ty: tyJSON(ty)})
		default:
			t.issue("parameter %d of type %s not modelled", i, typeName(ty))
		}
	}
	// Every LLVM block gets its block now, so branches can name it; calls
	// and switches add blocks after them.
	for b := f.FirstBasicBlock(); !b.IsNil(); b = llvm.NextBasicBlock(b) {
		t.bbIndex[b] = t.newBlock()
		for in := b.FirstInstruction(); !in.IsNil() && in.InstructionOpcode() == llvm.PHI; in = llvm.NextInstruction(in) {
			t.vars[in] = t.fresh()
			t.blocks[t.bbIndex[b]].params = append(t.blocks[t.bbIndex[b]].params, t.vars[in])
		}
	}
	for b := f.FirstBasicBlock(); !b.IsNil(); b = llvm.NextBasicBlock(b) {
		t.curBB = b
		t.cur = t.blocks[t.bbIndex[b]]
		t.dead = false
		for in := b.FirstInstruction(); !in.IsNil() && !t.dead; in = llvm.NextInstruction(in) {
			t.instr(in)
		}
	}
	fn.Issues = t.issues
	fn.Consts = t.consts
	var sb strings.Builder
	fmt.Fprintf(&sb, "def f%d : Go2Lean.LLVM.Func := {\n  name := %q, params := %s, nvals := %d, entry := 0,\n  blocks := #[\n", index, f.Name(), list(params), t.next)
	for i, blk := range t.blocks {
		sep := ","
		if i == len(t.blocks)-1 {
			sep = ""
		}
		term := blk.term
		if term == "" {
			term = ".panic .modelStuck"
		}
		fmt.Fprintf(&sb, "    { params := %s, instrs := #[%s], term := %s }%s\n", list(blk.params), strings.Join(blk.instrs, ", "), term, sep)
	}
	sb.WriteString("  ] }\n\n")
	return sb.String(), fn
}

// fields are the scalar fields of an aggregate type: their path and
// byte offset.
func (t *fnState) fields(ty llvm.Type, base int64, pre []uint32, out *[]aggField) {
	switch ty.TypeKind() {
	case llvm.StructTypeKind:
		for i, f := range ty.StructElementTypes() {
			t.fields(f, base+int64(t.l.td.ElementOffset(ty, i)), append(append([]uint32{}, pre...), uint32(i)), out)
		}
	case llvm.ArrayTypeKind:
		stride := int64(t.l.td.TypeAllocSize(ty.ElementType()))
		for i := 0; i < ty.ArrayLength(); i++ {
			t.fields(ty.ElementType(), base+int64(i)*stride, append(append([]uint32{}, pre...), uint32(i)), out)
		}
	default:
		*out = append(*out, aggField{path: pre, offset: base, ty: ty})
	}
}

type aggField struct {
	path   []uint32
	offset int64
	ty     llvm.Type
}

// loadAgg loads an aggregate field by field.
func (t *fnState) loadAgg(p int, ty llvm.Type) int {
	var fs []aggField
	t.fields(ty, 0, nil, &fs)
	acc := t.fresh()
	t.emit(acc, fmt.Sprintf("(.zero %s)", tyLean(ty)))
	for _, f := range fs {
		a, v, next := t.fresh(), t.fresh(), t.fresh()
		t.emit(a, fmt.Sprintf("(.gep %s [])", lint(f.offset)), p)
		if f.ty.TypeKind() == llvm.PointerTypeKind {
			t.emit(v, ".loadPtr", a)
		} else {
			t.emit(v, fmt.Sprintf("(.load %d)", f.ty.IntTypeWidth()), a)
		}
		t.emit(next, fmt.Sprintf("(.insert %s)", path(f.path)), acc, v)
		acc = next
	}
	return acc
}

// storeAgg stores an aggregate field by field.
func (t *fnState) storeAgg(v, p int, ty llvm.Type) {
	var fs []aggField
	t.fields(ty, 0, nil, &fs)
	for _, f := range fs {
		a, x := t.fresh(), t.fresh()
		t.emit(x, fmt.Sprintf("(.extract %s)", path(f.path)), v)
		t.emit(a, fmt.Sprintf("(.gep %s [])", lint(f.offset)), p)
		t.emit(t.fresh(), ".store", x, a)
	}
}

func (t *fnState) instr(in llvm.Value) {
	op := in.InstructionOpcode()
	ty := in.Type()
	switch op {
	case llvm.PHI:
		return // block parameters
	case llvm.Add, llvm.Sub, llvm.Mul:
		nsw, nuw, _ := flags(in)
		name := map[llvm.Opcode]string{llvm.Add: "add", llvm.Sub: "sub", llvm.Mul: "mul"}[op]
		// From C, nsw and nuw come from signed / unsigned arithmetic whose
		// overflow C makes undefined (nuw only for pointer arithmetic).
		a, b := t.val(in.Operand(0)), t.val(in.Operand(1))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], fmt.Sprintf("(.%s %s %s %s)", name, bool2(nsw), bool2(nuw), bool2(nsw)), a, b)
	case llvm.UDiv, llvm.SDiv, llvm.LShr, llvm.AShr:
		_, _, exact := flags(in)
		name := map[llvm.Opcode]string{llvm.UDiv: "udiv", llvm.SDiv: "sdiv", llvm.LShr: "lshr", llvm.AShr: "ashr"}[op]
		a, b := t.val(in.Operand(0)), t.val(in.Operand(1))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], fmt.Sprintf("(.%s %s)", name, bool2(exact)), a, b)
	case llvm.Shl:
		nsw, nuw, _ := flags(in)
		a, b := t.val(in.Operand(0)), t.val(in.Operand(1))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], fmt.Sprintf("(.shl %s %s)", bool2(nsw), bool2(nuw)), a, b)
	case llvm.URem, llvm.SRem, llvm.And, llvm.Or, llvm.Xor:
		name := map[llvm.Opcode]string{llvm.URem: "urem", llvm.SRem: "srem", llvm.And: "and", llvm.Or: "or", llvm.Xor: "xor"}[op]
		a, b := t.val(in.Operand(0)), t.val(in.Operand(1))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], "."+name, a, b)
	case llvm.ICmp:
		a, b := t.val(in.Operand(0)), t.val(in.Operand(1))
		t.vars[in] = t.fresh()
		name := "icmp"
		if in.Operand(0).Type().TypeKind() == llvm.PointerTypeKind {
			name = "icmpPtr"
		}
		t.emit(t.vars[in], fmt.Sprintf("(.%s .%s)", name, preds[in.IntPredicate()]), a, b)
	case llvm.Select:
		c, a, b := t.val(in.Operand(0)), t.val(in.Operand(1)), t.val(in.Operand(2))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], ".select", c, a, b)
	case llvm.Trunc, llvm.ZExt, llvm.SExt:
		name := map[llvm.Opcode]string{llvm.Trunc: "trunc", llvm.ZExt: "zext", llvm.SExt: "sext"}[op]
		a := t.val(in.Operand(0))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], fmt.Sprintf("(.%s %d)", name, ty.IntTypeWidth()), a)
	case llvm.Alloca:
		n := t.l.td.TypeAllocSize(in.AllocatedType())
		if c := in.Operand(0); !c.IsAConstantInt().IsNil() {
			n *= c.ZExtValue()
		} else {
			t.issue("alloca of a dynamic count not modelled")
			t.vars[in] = t.fresh()
			t.emit(t.vars[in], `(.unsupported "dynamic alloca")`)
			return
		}
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], fmt.Sprintf("(.alloca %d)", n))
	case llvm.ExtractValue:
		a := t.val(in.Operand(0))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], fmt.Sprintf("(.extract %s)", path(in.Indices())), a)
	case llvm.InsertValue:
		a, v := t.val(in.Operand(0)), t.val(in.Operand(1))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], fmt.Sprintf("(.insert %s)", path(in.Indices())), a, v)
	case llvm.Load:
		p := t.val(in.Operand(0))
		if isAgg(ty) {
			t.vars[in] = t.loadAgg(p, ty)
			return
		}
		t.vars[in] = t.fresh()
		switch ty.TypeKind() {
		case llvm.IntegerTypeKind:
			t.emit(t.vars[in], fmt.Sprintf("(.load %d)", ty.IntTypeWidth()), p)
		case llvm.PointerTypeKind:
			t.emit(t.vars[in], ".loadPtr", p)
		default:
			t.issue("load of %s not modelled", typeName(ty))
			t.emit(t.vars[in], fmt.Sprintf("(.unsupported %q)", "load "+typeName(ty)), p)
		}
	case llvm.Store:
		v, p := t.val(in.Operand(0)), t.val(in.Operand(1))
		if ty := in.Operand(0).Type(); isAgg(ty) {
			t.storeAgg(v, p, ty)
			return
		}
		t.emit(t.fresh(), ".store", v, p)
	case llvm.GetElementPtr:
		t.gep(in)
	case llvm.Call:
		t.call(in)
	case llvm.Ret:
		if in.OperandsCount() == 0 {
			u := t.fresh()
			t.emit(u, "(.const 1 0)")
			t.cur.term = fmt.Sprintf(".ret %d", u)
			return
		}
		t.cur.term = fmt.Sprintf(".ret %d", t.val(in.Operand(0)))
	case llvm.Br:
		if in.OperandsCount() == 1 {
			t.cur.term = t.jumpTo(in.Successor(0))
			return
		}
		c := t.fresh()
		t.emit(c, ".cond", t.val(in.Operand(0)))
		tb, fb := in.Successor(0), in.Successor(1)
		t.cur.term = fmt.Sprintf(".br %d %d %s %d %s", c, t.bbIndex[tb], list(t.edgeArgs(t.curBB, tb)), t.bbIndex[fb], list(t.edgeArgs(t.curBB, fb)))
	case llvm.Switch:
		t.switchInstr(in)
	case llvm.Unreachable:
		u := t.fresh()
		t.emit(u, ".unreachable")
		t.cur.term = fmt.Sprintf(".ret %d", u)
	default:
		what := strings.TrimSpace(in.String())
		t.issue("instruction %s not modelled", what)
		d := t.fresh()
		t.vars[in] = d
		t.emit(d, fmt.Sprintf("(.unsupported %q)", what))
	}
}

// gep folds constant indices into an offset and passes the others with
// their strides.
func (t *fnState) gep(in llvm.Value) {
	p := t.val(in.Operand(0))
	cur := in.GEPSourceElementType()
	var offset int64
	var idxs []int
	var strides []string
	for i := 1; i < in.OperandsCount(); i++ {
		idx := in.Operand(i)
		var stride int64
		if i == 1 {
			stride = int64(t.l.td.TypeAllocSize(cur))
		} else {
			switch cur.TypeKind() {
			case llvm.StructTypeKind:
				k := int(idx.ZExtValue())
				offset += int64(t.l.td.ElementOffset(cur, k))
				cur = cur.StructElementTypes()[k]
				continue
			case llvm.ArrayTypeKind:
				cur = cur.ElementType()
				stride = int64(t.l.td.TypeAllocSize(cur))
			default:
				t.issue("getelementptr into %s not modelled", typeName(cur))
				t.vars[in] = t.fresh()
				t.emit(t.vars[in], `(.unsupported "getelementptr")`)
				return
			}
		}
		if !idx.IsAConstantInt().IsNil() {
			offset += idx.SExtValue() * stride
			continue
		}
		idxs = append(idxs, t.val(idx))
		strides = append(strides, lint(stride))
	}
	t.vars[in] = t.fresh()
	t.emit(t.vars[in], fmt.Sprintf("(.gep %s [%s])", lint(offset), strings.Join(strides, ", ")), append([]int{p}, idxs...)...)
}

// call: a call to a function of the module is the core's call terminator
// (the block ends there; a fallback block havocs the result if the callee
// gets stuck); a call to a declaration is havoc, its effects unmodelled;
// debug and lifetime intrinsics are dropped.
func (t *fnState) call(in llvm.Value) {
	callee := in.CalledValue()
	if callee.IsAFunction().IsNil() {
		t.indirectCall(in, callee)
		return
	}
	name := callee.Name()
	if strings.HasPrefix(name, "llvm.dbg.") || strings.HasPrefix(name, "llvm.lifetime.") {
		return
	}
	switch {
	case strings.HasPrefix(name, "llvm.memcpy.") || strings.HasPrefix(name, "llvm.memmove."):
		dst, src, n := t.val(in.Operand(0)), t.val(in.Operand(1)), t.val(in.Operand(2))
		t.emit(t.fresh(), fmt.Sprintf("(.memcpy %v)", strings.HasPrefix(name, "llvm.memmove.")), dst, src, n)
		return
	case strings.HasPrefix(name, "llvm.smin.") || strings.HasPrefix(name, "llvm.smax.") ||
		strings.HasPrefix(name, "llvm.umin.") || strings.HasPrefix(name, "llvm.umax."):
		which := map[string]int{"smin": 0, "smax": 1, "umin": 2, "umax": 3}[strings.Split(name, ".")[1]]
		a, b := t.val(in.Operand(0)), t.val(in.Operand(1))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], fmt.Sprintf("(.minmax %d)", which), a, b)
		return
	case strings.HasPrefix(name, "llvm.abs."):
		a, p := t.val(in.Operand(0)), t.val(in.Operand(1))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], ".abs", a, p)
		return
	case strings.HasPrefix(name, "llvm.ctlz.") || strings.HasPrefix(name, "llvm.cttz.") || strings.HasPrefix(name, "llvm.ctpop."):
		which := map[string]int{"ctlz": 0, "cttz": 1, "ctpop": 2}[strings.Split(name, ".")[1]]
		args := []int{t.val(in.Operand(0))}
		if which < 2 {
			args = append(args, t.val(in.Operand(1)))
		}
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], fmt.Sprintf("(.bitCount %d)", which), args...)
		return
	case strings.HasPrefix(name, "llvm.fshl."):
		a, b, c := t.val(in.Operand(0)), t.val(in.Operand(1)), t.val(in.Operand(2))
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], ".fshl", a, b, c)
		return
	case strings.HasPrefix(name, "llvm.memset."):
		dst, b, n := t.val(in.Operand(0)), t.val(in.Operand(1)), t.val(in.Operand(2))
		t.emit(t.fresh(), ".memset", dst, b, n)
		return
	}
	if k, ok := panics[name]; ok {
		// The call never returns: the block ends in the panic.
		t.cur.term = ".panic ." + k
		t.dead = true
		return
	}
	ty := in.Type()
	if name == "runtime.alloc" {
		t.vars[in] = t.fresh()
		t.emit(t.vars[in], ".goAlloc", t.val(in.Operand(0)))
		return
	}
	// C's heap (#269), unless the module defines a function of the name:
	// malloc and calloc are fresh zeroed objects (malloc's bytes are left
	// zero, not uninitialised, so that a read of them is not undefined
	// behaviour the replay cannot see); free ends an object's lifetime;
	// realloc copies into a fresh object and frees the old one.
	if _, defined := t.l.fids[name]; !defined {
		switch name {
		case "malloc":
			t.vars[in] = t.fresh()
			t.emit(t.vars[in], ".goAlloc", t.val(in.Operand(0)))
			return
		case "calloc":
			n := t.fresh()
			t.emit(n, "(.mul false false false)", t.val(in.Operand(0)), t.val(in.Operand(1)))
			t.vars[in] = t.fresh()
			t.emit(t.vars[in], ".goAlloc", n)
			return
		case "free":
			t.emit(t.fresh(), ".cfree", t.val(in.Operand(0)))
			return
		case "realloc":
			t.vars[in] = t.fresh()
			t.emit(t.vars[in], ".realloc", t.val(in.Operand(0)), t.val(in.Operand(1)))
			return
		}
	}
	d := t.fresh()
	if ty.TypeKind() != llvm.VoidTypeKind {
		t.vars[in] = d
	}
	fid, known := t.l.fids[name]
	if !known && llvm.NextInstruction(in).InstructionOpcode() == llvm.Unreachable {
		// A call that does not return (an unmodelled panic or exit).
		t.issue("call %s does not return: taken as a panic", name)
		t.cur.term = ".panic .explicit"
		t.dead = true
		return
	}
	if !known {
		if ty.TypeKind() == llvm.IntegerTypeKind {
			t.issue("call %s: result is havoc, effects not modelled", name)
			t.l.havoc = append(t.l.havoc, ty.IntTypeWidth())
			t.cur.instrs = append(t.cur.instrs, fmt.Sprintf(".havoc %d %d", d, len(t.l.havoc)-1))
		} else if ty.TypeKind() == llvm.VoidTypeKind {
			t.issue("call %s: effects not modelled", name)
		} else {
			t.issue("call %s returning %s not modelled", name, typeName(ty))
			t.emit(d, fmt.Sprintf("(.unsupported %q)", "call "+name))
		}
		return
	}
	var args []int
	for i := 0; i < in.OperandsCount()-1; i++ {
		args = append(args, t.val(in.Operand(i)))
	}
	next, fb := t.newBlock(), t.newBlock()
	t.blocks[fb].instrs = []string{t.fallback(d, ty, name)}
	t.blocks[fb].term = fmt.Sprintf(".jump %d []", next)
	t.cur.term = fmt.Sprintf(".call %d %s %d %d [] %d", fid, list(args), d, next, fb)
	t.cur = t.blocks[next]
}

// fallback is what a call's result is when the callee gets stuck: havoc for
// an integer (or no result), stuck for anything else (an aggregate or
// pointer of havoc would be a value the callee cannot return).
func (t *fnState) fallback(d int, ty llvm.Type, what string) string {
	if ty.TypeKind() == llvm.IntegerTypeKind || ty.TypeKind() == llvm.VoidTypeKind {
		w := 64
		if ty.TypeKind() == llvm.IntegerTypeKind {
			w = ty.IntTypeWidth()
		}
		t.l.havoc = append(t.l.havoc, w)
		return fmt.Sprintf(".havoc %d %d", d, len(t.l.havoc)-1)
	}
	return fmt.Sprintf(".op %d (.unsupported %q) []", d, "stuck callee "+what)
}

// indirectCall calls through a function value: the core's callv, whose
// fallback havocs the result when the value is not a modelled function.
func (t *fnState) indirectCall(in, callee llvm.Value) {
	fv := t.fresh()
	t.emit(fv, "(.resolveFn fnTable)", t.val(callee))
	var args []int
	for i := 0; i < in.OperandsCount()-1; i++ {
		args = append(args, t.val(in.Operand(i)))
	}
	d := t.fresh()
	ty := in.Type()
	if ty.TypeKind() != llvm.VoidTypeKind {
		t.vars[in] = d
	}
	next, fb := t.newBlock(), t.newBlock()
	t.blocks[fb].instrs = []string{t.fallback(d, ty, "through a function value")}
	t.blocks[fb].term = fmt.Sprintf(".jump %d []", next)
	t.cur.term = fmt.Sprintf(".callv %d %s %d %d [] %d", fv, list(args), d, next, fb)
	t.cur = t.blocks[next]
}

// switchInstr is a chain of equality tests.
func (t *fnState) switchInstr(in llvm.Value) {
	v := t.val(in.Operand(0))
	w := in.Operand(0).Type().IntTypeWidth()
	n := in.SuccessorsCount()
	for i := 1; i < n; i++ {
		k := in.Operand(2 * i)
		kv := t.fresh()
		t.emit(kv, fmt.Sprintf("(.const %d %s)", w, lint(k.SExtValue())))
		e, c := t.fresh(), t.fresh()
		t.emit(e, "(.icmp .eq)", v, kv)
		t.emit(c, ".cond", e)
		dest := in.Successor(i)
		rest := t.newBlock()
		t.cur.term = fmt.Sprintf(".br %d %d %s %d []", c, t.bbIndex[dest], list(t.edgeArgs(t.curBB, dest)), rest)
		t.cur = t.blocks[rest]
	}
	t.cur.term = t.jumpTo(in.Successor(0))
}
