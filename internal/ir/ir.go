// Package ir is the frontend-independent program representation M1's
// frontends produce and the Lean printer consumes. It mirrors the Lean SSA
// core (Go2Lean.SSA): functions of basic blocks with block parameters, a
// dialect op per instruction, and a terminator.
//
// Ops and constants are kept as small structs rather than Lean strings so
// that a second frontend (LLVM) can target the same printer.
package ir

import (
	"fmt"
	"strings"

	"github.com/andreasfoo/go2lean/internal/contract"
)

// Var is an index into a function's value environment.
type Var int

// Val is a dialect constant.
type Val interface{ Lean() string }

// IntVal is a w-bit integer; Bits holds the two's-complement pattern as a
// non-negative decimal string.
type IntVal struct {
	Width int
	Bits  string
}

func (v IntVal) Lean() string { return fmt.Sprintf("(.int %d %s#%d)", v.Width, v.Bits, v.Width) }

type BoolVal bool

func (v BoolVal) Lean() string { return fmt.Sprintf("(.bool %v)", bool(v)) }

type UnitVal struct{}

func (UnitVal) Lean() string { return ".unit" }

// ErrVal is an opaque error value; only nil-ness is modelled.
type ErrVal struct{ NonNil bool }

func (v ErrVal) Lean() string { return fmt.Sprintf("(.err %v)", v.NonNil) }

// NilPtr is the nil pointer.
type NilPtr struct{}

func (NilPtr) Lean() string { return "(.ptr 0 [])" }

// NilSlice is the nil slice.
type NilSlice struct{}

func (NilSlice) Lean() string { return "(.slice 0 0#64 0#64 0#64)" }

// ArrVal is an array value (also a heap object) from its elements.
type ArrVal struct{ Elems []Val }

func (v ArrVal) Lean() string {
	parts := make([]string, len(v.Elems))
	for i, e := range v.Elems {
		parts[i] = e.Lean()
	}
	// A zero array is one element replicated: a long list literal would
	// exceed Lean's elaboration depth.
	if len(parts) > 1 && allEqual(parts) {
		return fmt.Sprintf("(.arr (List.replicate %d %s))", len(parts), parts[0])
	}
	return "(.arr [" + strings.Join(parts, ", ") + "])"
}

func allEqual(ss []string) bool {
	for _, s := range ss[1:] {
		if s != ss[0] {
			return false
		}
	}
	return true
}

// Opaque is a value the model does not look into (interfaces, maps, funcs).
// Opaque is a value the model knows nothing about (a float, the result of
// an unmodelled call of func, map, channel or interface type).
type Opaque struct{}

func (Opaque) Lean() string { return ".unit" }

// OpaqueNil is a nil func, map, channel or interface value.
type OpaqueNil struct{}

func (OpaqueNil) Lean() string { return ".opaqueNil" }

// OpaqueNonNil is a func, map or channel value the program built itself
// (a closure, make(map), make(chan)): never nil.
type OpaqueNonNil struct{}

func (OpaqueNonNil) Lean() string { return "(.boxed 0 .unit)" }

// Boxed is a value stored in an interface with the tag of its type.
type Boxed struct {
	Tag uint32
	V   Val
}

func (b Boxed) Lean() string { return fmt.Sprintf("(.boxed %d %s)", b.Tag, b.V.Lean()) }

// ReFind runs a compiled regexp on a string (the dialect's reFind): Insts
// are the program's instructions rendered for Lean, Kind the method
// (0 MatchString, 1 FindStringSubmatch, 2 FindString, 3 FindStringIndex).
type ReFind struct {
	Insts             []string
	Start, NCap, Kind int
}

func (o ReFind) Lean() string {
	return fmt.Sprintf("(.reFind [%s] %d %d %d)", strings.Join(o.Insts, ", "), o.Start, o.NCap, o.Kind)
}

// StrReplacer is strReplacer: a constant strings.Replacer's Replace.
type StrReplacer struct{ Pairs [][2]string }

func (o StrReplacer) Lean() string {
	parts := make([]string, len(o.Pairs))
	for i, p := range o.Pairs {
		parts[i] = "(" + byteList(p[0]) + ", " + byteList(p[1]) + ")"
	}
	return "(.strReplacer [" + strings.Join(parts, ", ") + "])"
}

func byteList(s string) string {
	parts := make([]string, len(s))
	for i := 0; i < len(s); i++ {
		parts[i] = fmt.Sprint(int(s[i]))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// ReProg is a compiled regexp with the identity of its pattern.
type ReProg struct {
	ID   uint32
	Prog ReFind
}

// ReFindIn is reFindIn: ReFind with the program of the regexp value it is
// given, among Progs.
type ReFindIn struct {
	Progs []ReProg
	Kind  int
}

func (o ReFindIn) Lean() string {
	parts := make([]string, len(o.Progs))
	for i, p := range o.Progs {
		parts[i] = fmt.Sprintf("(%d, [%s], %d, %d)", p.ID, strings.Join(p.Prog.Insts, ", "), p.Prog.Start, p.Prog.NCap)
	}
	return fmt.Sprintf("(.reFindIn [%s] %d)", strings.Join(parts, ", "), o.Kind)
}

// ReIn is reIn: is this a regexp value with one of IDs?
type ReIn struct{ IDs []string }

func (o ReIn) Lean() string { return "(.reIn [" + strings.Join(o.IDs, ", ") + "])" }

// ReflectKind is reflectKind, or reflectIsNil when IsNil: the reflect.Kind
// of each type tag the value may carry.
type ReflectKind struct {
	Table [][2]uint32
	IsNil bool
}

func (o ReflectKind) Lean() string {
	parts := make([]string, len(o.Table))
	for i, e := range o.Table {
		parts[i] = fmt.Sprintf("(%d, %d)", e[0], e[1])
	}
	name := "reflectKind"
	if o.IsNil {
		name = "reflectIsNil"
	}
	return fmt.Sprintf("(.%s [%s])", name, strings.Join(parts, ", "))
}

// IfaceComparable is ifaceComparable: is the dynamic type of an interface
// value comparable? NotComparable lists the tags of the types that are not.
type IfaceComparable struct{ NotComparable []uint32 }

func (o IfaceComparable) Lean() string {
	parts := make([]string, len(o.NotComparable))
	for i, t := range o.NotComparable {
		parts[i] = fmt.Sprint(t)
	}
	return "(.ifaceComparable [" + strings.Join(parts, ", ") + "])"
}

// ErrTagKnown is errTagKnown: is this error nil or boxed with Tag?
type ErrTagKnown struct{ Tag uint32 }

func (o ErrTagKnown) Lean() string { return fmt.Sprintf("(.errTagKnown %d)", o.Tag) }

// EmptyStr is the empty string value.
type EmptyStr struct{}

func (EmptyStr) Lean() string { return "(.str 0 0#64 0#64)" }

// StructVal is a struct value from its field values.
type StructVal struct{ Fields []Val }

func (v StructVal) Lean() string {
	parts := make([]string, len(v.Fields))
	for i, f := range v.Fields {
		parts[i] = f.Lean()
	}
	return "(.structv [" + strings.Join(parts, ", ") + "])"
}

// Op is a dialect instruction.
type Op interface{ Lean() string }

// Const materialises a constant.
type Const struct{ V Val }

func (c Const) Lean() string { return "(.const " + c.V.Lean() + ")" }

// Simple is a nullary-constructor op such as add, slt, len.
type Simple string

func (s Simple) Lean() string { return "." + string(s) }

// Raw is an op with arguments, written out as its Lean term
// ("(.ctxCancel 1)").
type Raw string

func (r Raw) Lean() string { return string(r) }

// Shl is `x << n`; CountSigned says whether n has a signed type.
type Shl struct{ CountSigned bool }

func (s Shl) Lean() string { return fmt.Sprintf("(.shl %v)", s.CountSigned) }

// Shr is `x >> n`, arithmetic when Signed.
type Shr struct{ Signed, CountSigned bool }

func (s Shr) Lean() string { return fmt.Sprintf("(.shr %v %v)", s.Signed, s.CountSigned) }

// Conv converts an integer to Width bits, sign-extending if SignExtend.
type Conv struct {
	Width      int
	SignExtend bool
}

func (c Conv) Lean() string { return fmt.Sprintf("(.conv %d %v)", c.Width, c.SignExtend) }

// Load reads a Width-bit element: args are [slice, index].
type Load struct{ Width int }

func (l Load) Lean() string { return fmt.Sprintf("(.load %d)", l.Width) }

// Extract takes component I of a tuple.
type Extract struct{ I int }

func (e Extract) Lean() string { return fmt.Sprintf("(.extract %d)", e.I) }

// Alloc allocates a heap object initialised to Zero and yields a pointer.
// AssertIface is x.(I) for an interface I; Tags are those of the boxed
// types of the program that implement I.
type AssertIface struct {
	Tags    []uint32
	CommaOk bool
}

func (a AssertIface) Lean() string {
	return fmt.Sprintf("(.assertIface %s %t)", tagList(a.Tags), a.CommaOk)
}

// Dispatch picks the method a call on an interface runs: args are the
// interface value and one func value per tag in Tags.
type Dispatch struct{ Tags []uint32 }

func (d Dispatch) Lean() string { return fmt.Sprintf("(.dispatch %s)", tagList(d.Tags)) }

func tagList(tags []uint32) string {
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = fmt.Sprint(t)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// FuncRef is a func value naming function ID of the program; the args are
// the values a closure captures.
type FuncRef struct{ ID int }

func (f FuncRef) Lean() string { return fmt.Sprintf("(.funcRef %d)", f.ID) }

// XFuncRef is a func value naming function ID of the model registered as
// Key; the driver links it into a FuncRef.
type XFuncRef struct {
	Key string
	ID  int
}

func (f XFuncRef) Lean() string { return fmt.Sprintf("(.xfuncRef %q %d)", f.Key, f.ID) }

// Box stores a concrete value in an interface; Tag names its dynamic type.
type Box struct{ Tag uint32 }

func (b Box) Lean() string { return fmt.Sprintf("(.box %d)", b.Tag) }

// Unbox is x.(T) on a boxed value for a concrete T of tag Tag.
type Unbox struct {
	Tag     uint32
	CommaOk bool
	Zero    Val
}

func (u Unbox) Lean() string {
	return fmt.Sprintf("(.unbox %d %t %s)", u.Tag, u.CommaOk, u.Zero.Lean())
}

// MapLookup is m[k] (with CommaOk, (v, ok)); Zero for a missing key.
// JSONUnmarshal is json.Unmarshal into a pointer to a value of Kind (0
// string, 1 bool, 2 interface{}, 3 map[string]interface{}, 4
// []interface{}); Tags are the type tags of string, bool, float64,
// map[string]interface{} and []interface{} for the decoded values.
type JSONUnmarshal struct {
	Kind int
	Tags [5]uint32
}

func (j JSONUnmarshal) Lean() string {
	return fmt.Sprintf("(.jsonUnmarshal %d [%d, %d, %d, %d, %d])", j.Kind, j.Tags[0], j.Tags[1], j.Tags[2], j.Tags[3], j.Tags[4])
}

// JSONMarshal is json.Marshal of a value described by entry Root of the
// module's jsonTable; it yields ([]byte, error).
type JSONMarshal struct{ Root int }

func (j JSONMarshal) Lean() string { return fmt.Sprintf("(.jsonMarshal jsonTable %d)", j.Root) }

// JSONEncode is (*json.Encoder).Encode(v) for an encoder writing to a
// strings.Builder: args [builder, v], v described by entry Root; HTML is
// the encoder's SetEscapeHTML setting. Yields the error.
type JSONEncode struct {
	Root int
	HTML bool
}

func (j JSONEncode) Lean() string {
	return fmt.Sprintf("(.jsonEncodeTo jsonTable %d %t)", j.Root, j.HTML)
}

// JSONDecode is json.Unmarshal(data, p) into *p described by entry Root.
type JSONDecode struct{ Root int }

func (j JSONDecode) Lean() string { return fmt.Sprintf("(.jsonDecode jsonTable %d)", j.Root) }

type MapLookup struct {
	CommaOk bool
	Zero    Val
}

func (m MapLookup) Lean() string {
	return fmt.Sprintf("(.mapLookup %t %s)", m.CommaOk, m.Zero.Lean())
}

// MapNext advances a map iterator; ZeroK and ZeroV fill a finished step.
type MapNext struct{ ZeroK, ZeroV Val }

func (m MapNext) Lean() string {
	return fmt.Sprintf("(.mapNext %s %s)", m.ZeroK.Lean(), m.ZeroV.Lean())
}

type Alloc struct{ Zero Val }

func (a Alloc) Lean() string { return "(.alloc " + a.Zero.Lean() + ")" }

// FieldAddr is &p.f_I for a pointer p.
type FieldAddr struct{ I int }

func (f FieldAddr) Lean() string { return fmt.Sprintf("(.fieldAddr %d)", f.I) }

// Field is s.f_I for a struct value s.
type Field struct{ I int }

func (f Field) Lean() string { return fmt.Sprintf("(.field %d)", f.I) }

// ChanMake is `make(chan T, n)`; Zero is the value a receive on a closed
// channel yields.
type ChanMake struct{ Zero Val }

func (o ChanMake) Lean() string { return "(.chanMake " + o.Zero.Lean() + ")" }

// GlobalAddr is the address of a package variable that holds
// synchronisation state (a cell): the same pointer for every function and
// goroutine of the run, to a cell holding Zero until the program writes it.
// Key identifies the variable across packages.
type GlobalAddr struct {
	Key  uint32
	Zero Val
}

func (o GlobalAddr) Lean() string {
	return fmt.Sprintf("(.conc (.globalAddr %d %s))", o.Key, o.Zero.Lean())
}

// Conc is one of the concurrent model's heap ops (GoSSA's ConcOp) without
// arguments of its own: ctxBackground, ctxNew, ctxDone, ctxErr, condEnter.
type Conc string

func (c Conc) Lean() string { return "(.conc ." + string(c) + ")" }

// MakeSlice is make([]T, len, cap) with elements Zero.
type MakeSlice struct{ Zero Val }

func (m MakeSlice) Lean() string { return "(.makeSlice " + m.Zero.Lean() + ")" }

// Append is append(s, t...) for slices s and t of the same element type;
// Zero pads the capacity when the backing array grows.
type Append struct{ Zero Val }

func (a Append) Lean() string { return "(.appendSlice " + a.Zero.Lean() + ")" }

// StrConst is a string literal, given as its UTF-8 bytes.
type StrConst struct{ Bytes []byte }

func (c StrConst) Lean() string {
	parts := make([]string, len(c.Bytes))
	for i, b := range c.Bytes {
		parts[i] = fmt.Sprint(int(b))
	}
	return "(.strConst [" + strings.Join(parts, ", ") + "])"
}

// StdlibOps maps fully qualified standard-library functions to the dialect
// ops that model them.
var StdlibOps = map[string]Op{
	// Errors built by the standard library are never nil; the message is
	// not modelled.
	"errors.New":                     Simple("errNew"),
	"fmt.Errorf":                     Simple("errNew"),
	"(*strings.Builder).WriteString": Simple("sbWriteString"),
	"(*strings.Builder).Write":       Simple("sbWrite"),
	"(*strings.Builder).WriteByte":   Simple("sbWriteByte"),
	"(*strings.Builder).WriteRune":   Simple("sbWriteRune"),
	"(*strings.Builder).String":      Simple("sbString"),
	"(*strings.Builder).Len":         Simple("sbLen"),
	"(*strings.Builder).Reset":       Simple("sbReset"),
	"strings.HasPrefix":              Simple("strHasPrefix"),
	"strings.HasSuffix":              Simple("strHasSuffix"),
	"strings.Contains":               Simple("strContains"),
	"strings.Index":                  Simple("strIndexOf"),
	"strings.TrimSpace":              Simple("strTrimSpace"),
	"strings.TrimPrefix":             Simple("strTrimPrefix"),
	"strings.TrimSuffix":             Simple("strTrimSuffix"),
	"strconv.Atoi":                   Simple("strAtoi"),
	"strconv.Itoa":                   Simple("strItoa"),
	"encoding/json.Valid":            Simple("jsonValid"),
	"unicode/utf16.Encode":           Simple("utf16Encode"),
	"strings.Cut":                    Simple("strCut"),
	"strings.LastIndexByte":          Simple("strLastIndexByte"),
	"strconv.ParseInt":               Simple("strParseInt"),
	"strconv.ParseUint":              Simple("strParseUint"),
	"strconv.ParseBool":              Simple("strParseBool"),
	"strconv.FormatInt":              Simple("strFormatInt"),
	"strconv.FormatUint":             Simple("strFormatUint"),
	"strings.IndexByte":              Simple("strIndexByte"),
	"strings.LastIndex":              Simple("strLastIndex"),
	"strings.Count":                  Simple("strCount"),
	"strings.EqualFold":              Simple("strEqualFold"),
	"strings.Trim":                   Simple("strTrim"),
	"strings.TrimLeft":               Simple("strTrimLeft"),
	"strings.TrimRight":              Simple("strTrimRight"),
	"strings.ToLower":                Simple("strToLower"),
	"strings.ToUpper":                Simple("strToUpper"),
	"strings.ReplaceAll":             Simple("strReplaceAll"),
	"strings.Split":                  Simple("strSplit"),
	"strings.Fields":                 Simple("strFields"),
	"strings.Join":                   Simple("strJoin"),
	"strings.Repeat":                 Simple("strRepeat"),
	"encoding/hex.EncodeToString":    Simple("hexEncode"),
	// A reflect.Value is the interface value it reflects; IsZero is the
	// only method modelled (param.IsOmitted of the OpenAI SDK uses it).
	"reflect.ValueOf":        Simple("reflectOf"),
	"(reflect.Value).IsZero": Simple("reflectIsZero"),
	// A random UUID: a fresh value, distinct from every other one a run
	// draws (the real values collide with negligible probability).
	"github.com/google/uuid.New": FreshArr{N: 16},
	// crypto/rand.Read never fails (Go 1.24 on) and fills b; the bytes
	// are left as they were, the model cannot know random ones.
	"crypto/rand.Read": Simple("randRead"),
}

// FreshArr is an array of N bytes distinct from every other FreshArr value
// of the run.
type FreshArr struct{ N int }

func (f FreshArr) Lean() string { return fmt.Sprintf("(.freshArr %d)", f.N) }

// Havoc site kinds, encoded in Prog.HavocWidths: 0 is an opaque error, 1 a
// bool, 2 a string, any other value the width of an integer.
const (
	HavocError  = 0
	HavocBool   = 1
	HavocString = 2
	// HavocAssumption marks a value the model assumed (the zero value of an
	// unmodelled call's result): reading it logs the assumption, so a spec
	// verdict that depended on one is not taken as decided.
	HavocAssumption = 3
)

// Instr is `Dst := Op(Args)`, or a havoc when Op is nil.
type Instr struct {
	Dst       Var
	Op        Op
	Args      []Var
	HavocSite int
	// Probe, when set, makes this a probe: record Args[0] under this label
	// (Op is nil and Dst unused).
	Probe string
	// Assume, when set, records that a run reaching this point takes the
	// under-approximation it describes (Op is nil and Dst unused).
	Assume string
}

type TermKind int

const (
	// TermNone is the zero value: a block the frontend never terminated.
	// Validate rejects it so a lost write cannot become a silent wrong model.
	TermNone TermKind = iota
	TermRet
	TermJump
	TermBr
	TermPanic
	TermCall
	// TermXCall calls a function of another package of the same compile;
	// the driver links it to a FuncId when it merges the models.
	TermXCall
	// TermCallV calls through the func value in FV: the function it names,
	// its captured values first; a nil func panics, an unknown one takes
	// the fallback.
	TermCallV
	// TermSpawn starts a goroutine running the func value FV on CArgs,
	// then jumps to Next. Only the concurrent machine runs it.
	TermSpawn
	// TermSync is the synchronisation operation Sync on CArgs: Dst gets
	// its result, then the block jumps to Next. Only the concurrent machine
	// runs it.
	TermSync
)

// SyncOp is a synchronisation operation of the concurrent machine (Lean's
// `SSA.SyncOp`, docs/concurrency.md).
type SyncOp struct {
	// Kind is the Lean constructor: send, recv, close, select, lock,
	// unlock, tryLock, wLock, wUnlock, rLock, rUnlock, wgAdd, wgWait,
	// onceEnter, yield, condWait, lockL, unlockL.
	Kind     string
	WithOk   bool   // recv: the result is (value, ok)
	Sends    []bool // select: per case, true for a send
	Blocking bool   // select: no default case
	Tag      uint32 // lockL, unlockL: the type tag of *sync.RWMutex
	RTag     uint32 // lockL, unlockL: the type tag of RWMutex.RLocker()'s locker
}

func (o SyncOp) Lean() string {
	switch o.Kind {
	case "recv":
		return fmt.Sprintf("(.recv %t)", o.WithOk)
	case "select":
		parts := make([]string, len(o.Sends))
		for i, s := range o.Sends {
			parts[i] = fmt.Sprint(s)
		}
		return fmt.Sprintf("(.select [%s] %t)", strings.Join(parts, ", "), o.Blocking)
	case "lockL", "unlockL":
		return fmt.Sprintf("(.%s %d %d)", o.Kind, o.Tag, o.RTag)
	}
	return "." + o.Kind
}

// Term is a block terminator. Which fields are meaningful depends on Kind.
type Term struct {
	Kind  TermKind
	V     Var   // ret
	Cond  Var   // br
	B     int   // jump target
	Args  []Var // jump args
	T, F  int   // br targets
	TArgs []Var
	FArgs []Var
	Panic contract.PanicKind
	// call; CalleePkg is set for a cross-package call (TermXCall) and
	// Callee is then an index into that package's program.
	Callee    int
	CalleePkg string
	FV        Var
	CArgs     []Var
	Dst       Var
	Next      int
	NArgs     []Var
	// Fallback is the block run instead of Next when the callee gets
	// stuck: it binds Dst to a havoc value, as an unmodelled call would.
	Fallback int
	// Sync is the operation of a TermSync.
	Sync SyncOp
}

type Block struct {
	Name   string // for comments only
	Params []Var
	Instrs []Instr
	Term   Term
}

type Func struct {
	Name   string
	GoSig  string
	Params []Var
	NVals  int
	Entry  int
	Blocks []Block

	Status    contract.ModelStatus
	Issues    []string
	SpecFor   string
	Closure   bool
	WrappedBy string
	CtorOnly  string
	TestFile  bool
	// Init: the slice of the package initializer that sets the package's
	// init-cells (#55); the search runs it once, before the trials.
	Init bool
	// Sync: the function can reach a goroutine start or a synchronisation
	// operation; the concurrent machine gives its calls frames.
	Sync bool
	// Daemon: a goroutine running it does not keep a run alive (a ticker).
	Daemon bool
	// SpecMaxLen is the spec's go2lean:maxlen directive (0: none).
	SpecMaxLen int
	// SequenceFor names the function a `go2lean:sequence` spec drives.
	SequenceFor string
	Consts      []string         // integer literals, see contract.ModelFunc.Consts
	StrConsts   []string         // string literals
	MapKeys     []string         // string constants used as map keys
	UsesJSON    bool             // the body decodes JSON (json.Unmarshal)
	CmpStrs     []string         // string constants compared with == or !=
	Variants    []string         // union variant fields (Of<Variant>) the body reads
	Calls       []int            // callees within the program, by function id
	XCalls      []contract.XCall // callees in other packages of the same compile
	Exits       []contract.ExitSite
	ABI         contract.ABI
}

// Prog is one package's worth of functions. Func indices are the Lean FuncIds.
type Prog struct {
	Package  string
	Frontend string
	// Dependency marks a package modelled only because the compiled ones
	// call into it (-deps).
	Dependency bool
	Funcs      []*Func
	// SchedSite is the havoc site the concurrent machine reads its schedule
	// from (HasSched false: the package has no concurrency).
	SchedSite   int
	HasSched    bool
	HavocWidths []int
	Havoc       []contract.ABIHavoc
	// Inits are the functions (Func.Init) the search runs before a trial.
	Inits []int
	// JSONTable is the Lean term of the module's `jsonTable` (the JSON
	// descriptions its jsonMarshal / jsonDecode ops index), empty if none.
	JSONTable string
}

// Validate checks structural invariants the Lean printer and interpreter
// rely on: every block has a terminator, and every block, function and
// variable reference is in range.
func (p *Prog) Validate() error {
	for fi, f := range p.Funcs {
		checkVar := func(v Var, where string) error {
			if int(v) < 0 || int(v) >= f.NVals {
				return fmt.Errorf("%s: %s references variable %d outside 0..%d", f.Name, where, v, f.NVals-1)
			}
			return nil
		}
		checkBlock := func(b int, where string) error {
			if b < 0 || b >= len(f.Blocks) {
				return fmt.Errorf("%s: %s targets block %d of %d", f.Name, where, b, len(f.Blocks))
			}
			return nil
		}
		if f.Status == contract.ModelRejected {
			continue
		}
		if err := checkBlock(f.Entry, "entry"); err != nil {
			return err
		}
		for bi, blk := range f.Blocks {
			where := fmt.Sprintf("block %d (%s)", bi, blk.Name)
			for _, in := range blk.Instrs {
				if err := checkVar(in.Dst, where); err != nil {
					return err
				}
				for _, a := range in.Args {
					if err := checkVar(a, where); err != nil {
						return err
					}
				}
			}
			t := blk.Term
			switch t.Kind {
			case TermNone:
				return fmt.Errorf("%s: %s has no terminator (frontend bug)", f.Name, where)
			case TermRet:
				if err := checkVar(t.V, where+" ret"); err != nil {
					return err
				}
			case TermJump:
				if err := checkBlock(t.B, where+" jump"); err != nil {
					return err
				}
			case TermBr:
				if err := checkBlock(t.T, where+" br"); err != nil {
					return err
				}
				if err := checkBlock(t.F, where+" br"); err != nil {
					return err
				}
			case TermSpawn, TermSync:
				if err := checkBlock(t.Next, where+" continuation"); err != nil {
					return err
				}
				if err := checkVar(t.Dst, where+" result"); err != nil {
					return err
				}
			case TermCall, TermXCall, TermCallV:
				if t.Kind == TermCall && (t.Callee < 0 || t.Callee >= len(p.Funcs)) {
					return fmt.Errorf("%s: %s calls function %d of %d", f.Name, where, t.Callee, len(p.Funcs))
				}
				if t.Kind == TermXCall && (t.CalleePkg == "" || t.Callee < 0) {
					return fmt.Errorf("%s: %s cross-package call without a target", f.Name, where)
				}
				if err := checkBlock(t.Next, where+" call continuation"); err != nil {
					return err
				}
				if err := checkBlock(t.Fallback, where+" call fallback"); err != nil {
					return err
				}
				if err := checkVar(t.Dst, where+" call result"); err != nil {
					return err
				}
			}
		}
		_ = fi
	}
	return nil
}

// FuncID returns the index of the function named name, or -1.
func (p *Prog) FuncID(name string) int {
	for i, f := range p.Funcs {
		if f.Name == name {
			return i
		}
	}
	return -1
}

// Succs are the blocks a terminator may continue in.
func (t *Term) Succs() []int {
	switch t.Kind {
	case TermJump:
		return []int{t.B}
	case TermBr:
		return []int{t.T, t.F}
	case TermCall, TermXCall, TermCallV:
		if t.Fallback > 0 {
			return []int{t.Next, t.Fallback}
		}
		return []int{t.Next}
	case TermSpawn, TermSync:
		return []int{t.Next}
	}
	return nil
}

// ProofSuccs are Succs without a call's fallback: a proof goes through the
// callee's contract, under which a callee never gets stuck.
func (t *Term) ProofSuccs() []int {
	switch t.Kind {
	case TermCall, TermXCall, TermCallV:
		return []int{t.Next}
	}
	return t.Succs()
}

// LoopHeaders are the blocks a back edge enters (in a depth-first walk
// from the entry): one per loop of the source, in block order.
func (f *Func) LoopHeaders() []int {
	state := make([]int8, len(f.Blocks)) // 0 unseen, 1 on the path, 2 done
	heads := map[int]bool{}
	var walk func(int)
	walk = func(b int) {
		state[b] = 1
		for _, s := range f.Blocks[b].Term.Succs() {
			if s < 0 || s >= len(f.Blocks) {
				continue
			}
			switch state[s] {
			case 0:
				walk(s)
			case 1:
				heads[s] = true
			}
		}
		state[b] = 2
	}
	if f.Entry >= 0 && f.Entry < len(f.Blocks) {
		walk(f.Entry)
	}
	var out []int
	for b := range f.Blocks {
		if heads[b] {
			out = append(out, b)
		}
	}
	return out
}
