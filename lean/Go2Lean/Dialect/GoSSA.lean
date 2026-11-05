import Go2Lean.SSA.Core
import Lean.Data.Json
import Lean.Meta.Eqns

/-!
# GoSSA dialect

Stage A of the `go/ssa` dialect: integers of every Go width, booleans and
read-only slices. Slice contents are not modelled; the frontend emits a
bounds check (`indexCheck`) followed by a `havoc` for the loaded value.

Semantics follow the Go specification, not LLVM: integer division by zero
panics, `MinInt / -1` wraps, shifts by at least the width give 0 (or the sign
fill), negative signed shift counts panic.
-/

-- The equation lemmas below are made under this limit (#94).
set_option maxHeartbeats 4000000
namespace Go2Lean.GoSSA

open Go2Lean.SSA

inductive Val where
  | int (w : Nat) (v : BitVec w)
  | bool (b : Bool)
  /-- A slice header. `ref` abstracts the backing array; contents are havoc. -/
  | slice (ref : Nat) (off len cap : BitVec 64)
  /-- Multiple results of a call or return. -/
  | tuple (vs : List Val)
  /-- An opaque `error` value: only nil-ness is modelled. -/
  | err (nonNil : Bool)
  /-- A pointer: heap object `ref` (0 is nil) and a path of field/element
  indices inside it. -/
  | ptr (ref : Nat) (path : List Nat)
  /-- A struct value (also the heap object for an allocated struct). -/
  | structv (fields : List Val)
  /-- A heap array: the backing store of a slice or an allocated array. -/
  | arr (elems : List Val)
  /-- A large heap array (more than `bigArr` elements) in an `Array`, which
  the model updates in place (#71): a `List` costs O(i) to read or write
  element `i`, so a loop over a large array is quadratic. -/
  | arrA (elems : Array Val)
  /-- A string: `len` bytes starting at `off` in the immutable byte array
  `ref` (elements are `.int 8`). The empty string is `str 0 0 0`. -/
  | str (ref : Nat) (off len : BitVec 64)
  /-- A concrete value stored in an interface: `tag` names its dynamic type
  (a hash of the Go type's name, the same in every package). Interface
  values the model did not build itself (inputs, havoc) are `unit`. -/
  | boxed (tag : Nat) (v : Val)
  /-- A nil func, map, channel or interface value. A non-nil one the
  program built itself is `boxed` (tag 0 for funcs, maps and channels);
  `unit` is one the model knows nothing about. -/
  | opaqueNil
  | unit
  deriving Repr

instance : Inhabited Val := ⟨.unit⟩

/-- Heap arrays with more elements than this are `arrA`. -/
def bigArr : Nat := 256

/-- A heap array holding `es`: `arrA` when it is large, else `arr`. -/
def mkArr (es : List Val) : Val := if es.length > bigArr then .arrA es.toArray else .arr es

/-- The elements of a heap array, in either form. -/
def arrList? : Val → Option (List Val)
  | .arr es => some es
  | .arrA es => some es.toList
  | _ => none

/-- `es` written over `as` from index `i`, in place when `as` is unshared. -/
def writeAtA (as : Array Val) (i : Nat) (es : List Val) : Array Val :=
  (es.foldl (fun (as, j) e => (as.set! j e, j + 1)) (as, i)).1

/-- A struct field as `encoding/json` sees it: its key, its position in the
struct value, its type in the table, and its tag options. -/
structure JField where
  key : List Nat
  idx : Nat
  ty : Nat
  omitEmpty : Bool := false
  omitZero : Bool := false
  /-- An embedded struct or an SDK `json:",inline"` field: its members are
  the struct's own. -/
  inline : Bool := false
  /-- Not a member: SDK metadata (paramObj, typed `ExtraFields`) that
  changes the encoding when set; the model follows only its zero value. -/
  guard : Bool := false
  /-- A guard that is the type's typed `ExtraFields` map: decoding fills it
  with the members no field reads. -/
  extras : Bool := false
  deriving Repr

/-- The JSON metadata an OpenAI/Anthropic SDK response type keeps (its
`JSON` field): decoding sets the raw text, one `respjson.Field` per typed
member (status and raw), and `ExtraFields` for the others. -/
structure JMeta where
  /-- Index of the `JSON` field in the struct. -/
  field : Nat
  /-- Inside it: the index of `raw`, of `ExtraFields` (if any), and of the
  `respjson.Field` for each typed member, by key. -/
  raw : Nat
  extra : Option Nat
  members : List (List Nat × Nat)
  deriving Repr

/-- How `encoding/json` (and the SDKs' encoders) treat a Go type. Table
entries refer to each other by index. -/
inductive JDesc where
  /-- A string; `dflt` is what a zero value encodes as (the SDKs' constant
  types, `default:"text"`), empty for none. -/
  | str (dflt : List Nat)
  | bool
  | int (w : Nat) (signed : Bool)
  /-- floats: decoded as opaque numbers; encoding gets stuck. -/
  | float
  /-- `[]byte`: base64. -/
  | bytes
  /-- `json.RawMessage`: the bytes themselves. -/
  | raw
  | slice (elem : Nat)
  | arr (n elem : Nat)
  | ptr (elem : Nat)
  /-- A map with string or integer keys; keys are encoded sorted. -/
  | map (key val : Nat)
  /-- `interface{}`: encoded by the dynamic type (`JTable.boxed`), decoded
  as strings, bools, numbers, maps and slices. -/
  | iface
  /-- A struct; `meta` for SDK response types. `api`: the SDKs' own
  decoder (apijson) reads it: exact keys, null leaves a member unchanged,
  members that do not decode are skipped (the model gets stuck there). -/
  | strct (fields : List JField) (md : Option JMeta) (api : Bool)
  /-- An SDK param union: exactly one variant field present is encoded. -/
  | union (variants : List JField)
  /-- `param.Opt[T]` (`Value`, `status`): omitted, null or the value. -/
  | opt (elem : Nat)
  /-- Anything else: the model gets stuck. -/
  | opaque
  deriving Repr

/-- The JSON descriptions of a program's types, the descriptions of the
dynamic types interfaces hold (by type tag), and the type tags of
string, bool, float64, `map[string]interface{}` and `[]interface{}`. -/
structure JTable where
  descs : Array JDesc
  /-- The zero value of each entry's type (what decoding starts from). -/
  zeros : Array Val
  boxed : List (Nat × Nat) := []
  basic : List Nat := []
  /-- Decoding an inline union variant: apijson is strict there, and a
  member it coerces is invalid rather than valid (its value is the same;
  the variant still decodes). -/
  strict : Bool := false
  deriving Repr

/-- A zero value in the frontend's zero-model JSON (`{"k": "int", "w": 64}`,
`{"k": "struct", "fields": [...]}`, ...). -/
partial def zeroOfJson (j : Lean.Json) : Val :=
  match (j.getObjValAs? String "k").toOption with
  | some "int" => .int ((j.getObjValAs? Nat "w").toOption.getD 64) 0
  | some "bool" => .bool false
  | some "err" => .err false
  | some "str" => .str 0 0#64 0#64
  | some "nil" => .opaqueNil
  | some "ptr" => .ptr 0 []
  | some "slice" => .slice 0 0#64 0#64 0#64
  | some "struct" => .structv (((j.getObjValAs? (Array Lean.Json) "fields").toOption.getD #[]).toList.map zeroOfJson)
  | some "arr" => .arr (((j.getObjValAs? (Array Lean.Json) "elems").toOption.getD #[]).toList.map zeroOfJson)
  | _ => .unit

def jBytesOf (j : Lean.Json) (key : String) : List Nat :=
  ((j.getObjValAs? String key).toOption.getD "").toUTF8.toList.map (·.toNat)

def jNatOf (j : Lean.Json) (key : String) : Nat := (j.getObjValAs? Nat key).toOption.getD 0

def jFieldOfJson (j : Lean.Json) : JField :=
  { key := jBytesOf j "key", idx := jNatOf j "idx", ty := jNatOf j "ty",
    omitEmpty := (j.getObjValAs? Bool "oe").toOption.getD false,
    omitZero := (j.getObjValAs? Bool "oz").toOption.getD false,
    inline := (j.getObjValAs? Bool "in").toOption.getD false,
    guard := (j.getObjValAs? Bool "guard").toOption.getD false,
    extras := (j.getObjValAs? Bool "extras").toOption.getD false }

def jDescOfJson (j : Lean.Json) : JDesc :=
  let fields (key : String) : List JField :=
    ((j.getObjValAs? (Array Lean.Json) key).toOption.getD #[]).toList.map jFieldOfJson
  match (j.getObjValAs? String "k").toOption with
  | some "str" => .str (jBytesOf j "dflt")
  | some "bool" => .bool
  | some "int" => .int (jNatOf j "w") ((j.getObjValAs? Bool "s").toOption.getD true)
  | some "float" => .float
  | some "bytes" => .bytes
  | some "raw" => .raw
  | some "slice" => .slice (jNatOf j "e")
  | some "arr" => .arr (jNatOf j "n") (jNatOf j "e")
  | some "ptr" => .ptr (jNatOf j "e")
  | some "map" => .map (jNatOf j "key") (jNatOf j "val")
  | some "iface" => .iface
  | some "strct" =>
    let md : Option JMeta := (j.getObjVal? "meta").toOption.bind fun m =>
      if m matches .null then none else
      some { field := jNatOf m "field", raw := jNatOf m "raw",
             extra := (m.getObjValAs? Nat "extra").toOption,
             members := ((m.getObjValAs? (Array Lean.Json) "members").toOption.getD #[]).toList.filterMap fun x =>
               match x with
               | .arr #[.str k, .num n] => some (k.toUTF8.toList.map (·.toNat), n.mantissa.toNat)
               | _ => none }
    .strct (fields "fields") md ((j.getObjValAs? Bool "api").toOption.getD false)
  | some "union" => .union (fields "variants")
  | some "opt" => .opt (jNatOf j "e")
  | _ => .opaque

/-- The table from the frontend's JSON (a string in the generated module:
a literal of thousands of descriptions is too slow to elaborate). -/
def JTable.ofJsonString (s : String) : JTable :=
  match Lean.Json.parse s with
  | .ok j =>
    { descs := ((j.getObjValAs? (Array Lean.Json) "descs").toOption.getD #[]).map jDescOfJson,
      zeros := ((j.getObjValAs? (Array Lean.Json) "zeros").toOption.getD #[]).map zeroOfJson,
      boxed := ((j.getObjValAs? (Array Lean.Json) "boxed").toOption.getD #[]).toList.filterMap fun x =>
        match x with
        | .arr #[.num t, .num d] => some (t.mantissa.toNat, d.mantissa.toNat)
        | _ => none,
      basic := ((j.getObjValAs? (Array Nat) "basic").toOption.getD #[]).toList }
  | .error _ => { descs := #[], zeros := #[] }

/-- Read-only memory: backing array `ref`, element index → 64-bit value. Loads
of narrower element types truncate. Fixed for a whole run, so every function
in the program sees the same slice contents. -/
structure Oracle where
  mem : Nat → BitVec 64 → BitVec 64

/-- One instruction of a regexp compiled by Go's regexp/syntax (its Prog,
after Simplify): `out` is the next instruction. A rune instruction lists
the ranges it accepts, case folding already expanded by the frontend. -/
inductive ReInst where
  | rune (ranges : List (Nat × Nat)) (out : Nat)
  /-- any rune; `nl`: newline too -/
  | any (nl : Bool) (out : Nat)
  /-- try `x`, then `y` (leftmost-first priority) -/
  | alt (x y : Nat)
  | cap (slot out : Nat)
  /-- an empty-width assertion: syntax.EmptyOp bits (1 ^ line, 2 $ line,
  4 ^ text, 8 $ text, 16 \b, 32 \B) -/
  | empty (flags out : Nat)
  | match
  | fail
  | nop (out : Nat)
  deriving Repr

/-- Heap operations the concurrent machine's programs use (C1). -/
inductive ConcOp where
  /-- The address of a package variable kept in a cell (one holding
  synchronisation state): allocated holding `zero` on first use; the
  directory of cells (key, pointer) lives in heap slot 0, which no pointer
  reads (`ptr 0` is nil). No args. -/
  | globalAddr (key : Nat) (zero : Val)
  /-- `context`: a context is a heap object (`ctxObj`). `ctxBackground`
  (no args) never ends; `ctxNew` `[parent]` is a cancellable child;
  `ctxDone` `[ctx]` is its done channel (nil, which blocks forever, for a
  context the model did not make); `ctxErr` `[ctx]` its error;
  `ctxCancel code` `[ctx]` ends it and its children (1 canceled, 2
  deadline exceeded). -/
  | ctxBackground | ctxNew | ctxDone | ctxErr | ctxCancel (code : Nat)
  /-- `sync.Cond`: `condEnter` `[cond]` takes a ticket for a `Wait`;
  `condSignal all` `[cond]` releases the oldest ticket, or all. -/
  | condEnter | condSignal (all : Bool)
  /-- `sync/atomic`: `atomicLoad` `[ptr]`, `atomicStore` `[ptr, v]`, as
  `loadPath` and `storePath`, but synchronising (acquire, release) instead
  of racing. -/
  | atomicLoad | atomicStore
  deriving Repr

inductive Op where
  | const (v : Val)
  -- integer arithmetic (same width on both operands)
  | add | sub | mul
  | sdiv | udiv | srem | urem
  | and | or | xor | andNot
  /-- `x << n`; `countSigned` says whether `n` has a signed Go type. -/
  | shl (countSigned : Bool)
  /-- `x >> n`; arithmetic if `signed`. -/
  | shr (signed countSigned : Bool)
  -- comparisons; `s*`/`u*` are signed/unsigned orderings
  | eq | ne
  | slt | sle | sgt | sge
  | ult | ule | ugt | uge
  -- unary
  | neg | bnot | not
  /-- Integer conversion to width `w`; `signExtend` iff the source type is signed. -/
  | conv (w : Nat) (signExtend : Bool)
  -- slices
  | len | cap
  /-- `xs[i]` bounds check. Panics on failure, returns `unit`. -/
  | indexCheck
  /-- `xs[i]` for a slice of `w`-bit integers: bounds check, then read memory. -/
  | load (w : Nat)
  /-- `xs[lo:hi]` -/
  | reslice
  /-- Pack the operands as a tuple (multiple return values). -/
  | mkTuple
  /-- Component `i` of a tuple. -/
  | extract (i : Nat)
  -- heap (stage B)
  /-- Allocate a copy of `zero` on the heap; returns a pointer to it. -/
  | alloc (zero : Val)
  /-- `*p` for a pointer to a scalar or struct. -/
  | loadPath
  /-- `*p = v`; args `[p, v]`, returns `unit`. -/
  | storePath
  /-- `&p.f_i`; args `[p]`. -/
  | fieldAddr (i : Nat)
  /-- `&xs[i]` for a slice whose backing array is on the heap; args `[xs, i]`. -/
  | indexAddr
  /-- `s.f_i` of a struct value. -/
  | field (i : Nat)
  /-- `a[i]` of an array value; args `[a, i]`. -/
  | arrIndex
  /-- A struct value from its fields. -/
  | mkStruct
  /-- An array value from its elements. -/
  | mkArr
  /-- `make([]T, len, cap)` filled with `zero`; args `[len, cap]`. -/
  | makeSlice (zero : Val)
  /-- `make(chan T, cap)`; args `[cap]`. The channel is a heap object
  (`chanObj`); `zero` is what a receive on a closed channel yields. -/
  | chanMake (zero : Val)
  /-- The heap operations of the concurrent model (`ConcOp`), kept out of
  this type so that `denote` stays small for the prover's `simp`. -/
  | conc (c : ConcOp)
  /-- `len(ch)`, `cap(ch)`; args `[ch]` (a nil channel has 0 of both). -/
  | chanLen | chanCap
  /-- Mark a `sync.Once` done (after its function ran); args `[ptr]`. -/
  | onceExit
  /-- `append(s, t...)`: writes in place while the capacity allows, else
  copies into a fresh backing array; `zero` pads the new capacity. -/
  | appendSlice (zero : Val)
  /-- `copy(dst, src)`: copies `min len` elements, yields the count. -/
  | copySlice
  /-- `p == nil` style comparisons go through `eq`/`ne`; this reads `p == nil`. -/
  | isNil
  -- strings
  /-- A string literal (its UTF-8 bytes), allocated on the heap when evaluated. -/
  | strConst (bytes : List Nat)
  | strLen
  /-- `s[i]`: a byte. -/
  | strIndex
  /-- `s[lo:hi]`; args `[s, lo, hi]`. -/
  | strSlice
  | strConcat
  /-- `string(b)` for a byte slice; copies. -/
  | bytesToStr
  /-- `[]byte(s)`; copies. -/
  | strToBytes
  /-- `[]rune(s)`: UTF-8 decoded, invalid bytes become U+FFFD. -/
  | strToRunes
  /-- `string(rs)` for a slice of runes. -/
  | runesToStr
  /-- unicode/utf16.Encode: []rune to []uint16 (surrogate pairs above
  U+FFFF; invalid runes and surrogates become U+FFFD) -/
  | utf16Encode
  /-- strings.Cut: `(before, after, found)` -/
  | strCut
  /-- `(*strings.Replacer).Replace` for a Replacer made from constant
  (old, new) pairs, none of them with an empty old string. -/
  | strReplacer (pairs : List (List Nat × List Nat))
  /-- strings.LastIndexByte -/
  | strLastIndexByte
  /-- `string(r)` for one rune. -/
  | runeToStr
  /-- One step of `for i, r := range s`: the argument points at a heap cell
  holding `(s, pos)`; yields `(ok, i, r)` and advances the cell. -/
  | strNext
  /-- `s[lo:hi:max]`. -/
  | reslice3
  -- a hand-written core of the strings / strconv packages
  | strHasPrefix | strHasSuffix | strContains | strIndexOf
  | strTrimSpace | strTrimPrefix | strTrimSuffix
  /-- `strconv.Atoi`: `(int, error)`. -/
  | strAtoi
  /-- `strconv.Itoa`. -/
  | strItoa
  /-- strconv.ParseInt(s, base, bitSize) and ParseUint: `(value, err)`. -/
  | strParseInt
  | strParseUint
  /-- strconv.ParseBool -/
  | strParseBool
  /-- strconv.ParseFloat(s, 64) where `parseFloatGo` decides it:
  `(value, err)`; `strParseFloatKnown` says whether it does. -/
  | strParseFloat
  | strParseFloatKnown
  /-- strconv.FormatInt(i, base) and FormatUint -/
  | strFormatInt
  | strFormatUint
  /-- `==` / `!=` on arrays and structs that hold strings: strings compare
  by their bytes, the rest as `eq`. -/
  | eqDeep | neDeep
  /-- `hex.EncodeToString(b)`: two lowercase hex digits per byte. -/
  | hexEncode
  /-- `DecodeString` of the base64 encoding its second operand names (see
  `base64DecK`): the bytes and the error (on an error the bytes are nil). -/
  | base64Decode
  /-- `EncodeToString` of the base64 encoding its second operand names. -/
  | base64Encode
  /-- `crypto/rand.Read(b)`: `len(b)` and no error (it never fails since
  Go 1.24); the bytes are left as they were. -/
  | randRead
  /-- `fmt.Sprintf(format, args...)` for the verbs `%s %d %v %t %%` without
  flags or widths on strings, integers and bools the program boxed; any
  other format or value gets the model stuck (the call is then unmodelled). -/
  | sprintf
  /-- Can `sprintf` format these arguments? -/
  | sprintfOk
  /-- `regexp.MustCompile(p)` of a pattern built at run time: 0 when the
  model compiles it (`reCompile`), 1 when Go rejects it, 2 when the model
  does not take its syntax (#46). -/
  | reCompileOk
  /-- `reflect.DeepEqual(x, y)` on values the program built (#46). -/
  | deepEqual
  /-- Can `deepEqual` decide on these values? -/
  | deepEqualOk
  /-- `reflect.ValueOf(x)`: the interface value itself. -/
  | reflectOf
  /-- `reflect.ValueOf(x).IsZero()` on a value the program boxed. -/
  | reflectIsZero
  /-- (reflect.Value).Kind: the kind of the reflected value's dynamic type,
  looked up by its tag (`(tag, kind)` pairs the frontend lists); the nil
  interface gives Invalid (0). An unknown tag gets the model stuck. -/
  | reflectKind (kinds : List (Nat × Nat))
  /-- (reflect.Value).IsNil, with the same table: Go's answer for the kinds
  that can be nil, a panic for the others (and for Invalid). -/
  | reflectIsNil (kinds : List (Nat × Nat))
  /-- An array of `n` bytes distinct from every other `freshArr` value of
  the run (a random UUID): the bytes spell the heap size, which the op
  grows by one. -/
  | freshArr (n : Nat)
  | strIndexByte | strLastIndex | strCount | strEqualFold
  /-- How often byte `c` occurs in `s` (`internal/bytealg.CountString`). -/
  | strCountByte
  | strTrim | strTrimLeft | strTrimRight
  | strToLower | strToUpper | strReplaceAll
  /-- `strings.Split`: a slice of strings. -/
  | strSplit
  /-- `strings.Fields`. -/
  | strFields
  /-- `strings.Join`; args `[elems, sep]`. -/
  | strJoin
  /-- `strings.Repeat`; a negative count panics as in Go. -/
  | strRepeat
  /-- `errors.New` / `fmt.Errorf`: a non-nil error, whatever the arguments. -/
  | errNew
  -- strings.Builder, modelled as a pointer to a string cell
  /-- `(*strings.Builder).WriteString`; args `[p, s]`; yields `(len s, nil)`. -/
  | sbWriteString
  /-- `(*strings.Builder).Write`; args `[p, b]` (a byte slice); yields
  `(len b, nil)`. -/
  | sbWrite
  /-- `WriteByte` / `WriteRune`; args `[p, c]`; yields `nil` resp. `(size, nil)`. -/
  | sbWriteByte | sbWriteRune
  /-- `String` / `Len` / `Reset`; arg `[p]`. -/
  | sbString | sbLen | sbReset
  -- interfaces
  /-- Store a concrete value of dynamic type `tag` in an interface. -/
  | box (tag : Nat)
  /-- `x.(I)` for an interface `I`: the boxed types of the program that
  implement `I` have tags `tags`; the value stays as it is. -/
  | assertIface (tags : List Nat) (commaOk : Bool)
  /-- The method a call on interface value `x` runs, args `[x, f₁, …, fₙ]`:
  `fᵢ` is the method of the boxed type with tag `tags[i]`; the result calls
  it with the boxed value as receiver. A nil interface gives a nil func
  (the call panics), any other value an unknown one. -/
  | dispatch (tags : List Nat)
  /-- A func value naming function `f` of the program, capturing the
  arguments (a closure's free variables). -/
  | funcRef (f : Nat)
  /-- A func value naming function `f` of the model registered as `pkg`;
  the driver links it into a `funcRef`, else it is an unknown non-nil func. -/
  | xfuncRef (pkg : String) (f : Nat)
  /-- Did the model build this interface value (so its dynamic type is known)? -/
  | isBoxed
  /-- `x.(T)` for a concrete `T` of tag `tag` on a boxed value; with
  `commaOk` the result is `(v, ok)` and a mismatch gives `(zero, false)`,
  without it a mismatch panics. -/
  | unbox (tag : Nat) (commaOk : Bool) (zero : Val)
  /-- `c ? a : b` (the builtins min and max) -/
  | ite
  /-- encoding/json.Valid on a byte slice -/
  | jsonValid
  /-- Is it known whether this func/map/channel/interface value is nil? A
  heap object the program made (a channel, a map) is not. -/
  | nilKnown
  /-- Is this error value one whose identity the model knows: nil, a
  sentinel (`.boxed` with the tag of `*errors.errorString`) or another boxed
  error? An anonymous `.err true` (made by the standard library, or havoc)
  may or may not be a given sentinel. -/
  | errKnown
  /-- As `errKnown` for `errors.Is`: nil or a value with this tag (a
  sentinel); another boxed error may wrap one (its `Unwrap` or `Is`). -/
  | errTagKnown (tag : Nat)
  /-- Is this func/map/channel/interface value nil? (Only on known values.) -/
  | isNilOpaque
  /-- Can `==` on these two interface values be decided: both values the
  model built (boxed with their dynamic type's tag, or nil)? (#60) -/
  | ifaceEqKnown
  /-- `==` on two interface values the model built: equal dynamic types and
  equal dynamic values, or both nil. (Only where `ifaceEqKnown`.) -/
  | ifaceEq
  /-- `reflectlite.TypeOf(x).Comparable()`, `errors.Is`'s test of its
  target: false for a value boxed with one of these tags (the boxed types
  `==` does not apply to), true otherwise (#69). -/
  | ifaceComparable (notComparable : List Nat)
  -- maps the program builds: `.boxed 1 p`, `p` pointing at an `.arr` of
  -- `.tuple [key, value]` in insertion order
  | makeMap
  /-- `m[k]` (with `commaOk`, `(v, ok)`); `zero` for a missing key. -/
  | mapLookup (commaOk : Bool) (zero : Val)
  | mapUpdate | mapDelete | mapLen
  /-- `json.Unmarshal(data, p)` for `p` pointing at a `string` (kind 0),
  `bool` (1), `interface{}` (2), `map[string]interface{}` (3) or
  `[]interface{}` (4); args `[data, p]`, yields the error. Decoded values
  are boxed with `tags`, the type tags of string, bool, float64,
  map[string]interface{} and []interface{} (numbers stay opaque). -/
  | jsonUnmarshal (kind : Nat) (tags : List Nat)
  /-- `json.Marshal(v)` for `v` described by entry `root` of `t`; yields
  `(bytes, error)`. -/
  | jsonMarshal (t : JTable) (root : Nat)
  /-- `(*json.Encoder).Encode(v)` for an encoder writing to a
  `strings.Builder`: args `[builder, v]`, `v` described by entry `root`;
  `html` is the encoder's `SetEscapeHTML` setting. Appends the JSON and a
  newline to the builder; yields the error. -/
  | jsonEncodeTo (t : JTable) (root : Nat) (html : Bool)
  /-- `json.Unmarshal(data, p)` for `*p` described by entry `root` of `t`;
  args `[data, p]`, yields the error. -/
  | jsonDecode (t : JTable) (root : Nat)
  /-- An iterator over a map: a heap cell holding `(map, next index)`. -/
  | mapRange
  /-- Advance a map iterator: `(ok, key, value)`, zeros when done. -/
  | mapNext (zeroK zeroV : Val)
  /-- A method of a `*regexp.Regexp` compiled from a constant pattern, run
  on a string: kind 0 MatchString (bool), 1 FindStringSubmatch ([]string,
  nil without a match), 2 FindString (string), 3 FindStringIndex ([]int);
  with a second argument `n`, 4 FindAllString, 5 FindAllStringSubmatch,
  6 FindAllStringIndex (see `reAll`). -/
  | reFind (prog : List ReInst) (start ncap kind : Nat)
  /-- `reFind` on a regexp value (`.boxed _ (.int _ id)`): the program of
  `id` among these (id, program, start, ncap). -/
  | reFindIn (progs : List (Nat × List ReInst × Nat × Nat)) (kind : Nat)
  /-- Is this a regexp value whose id is among these? -/
  | reIn (ids : List Nat)
  /- Floats (#148): a `w`-bit float (64 or 32) is `.int w bits`, its IEEE
  754 encoding; a float64 decoded from JSON may also be `knownFloat`'s
  decimal bytes. Computed with Lean's `Float` / `Float32` (the C double and
  float), so a result rests on compiled code, not on the kernel. -/
  /-- Are all operands floats of width `w` the model knows? -/
  | fknown (w : Nat)
  /-- `x op y` for `k` = 0 add, 1 sub, 2 mul, 3 div (IEEE, round to nearest even). -/
  | fbin (k w : Nat)
  /-- `x op y` for `k` = 0 ==, 1 !=, 2 <, 3 <=, 4 >, 5 >= (false on NaN but !=). -/
  | fcmp (k w : Nat)
  /-- `-x`: the sign bit flipped. -/
  | fneg (w : Nat)
  /-- An integer (of its own width, `signed` or not) converted to a `w`-bit float. -/
  | itof (w : Nat) (signed : Bool)
  /-- Is the truncation of a `w`-bit float a `dw`-bit integer (`signed` or
  not)? Go leaves the result of a conversion out of range to the machine. -/
  | ftoiOk (w dw : Nat) (signed : Bool)
  /-- A `w`-bit float truncated to a `dw`-bit integer (in range: see `ftoiOk`). -/
  | ftoi (w dw : Nat) (signed : Bool)
  /-- A `w`-bit float converted to `dw` bits (float32 <-> float64). -/
  | fconv (w dw : Nat)
  /-- The square root (IEEE: correctly rounded; NaN below zero, `-0` of `-0`). -/
  | fsqrt (w : Nat)
  deriving Repr

/-- Lift a width-polymorphic binary function to two `int` values of equal width. -/
@[inline] def binInt (f : {w : Nat} → BitVec w → BitVec w → BitVec w) : List Val → Res Val
  | [.int w a, .int w' b] =>
    if h : w' = w then .ok (.int w (f a (h ▸ b))) else .panic .modelStuck
  | _ => .panic .modelStuck

@[inline] def cmpInt (f : {w : Nat} → BitVec w → BitVec w → Bool) : List Val → Res Val
  | [.int w a, .int w' b] =>
    if h : w' = w then .ok (.bool (f a (h ▸ b))) else .panic .modelStuck
  | _ => .panic .modelStuck

/-- Division helpers: Go panics on a zero divisor. -/
@[inline] def divInt (f : {w : Nat} → BitVec w → BitVec w → BitVec w) : List Val → Res Val
  | [.int w a, .int w' b] =>
    if h : w' = w then
      if (h ▸ b) = 0#w then .panic .divideByZero else .ok (.int w (f a (h ▸ b)))
    else .panic .modelStuck
  | _ => .panic .modelStuck

/-- Shift count as a natural number, or `negativeShift` for a negative signed count. -/
def shiftCount (countSigned : Bool) : Val → Res Nat
  | .int _ n => if countSigned && n.msb then .panic .negativeShift else .ok n.toNat
  | _ => .panic .modelStuck

/-- Equality where Go defines it for modelled values. Two non-nil errors
compare equal (an over-approximation of identity; M3 filters the fallout). -/
def eqVal : Val → Val → Option Bool
  | .int w a, .int w' b => if h : w' = w then some (a = (h ▸ b)) else none
  -- Arrays and structs compare element by element (the frontend only
  -- lowers == on those whose elements compare without the heap).
  | .arr as, .arr bs => eqVals as bs
  | .arrA ⟨as⟩, .arrA ⟨bs⟩ | .arr as, .arrA ⟨bs⟩ | .arrA ⟨as⟩, .arr bs => eqVals as bs
  | .structv as, .structv bs => eqVals as bs
  | .bool a, .bool b => some (a == b)
  | .err a, .err b => some (a == b)
  -- A nil error is no boxed one (a sentinel, a value of an error type).
  | .err false, .boxed .. => some false
  | .boxed .., .err false => some false
  | .ptr r p, .ptr r' p' => some (r == r' && p == p')
  | .boxed t v, .boxed t' v' => if t == t' then eqVal v v' else some false
  | .opaqueNil, .opaqueNil => some true
  | .opaqueNil, .boxed .. => some false
  | .boxed .., .opaqueNil => some false
  -- Go only compares slices against nil: equal when both are nil.
  | .slice r .., .slice r' .. => some ((r == 0) == (r' == 0))
  | .unit, .unit => none -- opaque values: the frontend havocs these comparisons
  | _, _ => none
where
  eqVals : List Val → List Val → Option Bool
    | [], [] => some true
    | a :: as, b :: bs => match eqVal a b, eqVals as bs with
      | some false, _ => some false
      | some true, r => r
      | none, _ => none
    | _, _ => some false



-- Heap navigation ------------------------------------------------------------

/-- Read the value at `path` inside object `v`. -/
def getPath : Val → List Nat → Option Val
  | v, [] => some v
  | .structv fs, i :: rest => fs[i]? >>= (getPath · rest)
  | .arr es, i :: rest => es[i]? >>= (getPath · rest)
  | .arrA es, i :: rest => es[i]? >>= (getPath · rest)
  | _, _ => none

/-- Replace the value at `path` inside object `v`. -/
def setPath : Val → List Nat → Val → Option Val
  | _, [], nv => some nv
  | .structv fs, i :: rest, nv =>
    fs[i]? >>= fun f => (setPath f rest nv).map fun f' => .structv (fs.set i f')
  | .arr es, i :: rest, nv =>
    es[i]? >>= fun e => (setPath e rest nv).map fun e' => .arr (es.set i e')
  | .arrA es, i :: rest, nv =>
    if h : i < es.size then
      match setPath es[i] rest nv with
      | some e' => some (.arrA (es.set i e' h))
      | none => none
    else none
  | _, _, _ => none

-- Channels ---------------------------------------------------------------------

/-- A channel's heap object: capacity, buffered values (oldest first),
closed flag, and the zero value a receive on a closed channel yields. -/
def chanObj (cap : BitVec 64) (buf : List Val) (closed : Bool) (zero : Val) : Val :=
  .structv [.int 64 cap, .arr buf, .bool closed, zero]

/-- A context: its done channel, error code (0 while it lives) and children. -/
def ctxObj (done : Val) (err : Nat) (children : List Val) : Val :=
  .structv [done, .int 64 (BitVec.ofNat 64 err), .tuple children]

def ctxGet (heap : Heap Val) (c : Val) : Option (Nat × Val × Nat × List Val) :=
  match c with
  | .ptr ref [] =>
    if ref = 0 then none else
    match heap[ref]? with
    | some (.structv [done, .int 64 e, .tuple cs]) => some (ref, done, e.toNat, cs)
    | _ => none
  | _ => none

/-- The channel `ch` points to: its heap ref, capacity, buffer, closed flag
and zero value; `none` for anything else (a nil channel included). -/
def chanGet (heap : Heap Val) (ch : Val) : Option (Nat × BitVec 64 × List Val × Bool × Val) :=
  match ch with
  | .ptr ref [] =>
    if ref = 0 then none else
    match heap[ref]? with
    | some (.structv [.int 64 c, .arr buf, .bool cl, z]) => some (ref, c, buf, cl, z)
    | _ => none
  | _ => none

/-- A nil channel: blocks forever on send and receive. -/
def chanIsNil : Val → Bool
  | .ptr 0 _ => true
  | .opaqueNil => true
  | _ => false

/-- Read through a pointer. `ref = 0` is nil. -/
def heapLoad (heap : Heap Val) : Val → Res Val
  | .ptr 0 _ => .panic .nilDereference
  | .ptr ref path =>
    match heap[ref]? >>= (getPath · path) with
    | some v => .ok v
    | none => .panic .modelStuck
  | _ => .panic .modelStuck

/-- Write through a pointer. -/
def heapStore (heap : Heap Val) : Val → Val → Res (Heap Val)
  | .ptr 0 _, _ => .panic .nilDereference
  | .ptr ref path, nv =>
    match heap[ref]? with
    | some obj@(.arrA _) =>
      -- A large array is updated in place: take it out of the heap first,
      -- so that the heap does not share it (#71).
      let heap := heap.set! ref .unit
      match setPath obj path nv with
      | some obj => .ok (heap.set! ref obj)
      | none => .panic .modelStuck
    | o =>
      match o >>= (setPath · path nv) with
      | some obj => .ok (heap.set! ref obj)
      | none => .panic .modelStuck
  | _, _ => .panic .modelStuck

/-- `setPath` succeeds exactly where `getPath` does. -/
theorem setPath_isSome (v : Val) (path : List Nat) (nv : Val) :
    (setPath v path nv).isSome = (getPath v path).isSome := by
  induction path generalizing v with
  | nil => simp [setPath, getPath]
  | cons i rest ih =>
    cases v <;> simp only [setPath, getPath] <;> try rfl
    case structv fs =>
      cases h : fs[i]? <;> simp [ih]
    case arr es =>
      cases h : es[i]? <;> simp [ih]
    case arrA es =>
      by_cases hi : i < es.size
      · have := ih es[i]
        simp only [hi, dite_true, getElem?_pos, Option.bind_some]
        cases h : setPath es[i] rest nv <;> simp_all
      · simp [hi]

/-- Whether a store through `p` succeeds: `none`, or the panic it gives.
It reads the heap without changing it, so that `denoteH` can hand the heap
to `heapStore` unshared and still give it back unchanged on a failure. -/
def storeCheck (heap : Heap Val) : Val → Option PanicKind
  | .ptr 0 _ => some .nilDereference
  | .ptr ref path => if (heap[ref]? >>= (getPath · path)).isSome then none else some .modelStuck
  | _ => some .modelStuck

theorem heapStore_of_storeCheck (heap : Heap Val) (p v : Val) :
    match storeCheck heap p with
    | none => ∃ h', heapStore heap p v = .ok h'
    | some k => heapStore heap p v = .panic k := by
  match p with
  | .ptr 0 _ => simp [storeCheck, heapStore]
  | .ptr (r + 1) path =>
    simp only [storeCheck, heapStore]
    cases hr : heap[r + 1]? with
    | none => simp
    | some obj =>
      have hs := setPath_isSome obj path v
      cases obj <;> simp only [Option.bind_some] at hs ⊢ <;>
        (split <;> rename_i hg <;> simp_all <;>
          (try (cases hset : setPath _ path v <;> simp_all)))
  | .int .. | .bool .. | .slice .. | .tuple .. | .err .. | .structv .. | .arr .. | .arrA ..
  | .str .. | .boxed .. | .opaqueNil | .unit => simp [storeCheck, heapStore]

/-- Allocate `obj`; the heap's slot 0 is nil, so the first real object gets
reference 1. -/
def heapAlloc (heap : Heap Val) (obj : Val) : Val × Heap Val :=
  let heap := if heap.size = 0 then #[.unit] else heap
  (.ptr heap.size [], heap.push obj)

-- Strings --------------------------------------------------------------------

/-- Go's string order: byte-wise lexicographic, a proper prefix first. -/
def bytesLt : List Nat → List Nat → Bool
  | [], [] => false
  | [], _ :: _ => true
  | _ :: _, [] => false
  | x :: xs, y :: ys => if x < y then true else if y < x then false else bytesLt xs ys

/-- The bytes of a string value, read from the heap. -/
def strBytes (heap : Heap Val) : Val → Option (List Nat)
  | .str _ _ 0#64 => some []
  | .str ref off len =>
    match heap[ref]? with
    | some (.arr es) =>
      some (((es.drop off.toNat).take len.toNat).map fun | .int _ v => v.toNat | _ => 0)
    | some (.arrA es) =>
      some ((es.extract off.toNat (off.toNat + len.toNat)).toList.map fun | .int _ v => v.toNat | _ => 0)
    | _ => none
  | _ => none

/-- A comparison of two strings by their bytes. -/
def strCmp (heap : Heap Val) (a b : Val) (f : List Nat → List Nat → Bool) : Res Val :=
  match strBytes heap a, strBytes heap b with
  | some x, some y => .ok (.bool (f x y))
  | _, _ => .panic .modelStuck

/-- The longest string concatenation, a Builder or Join/ReplaceAll may
build (bytes): longer ones get the model stuck. Strings are lists of
bytes and every intermediate string stays on the heap, so a loop
appending to a string would otherwise use memory quadratic in its
length (one search of the sixth full pass was killed at 9 GB). -/
def strLimit : Nat := 65536

/-- Allocate a string with these bytes. -/
def strAlloc (heap : Heap Val) (bs : List Nat) : Val × Heap Val :=
  if bs.isEmpty then (.str 0 0#64 0#64, heap)
  else
    let (p, heap) := heapAlloc heap (.arr (bs.map fun b => Val.int 8 (BitVec.ofNat 8 b)))
    match p with
    | Val.ptr ref _ => (Val.str ref 0#64 (BitVec.ofNat 64 bs.length), heap)
    | _ => (Val.unit, heap)

def isSpaceByte (b : Nat) : Bool := b == 32 || (9 ≤ b && b ≤ 13)

/-- Index of the first occurrence of `pat` in `s`, or none. -/
def listIndexOf (s pat : List Nat) : Option Nat :=
  (List.range (s.length + 1)).find? fun i => (s.drop i).take pat.length == pat && i + pat.length ≤ s.length

/-- `strconv.Atoi` on bytes: optional sign, decimal digits, must fit int64. -/
def atoiBytes (bs : List Nat) : Option Int :=
  let (neg, digits) := match bs with
    | 45 :: rest => (true, rest)
    | 43 :: rest => (false, rest)
    | rest => (false, rest)
  if digits.isEmpty || !digits.all (fun d => 48 ≤ d && d ≤ 57) then none
  else
    let n : Nat := digits.foldl (fun acc d => acc * 10 + (d - 48)) 0
    let v : Int := if neg then -n else n
    if v < -(2 ^ 63) || v ≥ 2 ^ 63 then none else some v

/-- What `strconv.Atoi` returns for a well-formed number out of the int64
range: the nearest bound (with `ErrRange`). `none` for a syntax error. -/
def atoiClamped (bs : List Nat) : Option Int :=
  let (neg, digits) := match bs with
    | 45 :: rest => (true, rest)
    | 43 :: rest => (false, rest)
    | rest => (false, rest)
  if digits.isEmpty || !digits.all (fun d => 48 ≤ d && d ≤ 57) then none
  else some (if neg then -(2 ^ 63) else 2 ^ 63 - 1)

/-- strings.Replacer's replacement: left to right, at each position the
first pair (in argument order) whose old string starts there, without
overlapping matches. Pairs with an empty old string are not taken (the
frontend does not use this for them). -/
def replacePairs (pairs : List (List Nat × List Nat)) (s : List Nat) : List Nat :=
  let rec go : Nat → List Nat → List Nat
    | 0, s => s
    | _, [] => []
    | fuel + 1, s@(c :: rest) =>
      match pairs.find? (fun (old, _) => !old.isEmpty && s.take old.length == old) with
      | some (old, new) => new ++ go fuel (s.drop old.length)
      | none => c :: go fuel rest
  go (s.length + 1) s

/-- strconv's `lower` on a byte: `c | 0x20`. -/
def strconvLower (c : Nat) : Nat := c ||| 0x20

/-- strconv's underscoreOK: in a base-0 number, underscores only between
digits or after a base prefix. -/
def underscoreOK (s : List Nat) : Bool :=
  let s := match s with | 45 :: r | 43 :: r => r | r => r
  let (hex, start, rest) : Bool × Nat × List Nat := match s with
    | 48 :: p :: r =>
      if strconvLower p == 98 || strconvLower p == 111 || strconvLower p == 120 then (strconvLower p == 120, 48, r)
      else (false, 94, s)
    | _ => (false, 94, s)
  -- saw: 94 start, 48 digit or prefix, 95 underscore, 33 anything else
  let saw := rest.foldl (init := some start) fun saw c =>
    match saw with
    | none => none
    | some sw =>
      if (48 ≤ c && c ≤ 57) || (hex && 97 ≤ strconvLower c && strconvLower c ≤ 102) then some 48
      else if c == 95 then (if sw != 48 then none else some 95)
      else if sw == 95 then none
      else some 33
  match saw with | some sw => sw != 95 | none => false

/-- strconv.ParseUint, step by step as Go does it: the value and the error
(0 none, 1 syntax, 2 range, 3 base or bit size). A range error returns the
bit size's maximum as soon as the value overflows. -/
def parseUintGo (s : List Nat) (base bitSize : Int) : Nat × Nat :=
  if s.isEmpty then (0, 1) else
  let base0 := base == 0
  let (b, body) : Int × List Nat :=
    if 2 ≤ base && base ≤ 36 then (base, s)
    else if base == 0 then
      match s with
      | 48 :: p :: r :: rest =>
        let lp := strconvLower p
        if lp == 98 then (2, r :: rest) else if lp == 111 then (8, r :: rest)
        else if lp == 120 then (16, r :: rest) else (8, p :: r :: rest)
      | 48 :: rest => (8, rest)
      | _ => (10, s)
    else (-1, s)
  if b < 0 || bitSize < 0 || bitSize > 64 then (0, 3) else
  let bits : Nat := if bitSize == 0 then 64 else bitSize.toNat
  let maxVal : Nat := 2 ^ bits - 1
  let rec go : List Nat → Nat → Bool → Nat × Nat
    | [], n, us => if us && !underscoreOK s then (0, 1) else (n, 0)
    | c :: cs, n, us =>
      if c == 95 && base0 then go cs n true
      else
        let d := if 48 ≤ c && c ≤ 57 then c - 48
          else if 97 ≤ strconvLower c && strconvLower c ≤ 122 then strconvLower c - 87 else 99
        if d ≥ b.toNat then (0, 1)
        else
          let n1 := n * b.toNat + d
          if n1 > maxVal then (maxVal, 2) else go cs n1 us
  go body 0 false

/-- strconv.ParseInt: the sign, then ParseUint; out of range gives the
nearest bound of the bit size. -/
def parseIntGo (s : List Nat) (base bitSize : Int) : Int × Nat :=
  if s.isEmpty then (0, 1) else
  let (neg, body) := match s with | 43 :: r => (false, r) | 45 :: r => (true, r) | r => (false, r)
  let (un, e) := parseUintGo body base bitSize
  if e != 0 && e != 2 then (0, e) else
  let bits : Nat := if bitSize == 0 then 64 else bitSize.toNat
  let cutoff : Nat := 2 ^ (bits - 1)
  if !neg && un ≥ cutoff then ((cutoff : Int) - 1, 2)
  else if neg && un > cutoff then (-(cutoff : Int), 2)
  else (if neg then -(un : Int) else un, e)

/-- strconv.FormatUint's digits of n in base (2..36), lower case. -/
def formatNatGo (n base : Nat) : List Nat :=
  let digit (d : Nat) : Nat := if d < 10 then 48 + d else 87 + d
  let rec go : Nat → Nat → List Nat → List Nat
    | 0, _, acc => acc
    | fuel + 1, n, acc => if n < base then digit n :: acc else go fuel (n / base) (digit (n % base) :: acc)
  go 70 n []

def itoaBytes (v : Int) : List Nat :=
  let digits := (toString v.natAbs).toUTF8.toList.map (·.toNat)
  if v < 0 then 45 :: digits else digits

/-- Every index at which `pat` occurs in `s`, left to right. -/
def allIndexOf (s pat : List Nat) : List Nat :=
  (List.range (s.length + 1)).filter fun i => (s.drop i).take pat.length == pat && i + pat.length ≤ s.length

/-- `strings.Split` on bytes: Go's semantics for an empty separator (every
byte) and for an empty string (one empty field). -/
partial def splitBytes (s sep : List Nat) : List (List Nat) :=
  if sep.isEmpty then s.map fun b => [b]
  else
    match listIndexOf s sep with
    | some i => s.take i :: splitBytes (s.drop (i + sep.length)) sep
    | none => [s]

/-- `strings.Fields` on bytes: runs of non-space bytes. -/
partial def fieldsBytes (s : List Nat) : List (List Nat) :=
  let s := s.dropWhile isSpaceByte
  if s.isEmpty then []
  else
    let w := s.takeWhile fun b => !isSpaceByte b
    w :: fieldsBytes (s.drop w.length)

/-- Replace every non-overlapping occurrence of `old` by `new`. -/
partial def replaceBytes (s old new : List Nat) : List Nat :=
  if old.isEmpty then s
  else
    match listIndexOf s old with
    | some i => s.take i ++ new ++ replaceBytes (s.drop (i + old.length)) old new
    | none => s

def lowerByte (b : Nat) : Nat := if 65 ≤ b && b ≤ 90 then b + 32 else b
def upperByte (b : Nat) : Nat := if 97 ≤ b && b ≤ 122 then b - 32 else b

/-- The bytes of each string in a slice of strings on the heap. -/
def strSliceBytes (heap : Heap Val) : Val → Option (List (List Nat))
  | .slice _ _ 0#64 _ => some []
  | .slice ref off len _ =>
    match heap[ref]? with
    | some (.arr es) => ((es.drop off.toNat).take len.toNat).mapM (strBytes heap)
    | some (.arrA es) => (es.extract off.toNat (off.toNat + len.toNat)).toList.mapM (strBytes heap)
    | _ => none
  | _ => none

/-- The elements of a heap-backed slice (nil is empty). -/
def sliceElems (heap : Heap Val) : Val → Option (List Val)
  | .slice 0 _ _ _ => some []
  | .slice ref off len _ =>
    match heap[ref]? with
    | some (.arr es) => some ((es.drop off.toNat).take len.toNat)
    | some (.arrA es) => some (es.extract off.toNat (off.toNat + len.toNat)).toList
    | _ => none
  | _ => none

/-- Equality of arrays and structs whose strings live on the heap. -/
def eqDeep (heap : Heap Val) : Val → Val → Option Bool
  | .str r o l, .str r' o' l' =>
    match strBytes heap (.str r o l), strBytes heap (.str r' o' l') with
    | some x, some y => some (x == y)
    | _, _ => none
  | .arr as, .arr bs => eqDeeps heap as bs
  | .arrA ⟨as⟩, .arrA ⟨bs⟩ | .arr as, .arrA ⟨bs⟩ | .arrA ⟨as⟩, .arr bs => eqDeeps heap as bs
  | .structv as, .structv bs => eqDeeps heap as bs
  | a, b => eqVal a b
where
  eqDeeps (heap : Heap Val) : List Val → List Val → Option Bool
    | [], [] => some true
    | a :: as, b :: bs => match eqDeep heap a b, eqDeeps heap as bs with
      | some false, _ => some false
      | some true, r => r
      | none, _ => none
    | _, _ => some false

/-- Is this the zero value of its type? Strings by their length; `none`
for values the model does not know. -/
def isZeroVal : Val → Option Bool
  | .int _ v => some (v == 0)
  | .bool b => some !b
  | .err b => some !b
  | .ptr r _ => some (r == 0)
  | .slice r .. => some (r == 0)
  | .str _ _ len => some (len == 0)
  | .opaqueNil => some true
  | .boxed .. => some false
  | .arr vs => allZero vs
  | .arrA ⟨vs⟩ => allZero vs
  | .structv vs => allZero vs
  | _ => none
where
  allZero : List Val → Option Bool
    | [] => some true
    | v :: vs => match isZeroVal v, allZero vs with
      | some false, _ => some false
      | some true, r => r
      | none, _ => none

/-- Lower-case hex of bytes, as `%x` and `hex.EncodeToString` print them. -/
def hexDigits (x : List Nat) : List Nat :=
  let digit (d : Nat) := if d < 10 then 48 + d else 87 + d
  x.flatMap fun b => [digit (b / 16), digit (b % 16)]

/-- Upper-case a hex digit (`%X`). -/
def upperHex (c : Nat) : Nat := if 97 ≤ c && c ≤ 102 then c - 32 else c

/-- An integer in hex, lower-case, as `%x` prints it: `-` and the digits of
its magnitude when negative (#46). -/
def intHex (i : Int) : List Nat :=
  let digits := (Nat.toDigits 16 i.natAbs).map Char.toNat
  if i < 0 then 45 :: digits else digits

/-- The bytes `%v` (and `%s`, `%d`, `%t`, `%x`/`%X` of an integer, a string
or a byte slice, whose boxed type tag is `bytesTag`) prints for a value the
program boxed, or `none`. An integer whose type tag is in `unsigned` (the
frontend lists the unsigned integer types the program boxes) prints
unsigned, any other signed. -/
def fmtArg (heap : Heap Val) (bytesTag verb : Nat) (unsigned : List Nat := []) : Val → Option (List Nat)
  | .boxed _ (.str r o l) =>
    if verb == 115 || verb == 118 then strBytes heap (.str r o l)
    else if verb == 120 then hexDigits <$> strBytes heap (.str r o l)
    else if verb == 88 then (·.map upperHex) <$> hexDigits <$> strBytes heap (.str r o l) else none
  | .boxed t (.slice r o l _) =>
    if verb == 120 && t == bytesTag then hexDigits <$> strBytes heap (.str r o l)
    else if verb == 88 && t == bytesTag then (·.map upperHex) <$> hexDigits <$> strBytes heap (.str r o l) else none
  | .boxed t (.int _ v) =>
    let i : Int := if unsigned.contains t then (v.toNat : Int) else v.toInt
    if verb == 100 || verb == 118 then some (itoaBytes i)
    else if verb == 120 then some (intHex i)
    else if verb == 88 then some ((intHex i).map upperHex)
    else none
  | .boxed _ (.bool b) =>
    if verb == 116 || verb == 118 then some (if b then [116, 114, 117, 101] else [102, 97, 108, 115, 101]) else none
  | _ => none

/-- The naturals of a constant array of integers (type tags). -/
def natsOf (vs : List Val) : List Nat := vs.filterMap fun | .int _ v => some v.toNat | _ => none

/-- `x` padded to `width` (#46): with spaces on the left, or on the right
for the `-` flag; with zeros for the `0` flag on an integer, after its
sign. The `0` flag on another value is not modelled (`none`). -/
def fmtPad (zero minus : Bool) (width : Nat) (isInt : Bool) (x : List Nat) : Option (List Nat) :=
  if x.length ≥ width then some x
  else
    let n := width - x.length
    if minus then some (x ++ List.replicate n 32)
    else if zero then
      if !isInt then none
      else match x with
        | 45 :: digits => some (45 :: List.replicate n 48 ++ digits)
        | _ => some (List.replicate n 48 ++ x)
    else some (List.replicate n 32 ++ x)

mutual
/-- Format `fmt` with `args` (see `Op.sprintf`): the verbs `%v %s %d %t %x
%X %%`, with the flags `0` and `-` and a decimal width (#46). -/
def sprintfBytes (heap : Heap Val) (bytesTag : Nat) (unsigned : List Nat) : List Nat → List Val → Option (List Nat)
  | [], [] => some []
  | [], _ :: _ => none -- Go appends %!(EXTRA ...): not modelled
  | 37 :: rest, args => sprintfVerb heap bytesTag unsigned false false 0 rest args
  | c :: rest, args => (c :: ·) <$> sprintfBytes heap bytesTag unsigned rest args

/-- After a `%`: the flags and width so far, then the verb. -/
def sprintfVerb (heap : Heap Val) (bytesTag : Nat) (unsigned : List Nat) (zero minus : Bool) (width : Nat) :
    List Nat → List Val → Option (List Nat)
  | [], _ => none -- a bare % at the end
  | 37 :: rest, args =>
    if zero || minus || width > 0 then none
    else (37 :: ·) <$> sprintfBytes heap bytesTag unsigned rest args
  | 45 :: rest, args => sprintfVerb heap bytesTag unsigned zero true width rest args
  | d :: rest, args =>
    if d == 48 && width == 0 then sprintfVerb heap bytesTag unsigned true minus 0 rest args
    else if 48 ≤ d && d ≤ 57 then sprintfVerb heap bytesTag unsigned zero minus (width * 10 + (d - 48)) rest args
    else match args with
      | [] => none -- a missing argument
      | a :: args =>
        let isInt := match a with | .boxed _ (.int ..) => true | _ => false
        match fmtArg heap bytesTag d unsigned a >>= fmtPad zero minus width isInt,
            sprintfBytes heap bytesTag unsigned rest args with
        | some x, some y => some (x ++ y)
        | _, _ => none
end

/-- Key equality for maps: strings by their bytes, the rest by `eqVal`. -/
def keyEq (heap : Heap Val) (a b : Val) : Bool :=
  match a, b with
  | .str .., .str .. => strBytes heap a == strBytes heap b
  | _, _ => eqVal a b == some true

/-- The entries of the map object `m` (`.boxed 1 p`). -/
def mapEntries (heap : Heap Val) : Val → Option (Val × List (Val × Val))
  | .boxed 1 p =>
    match heapLoad heap p with
    | .ok (.arr es) => some (p, es.filterMap fun | .tuple [k, v] => some (k, v) | _ => none)
    | _ => none
  | _ => none

def mapObj (entries : List (Val × Val)) : Val :=
  .arr (entries.map fun (k, v) => .tuple [k, v])

/-- How many levels `reflectDeepEq` follows (pointers, elements, fields). -/
def deepEqLevels : Nat := 32

/-- `reflect.DeepEqual` on values the program built (#46), within `n`
levels (`none` past them, or on a value the model does not know: an
input's opaque value, an opaque error). As Go's: a nil slice or map is
not deeply equal to a non-nil empty one; pointers are equal when they are
the same or point to deeply equal values; slices when they share their
start and length or hold deeply equal elements; maps when they have the
same keys with deeply equal values; non-nil funcs never; interface
values when their dynamic types are the same and their values deeply
equal. -/
def reflectDeepEq (heap : Heap Val) : Nat → Val → Val → Option Bool
  | 0, _, _ => none
  | n + 1, a, b =>
    match a, b with
    | .str .., .str .. =>
      match strBytes heap a, strBytes heap b with
      | some x, some y => some (x == y)
      | _, _ => none
    | .slice r o l _, .slice r' o' l' _ =>
      if r == 0 || r' == 0 then some (r == r')
      else if l != l' then some false
      else if r == r' && o == o' then some true
      else match sliceElems heap a, sliceElems heap b with
        | some xs, some ys => all n xs ys
        | _, _ => none
    | .ptr r p, .ptr r' p' =>
      if r == r' && p == p' then some true
      else if r == 0 || r' == 0 then some false
      else match heapLoad heap a, heapLoad heap b with
        | .ok x, .ok y => reflectDeepEq heap n x y
        | _, _ => none
    | .arr xs, .arr ys | .structv xs, .structv ys => all n xs ys
    | .arrA ⟨xs⟩, .arrA ⟨ys⟩ | .arr xs, .arrA ⟨ys⟩ | .arrA ⟨xs⟩, .arr ys => all n xs ys
    | .boxed 1 _, .boxed 1 _ =>
      match mapEntries heap a, mapEntries heap b with
      | some (_, es), some (_, fs) =>
        if es.length != fs.length then some false
        else es.foldl (init := some true) fun acc (k, v) =>
          match acc with
          | some true =>
            match fs.find? (fun (k', _) => keyEq heap k k') with
            | some (_, v') => reflectDeepEq heap n v v'
            | none => some false
          | r => r
      | _, _ => none
    | .boxed 0 _, .boxed 0 _ => some false -- non-nil funcs: never deeply equal
    | .boxed t x, .boxed t' y => if t != t' then some false else reflectDeepEq heap n x y
    | .opaqueNil, .opaqueNil => some true
    | .opaqueNil, .boxed .. | .boxed .., .opaqueNil => some false
    | .opaqueNil, .slice r .. | .slice r .., .opaqueNil => some (r == 0)
    | .int .., .int .. | .bool .., .bool .. | .err false, .err false => eqVal a b
    | _, _ => none
where
  all (n : Nat) : List Val → List Val → Option Bool
    | [], [] => some true
    | x :: xs, y :: ys => match reflectDeepEq heap n x y with
      | some true => all n xs ys
      | r => r
    | _, _ => some false

/-- Overwrite `es` from index `i` with `vs`. -/
def writeAt (es : List Val) (i : Nat) (vs : List Val) : List Val :=
  es.take i ++ vs ++ es.drop (i + vs.length)

/-- Decode one UTF-8 sequence at `i`: the code point and its width. Invalid
or truncated input gives U+FFFD of width 1, as Go's range loop does. -/
def decodeRuneA (bs : Array Nat) (i : Nat) : Nat × Nat :=
  let cont (b : Option Nat) : Bool := match b with | some b => 0x80 ≤ b && b < 0xC0 | none => false
  let low (b : Option Nat) : Nat := (b.getD 0) % 0x40
  match bs[i]? with
  | none => (0xFFFD, 1)
  | some b0 =>
    if b0 < 0x80 then (b0, 1)
    else if b0 < 0xC2 then (0xFFFD, 1)
    else if b0 < 0xE0 then
      if cont bs[i+1]? then (((b0 % 0x20) <<< 6) ||| low bs[i+1]?, 2) else (0xFFFD, 1)
    else if b0 < 0xF0 then
      if cont bs[i+1]? && cont bs[i+2]? then
        let r := ((b0 % 0x10) <<< 12) ||| (low bs[i+1]? <<< 6) ||| low bs[i+2]?
        if r < 0x800 || (0xD800 ≤ r && r ≤ 0xDFFF) then (0xFFFD, 1) else (r, 3)
      else (0xFFFD, 1)
    else if b0 < 0xF5 then
      if cont bs[i+1]? && cont bs[i+2]? && cont bs[i+3]? then
        let r := ((b0 % 0x08) <<< 18) ||| (low bs[i+1]? <<< 12) ||| (low bs[i+2]? <<< 6) ||| low bs[i+3]?
        if r < 0x10000 || r > 0x10FFFF then (0xFFFD, 1) else (r, 4)
      else (0xFFFD, 1)
    else (0xFFFD, 1)

/-- `decodeRuneA` on a list (an array of it is built once per call: loops
over a whole string convert once and use `decodeRuneA`). -/
def decodeRune (bs : List Nat) (i : Nat) : Nat × Nat := decodeRuneA bs.toArray i

/-- UTF-8 encode one code point; surrogates and values above U+10FFFF
encode as U+FFFD, as Go's `string(r)` does. -/
def encodeRune (r : Nat) : List Nat :=
  let r := if r > 0x10FFFF || (0xD800 ≤ r && r ≤ 0xDFFF) then 0xFFFD else r
  if r < 0x80 then [r]
  else if r < 0x800 then [0xC0 ||| (r >>> 6), 0x80 ||| (r % 0x40)]
  else if r < 0x10000 then [0xE0 ||| (r >>> 12), 0x80 ||| ((r >>> 6) % 0x40), 0x80 ||| (r % 0x40)]
  else [0xF0 ||| (r >>> 18), 0x80 ||| ((r >>> 12) % 0x40), 0x80 ||| ((r >>> 6) % 0x40), 0x80 ||| (r % 0x40)]

-- Regexps built at run time (#46) ---------------------------------------------

/-- A parsed regexp, the subset `reCompile` takes. -/
inductive ReAst where
  | lit (r : Nat)
  | cls (ranges : List (Nat × Nat))
  | any
  | empty (flag : Nat)
  | cat (xs : List ReAst)
  | alt (xs : List ReAst)
  | star (greedy : Bool) (x : ReAst)
  | plus (greedy : Bool) (x : ReAst)
  | quest (greedy : Bool) (x : ReAst)
  | rep (n : Nat) (m : Option Nat) (greedy : Bool) (x : ReAst)
  | cap (k : Nat) (x : ReAst)
  deriving Inhabited

/-- Why a pattern does not compile: Go rejects it (1), or the model does not
take its syntax (2). -/
abbrev ReErr := Nat

def reMaxRune : Nat := 0x10FFFF

/-- Sorted, merged ranges. -/
def reNormRanges (rs : List (Nat × Nat)) : List (Nat × Nat) :=
  let sorted := (rs.toArray.qsort fun a b => a.1 < b.1).toList
  sorted.foldl (fun acc (lo, hi) =>
    match acc.reverse with
    | (plo, phi) :: rest => if lo ≤ phi + 1 then (rest.reverse ++ [(plo, max phi hi)]) else acc ++ [(lo, hi)]
    | [] => [(lo, hi)]) []

/-- The runes not in `rs` (sorted, merged). -/
def reNegate (rs : List (Nat × Nat)) : List (Nat × Nat) :=
  let (out, next) := (reNormRanges rs).foldl (fun (out, next) (lo, hi) =>
    (if next < lo then out ++ [(next, lo - 1)] else out, hi + 1)) ([], 0)
  if next ≤ reMaxRune then out ++ [(next, reMaxRune)] else out

def reDigit : List (Nat × Nat) := [(48, 57)]
def reWord : List (Nat × Nat) := [(48, 57), (65, 90), (95, 95), (97, 122)]
def reSpace : List (Nat × Nat) := [(9, 10), (12, 13), (32, 32)]

/-- The class a Perl escape letter names (`\d \w \s` and negations). -/
def rePerlClass : Nat → Option (List (Nat × Nat))
  | 100 => some reDigit | 68 => some (reNegate reDigit)
  | 119 => some reWord | 87 => some (reNegate reWord)
  | 115 => some reSpace | 83 => some (reNegate reSpace)
  | _ => none

/-- The rune an escape `\c` stands for, for `c` a punctuation or control
letter; `none` for any other escape. -/
def reEscRune (c : Nat) : Option Nat :=
  if c == 110 then some 10 else if c == 116 then some 9 else if c == 114 then some 13
  else if c == 102 then some 12 else if c == 118 then some 11
  else if c < 128 && !(48 ≤ c && c ≤ 57) && !(65 ≤ c && c ≤ 90) && !(97 ≤ c && c ≤ 122) then some c
  else none

/-- A decimal number at `i`: its value and the position after it. -/
def reNum (rs : Array Nat) : Nat → Nat → Option (Nat × Nat)
  | 0, _ => none
  | fuel + 1, i =>
    match rs[i]? with
    | some d =>
      if 48 ≤ d && d ≤ 57 then
        match reNum rs fuel (i + 1) with
        | some (v, j) =>
          let digits := j - (i + 1)
          some ((d - 48) * 10 ^ digits + v, j)
        | none => some (d - 48, i + 1)
      else none
    | none => none

/-- `{n}`, `{n,}`, `{n,m}` at `i` (at the `{`): n, m and the position after. -/
def reRepeatAt (rs : Array Nat) (i : Nat) : Option (Nat × Option Nat × Nat) := do
  -- A count with a leading zero is no count (Go's parseInt): the brace is
  -- then a literal.
  let num (k : Nat) : Option (Nat × Nat) := do
    let (v, e) ← reNum rs rs.size k
    if e > k + 1 && rs[k]? == some 48 then none else some (v, e)
  let (n, j) ← num (i + 1)
  match rs[j]? with
  | some 125 => some (n, some n, j + 1)
  | some 44 =>
    match rs[j + 1]? with
    | some 125 => some (n, none, j + 2)
    | _ =>
      let (m, k) ← num (j + 1)
      if rs[k]? == some 125 then some (n, some m, k + 1) else none
  | _ => none

/-- A bracketed class from after `[`: its ranges and the position after `]`. -/
def reClass (rs : Array Nat) : Nat → Nat → Bool → List (Nat × Nat) → Except ReErr (List (Nat × Nat) × Nat)
  | 0, _, _, _ => .error 2
  | fuel + 1, i, first, acc =>
    match rs[i]? with
    | none => .error 1 -- missing ]
    | some 93 =>
      if first then reClass rs fuel (i + 1) false (acc ++ [(93, 93)]) else .ok (acc, i + 1)
    | some 91 => if rs[i + 1]? == some 58 then .error 2 else reClass rs fuel (i + 1) false (acc ++ [(91, 91)]) -- [:alpha:]
    | some c =>
      -- one item: a rune, an escape, or a range lo-hi
      let item : Except ReErr (Sum Nat (List (Nat × Nat)) × Nat) :=
        if c == 92 then
          match rs[i + 1]? with
          | some e =>
            match rePerlClass e with
            | some cl => .ok (.inr cl, i + 2)
            | none => match reEscRune e with
              | some r => .ok (.inl r, i + 2)
              | none => .error 2
          | none => .error 1
        else .ok (.inl c, i + 1)
      match item with
      | .error e => .error e
      | .ok (.inr cl, j) => reClass rs fuel j false (acc ++ cl)
      | .ok (.inl lo, j) =>
        if rs[j]? == some 45 && rs[j + 1]? != some 93 && rs[j + 1]?.isSome then
          match rs[j + 1]? with
          | some 92 =>
            match rs[j + 2]? >>= reEscRune with
            | some hi => if hi < lo then .error 1 else reClass rs fuel (j + 3) false (acc ++ [(lo, hi)])
            | none => .error 2
          | some hi => if hi < lo then .error 1 else reClass rs fuel (j + 2) false (acc ++ [(lo, hi)])
          | none => .error 1
        else reClass rs fuel j false (acc ++ [(lo, lo)])

mutual
/-- Alternation from `i` up to `)` or the end: the tree, the position, and the
groups numbered so far. -/
def reAlt (rs : Array Nat) : Nat → Nat → Nat → Except ReErr (ReAst × Nat × Nat)
  | 0, _, _ => .error 2
  | fuel + 1, i, g =>
    match reCat rs fuel i g [] with
    | .error e => .error e
    | .ok (x, j, g) =>
      if rs[j]? == some 124 then
        match reAlt rs fuel (j + 1) g with
        | .ok (.alt ys, k, g) => .ok (.alt (x :: ys), k, g)
        | .ok (y, k, g) => .ok (.alt [x, y], k, g)
        | .error e => .error e
      else .ok (x, j, g)

/-- A concatenation from `i` up to `|`, `)` or the end. -/
def reCat (rs : Array Nat) : Nat → Nat → Nat → List ReAst → Except ReErr (ReAst × Nat × Nat)
  | 0, _, _, _ => .error 2
  | fuel + 1, i, g, acc =>
    match rs[i]? with
    | none | some 124 | some 41 => .ok (.cat acc, i, g)
    | some _ =>
      match reAtom rs fuel i g with
      | .error e => .error e
      | .ok (a, j, g) =>
        match reSuffix rs fuel a j with
        | .error e => .error e
        | .ok (b, k) => reCat rs fuel k g (acc ++ [b])

/-- The repetition operators after an atom (one; a second is an error, as
Go reports `**`). -/
def reSuffix (rs : Array Nat) : Nat → ReAst → Nat → Except ReErr (ReAst × Nat)
  | 0, _, _ => .error 2
  | _ + 1, a, i =>
    let lazy (j : Nat) : Bool × Nat := if rs[j]? == some 63 then (false, j + 1) else (true, j)
    let op : Option (ReAst × Nat) :=
      match rs[i]? with
      | some 42 => let (gr, j) := lazy (i + 1); some (.star gr a, j)
      | some 43 => let (gr, j) := lazy (i + 1); some (.plus gr a, j)
      | some 63 => let (gr, j) := lazy (i + 1); some (.quest gr a, j)
      | some 123 =>
        match reRepeatAt rs i with
        | some (n, m, j) => let (gr, k) := lazy j; some (.rep n m gr a, k)
        | none => none
      | _ => none
    -- Perl does not stack repetition operators: `a**`, `a{2}{3}` are errors.
    let stacked (j : Nat) : Bool :=
      [some 42, some 43, some 63].contains rs[j]? || (rs[j]? == some 123 && (reRepeatAt rs j).isSome)
    match op with
    | none => .ok (a, i)
    | some (b, j) =>
      match b with
      | .rep n m _ _ =>
        let tooBig : Bool := match m with | some m => m > 1000 || m < n | none => false
        if n > 1000 || tooBig then .error 1
        else if stacked j then .error 1 else .ok (b, j)
      | _ => if stacked j then .error 1 else .ok (b, j)

/-- One atom at `i`. -/
def reAtom (rs : Array Nat) : Nat → Nat → Nat → Except ReErr (ReAst × Nat × Nat)
  | 0, _, _ => .error 2
  | fuel + 1, i, g =>
    match rs[i]? with
    | none => .error 1
    | some 40 =>
      if rs[i + 1]? == some 63 then
        if rs[i + 2]? == some 58 then
          match reAlt rs fuel (i + 3) g with
          | .ok (x, j, g) => if rs[j]? == some 41 then .ok (x, j + 1, g) else .error 1
          | .error e => .error e
        else .error 2 -- flags, named groups
      else
        match reAlt rs fuel (i + 1) (g + 1) with
        | .ok (x, j, g') => if rs[j]? == some 41 then .ok (.cap (g + 1) x, j + 1, g') else .error 1
        | .error e => .error e
    | some 41 => .error 1
    | some 42 | some 43 | some 63 => .error 1 -- missing argument to repetition
    | some 46 => .ok (.any, i + 1, g)
    | some 94 => .ok (.empty 4, i + 1, g)
    | some 36 => .ok (.empty 8, i + 1, g)
    | some 91 =>
      let neg := rs[i + 1]? == some 94
      match reClass rs rs.size (if neg then i + 2 else i + 1) true [] with
      | .ok (cl, j) => .ok (.cls (if neg then reNegate cl else reNormRanges cl), j, g)
      | .error e => .error e
    | some 92 =>
      match rs[i + 1]? with
      | none => .error 1
      | some e =>
        if e == 98 then .ok (.empty 16, i + 2, g)
        else if e == 66 then .ok (.empty 32, i + 2, g)
        else if e == 65 then .ok (.empty 4, i + 2, g)
        else if e == 122 then .ok (.empty 8, i + 2, g)
        else match rePerlClass e with
          | some cl => .ok (.cls cl, i + 2, g)
          | none => match reEscRune e with
            | some r => .ok (.lit r, i + 2, g)
            | none => .error 2
    | some c => .ok (.lit c, i + 1, g)
end

/-- `x{n,m}` as copies, Go's simplification: n copies, then `m - n` nested
optional ones (`x*` after the copies for `{n,}`). -/
def reExpand (n : Nat) (m : Option Nat) (greedy : Bool) (x : ReAst) : ReAst :=
  let copies := List.replicate n x
  match m with
  | none => .cat (copies ++ [.star greedy x])
  | some m =>
    let rec opt : Nat → ReAst
      | 0 => .cat []
      | k + 1 => .quest greedy (.cat [x, opt k])
    .cat (copies ++ (if m > n then [opt (m - n)] else []))

/-- Whether `x` can match the empty string (Go's `frag.nullable`), within
`n` levels (`false` past them). -/
def reNullable : Nat → ReAst → Bool
  | 0, _ => false
  | n + 1, x =>
    match x with
    | .lit _ | .cls _ | .any => false
    | .empty _ | .star .. | .quest .. => true
    | .cat xs => xs.all (reNullable n)
    | .alt xs => xs.any (reNullable n)
    | .plus _ y | .cap _ y => reNullable n y
    | .rep k _ _ y => k == 0 || reNullable n y

/-- Compile `x` to continue at `next`, appending to `p`: the entry (#46).
Instructions go in as Go's do; a loop's `alt` is placed first and set once
its body's entry is known. -/
def reComp : Nat → ReAst → Nat → Array ReInst → Option (Nat × Array ReInst)
  | 0, _, _, _ => none
  | fuel + 1, x, next, p =>
    let push (i : ReInst) (p : Array ReInst) : Nat × Array ReInst := (p.size, p.push i)
    match x with
    | .lit r => some (push (.rune [(r, r)] next) p)
    | .cls rs => some (push (.rune rs next) p)
    | .any => some (push (.any false next) p)
    | .empty f => some (push (.empty f next) p)
    | .cat xs => xs.reverse.foldlM (fun (e, p) y => reComp fuel y e p) (next, p)
    | .alt [] => some (next, p)
    | .alt [y] => reComp fuel y next p
    | .alt (y :: ys) => do
      let (e1, p) ← reComp fuel y next p
      let (e2, p) ← reComp fuel (.alt ys) next p
      some (push (.alt e1 e2) p)
    | .quest gr y => do
      let (e, p) ← reComp fuel y next p
      some (push (if gr then .alt e next else .alt next e) p)
    | .star gr y =>
      -- As Go's compiler: a loop whose body can match empty is `(y+)?`, so
      -- that the body runs once (and sets its captures) on an empty match.
      if reNullable fuel y then reComp fuel (.quest gr (.plus gr y)) next p else do
      let (l, p) := push .fail p
      let (e, p) ← reComp fuel y l p
      some (l, p.set! l (if gr then .alt e next else .alt next e))
    | .plus gr y => do
      let (l, p) := push .fail p
      let (e, p) ← reComp fuel y l p
      some (e, p.set! l (if gr then .alt e next else .alt next e))
    | .cap k y => do
      let (c1, p) := push (.cap (2 * k + 1) next) p
      let (e, p) ← reComp fuel y c1 p
      some (push (.cap (2 * k) e) p)
    | .rep n m gr y => reComp fuel (reExpand n m gr y) next p

/-- The largest program a pattern built at run time may compile to. -/
def reMaxInsts : Nat := 4000

/-- Compile a pattern (its bytes) as `regexp.Compile` does (Perl syntax):
the program, its start and its capture count; error 1 when Go rejects it, 2
when the model does not take its syntax (flags, Unicode classes, `\p`,
`\x`, named groups, POSIX classes) or it is too large (#46). -/
def reCompile (pat : List Nat) : Except ReErr (List ReInst × Nat × Nat) :=
  let rec runes (fuel i : Nat) : List Nat :=
    match fuel with
    | 0 => []
    | fuel + 1 => if i < pat.length then let (r, w) := decodeRune pat i; r :: runes fuel (i + w) else []
  let rs := (runes (pat.length + 1) 0).toArray
  match reAlt rs (4 * rs.size + 8) 0 0 with
  | .error e => .error e
  | .ok (ast, j, groups) =>
    if j < rs.size then .error 1 -- an unmatched )
    else
      let p : Array ReInst := #[.fail, .match]
      match reComp (4 * rs.size + 4096) ast 1 p with
      | some (start, p) => if p.size > reMaxInsts then .error 2 else .ok (p.toList, start, 2 * (groups + 1))
      | none => .error 2

/-- All code points of a byte string. -/
def decodeAll (bs : List Nat) : List Nat := go bs.toArray 0 bs.length
where
  go (arr : Array Nat) (i fuel : Nat) : List Nat :=
    match fuel with
    | 0 => []
    | fuel + 1 =>
      if i < arr.size then
        let (r, n) := decodeRuneA arr i
        r :: go arr (i + n) fuel
      else []

/-- Go index semantics: a negative index is out of range, so compare unsigned
after sign-extending to 64 bits. -/
def asIndex : Val → Option (BitVec 64)
  | .int _ i => some (i.signExtend 64)
  | _ => none

/-- Go's `==` on two interface values, where the model can decide it: the
same dynamic type (tag) and equal dynamic values, a string read from the
heap, or both nil. `none` when either side is a value the model did not
build. -/
def ifaceEqVal (heap : Heap Val) : Val → Val → Option Bool
  | .boxed t x@(.str ..), .boxed t' y@(.str ..) =>
    if t != t' then some false else
    match strBytes heap x, strBytes heap y with
    | some p, some q => some (p == q)
    | _, _ => none
  | a@(.boxed ..), b@(.boxed ..) | a@.opaqueNil, b@(.boxed ..)
  | a@(.boxed ..), b@.opaqueNil | a@.opaqueNil, b@.opaqueNil => eqVal a b
  -- A nil error is nil whichever way it was made.
  | .err false, .err false | .err false, .opaqueNil | .opaqueNil, .err false => some true
  | .err false, .boxed .. | .boxed .., .err false => some false
  | _, _ => none

/-- The model's size bound on a slice's backing array: an allocation past
it ends the run out of fuel (no behaviour of the program). 16384 covers a
library's fixed buffers, bufio's 4096 bytes and a stripe lock table
(#71). -/
def sliceBound : Nat := 16384

/-- The bits of a float64 that `knownFloat`'s decimal bytes denote
(digits, an optional point and fraction, an optional sign), correctly
rounded. -/
def decimalBits (bs : List Val) : Option Nat := do
  let cs ← bs.mapM fun | .int 8 b => some b.toNat | _ => none
  let digit (c : Nat) := 48 ≤ c && c ≤ 57
  let (neg, rest) := match cs with
    | 45 :: r => (true, r)
    | r => (false, r)
  let ip := rest.takeWhile digit
  let fp ← match rest.drop ip.length with
    | [] => some []
    | 46 :: r => if r.all digit then some r else none
    | _ => none
  if ip.isEmpty then none
  let m := (ip ++ fp).foldl (fun acc c => acc * 10 + (c - 48)) 0
  let f := Float.ofScientific m true fp.length
  some (if neg then -f else f).toBits.toNat

/-- The IEEE bits of a `w`-bit float value the model knows. -/
def floatBits? (w : Nat) : Val → Option Nat
  | .int w' b => if w' == w then some b.toNat else none
  | .tuple [.arr bs] => if w == 64 then decimalBits bs else none
  | _ => none

def f64Of (n : Nat) : Float := Float.ofBits (UInt64.ofNat n)
def f32Of (n : Nat) : Float32 := Float32.ofBits (UInt32.ofNat n)
def f64Val (f : Float) : Val := .int 64 (BitVec.ofNat 64 f.toBits.toNat)
def f32Val (f : Float32) : Val := .int 32 (BitVec.ofNat 32 f.toBits.toNat)

/-- `fbin k w` on known bits. -/
def floatBin (k w x y : Nat) : Val :=
  if w == 32 then
    let a := f32Of x; let b := f32Of y
    f32Val (match k with | 0 => a + b | 1 => a - b | 2 => a * b | _ => a / b)
  else
    let a := f64Of x; let b := f64Of y
    f64Val (match k with | 0 => a + b | 1 => a - b | 2 => a * b | _ => a / b)

/-- `fcmp k w` on known bits: IEEE comparisons (a float32 compares as the
float64 it widens to, exactly). -/
def floatCmp (k w x y : Nat) : Bool :=
  let a := if w == 32 then (f32Of x).toFloat else f64Of x
  let b := if w == 32 then (f32Of y).toFloat else f64Of y
  match k with
  | 0 => a == b
  | 1 => !(a == b)
  | 2 => decide (a < b)
  | 3 => decide (a ≤ b)
  | 4 => decide (b < a)
  | _ => decide (b ≤ a)

/-- The truncation of a `w`-bit float, when it is a `dw`-bit integer. -/
def floatTrunc? (w dw : Nat) (signed : Bool) (x : Nat) : Option Int :=
  let f := if w == 32 then (f32Of x).toFloat else f64Of x
  -- toward zero, exact: |f| < 2^64 here, or the range check fails
  let t := if f < 0 then -((-f).floor) else f.floor
  let lo : Float := if signed then -(Float.ofNat (2 ^ (dw - 1))) else 0
  let hi : Float := Float.ofNat (if signed then 2 ^ (dw - 1) else 2 ^ dw)
  if lo ≤ t && t < hi then
    some (if signed then t.toInt64.toInt else t.toUInt64.toNat)
  else none

/-- Pure instructions: no heap access. -/
def denotePure (or : Oracle) (heap : Heap Val) : Op → List Val → Res Val
  | .const v, [] => .ok v
  | .funcRef f, bound => .ok (.boxed 0 (.tuple (.int 64 (BitVec.ofNat 64 f) :: bound)))
  | .xfuncRef .., _ => .ok (.boxed 0 .unit)
  | .box tag, [v] => .ok (.boxed tag v)
  | .isBoxed, [x] => .ok (.bool (match x with | .boxed .. => true | _ => false))
  | .unbox tag commaOk zero, [.boxed t v] =>
    if t == tag then .ok (if commaOk then .tuple [v, .bool true] else v)
    else if commaOk then .ok (.tuple [zero, .bool false])
    else .panic .typeAssertion
  | .unbox _ commaOk zero, [.opaqueNil] =>
    if commaOk then .ok (.tuple [zero, .bool false]) else .panic .typeAssertion
  | .assertIface tags commaOk, [x] =>
    let fail := if commaOk then .ok (.tuple [.opaqueNil, .bool false]) else .panic .typeAssertion
    match x with
    | .boxed t _ =>
      if tags.contains t then .ok (if commaOk then .tuple [x, .bool true] else x) else fail
    | .opaqueNil => fail
    | _ => .panic .modelStuck
  | .dispatch tags, x :: fs =>
    match x with
    | .boxed t v =>
      match (tags.zip fs).lookup t with
      | some (.boxed 0 (.tuple (f :: bound))) => .ok (.boxed 0 (.tuple (f :: bound ++ [v])))
      | _ => .ok .unit
    | .opaqueNil => .ok .opaqueNil
    | _ => .ok .unit
  | .reflectOf, [x] => .ok x
  | .reflectKind kinds, [x] =>
    match x with
    | .opaqueNil => .ok (.int 64 0#64)
    | .boxed t _ =>
      match kinds.lookup t with
      | some k => .ok (.int 64 (BitVec.ofNat 64 k))
      | none => .panic .modelStuck
    | _ => .panic .modelStuck
  | .reflectIsNil kinds, [x] =>
    match x with
    | .boxed t v =>
      match kinds.lookup t with
      | some k =>
        -- Chan, Func, Interface, Map, Pointer, Slice, UnsafePointer
        if [18, 19, 20, 21, 22, 23, 26].contains k then
          match v with
          | .ptr 0 _ | .slice 0 .. | .opaqueNil => .ok (.bool true)
          | .unit => .panic .modelStuck
          | _ => .ok (.bool false)
        else .panic .explicit
      | none => .panic .modelStuck
    | .opaqueNil => .panic .explicit -- IsNil on the zero Value
    | _ => .panic .modelStuck
  | .reflectIsZero, [.boxed _ v] =>
    match isZeroVal v with | some b => .ok (.bool b) | none => .panic .modelStuck
  | .nilKnown, [x] => .ok (.bool (match x with | .boxed .. | .opaqueNil | .ptr .. => true | _ => false))
  | .ite, [.bool c, a, b] => .ok (if c then a else b)
  | .reIn ids, [x] => .ok (.bool (match x with
    | .boxed _ (.int _ id) => ids.contains id.toNat
    -- A regexp built at run time holds its pattern (#46).
    | .boxed _ (.str r o l) => match strBytes heap (.str r o l) >>= fun b => (reCompile b).toOption with
      | some _ => true
      | none => false
    | _ => false))
  | .reCompileOk, [p] => .ok (.int 64 (BitVec.ofNat 64 (match strBytes heap p with
    | some b => match reCompile b with | .ok _ => 0 | .error e => e
    | none => 2)))
  | .errKnown, [x] => .ok (.bool (match x with | .boxed .. | .err false => true | _ => false))
  | .errTagKnown tag, [x] => .ok (.bool (match x with | .boxed t _ => t == tag | .err false => true | _ => false))
  | .isNilOpaque, [x] => .ok (.bool (match x with | .opaqueNil => true | _ => false))
  | .ifaceEqKnown, [a, b] => .ok (.bool (ifaceEqVal heap a b).isSome)
  | .ifaceEq, [a, b] => match ifaceEqVal heap a b with
    | some r => .ok (.bool r)
    | none => .panic .modelStuck
  | .ifaceComparable nc, [x] => .ok (.bool (match x with | .boxed t _ => !nc.contains t | _ => true))
  | .add, args => binInt (· + ·) args
  | .sub, args => binInt (· - ·) args
  | .mul, args => binInt (· * ·) args
  | .sdiv, args => divInt BitVec.sdiv args
  | .udiv, args => divInt BitVec.udiv args
  | .srem, args => divInt BitVec.srem args
  | .urem, args => divInt BitVec.umod args
  | .and, args => binInt (· &&& ·) args
  | .or, args => binInt (· ||| ·) args
  | .xor, args => binInt (· ^^^ ·) args
  | .andNot, args => binInt (fun a b => a &&& ~~~b) args
  -- Shift counts at or beyond the width give 0 (or the sign fill) without
  -- computing a huge shift: Go's semantics, and Lean would otherwise try to
  -- build 2^k.
  | .shl cs, [.int w a, n] =>
    match shiftCount cs n with
    | .ok k => .ok (.int w (if k ≥ w then 0#w else a <<< k))
    | .panic p => .panic p
    | .outOfFuel => .outOfFuel
  | .shr signed cs, [.int w a, n] =>
    match shiftCount cs n with
    | .ok k =>
      if k ≥ w then .ok (.int w (if signed && a.msb then -1#w else 0#w))
      else .ok (.int w (if signed then a.sshiftRight k else a >>> k))
    | .panic p => .panic p
    | .outOfFuel => .outOfFuel
  | .eq, [a@(.str ..), b@(.str ..)] =>
    match strBytes heap a, strBytes heap b with
    | some x, some y => .ok (.bool (x == y))
    | _, _ => .panic .modelStuck
  | .ne, [a@(.str ..), b@(.str ..)] =>
    match strBytes heap a, strBytes heap b with
    | some x, some y => .ok (.bool (x != y))
    | _, _ => .panic .modelStuck
  | .eq, [a, b] => match eqVal a b with | some r => .ok (.bool r) | none => .panic .modelStuck
  | .ne, [a, b] => match eqVal a b with | some r => .ok (.bool !r) | none => .panic .modelStuck
  | .slt, args => cmpInt BitVec.slt args
  | .sle, args => cmpInt BitVec.sle args
  | .sgt, args => cmpInt (fun a b => BitVec.slt b a) args
  | .sge, args => cmpInt (fun a b => BitVec.sle b a) args
  | .ult, [a@(.str ..), b@(.str ..)] => strCmp heap a b fun x y => bytesLt x y
  | .ule, [a@(.str ..), b@(.str ..)] => strCmp heap a b fun x y => !bytesLt y x
  | .ugt, [a@(.str ..), b@(.str ..)] => strCmp heap a b fun x y => bytesLt y x
  | .uge, [a@(.str ..), b@(.str ..)] => strCmp heap a b fun x y => !bytesLt x y
  | .ult, args => cmpInt BitVec.ult args
  | .ule, args => cmpInt BitVec.ule args
  | .ugt, args => cmpInt (fun a b => BitVec.ult b a) args
  | .uge, args => cmpInt (fun a b => BitVec.ule b a) args
  | .neg, [.int w a] => .ok (.int w (-a))
  | .bnot, [.int w a] => .ok (.int w (~~~a))
  | .not, [.bool b] => .ok (.bool !b)
  | .conv w' se, [.int _ a] =>
    .ok (.int w' (if se then a.signExtend w' else a.zeroExtend w'))
  | .fknown w, args => .ok (.bool (args.all fun v => (floatBits? w v).isSome))
  | .fbin k w, [a, b] => match floatBits? w a, floatBits? w b with
    | some x, some y => .ok (floatBin k w x y)
    | _, _ => .panic .modelStuck
  | .fcmp k w, [a, b] => match floatBits? w a, floatBits? w b with
    | some x, some y => .ok (.bool (floatCmp k w x y))
    | _, _ => .panic .modelStuck
  | .fneg w, [a] => match floatBits? w a with
    | some x => .ok (.int w (BitVec.ofNat w x ^^^ (1#w <<< (w - 1))))
    | none => .panic .modelStuck
  | .itof w signed, [.int _ a] =>
    .ok (if w == 32 then f32Val (if signed then Float32.ofInt a.toInt else Float32.ofNat a.toNat)
      else f64Val (if signed then Float.ofInt a.toInt else Float.ofNat a.toNat))
  | .ftoiOk w dw signed, [a] =>
    .ok (.bool (match floatBits? w a with
      | some x => (floatTrunc? w dw signed x).isSome
      | none => false))
  | .ftoi w dw signed, [a] => match floatBits? w a >>= floatTrunc? w dw signed with
    | some i => .ok (.int dw (BitVec.ofInt dw i))
    | none => .panic .modelStuck
  | .fsqrt w, [a] => match floatBits? w a with
    | some x => .ok (if w == 32 then f32Val (f32Of x).sqrt else f64Val (f64Of x).sqrt)
    | none => .panic .modelStuck
  | .fconv w dw, [a] => match floatBits? w a with
    | some x =>
      if w == dw then .ok (.int w (BitVec.ofNat w x))
      else if dw == 32 then .ok (f32Val (f64Of x).toFloat32)
      else .ok (f64Val (f32Of x).toFloat)
    | none => .panic .modelStuck
  | .len, [.slice _ _ len _] => .ok (.int 64 len)
  | .cap, [.slice _ _ _ cap] => .ok (.int 64 cap)
  | .indexCheck, [.slice _ _ len _, i] =>
    match asIndex i with
    | some i => if i.ult len then .ok .unit else .panic .indexOutOfRange
    | none => .panic .modelStuck
  | .load w, [.slice ref off len _, i] =>
    match asIndex i with
    | some i =>
      if i.ult len then
        -- A backing array on the heap (materialised input or allocation)
        -- is authoritative; anything else reads the oracle.
        match heap[ref]? with
        | some (.arr es) =>
          match es[(off + i).toNat]? with
          | some (.int _ v) => .ok (.int w (v.zeroExtend w))
          | some v => .ok v
          | none => .panic .modelStuck
        | some (.arrA es) =>
          match es[(off + i).toNat]? with
          | some (.int _ v) => .ok (.int w (v.zeroExtend w))
          | some v => .ok v
          | none => .panic .modelStuck
        | _ => .ok (.int w ((or.mem ref (off + i)).zeroExtend w))
      else .panic .indexOutOfRange
    | none => .panic .modelStuck
  | .field i, [.structv fs] =>
    match fs[i]? with
    | some v => .ok v
    | none => .panic .modelStuck
  | .mkStruct, fs => .ok (.structv fs)
  | .mkArr, es => .ok (.arr es)
  | .fieldAddr i, [.ptr 0 _] => .ok (.ptr 0 [i]) -- &nil.f stays nil-based; deref panics
  | .fieldAddr i, [.ptr ref path] => .ok (.ptr ref (path ++ [i]))
  | .indexAddr, [.slice ref off len _, i] =>
    match asIndex i with
    | some i => if i.ult len then .ok (.ptr ref [(off + i).toNat]) else .panic .indexOutOfRange
    | none => .panic .modelStuck
  | .indexAddr, [.ptr 0 _, _] => .panic .nilDereference
  | .indexAddr, [.ptr ref path, i] =>
    -- `&a[i]` for a pointer to an array: bounds come from the array object.
    match asIndex i, heap[ref]? >>= (getPath · path) with
    | some i, some (.arr es) =>
      if i.toNat < es.length then .ok (.ptr ref (path ++ [i.toNat])) else .panic .indexOutOfRange
    | some i, some (.arrA es) =>
      if i.toNat < es.size then .ok (.ptr ref (path ++ [i.toNat])) else .panic .indexOutOfRange
    | _, _ => .panic .modelStuck
  | .arrIndex, [.arr es, i] =>
    match asIndex i with
    | some i =>
      match es[i.toNat]? with
      | some v => .ok v
      | none => .panic .indexOutOfRange
    | none => .panic .modelStuck
  | .arrIndex, [.arrA es, i] =>
    match asIndex i with
    | some i =>
      match es[i.toNat]? with
      | some v => .ok v
      | none => .panic .indexOutOfRange
    | none => .panic .modelStuck
  | .reslice, [.ptr 0 _, _, _] => .panic .nilDereference
  | .reslice, [.ptr ref path, lo, hi] =>
    -- `a[lo:hi]` for a pointer to an array: the slice aliases the array.
    match asIndex lo, asIndex hi, heap[ref]? >>= (getPath · path) with
    | some lo, some hi, some (.arr es) =>
      if path.isEmpty && lo.ule hi && hi.toNat ≤ es.length then
        .ok (.slice ref lo (hi - lo) (BitVec.ofNat 64 es.length - lo))
      else if !path.isEmpty then .panic .modelStuck
      else .panic .sliceBounds
    | some lo, some hi, some (.arrA es) =>
      if path.isEmpty && lo.ule hi && hi.toNat ≤ es.size then
        .ok (.slice ref lo (hi - lo) (BitVec.ofNat 64 es.size - lo))
      else if !path.isEmpty then .panic .modelStuck
      else .panic .sliceBounds
    | _, _, _ => .panic .modelStuck
  | .strLen, [.ptr ref path] =>
    -- `len(a)` for a pointer to an array, reached through the generic path.
    match heap[ref]? >>= (getPath · path) with
    | some (.arr es) => .ok (.int 64 (BitVec.ofNat 64 es.length))
    | some (.arrA es) => .ok (.int 64 (BitVec.ofNat 64 es.size))
    | _ => .panic .modelStuck
  | .isNil, [.ptr r _] => .ok (.bool (r == 0))
  | .isNil, [.err nonNil] => .ok (.bool !nonNil)
  | .isNil, [.boxed ..] => .ok (.bool false)
  | .strLen, [.str _ _ len] => .ok (.int 64 len)
  | .strIndex, [s@(.str _ _ len), i] =>
    match asIndex i, strBytes heap s with
    | some i, some bs =>
      if i.ult len then
        match bs[i.toNat]? with
        | some b => .ok (.int 8 (BitVec.ofNat 8 b))
        | none => .panic .modelStuck
      else .panic .indexOutOfRange
    | _, _ => .panic .modelStuck
  | .strSlice, [.str ref off len, lo, hi] =>
    match asIndex lo, asIndex hi with
    | some lo, some hi =>
      if lo.ule hi && hi.ule len then .ok (.str ref (off + lo) (hi - lo)) else .panic .sliceBounds
    | _, _ => .panic .modelStuck
  | .strHasPrefix, [s, p] =>
    match strBytes heap s, strBytes heap p with
    | some x, some y => .ok (.bool (x.take y.length == y))
    | _, _ => .panic .modelStuck
  | .strHasSuffix, [s, p] =>
    match strBytes heap s, strBytes heap p with
    | some x, some y => .ok (.bool (x.drop (x.length - y.length) == y && y.length ≤ x.length))
    | _, _ => .panic .modelStuck
  | .strContains, [s, p] =>
    match strBytes heap s, strBytes heap p with
    | some x, some y => .ok (.bool (listIndexOf x y).isSome)
    | _, _ => .panic .modelStuck
  | .strIndexOf, [s, p] =>
    match strBytes heap s, strBytes heap p with
    | some x, some y => .ok (.int 64 (match listIndexOf x y with | some i => BitVec.ofNat 64 i | none => -1#64))
    | _, _ => .panic .modelStuck
  | .strTrimPrefix, [s@(.str ref off len), p] =>
    match strBytes heap s, strBytes heap p with
    | some x, some y =>
      if x.take y.length == y && y.length ≤ x.length then
        .ok (.str ref (off + BitVec.ofNat 64 y.length) (len - BitVec.ofNat 64 y.length))
      else .ok s
    | _, _ => .panic .modelStuck
  | .strTrimSuffix, [s@(.str ref off len), p] =>
    match strBytes heap s, strBytes heap p with
    | some x, some y =>
      if y.length ≤ x.length && x.drop (x.length - y.length) == y then
        .ok (.str ref off (len - BitVec.ofNat 64 y.length))
      else .ok s
    | _, _ => .panic .modelStuck
  | .strTrimSpace, [s@(.str ref off len)] =>
    match strBytes heap s with
    | some x =>
      let lead := (x.takeWhile isSpaceByte).length
      let trail := (x.reverse.takeWhile isSpaceByte).length
      if lead + trail ≥ x.length then .ok (.str 0 0#64 0#64)
      else .ok (.str ref (off + BitVec.ofNat 64 lead) (len - BitVec.ofNat 64 (lead + trail)))
    | none => .panic .modelStuck
  | .strAtoi, [s] =>
    match strBytes heap s with
    | some x =>
      match atoiBytes x with
      | some v => .ok (.tuple [.int 64 (BitVec.ofInt 64 v), .err false])
      | none => .ok (.tuple [.int 64 (BitVec.ofInt 64 ((atoiClamped x).getD 0)), .err true])
    | none => .panic .modelStuck
  | .strIndexByte, [s, .int _ b] =>
    match strBytes heap s with
    | some x => .ok (.int 64 (match listIndexOf x [b.toNat % 256] with | some i => BitVec.ofNat 64 i | none => -1#64))
    | none => .panic .modelStuck
  | .strCut, [s, p] =>
    match s, strBytes heap s, strBytes heap p with
    | .str ref off len, some x, some y =>
      match listIndexOf x y with
      | some i =>
        let i' := BitVec.ofNat 64 i
        let skip := BitVec.ofNat 64 (i + y.length)
        .ok (.tuple [.str ref off i', .str ref (off + skip) (len - skip), .bool true])
      | none => .ok (.tuple [s, .str 0 0#64 0#64, .bool false])
    | _, _, _ => .panic .modelStuck
  | .strLastIndexByte, [s, .int _ b] =>
    match strBytes heap s with
    | some x => .ok (.int 64 (match (allIndexOf x [b.toNat % 256]).getLast? with | some i => BitVec.ofNat 64 i | none => -1#64))
    | none => .panic .modelStuck
  | .strLastIndex, [s, p] =>
    match strBytes heap s, strBytes heap p with
    | some x, some y => .ok (.int 64 (match (allIndexOf x y).getLast? with | some i => BitVec.ofNat 64 i | none => -1#64))
    | _, _ => .panic .modelStuck
  | .strCount, [s, p] =>
    match strBytes heap s, strBytes heap p with
    | some x, some y =>
      if y.isEmpty then .ok (.int 64 (BitVec.ofNat 64 (x.length + 1)))
      else .ok (.int 64 (BitVec.ofNat 64 (splitBytes x y).length.pred))
    | _, _ => .panic .modelStuck
  | .strCountByte, [s, .int _ c] =>
    match strBytes heap s with
    | some x => .ok (.int 64 (BitVec.ofNat 64 (x.filter (· == c.toNat)).length))
    | none => .panic .modelStuck
  | .strEqualFold, [s, p] =>
    match strBytes heap s, strBytes heap p with
    | some x, some y => .ok (.bool (x.map lowerByte == y.map lowerByte))
    | _, _ => .panic .modelStuck
  | .strTrimLeft, [s@(.str ref off len), c] =>
    match strBytes heap s, strBytes heap c with
    | some x, some cut =>
      let n := (x.takeWhile (cut.contains ·)).length
      .ok (.str ref (off + BitVec.ofNat 64 n) (len - BitVec.ofNat 64 n))
    | _, _ => .panic .modelStuck
  | .strTrimRight, [s@(.str ref off len), c] =>
    match strBytes heap s, strBytes heap c with
    | some x, some cut =>
      let n := (x.reverse.takeWhile (cut.contains ·)).length
      .ok (.str ref off (len - BitVec.ofNat 64 n))
    | _, _ => .panic .modelStuck
  | .strTrim, [s@(.str ref off len), c] =>
    match strBytes heap s, strBytes heap c with
    | some x, some cut =>
      let l := (x.takeWhile (cut.contains ·)).length
      let r := (x.reverse.takeWhile (cut.contains ·)).length
      if l + r ≥ x.length then .ok (.str 0 0#64 0#64)
      else .ok (.str ref (off + BitVec.ofNat 64 l) (len - BitVec.ofNat 64 (l + r)))
    | _, _ => .panic .modelStuck
  | .mkTuple, vs => .ok (.tuple vs)
  | .extract i, [.tuple vs] =>
    match vs[i]? with
    | some v => .ok v
    | none => .panic .modelStuck
  | .reslice3, [.slice ref off _ cap, lo, hi, mx] =>
    match asIndex lo, asIndex hi, asIndex mx with
    | some lo, some hi, some mx =>
      if lo.ule hi && hi.ule mx && mx.ule cap then .ok (.slice ref (off + lo) (hi - lo) (mx - lo))
      else .panic .sliceBounds
    | _, _, _ => .panic .modelStuck
  | .reslice3, [.ptr 0 _, _, _, _] => .panic .nilDereference
  | .reslice3, [.ptr ref path, lo, hi, mx] =>
    match asIndex lo, asIndex hi, asIndex mx, heap[ref]? >>= (getPath · path) with
    | some lo, some hi, some mx, some (.arr es) =>
      if path.isEmpty && lo.ule hi && hi.ule mx && mx.toNat ≤ es.length then
        .ok (.slice ref lo (hi - lo) (mx - lo))
      else .panic (if path.isEmpty then .sliceBounds else .modelStuck)
    | some lo, some hi, some mx, some (.arrA es) =>
      if path.isEmpty && lo.ule hi && hi.ule mx && mx.toNat ≤ es.size then
        .ok (.slice ref lo (hi - lo) (mx - lo))
      else .panic (if path.isEmpty then .sliceBounds else .modelStuck)
    | _, _, _, _ => .panic .modelStuck
  | .reslice, [.slice ref off _ cap, lo, hi] =>
    match asIndex lo, asIndex hi with
    | some lo, some hi =>
      if lo.ule hi && hi.ule cap then .ok (.slice ref (off + lo) (hi - lo) (cap - lo))
      else .panic .sliceBounds
    | _, _ => .panic .modelStuck
  | _, _ => .panic .modelStuck

/-- Allocate each string, then a slice holding them. -/
def allocStrSlice (heap : Heap Val) (parts : List (List Nat)) : Val × Heap Val :=
  let (vals, heap) := parts.foldl (fun (acc, heap) bs =>
    let (v, heap) := strAlloc heap bs
    (acc ++ [v], heap)) ([], heap)
  let (p, heap) := heapAlloc heap (.arr vals)
  match p with
  | .ptr ref _ => (.slice ref 0#64 (BitVec.ofNat 64 vals.length) (BitVec.ofNat 64 vals.length), heap)
  | _ => (.unit, heap)

-- Regular expressions ------------------------------------------------------

def isWordByte (b : Nat) : Bool :=
  (48 ≤ b && b ≤ 57) || (65 ≤ b && b ≤ 90) || (97 ≤ b && b ≤ 122) || b == 95

/-- Does an empty-width assertion hold at byte `pos`? -/
def reEmptyOk (f : Nat) (bs : Array Nat) (pos : Nat) : Bool :=
  let before := if pos == 0 then none else bs[pos - 1]?
  let after := bs[pos]?
  let wb := (before.map isWordByte).getD false != (after.map isWordByte).getD false
  ((f &&& 1) == 0 || pos == 0 || before == some 10) &&
  ((f &&& 2) == 0 || pos == bs.size || after == some 10) &&
  ((f &&& 4) == 0 || pos == 0) &&
  ((f &&& 8) == 0 || pos == bs.size) &&
  ((f &&& 16) == 0 || wb) &&
  ((f &&& 32) == 0 || !wb)

/-- Go's backtracking matcher (regexp/backtrack.go) from instruction `pc`
at byte `pos`: the first match in priority order (leftmost-first), with
the capture slots it set. A (pc, pos) pair is tried once: `seen` is shared
by all start positions, so the work stays linear. `none`: out of fuel. -/
def reBT (prog : Array ReInst) (bs : Array Nat) : Nat → Nat → Nat → Array (Option Nat) → Array Bool →
    Option (Option (Array (Option Nat)) × Array Bool)
  | 0, _, _, _, _ => none
  | fuel + 1, pc, pos, caps, seen =>
    let key := pc * (bs.size + 1) + pos
    if seen[key]?.getD true then some (none, seen) else
    let seen := seen.set! key true
    match prog[pc]? with
    | none | some .fail => some (none, seen)
    | some .match => some (some (if 1 < caps.size then caps.set! 1 (some pos) else caps), seen)
    | some (.nop o) => reBT prog bs fuel o pos caps seen
    | some (.cap slot o) =>
      reBT prog bs fuel o pos (if slot < caps.size then caps.set! slot (some pos) else caps) seen
    | some (.empty f o) =>
      if reEmptyOk f bs pos then reBT prog bs fuel o pos caps seen else some (none, seen)
    | some (.alt x y) =>
      match reBT prog bs fuel x pos caps seen with
      | none => none
      | some (some r, seen) => some (some r, seen)
      | some (none, seen) => reBT prog bs fuel y pos caps seen
    | some (.rune rs o) =>
      if pos < bs.size then
        let (r, n) := decodeRuneA bs pos
        if rs.any (fun (lo, hi) => lo ≤ r && r ≤ hi) then reBT prog bs fuel o (pos + n) caps seen
        else some (none, seen)
      else some (none, seen)
    | some (.any nl o) =>
      if pos < bs.size then
        let (r, n) := decodeRuneA bs pos
        if nl || r != 10 then reBT prog bs fuel o (pos + n) caps seen else some (none, seen)
      else some (none, seen)

/-- Unanchored search, as Go's regexp: start positions left to right (by
rune), the first that matches. The capture slots of the match (group 0 is
the whole match), `some none` without a match, `none` out of fuel. -/
def reSearch (prog : Array ReInst) (start ncap : Nat) (bs : Array Nat) (from_ : Nat := 0) : Option (Option (Array (Option Nat))) :=
  let fuel := prog.size * (bs.size + 1) + 2
  let rec go : Nat → Nat → Array Bool → Option (Option (Array (Option Nat)))
    | 0, _, _ => some none
    | k + 1, pos, seen =>
      -- Group 0 has no capture instructions: its start is the start
      -- position, its end where the match instruction is reached.
      let caps := (Array.replicate (max ncap 2) none).set! 0 (some pos)
      match reBT prog bs fuel start pos caps seen with
      | none => none
      | some (some caps, _) => some (some caps)
      | some (none, seen) =>
        if pos ≥ bs.size then some none
        else go k (pos + (decodeRuneA bs pos).2) seen
  go (bs.size + 2) from_ (Array.replicate (prog.size * (bs.size + 1)) false)

/-- Go's `allMatches`: successive non-overlapping matches left to right,
at most `n` (all when `n` is negative); an empty match right where the
previous match ended is skipped, and after an empty match the search
moves on one rune. Each match is its capture slots; `none`: out of fuel. -/
def reAll (prog : Array ReInst) (start ncap : Nat) (bs : Array Nat) (n : Int) : Option (List (Array (Option Nat))) :=
  let rec go : Nat → Nat → Option Nat → Nat → Option (List (Array (Option Nat)))
    | 0, _, _, _ => some []
    | fuel + 1, pos, prevEnd, count =>
      if (n ≥ 0 && count ≥ n.toNat) || pos > bs.size then some [] else
      match reSearch prog start ncap bs pos with
      | none => none
      | some none => some []
      | some (some caps) =>
        match caps[0]?, caps[1]? with
        | some (some a), some (some b) =>
          let empty := b == pos
          let accept := !(empty && prevEnd == some a)
          let next := if empty then (if pos < bs.size then pos + (decodeRuneA bs pos).2 else pos + 1) else b
          match go fuel next (some b) (if accept then count + 1 else count) with
          | none => none
          | some rest => some (if accept then caps :: rest else rest)
        | _, _ => none
  go (bs.size + 2) 0 none 0

/-- `bytes` as a string, invalid UTF-8 sequences replaced by U+FFFD. -/
def utf8Lossy (bytes : List Nat) : String := Id.run do
  let arr := bytes.toArray
  let mut out := ""
  let mut i := 0
  for _ in [0:arr.size] do
    if i ≥ arr.size then break
    let (r, w) := decodeRuneA arr i
    out := out.push (Char.ofNat r)
    i := i + w
  return out

/-- Does a JSON document hold the token `-0` (a negative zero, or a number
starting `-0.`)? Lean's parser reads `-0` as 0, so zeros are then unknown. -/
def hasNegZero (bs : List Nat) : Bool :=
  (bs.zip (bs.drop 1)).any fun (a, b) => a == 45 && b == 48

/-- A float decoded from JSON whose `json.Marshal` output the model knows:
`.tuple [.arr bytes]` (the bytes Go writes), else `.unit` (unknown). Go
writes a float64 with 1e-6 <= |v| < 1e21 in the shortest decimal that reads
back as the same float; for a number written with at most 15 significant
digits that is the number itself (any two such decimals are distinct
floats), without trailing zeros. -/
def knownFloat (n : Lean.JsonNumber) (negZero : Bool) : Val :=
  let digitsOf (k : Nat) : List Nat := (toString k).toUTF8.toList.map (·.toNat)
  let rec strip : Nat → Nat → Nat → Nat × Nat
    | 0, m, e => (m, e)
    | fuel + 1, m, e => if e > 0 && m % 10 == 0 then strip fuel (m / 10) (e - 1) else (m, e)
  let rec sig : Nat → Nat → Nat
    | 0, m => m
    | fuel + 1, m => if m > 0 && m % 10 == 0 then sig fuel (m / 10) else m
  let a := n.mantissa.natAbs
  if a == 0 then
    if negZero then .unit else .tuple [.arr [.int 8 48#8]]
  else
    let (m, e) := strip 400 a n.exponent
    if (digitsOf (sig 400 m)).length > 15 || m ≥ 10 ^ (21 + e) || m * 10 ^ 6 < 10 ^ e then .unit
    else
      let ds := digitsOf m
      let ds := List.replicate (e + 1 - min ds.length (e + 1)) 48 ++ ds
      let body := if e == 0 then ds else ds.take (ds.length - e) ++ [46] ++ ds.drop (ds.length - e)
      let out := (if n.mantissa < 0 then [45] else []) ++ body
      .tuple [.arr (out.map fun b => .int 8 (BitVec.ofNat 8 b))]

/-- A decoded JSON value as the program would hold it in an interface:
strings, bools, numbers (opaque), `map[string]interface{}` and
`[]interface{}` boxed with their `tags`, null as the nil interface. -/
partial def jsonToVal (tags : List Nat) (heap : Heap Val) (negZero : Bool := true) : Lean.Json → Val × Heap Val
  | .null => (.opaqueNil, heap)
  | .bool b => (.boxed (tags.getD 1 0) (.bool b), heap)
  | .num n => (.boxed (tags.getD 2 0) (knownFloat n negZero), heap)
  | .str s =>
    let (v, heap) := strAlloc heap (s.toUTF8.toList.map (·.toNat))
    (.boxed (tags.getD 0 0) v, heap)
  | .arr es =>
    let (vs, heap) := es.toList.foldl (fun (acc, heap) e =>
      let (v, heap) := jsonToVal tags heap negZero e
      (acc ++ [v], heap)) ([], heap)
    let (p, heap) := heapAlloc heap (.arr vs)
    let n := BitVec.ofNat 64 vs.length
    match p with
    | .ptr ref _ => (.boxed (tags.getD 4 0) (.slice ref 0#64 n n), heap)
    | _ => (.unit, heap)
  | .obj kvs =>
    let (es, heap) := kvs.toList.foldl (fun (acc, heap) (k, e) =>
      let (kv, heap) := strAlloc heap (k.toUTF8.toList.map (·.toNat))
      let (v, heap) := jsonToVal tags heap negZero e
      (acc ++ [(kv, v)], heap)) ([], heap)
    let (p, heap) := heapAlloc heap (mapObj es)
    (.boxed (tags.getD 3 0) (.boxed 1 p), heap)

/-- `json.Unmarshal` of `bytes` into the `kind` value `p` points at (see
`Op.jsonUnmarshal`): Go's rules for these targets. Invalid JSON is an
error and leaves the target alone; null leaves a string or bool alone and
sets an interface, map or slice to nil; a value of the wrong JSON type is
an error; an object is merged into a non-nil map. -/
def jsonUnmarshalOp (heap : Heap Val) (kind : Nat) (tags : List Nat) (bytes : List Nat) (p : Val) :
    Res (Val × Heap Val) :=
  let store (heap : Heap Val) (v : Val) : Res (Val × Heap Val) :=
    match heapStore heap p v with
    | .ok heap => .ok (.err false, heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  let typeErr : Res (Val × Heap Val) := .ok (.err true, heap)
  -- Invalid UTF-8 becomes U+FFFD, as Go's decoder does inside strings
  -- (outside them both parsers reject the document).
  let text := utf8Lossy bytes
  match Lean.Json.parse text with
  | .error _ => .ok (.err true, heap)
  | .ok j =>
    match kind, j with
    | 0, .null | 1, .null => .ok (.err false, heap)
    | 0, .str s => let (v, heap) := strAlloc heap (s.toUTF8.toList.map (·.toNat)); store heap v
    | 0, _ => typeErr
    | 1, .bool b => store heap (.bool b)
    | 1, _ => typeErr
    | 2, j => let (v, heap) := jsonToVal tags heap (hasNegZero bytes) j; store heap v
    | 3, .null => store heap .opaqueNil
    | 3, .obj _ =>
      match jsonToVal tags heap (hasNegZero bytes) j, heapLoad heap p with
      | (.boxed _ fresh, heap), .ok .opaqueNil => store heap fresh
      | (.boxed _ fresh, heap), .ok old =>
        -- Merge into the map the target already holds.
        match mapEntries heap old, mapEntries heap fresh with
        | some (q, es), some (_, new) =>
          let es := new.foldl (fun es (k, v) =>
            if es.any (fun (k', _) => keyEq heap k k') then
              es.map fun (k', v') => if keyEq heap k k' then (k', v) else (k', v')
            else es ++ [(k, v)]) es
          match heapStore heap q (mapObj es) with
          | .ok heap => .ok (.err false, heap)
          | .panic k => .panic k
          | .outOfFuel => .outOfFuel
        | _, _ => .panic .modelStuck
      | _, _ => .panic .modelStuck
    | 3, _ => typeErr
    | 4, .null => store heap (.slice 0 0#64 0#64 0#64)
    | 4, .arr _ =>
      match jsonToVal tags heap (hasNegZero bytes) j with
      | (.boxed _ sl, heap) => store heap sl
      | _ => .panic .modelStuck
    | 4, _ => typeErr
    | _, _ => .panic .modelStuck

-- encoding/json over the type descriptions -------------------------------

def asciiBytes (s : String) : List Nat := s.toUTF8.toList.map (·.toNat)

/-- The value of a list of decimal digits. -/
def digitsVal (ds : List Nat) : Int := ds.foldl (fun a d => a * 10 + (d - 48 : Nat)) 0

/-- `strconv.ParseFloat(s, 64)` on what the model decides: `some (value,
err)` with the value as `knownFloat` gives it; `none` for infinities, NaN,
hex floats and magnitudes near the float64 limit (left to havoc). Decimal
syntax: a sign, digits with an optional point (at least one digit), an
optional exponent; anything else is a syntax error (zero, err). Values
from 1e309 up overflow (±Inf, err); smaller ones underflow without error. -/
def parseFloatGo (s : List Nat) : Option (Val × Bool) :=
  let isDigit (b : Nat) := 48 ≤ b && b ≤ 57
  let lower (b : Nat) := if 65 ≤ b && b ≤ 90 then b + 32 else b
  let (neg, rest) := match s with
    | 45 :: r => (true, r)
    | 43 :: r => (false, r)
    | r => (false, r)
  let low := rest.map lower
  if low == asciiBytes "inf" || low == asciiBytes "infinity" || low == asciiBytes "nan" then none
  else if low.take 2 == asciiBytes "0x" then none
  else
    let ip := rest.takeWhile isDigit
    let r1 := rest.drop ip.length
    let (fp, r2) := match r1 with
      | 46 :: r => (r.takeWhile isDigit, r.drop (r.takeWhile isDigit).length)
      | r => ([], r)
    let (ex, r3, ok) := match r2 with
      | e :: r =>
        if e == 101 || e == 69 then
          let (es, r') := match r with
            | 45 :: r' => ([45], r')
            | 43 :: r' => ([43], r')
            | r' => ([], r')
          let ds := r'.takeWhile isDigit
          (es ++ ds, r'.drop ds.length, !ds.isEmpty)
        else ([], r2, true)
      | [] => ([], [], true)
    let zero : Val := .tuple [.arr [.int 8 48#8]]
    if ip.isEmpty && fp.isEmpty || !r3.isEmpty || !ok then some (zero, true)
    else
      let digits := ip ++ fp
      match digits.findIdx? (· != 48) with
      | none => some (if neg then .unit else zero, false)
      | some first =>
        let expVal : Int := match ex with
          | 45 :: ds => -(digitsVal ds)
          | 43 :: ds => digitsVal ds
          | ds => digitsVal ds
        let p := expVal + ip.length - first
        if p > 309 then some (.unit, true)
        else if p ≥ 308 then none
        else
          let ipS := match ip.dropWhile (· == 48) with | [] => [48] | x => x
          let norm := (if neg then [45] else []) ++ ipS ++ (if fp.isEmpty then [] else 46 :: fp) ++
            (if ex.isEmpty then [] else 101 :: ex)
          match Lean.Json.parse (utf8Lossy norm) with
          | .ok (.num n) => some (knownFloat n false, false)
          | _ => some (.unit, false)


/-- Go's string encoding: quotes, backslash and control characters
escaped, `<`, `>`, `&`, U+2028 and U+2029 as `\u` escapes (HTML-safe, the
default), invalid UTF-8 as `\ufffd`. -/
def goJsonQuote (bs : List Nat) : List Nat := Id.run do
  let hex (d : Nat) : Nat := if d < 10 then 48 + d else 87 + d
  let u4 (c : Nat) : Array Nat := #[92, 117, hex (c / 4096 % 16), hex (c / 256 % 16), hex (c / 16 % 16), hex (c % 16)]
  let arr := bs.toArray
  let mut out : Array Nat := #[34]
  let mut i := 0
  for _ in [0:arr.size] do
    if i ≥ arr.size then break
    let b := arr[i]!
    if b < 0x80 then
      if b == 34 then out := out ++ #[92, 34]
      else if b == 92 then out := out ++ #[92, 92]
      else if b == 10 then out := out ++ #[92, 110]
      else if b == 13 then out := out ++ #[92, 114]
      else if b == 9 then out := out ++ #[92, 116]
      else if b == 8 then out := out ++ #[92, 98]
      else if b == 12 then out := out ++ #[92, 102]
      else if b < 32 || b == 60 || b == 62 || b == 38 then out := out ++ u4 b
      else out := out.push b
      i := i + 1
    else
      let (r, w) := decodeRuneA arr i
      if r == 0xFFFD && w == 1 then out := out ++ u4 0xFFFD
      else if r == 0x2028 || r == 0x2029 then out := out ++ u4 r
      else out := out ++ arr.extract i (i + w)
      i := i + w
  return (out.push 34).toList

def bvDec (signed : Bool) (v : BitVec w) : List Nat :=
  asciiBytes (if signed then toString v.toInt else toString v.toNat)

def b64Alphabet : Array Nat :=
  (asciiBytes "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/").toArray

/-- Standard base64 with padding (`[]byte` in JSON). -/
def base64Enc (bs : List Nat) : List Nat :=
  let rec go : List Nat → List Nat
    | a :: b :: c :: rest =>
      let n := a * 65536 + b * 256 + c
      [b64Alphabet[n / 262144 % 64]!, b64Alphabet[n / 4096 % 64]!, b64Alphabet[n / 64 % 64]!, b64Alphabet[n % 64]!] ++ go rest
    | [a, b] =>
      let n := a * 65536 + b * 256
      [b64Alphabet[n / 262144 % 64]!, b64Alphabet[n / 4096 % 64]!, b64Alphabet[n / 64 % 64]!, 61]
    | [a] =>
      let n := a * 65536
      [b64Alphabet[n / 262144 % 64]!, b64Alphabet[n / 4096 % 64]!, 61, 61]
    | [] => []
  go bs

/-- Strict standard base64 decoding; none on invalid input. -/
def base64Dec (bs : List Nat) : Option (List Nat) :=
  let val (c : Nat) : Option Nat := (b64Alphabet.toList.idxOf? c)
  if bs.length % 4 != 0 then none else
  let rec go : List Nat → Option (List Nat)
    | [a, b, 61, 61] => do
      let n := (← val a) * 262144 + (← val b) * 4096
      return [n / 65536 % 256]
    | [a, b, c, 61] => do
      let n := (← val a) * 262144 + (← val b) * 4096 + (← val c) * 64
      return [n / 65536 % 256, n / 256 % 256]
    | a :: b :: c :: d :: rest => do
      let n := (← val a) * 262144 + (← val b) * 4096 + (← val c) * 64 + (← val d)
      return [n / 65536 % 256, n / 256 % 256, n % 256] ++ (← go rest)
    | [] => some []
    | _ => none
  go bs

/-- The four encodings of package base64, by `k`: bit 0 the URL alphabet
(`-_` for `+/`), bit 1 no padding (`RawStdEncoding`, `RawURLEncoding`). -/
def base64EncK (k : Nat) (bs : List Nat) : List Nat :=
  let out := base64Enc bs
  let out := if k % 2 == 1 then out.map fun c => if c == 43 then 45 else if c == 47 then 95 else c else out
  if k / 2 % 2 == 1 then out.filter (· != 61) else out

/-- `DecodeString` of encoding `k` (see `base64EncK`); CR and LF are
skipped, as Go does. -/
def base64DecK (k : Nat) (bs : List Nat) : Option (List Nat) :=
  let bs := bs.filter fun b => b != 10 && b != 13
  -- The other alphabet's two characters are invalid.
  let bs := if k % 2 == 1 then bs.map fun c => if c == 43 || c == 47 then 0 else if c == 45 then 43 else if c == 95 then 47 else c else bs
  if k / 2 % 2 == 1 then
    if bs.contains 61 || bs.length % 4 == 1 then none
    else base64Dec (bs ++ List.replicate ((4 - bs.length % 4) % 4) 61)
  else base64Dec bs

/-- JSON text without the whitespace outside strings (what Go does to a
`json.RawMessage` it encodes). -/
def compactJson (bs : List Nat) : List Nat := Id.run do
  let mut out : Array Nat := #[]
  let mut inStr := false
  let mut esc := false
  for b in bs do
    if inStr then
      out := out.push b
      if esc then esc := false
      else if b == 92 then esc := true
      else if b == 34 then inStr := false
    else if b == 32 || b == 9 || b == 10 || b == 13 then pure ()
    else
      out := out.push b
      if b == 34 then inStr := true
  return out.toList

/-- Lexicographic order on byte strings (Go sorts map keys this way). -/
def bytesLe (a b : List Nat) : Bool :=
  match a, b with
  | [], _ => true
  | _ :: _, [] => false
  | x :: xs, y :: ys => if x < y then true else if x > y then false else bytesLe xs ys

/-- `omitempty`: false, 0, "", nil, and empty slices and maps. -/
def jIsEmpty (heap : Heap Val) : Val → Bool
  | .bool b => !b
  | .int _ v => v == 0
  | .str _ _ len => len == 0
  | .ptr 0 _ | .opaqueNil => true
  | .slice _ _ len _ => len == 0
  | .arr es => es.isEmpty
  | .arrA es => es.isEmpty
  | m@(.boxed 1 _) => match mapEntries heap m with | some (_, es) => es.isEmpty | none => false
  | .tuple [.arr [.int _ b]] => b.toNat == 48 -- a known float: zero
  | _ => false

def jSep (parts : List (List Nat)) (sep : Nat) : List Nat := (parts.intersperse [sep]).flatten

/-- Go's HTML escaping of encoded JSON (`<`, `>`, `&`, U+2028, U+2029), as
`json.Marshal` applies it to a Marshaler's output (`json.RawMessage`). -/
def htmlEscape : List Nat → List Nat
  | 60 :: bs => asciiBytes "\\u003c" ++ htmlEscape bs
  | 62 :: bs => asciiBytes "\\u003e" ++ htmlEscape bs
  | 38 :: bs => asciiBytes "\\u0026" ++ htmlEscape bs
  | 0xE2 :: 0x80 :: 0xA8 :: bs => asciiBytes "\\u2028" ++ htmlEscape bs
  | 0xE2 :: 0x80 :: 0xA9 :: bs => asciiBytes "\\u2029" ++ htmlEscape bs
  | b :: bs => b :: htmlEscape bs
  | [] => []

/-- Undoes `htmlEscape` of `<`, `>` and `&` on encoded JSON: the output of
an encoder with `SetEscapeHTML(false)` (U+2028 and U+2029 stay escaped). -/
def htmlUnescape : List Nat → List Nat
  | 92 :: 117 :: 48 :: 48 :: 51 :: 99 :: bs => 60 :: htmlUnescape bs
  | 92 :: 117 :: 48 :: 48 :: 51 :: 101 :: bs => 62 :: htmlUnescape bs
  | 92 :: 117 :: 48 :: 48 :: 50 :: 54 :: bs => 38 :: htmlUnescape bs
  | 92 :: b :: bs => 92 :: b :: htmlUnescape bs
  | b :: bs => b :: htmlUnescape bs
  | [] => []

/-- `omitzero`, and whether an SDK union variant is set (`param.IsOmitted`):
is `v` the zero value of entry `d`? A `param.Opt` is zero when omitted with
a zero value; the SDKs set `Value` only together with the status, so an
omitted Opt counts as zero also where the model does not know the value
(floats). `none`: unknown. -/
partial def jIsZero (t : JTable) (d : Nat) (v : Val) : Option Bool :=
  match t.descs[d]?, v with
  | some (.opt e), .structv (x :: .int _ st :: _) =>
    if st != 0 then some false else if jIsZero t e x == some false then some false else some true
  | some (.strct fields _ _), .structv vs => byFields fields vs
  | some (.union variants), .structv vs => byFields variants vs
  | _, _ => isZeroVal v
where
  /-- Described fields by their type, the others (metadata) as values. -/
  byFields (fields : List JField) (vs : List Val) : Option Bool :=
    let flags := vs.zipIdx.map fun (x, i) =>
      match fields.find? (·.idx == i) with
      | some f => if f.guard then isZeroVal x else jIsZero t f.ty x
      | none => isZeroVal x
    if flags.any (· == some false) then some false
    else if flags.all (· == some true) then some true else none

/-- `json.Marshal` of `v` as entry `d` of `t`: the bytes, `.error true` for
an error Go reports, `.error false` where the model is stuck. -/
partial def jEnc (t : JTable) (heap : Heap Val) (d : Nat) (v : Val) : Except Bool (List Nat) :=
  match t.descs[d]?, v with
  | none, _ => .error false
  | some _, .unit => .error false
  | some (.str dflt), s@(.str ..) =>
    match strBytes heap s with
    | some bs => .ok (goJsonQuote (if bs.isEmpty && !dflt.isEmpty then dflt else bs))
    | none => .error false
  | some .bool, .bool b => .ok (asciiBytes (if b then "true" else "false"))
  | some (.int _ signed), .int _ x => .ok (bvDec signed x)
  | some .float, .tuple [.arr bs] => .ok (bs.map fun | .int _ b => b.toNat | _ => 0)
  | some .bytes, .slice 0 .. | some .raw, .slice 0 .. | some (.slice _), .slice 0 .. => .ok (asciiBytes "null")
  | some .bytes, .slice ref off len _ =>
    match strBytes heap (.str ref off len) with
    | some bs => .ok ([34] ++ base64Enc bs ++ [34])
    | none => .error false
  | some .raw, .slice ref off len _ =>
    match strBytes heap (.str ref off len) with
    | some bs =>
      match Lean.Json.parse (utf8Lossy bs) with
      | .ok _ => .ok (htmlEscape (compactJson bs))
      | .error _ => .error true
    | none => .error false
  | some (.slice e), .slice ref off len _ =>
    match heap[ref]? with
    | some (.arr es) => elems e ((es.drop off.toNat).take len.toNat)
    | some (.arrA es) => elems e (es.extract off.toNat (off.toNat + len.toNat)).toList
    | _ => .error false
  | some (.arr _ e), .arr es => elems e es
  | some (.arr _ e), .arrA es => elems e es.toList
  | some (.ptr _), .ptr 0 _ => .ok (asciiBytes "null")
  | some (.ptr e), p@(.ptr ..) =>
    match heapLoad heap p with
    | .ok x => jEnc t heap e x
    | _ => .error false
  | some (.map _ _), .opaqueNil | some .iface, .opaqueNil => .ok (asciiBytes "null")
  | some (.map k vd), m@(.boxed 1 _) =>
    match mapEntries heap m with
    | some (_, es) => do
      let kd := t.descs[k]?.getD .opaque
      let mut parts : List (List Nat × List Nat) := []
      for (kv, x) in es do
        let key ← match kd, kv with
          | .str _, s@(.str ..) => (strBytes heap s).elim (.error false) .ok
          | .int _ sg, .int _ n => .ok (bvDec sg n)
          | _, _ => .error false
        parts := parts ++ [(key, ← jEnc t heap vd x)]
      let sorted := parts.mergeSort fun a b => bytesLe a.1 b.1
      .ok ([123] ++ jSep (sorted.map fun (key, x) => goJsonQuote key ++ [58] ++ x) 44 ++ [125])
    | none => .error false
  | some .iface, .boxed tag inner =>
    match t.boxed.lookup tag, inner with
    | some d', _ => jEnc t heap d' inner
    -- A scalar of a type the program does not box itself (an int an
    -- interface{} input holds): its JSON does not depend on the type.
    | none, s@(.str ..) => (strBytes heap s).elim (.error false) (.ok ∘ goJsonQuote)
    | none, .bool b => .ok (asciiBytes (if b then "true" else "false"))
    | none, .int _ x => .ok (bvDec true x)
    -- Decoded interface numbers use knownFloat's canonical JSON bytes,
    -- even when the program never explicitly boxes a float64 itself.
    | none, .tuple [.arr bs] => .ok (bs.map fun | .int _ b => b.toNat | _ => 0)
    -- A map[string]interface{} or []interface{} the program does not box
    -- itself (a decoded JSON object, a generated input): its members are
    -- interface values again.
    | none, m@(.boxed 1 _) =>
      match mapEntries heap m with
      | some (_, es) => do
        -- Interface members only (map[string]interface{}): another map
        -- type's values need their own description.
        if !es.all (fun (_, x) => x matches .boxed .. || x matches .opaqueNil) then .error false
        let mut parts : List (List Nat × List Nat) := []
        for (kv, x) in es do
          let key ← (strBytes heap kv).elim (.error false) .ok
          parts := parts ++ [(key, ← jEnc t heap d x)]
        let sorted := parts.mergeSort fun a b => bytesLe a.1 b.1
        .ok ([123] ++ jSep (sorted.map fun (key, x) => goJsonQuote key ++ [58] ++ x) 44 ++ [125])
      | none => .error false
    | none, .slice 0 .. => .ok (asciiBytes "null")
    | none, .slice ref off len _ =>
      match heap[ref]?.bind arrList? with
      | some es =>
        let es := (es.drop off.toNat).take len.toNat
        -- []interface{} only: a []byte encodes as base64.
        if es.all (fun x => x matches .boxed .. || x matches .opaqueNil) then elems d es else .error false
      | _ => .error false
    | none, _ => .error false
  | some (.strct fields _ _), .structv vs => do
    let ms ← members fields vs
    .ok ([123] ++ jSep (ms.map fun (key, x) => goJsonQuote key ++ [58] ++ x) 44 ++ [125])
  | some (.union variants), .structv vs =>
    let zero := variants.map fun f => (f, match vs[f.idx]? with
      | some x => jIsZero t f.ty x
      | none => some true)
    if zero.any (·.2 == none) then .error false else
    let present := (zero.filter (·.2 == some false)).map (·.1)
    match present with
    | [] => .ok (asciiBytes "null")
    | [f] => jEnc t heap f.ty (vs[f.idx]!)
    | _ => .error true
  | some (.opt e), .structv (x :: .int _ st :: _) =>
    if st.toNat == 2 then jEnc t heap e x else .ok (asciiBytes "null")
  | some _, _ => .error false
where
  elems (e : Nat) (es : List Val) : Except Bool (List Nat) := do
    let parts ← es.mapM (jEnc t heap e)
    .ok ([91] ++ jSep parts 44 ++ [93])
  /-- The encoded members of a struct: omitted ones left out, inline ones
  spliced in. -/
  members (fields : List JField) (vs : List Val) : Except Bool (List (List Nat × List Nat)) := do
    let mut out := []
    for f in fields do
      let x := vs[f.idx]?.getD .unit
      if f.guard then
        if isZeroVal x == some true then continue else .error false
      if f.inline then
        let inner := match x with
          | .ptr 0 _ => none
          | p@(.ptr ..) => match heapLoad heap p with | .ok y => some y | _ => none
          | y => some y
        let innerDesc := match t.descs[f.ty]? with
          | some (.ptr e) => t.descs[e]?
          | dd => dd
        match innerDesc, inner with
        | some (.strct fs _ _), some (.structv ws) => out := out ++ (← members fs ws)
        | _, none => pure ()
        | _, _ => .error false
      else if f.omitEmpty && jIsEmpty heap x then pure ()
      else if f.omitZero && jIsZero t f.ty x == some true then pure ()
      else if f.omitZero && jIsZero t f.ty x == none then .error false
      else out := out ++ [(f.key, ← jEnc t heap f.ty x)]
    return out

/-! Byte-level scanning of a valid JSON document: the source bytes of each
value. Go keeps them where it keeps raw text (`json.RawMessage`, the SDKs'
`raw` and `respjson.Field`), and checks integer literals on them; Lean's
parsed `Json` has sorted keys and normalised numbers and strings. -/

def jWs (b : Nat) : Bool := b == 32 || b == 9 || b == 10 || b == 13

def jSkipWs : List Nat → List Nat
  | b :: bs => if jWs b then jSkipWs bs else b :: bs
  | [] => []

/-- The value at the start of `bs` (no leading whitespace) and the rest. -/
def jSplitValue (bs : List Nat) : List Nat × List Nat := Id.run do
  let arr := bs.toArray
  let mut depth := 0
  let mut inStr := false
  let mut esc := false
  for i in [0:arr.size] do
    let b := arr[i]!
    if inStr then
      if esc then esc := false
      else if b == 92 then esc := true
      else if b == 34 then
        inStr := false
        if depth == 0 then return (bs.take (i + 1), bs.drop (i + 1))
    else if b == 34 then inStr := true
    else if b == 123 || b == 91 then depth := depth + 1
    else if b == 125 || b == 93 then
      if depth == 0 then return (bs.take i, bs.drop i)
      depth := depth - 1
      if depth == 0 then return (bs.take (i + 1), bs.drop (i + 1))
    else if depth == 0 && (b == 44 || b == 58 || jWs b) then return (bs.take i, bs.drop i)
  return (bs, [])

/-- The source of a whole document without surrounding whitespace. -/
def jTrim (bs : List Nat) : List Nat := (jSplitValue (jSkipWs bs)).1

/-- An object's members in source order: the key (decoded) and the value's
source. -/
partial def jObjMembers (src : List Nat) : List (String × List Nat) :=
  match src with
  | 123 :: rest => go (jSkipWs rest) []
  | _ => []
where
  go (bs : List Nat) (acc : List (String × List Nat)) : List (String × List Nat) :=
    match bs with
    | [] | 125 :: _ => acc.reverse
    | 44 :: rest => go (jSkipWs rest) acc
    | _ =>
      let (k, rest) := jSplitValue bs
      let key := match Lean.Json.parse (utf8Lossy k) with | .ok (.str s) => s | _ => ""
      match jSkipWs rest with
      | 58 :: rest =>
        let (v, rest) := jSplitValue (jSkipWs rest)
        go (jSkipWs rest) ((key, v) :: acc)
      | _ => acc.reverse

/-- An array's element sources in order. -/
partial def jArrElems (src : List Nat) : List (List Nat) :=
  match src with
  | 91 :: rest => go (jSkipWs rest) []
  | _ => []
where
  go (bs : List Nat) (acc : List (List Nat)) : List (List Nat) :=
    match bs with
    | [] | 93 :: _ => acc.reverse
    | 44 :: rest => go (jSkipWs rest) acc
    | _ => let (v, rest) := jSplitValue bs; go (jSkipWs rest) (v :: acc)

/-- Go's integer literal syntax (strconv.ParseInt on the literal): an
optional minus and digits only (`1e2`, `1.0` do not decode into an int). -/
def jIntLiteral (src : List Nat) : Bool :=
  let ds := match src with | 45 :: ds => ds | ds => ds
  !ds.isEmpty && ds.all fun b => 48 ≤ b && b ≤ 57

/-- A respjson.Field: its status (1 null, 2 invalid, 3 valid) and raw text. -/
def respField (heap : Heap Val) (status : Nat) (src : List Nat) : Val × Heap Val :=
  let (r, heap) := strAlloc heap src
  (.structv [.int 8 (BitVec.ofNat 8 status), r], heap)

/-- Decode `j` (whose source bytes are `src`) into the current value `cur`
of entry `d`: the new value, the heap, and whether some member had the
wrong JSON type (Go keeps going and reports the first such error).
`.error ()`: the model is stuck. -/
partial def jDec (t : JTable) (heap : Heap Val) (d : Nat) (j : Lean.Json) (src : List Nat) (cur : Val) :
    Except Unit (Val × Heap Val × Bool) :=
  let keep := Except.ok (cur, heap, false)
  let typeErr := Except.ok (cur, heap, true)
  match t.descs[d]?, j with
  | none, _ => .error ()
  | some (.str _), .str s => let (v, heap) := strAlloc heap (asciiBytes s); .ok (v, heap, false)
  | some (.str _), .null | some .bool, .null | some (.int ..), .null | some .float, .null => keep
  | some (.str _), _ | some .bool, _ | some (.int ..), _ | some .float, _ =>
    match t.descs[d]?, j with
    | some .bool, .bool b => .ok (.bool b, heap, false)
    | some (.int w signed), .num n =>
      if n.exponent != 0 || !jIntLiteral src then typeErr else
      let m := n.mantissa
      let ok := if signed then -(2 ^ (w - 1) : Int) ≤ m && m < 2 ^ (w - 1) else 0 ≤ m && m < 2 ^ w
      if ok then .ok (.int w (BitVec.ofInt w m), heap, false) else typeErr
    | some .float, .num n => .ok (knownFloat n (hasNegZero src), heap, false)
    | _, _ => typeErr
  | some .bytes, .null => .ok (.slice 0 0#64 0#64 0#64, heap, false)
  | some .bytes, .str s =>
    match base64Dec (asciiBytes s) with
    | some bs => .ok (byteSlice heap bs)
    | none => typeErr
  | some .bytes, _ => typeErr
  | some .raw, _ => .ok (byteSlice heap src)
  | some (.slice _), .null => .ok (.slice 0 0#64 0#64 0#64, heap, false)
  | some (.slice e), .arr js => do
    let srcs := jArrElems src
    if srcs.length != js.size then .error () else
    let (vs, heap, err) ← decList e (js.toList.zip srcs) (List.replicate js.size (t.zeros[e]?.getD .unit)) heap
    let (p, heap) := heapAlloc heap (.arr vs)
    match p with
    | .ptr ref _ => .ok (.slice ref 0#64 (BitVec.ofNat 64 vs.length) (BitVec.ofNat 64 vs.length), heap, err)
    | _ => .error ()
  | some (.slice _), _ => typeErr
  | some (.arr n e), .arr js =>
    match cur with
    | .arr old => do
      let srcs := jArrElems src
      if srcs.length != js.size then .error () else
      let js := (js.toList.zip srcs).take n
      let (vs, heap, err) ← decList e js (old.take js.length) heap
      .ok (.arr (vs ++ (old.drop js.length).map fun _ => t.zeros[e]?.getD .unit), heap, err)
    | _ => .error ()
  | some (.arr ..), .null => keep
  | some (.arr ..), _ => typeErr
  | some (.ptr _), .null => .ok (.ptr 0 [], heap, false)
  | some (.ptr e), j' =>
    match cur with
    | .ptr 0 _ => do
      let (v, heap, err) ← jDec t heap e j' src (t.zeros[e]?.getD .unit)
      let (p, heap) := heapAlloc heap v
      .ok (p, heap, err)
    | p@(.ptr ..) =>
      match heapLoad heap p with
      | .ok old => do
        let (v, heap, err) ← jDec t heap e j' src old
        match heapStore heap p v with
        | .ok heap => .ok (p, heap, err)
        | _ => .error ()
      | _ => .error ()
    | _ => .error ()
  | some (.map _ _), .null => .ok (.opaqueNil, heap, false)
  | some (.map k vd), .obj kvs => do
    let members := jObjMembers src
    -- Duplicate keys: Go decodes each occurrence in turn; Lean's Json
    -- keeps the last one only.
    if members.length != kvs.size then .error () else
    let (m, heap) := match cur with
      | .opaqueNil => let (p, heap) := heapAlloc heap (mapObj []); (Val.boxed 1 p, heap)
      | m => (m, heap)
    let (q, old) ← (mapEntries heap m).elim (.error ()) .ok
    let mut es := old
    let mut heap := heap
    let mut err := false
    for (key, vsrc) in members do
      let jv ← (kvs.get? key).elim (.error ()) .ok
      let (kv, heap') ← match t.descs[k]? with
        | some (.str _) => .ok (strAlloc heap (asciiBytes key))
        | some (.int w sg) =>
          match atoiBytes (asciiBytes key) with
          | some n => if sg || n ≥ 0 then .ok (.int w (BitVec.ofInt w n), heap) else .error ()
          | none => .error ()
        | _ => .error ()
      let (v, heap'', e) ← jDec t heap' vd jv vsrc (t.zeros[vd]?.getD .unit)
      heap := heap''
      err := err || e
      es := if es.any (fun (k', _) => keyEq heap kv k') then
          es.map fun (k', v') => if keyEq heap kv k' then (k', v) else (k', v')
        else es ++ [(kv, v)]
    match heapStore heap q (mapObj es) with
    | .ok h => .ok (m, h, err)
    | _ => .error ()
  | some (.map ..), _ => typeErr
  | some .iface, j' => let (v, heap) := jsonToVal t.basic heap (hasNegZero src) j'; .ok (v, heap, false)
  | some (.strct _ _ false), .null => keep
  | some (.strct fields md api), .obj kvs =>
    match cur with
    | .structv vs => do
      let members := jObjMembers src
      if members.length != kvs.size then .error () else
      -- Members no field reads go to a typed ExtraFields map (apijson;
      -- set only when there are some), else they are dropped.
      let known := knownKeys fields
      let unknown := members.filter fun (k, _) => !known.contains (asciiBytes k)
      let (vs, heap) ← match fields.find? (·.extras), unknown with
        | some f, _ :: _ =>
          let ukvs := unknown.filterMap fun (k, _) => (kvs.get? k).map (k, ·)
          match jDec t heap f.ty (.obj (Std.TreeMap.Raw.ofList ukvs)) (wholeObject unknown) .opaqueNil with
          | .ok (m, heap, false) => .ok (vs.set f.idx m, heap)
          | _ => .error ()
        | _, _ => .ok (vs, heap)
      let (vs, heap, err, metas) ←
        if api then decApi fields members kvs src vs heap else do
          let (vs, heap, err) ← decStruct fields members kvs vs heap
          pure (vs, heap, err, members.map fun (k, v) =>
            (asciiBytes k, (if kvs.get? k matches some .null then 1 else 3, v)))
      -- An SDK response type: its JSON metadata.
      match md with
      | none => .ok (.structv vs, heap, err)
      | some mt =>
        match vs[mt.field]? with
        | some (.structv ms) =>
          let (raw, heap) := strAlloc heap src
          let ms := ms.set mt.raw raw
          let (ms, heap) := mt.members.foldl (fun (ms, heap) (key, i) =>
            match metas.find? (fun (k, _) => k == key) with
            | some (_, (st, vsrc)) => let (f, heap) := respField heap st vsrc; (ms.set i f, heap)
            | none => (ms, heap)) (ms, heap)
          let known := knownKeys fields
          let extras := members.filter fun (k, _) => !known.contains (asciiBytes k)
          let (ms, heap) := match mt.extra with
            | some ei =>
              if extras.isEmpty then (ms, heap) else
              let (es, heap) := extras.foldl (fun (acc, heap) (k, vsrc) =>
                let (kv, heap) := strAlloc heap (asciiBytes k)
                -- apijson has no decoder for an extra key: invalid unless null.
                let st := if (kvs.get? k) matches some .null then 1 else 2
                let (f, heap) := respField heap st vsrc
                (acc ++ [(kv, f)], heap)) ([], heap)
              let (p, heap) := heapAlloc heap (mapObj es)
              (ms.set ei (.boxed 1 p), heap)
            | none => (ms, heap)
          .ok (.structv (vs.set mt.field (.structv ms)), heap, err)
        | _ => .ok (.structv vs, heap, err)
    | _ => .error ()
  | some (.strct fields md true), j' =>
    -- An SDK union read from a scalar or array (instructions: "text" or a
    -- list): apijson decodes each inline variant from the whole value.
    match cur with
    | .structv vs => do
      if !(fields.any fun f => f.inline && !f.guard) then .error () else
      let mut vs := vs
      let mut heap := heap
      let mut metas : List (List Nat × (Nat × List Nat)) := []
      for f in fields do
        if f.guard || !f.inline then continue
        if j' matches .null then
          metas := metas ++ [(f.key, (1, src))]
          continue
        let (v, heap', ok) ← inlineVariant f j' src heap
        vs := vs.set f.idx v
        heap := heap'
        if ok then metas := metas ++ [(f.key, (3, src))]
      let vs' := vs
      let heap' := heap
      let metas' := metas
      match md with
      | none => .ok (.structv vs', heap', false)
      | some mt =>
        match vs'[mt.field]? with
        | some (.structv ms) =>
          let (raw, h) := strAlloc heap' src
          let ms := ms.set mt.raw raw
          let (ms, h) := mt.members.foldl (fun (ms, h) (key, i) =>
            match metas'.find? (fun (k, _) => k == key) with
            | some (_, (st, vsrc)) => let (f, h) := respField h st vsrc; (ms.set i f, h)
            | none => (ms, h)) (ms, h)
          .ok (.structv (vs'.set mt.field (.structv ms)), h, false)
        | _ => .ok (.structv vs', heap', false)
    | _ => .error ()
  | some (.strct ..), _ => typeErr
  | some (.opt _), .null =>
    match cur with
    | .structv (x :: _ :: rest) => .ok (.structv (x :: .int 8 1#8 :: rest), heap, false)
    | _ => .error ()
  | some (.opt e), j' =>
    match cur with
    | .structv (x :: _ :: rest) => do
      let (v, heap, err) ← jDec t heap e j' src x
      -- Opt.UnmarshalJSON leaves the option as it was when the value
      -- does not decode.
      if err then .ok (cur, heap, true) else
      .ok (.structv (v :: .int 8 2#8 :: rest), heap, false)
    | _ => .error ()
  | some _, _ => .error ()
where
  byteSlice (heap : Heap Val) (bs : List Nat) : Val × Heap Val × Bool :=
    let (p, heap) := heapAlloc heap (.arr (bs.map fun b => .int 8 (BitVec.ofNat 8 b)))
    match p with
    | .ptr ref _ => (.slice ref 0#64 (BitVec.ofNat 64 bs.length) (BitVec.ofNat 64 bs.length), heap, false)
    | _ => (.unit, heap, false)
  decList (e : Nat) : List (Lean.Json × List Nat) → List Val → Heap Val → Except Unit (List Val × Heap Val × Bool)
    | (j, s) :: js, c :: cs, heap => do
      let (v, heap, e1) ← jDec t heap e j s c
      let (vs, heap, e2) ← decList e js cs heap
      .ok (v :: vs, heap, e1 || e2)
    | _, _, heap => .ok ([], heap, false)
  /-- The keys the struct's fields (inline ones included) decode. -/
  knownKeys (fields : List JField) : List (List Nat) :=
    fields.flatMap fun f =>
      if f.guard then [] else
      if f.inline then
        match t.descs[f.ty]? with
        | some (.strct fs _ _) => knownKeys fs
        | some (.ptr e) => match t.descs[e]? with | some (.strct fs _ _) => knownKeys fs | _ => []
        | _ => []
      else [f.key]
  /-- Members into fields (encoding/json): the exact key, else a
  case-insensitive match; inline fields take the whole object. -/
  decStruct (fields : List JField) (members : List (String × List Nat)) (kvs : Std.TreeMap.Raw String Lean.Json)
      (vs : List Val) (heap : Heap Val) : Except Unit (List Val × Heap Val × Bool) := do
    let mut vs := vs
    let mut heap := heap
    let mut err := false
    for f in fields do
      if f.guard then continue
      if f.inline then
        let (v, heap', e) ← jDec t heap f.ty (.obj kvs) (wholeObject members) (vs[f.idx]?.getD .unit)
        vs := vs.set f.idx v
        heap := heap'
        err := err || e
      else
        let hit := (members.find? fun (k, _) => asciiBytes k == f.key) <|>
          (members.find? fun (k, _) => (asciiBytes k).map lowerByte == f.key.map lowerByte)
        match hit with
        | some (k, vsrc) =>
          let jv ← (kvs.get? k).elim (.error ()) .ok
          let (v, heap', e) ← jDec t heap f.ty jv vsrc (vs[f.idx]?.getD .unit)
          vs := vs.set f.idx v
          heap := heap'
          err := err || e
        | none => pure ()
    return (vs, heap, err)
  /-- Members into fields as the SDKs' apijson decoder does: exact keys
  only; null leaves a field unchanged (an Opt becomes null); pointers and
  maps are decoded afresh; a member of the wrong JSON type follows
  apijson's loose rules for strings (a bool or an integer literal is
  stringified, an object or array kept as raw text), bools (gjson's
  Bool()) and slices (a non-array leaves the field as it was), and gets
  the model stuck otherwise. Also the
  status and source of each typed member, for the metadata. -/
  decApi (fields : List JField) (members : List (String × List Nat)) (kvs : Std.TreeMap.Raw String Lean.Json)
      (src : List Nat) (vs : List Val) (heap : Heap Val) :
      Except Unit (List Val × Heap Val × Bool × List (List Nat × (Nat × List Nat))) := do
    let mut vs := vs
    let mut heap := heap
    let mut metas := []
    for f in fields do
      if f.guard then continue
      if f.inline then
        match t.descs[f.ty]? with
        | some (.strct ..) =>
          let (v, heap', e) ← jDec t heap f.ty (.obj kvs) (wholeObject members) (vs[f.idx]?.getD .unit)
          if e then .error ()
          vs := vs.set f.idx v
          heap := heap'
        | _ =>
          let (v, heap', ok) ← inlineVariant f (.obj kvs) src heap
          vs := vs.set f.idx v
          heap := heap'
          if ok then metas := metas ++ [(f.key, (3, src))]
      else
        match members.find? fun (k, _) => asciiBytes k == f.key with
        | some (k, vsrc) =>
          let jv ← (kvs.get? k).elim (.error ()) .ok
          if jv matches .null then
            if let some (.opt _) := t.descs[f.ty]? then
              let (v, heap', _) ← jDec t heap f.ty jv vsrc (vs[f.idx]?.getD .unit)
              vs := vs.set f.idx v
              heap := heap'
            metas := metas ++ [(f.key, (1, vsrc))]
          else
            let start := match t.descs[f.ty]? with
              | some (.ptr _) => .ptr 0 []
              | some (.map ..) => .opaqueNil
              | _ => vs[f.idx]?.getD .unit
            let (v, heap', e) ← jDec t heap f.ty jv vsrc start
            if !e then
              vs := vs.set f.idx v
              heap := heap'
              metas := metas ++ [(f.key, (3, vsrc))]
            else
              -- A member of the wrong JSON type: apijson's loose rules
              -- where they are simple, else the model is stuck.
              let coerced := if t.strict then 2 else 3
              match t.descs[f.ty]?, jv with
              | some (.str _), .bool b =>
                let (x, h) := strAlloc heap (asciiBytes (if b then "true" else "false"))
                vs := vs.set f.idx x
                heap := h
                metas := metas ++ [(f.key, (coerced, vsrc))]
              | some (.str _), .num _ =>
                -- gjson prints an integer literal as written (others are
                -- reformatted as floats: not modelled).
                if !jIntLiteral (jTrim vsrc) then .error ()
                let (x, h) := strAlloc heap (jTrim vsrc)
                vs := vs.set f.idx x
                heap := h
                metas := metas ++ [(f.key, (coerced, vsrc))]
              | some (.str _), .obj _ | some (.str _), .arr _ =>
                -- The raw text is stored, and the member is invalid.
                let (x, h) := strAlloc heap (jTrim vsrc)
                vs := vs.set f.idx x
                heap := h
                metas := metas ++ [(f.key, (2, vsrc))]
              | some .bool, j' =>
                -- gjson's Bool(): ParseBool of a lower-cased string, a
                -- non-zero number, false otherwise; only a number is valid.
                let b := match j' with
                  | .str x => ["1", "t", "true"].contains x.toLower
                  | .num n => n.mantissa != 0
                  | _ => false
                vs := vs.set f.idx (.bool b)
                metas := metas ++ [(f.key, (if j' matches .num _ then coerced else 2, vsrc))]
              | some (.slice _), .arr _ => .error () -- elements may be coerced
              | some (.slice _), _ =>
                -- Not an array: the field is left as it was, invalid.
                metas := metas ++ [(f.key, (2, vsrc))]
              | _, _ => .error ()
        | none => pure ()
    return (vs, heap, false, metas)
  /-- An inline union variant decoded strictly from the whole value
  (apijson): the value and whether it decoded; one that does not is left
  zero. -/
  inlineVariant (f : JField) (j : Lean.Json) (src : List Nat) (heap : Heap Val) :
      Except Unit (Val × Heap Val × Bool) :=
    let zero := t.zeros[f.ty]?.getD .unit
    match jDec { t with strict := true } heap f.ty j src zero with
    | .ok (v, heap, false) => .ok (v, heap, true)
    | .ok (_, heap, true) => .ok (zero, heap, false)
    | .error () => .error ()
  /-- An object's source rebuilt from its members (for inline fields, which
  read the whole object). -/
  wholeObject (members : List (String × List Nat)) : List Nat :=
    [123] ++ jSep (members.map fun (k, v) => goJsonQuote (asciiBytes k) ++ [58] ++ v) 44 ++ [125]

/-! SDK response values for the search: generated values whose typed
fields and raw JSON agree, as only decoding produces them. -/

/-- The harness's zero test for a rendered value (poc/harness.go
isZeroJSON): a zero field is left out of the document. -/
partial def jsZero : Val → Bool
  | .int _ v => v == 0
  | .bool b => !b
  | .err b => !b
  | .str _ _ len => len == 0
  | .ptr 0 _ | .slice 0 .. | .opaqueNil | .unit | .boxed 0 _ => true
  | .tuple [.arr [.int _ b]] => b.toNat == 48 -- a known float: zero
  | .structv vs | .arr vs => vs.all jsZero
  | .arrA vs => vs.all jsZero
  | _ => false

def jObjBytes (members : List (List Nat × List Nat)) : List Nat :=
  let sorted := members.mergeSort fun a b => bytesLe a.1 b.1
  [123] ++ jSep (sorted.map fun (k, x) => goJsonQuote k ++ [58] ++ x) 44 ++ [125]

/-- The JSON document the harness decodes an SDK response value from
(poc/harness.go jsonDoc, then json.Marshal): the non-zero typed fields
under their keys, each level's raw JSON members merged in where no typed
field has the key, keys sorted. Floats and other opaque fields are left
out, as the harness leaves them out. `none`: a value the model cannot
write as that document. -/
partial def jDoc (t : JTable) (heap : Heap Val) (d : Nat) (v : Val) : Option (List Nat) :=
  match t.descs[d]?, v with
  | some (.str _), s@(.str ..) => (strBytes heap s).map goJsonQuote
  | some .bool, .bool b => some (asciiBytes (if b then "true" else "false"))
  | some (.int _ signed), .int _ x => some (bvDec signed x)
  | some (.ptr _), .ptr 0 _ => some (asciiBytes "null")
  | some (.ptr e), p@(.ptr ..) => match heapLoad heap p with | .ok x => jDoc t heap e x | _ => none
  -- json.RawMessage: itself when it is JSON, null otherwise (the harness
  -- writes it so).
  | some .raw, .slice 0 .. => some (asciiBytes "null")
  | some .raw, .slice ref off len _ =>
    (strBytes heap (.str ref off len)).map fun bs =>
      match Lean.Json.parse (utf8Lossy bs) with
      | .ok _ => htmlEscape (compactJson bs)
      | .error _ => asciiBytes "null"
  | some (.slice _), .slice 0 .. => some (asciiBytes "[]")
  | some (.slice e), .slice ref off len _ =>
    match heap[ref]? with
    | some (.arr es) => elems e ((es.drop off.toNat).take len.toNat)
    | some (.arrA es) => elems e (es.extract off.toNat (off.toNat + len.toNat)).toList
    | _ => none
  | some (.arr _ e), .arr es => elems e es
  | some (.arr _ e), .arrA es => elems e es.toList
  | some (.map ..), .opaqueNil => some (asciiBytes "{}")
  | some (.map kd vd), m@(.boxed 1 _) => do
    let (_, es) ← mapEntries heap m
    let members ← es.mapM fun (k, x) => do
      let key ← match t.descs[kd]?, k with
        | some (.str _), s@(.str ..) => strBytes heap s
        | some (.int _ sg), .int _ n => some (bvDec sg n)
        | _, _ => none
      pure (key, ← jDoc t heap vd x)
    pure (jObjBytes (dedup members))
  | some .iface, .opaqueNil | some .iface, .boxed 0 _ => some (asciiBytes "null")
  | some .iface, .boxed tag inner =>
    match t.boxed.lookup tag with
    | some d' => jDoc t heap d' inner
    | none =>
      -- The basic types an `any` holds after decoding (see jsonToVal).
      -- A float64's value is not known to the model, and the harness has
      -- no alternative for it either: it writes null.
      if tag == t.basic.getD 0 0 then (strBytes heap inner).map goJsonQuote
      else if tag == t.basic.getD 2 0 then some (asciiBytes "null")
      else if tag == t.basic.getD 1 0 then
        match inner with
        | .bool b => some (asciiBytes (if b then "true" else "false"))
        | _ => none
      else if tag == t.basic.getD 3 0 then do
        let (_, es) ← mapEntries heap inner
        let members ← es.mapM fun (k, x) => do pure ((← strBytes heap k), ← jDoc t heap d x)
        pure (jObjBytes (dedup members))
      else if tag == t.basic.getD 4 0 then
        match inner with
        | .slice ref off len _ =>
          match heap[ref]? with
          | some (.arr es) => elems d ((es.drop off.toNat).take len.toNat)
          | some (.arrA es) => elems d (es.extract off.toNat (off.toNat + len.toNat)).toList
          | _ => none
        | _ => none
      else none
  -- A float element: the model does not know its value, and the harness
  -- writes null for it.
  | some .float, _ => some (asciiBytes "null")
  | some (.strct fields md _), .structv vs => do
    let mut members : List (List Nat × List Nat) := []
    let mut inlined : Option (List Nat) := none
    for f in fields do
      if f.guard then continue
      let x := vs[f.idx]?.getD .unit
      if jsZero x then continue
      if (t.descs[f.ty]?.map fun | .opaque | .float => true | _ => false) == some true then continue
      let doc ← jDoc t heap f.ty x
      if f.inline then
        match doc with
        | 123 :: _ => members := members ++ (jObjMembers doc).map fun (k, s) => (asciiBytes k, s)
        | _ => inlined := some doc
      else members := members ++ [(f.key, doc)]
    -- Typed members as the harness's map holds them: a later field (an
    -- inline variant's members included) replaces an earlier one with
    -- the same key.
    members := dedup members
    -- The raw JSON's members no typed field wrote (harness: decoded into
    -- map[string]json.RawMessage, so the last of a duplicate key wins, and
    -- re-encoded compact and HTML-escaped).
    if let some mt := md then
      if let some (.structv ms) := vs[mt.field]? then
        if let some raw := ms[mt.raw]? >>= strBytes heap then
          if let .ok (.obj _) := Lean.Json.parse (utf8Lossy raw) then
            let extras := dedup ((jObjMembers (jTrim raw)).map fun (k, src) => (asciiBytes k, htmlEscape (compactJson src)))
            let typed := members
            members := members ++ extras.filter fun (k, _) => !typed.any (·.1 == k)
    if let some doc := inlined then
      if members.isEmpty then return doc
    pure (jObjBytes members)
  | _, _ => none
where
  elems (e : Nat) (es : List Val) : Option (List Nat) := do
    let parts ← es.mapM (jDoc t heap e)
    pure ([91] ++ jSep parts 44 ++ [93])
  /-- Later entries replace earlier ones with the same key. -/
  dedup (ms : List (List Nat × List Nat)) : List (List Nat × List Nat) :=
    ms.foldl (fun acc (k, x) => (acc.filter (·.1 != k)) ++ [(k, x)]) []

/-- An SDK response value as decoding makes it from `v`'s document: typed
fields, raw text, per-member metadata and ExtraFields that agree (what
the harness builds by decoding the same document). `none` where the
document or its decoding is beyond the model. -/
def sdkNormalize (t : JTable) (d : Nat) (heap : Heap Val) (v : Val) : Option (Val × Heap Val) := do
  let doc ← jDoc t heap d v
  let j ← (Lean.Json.parse (utf8Lossy doc)).toOption
  match jDec t heap d j doc (t.zeros[d]?.getD .unit) with
  | .ok (v', heap', false) => some (v', heap')
  | _ => none

/-- Run a compiled regexp on string `s` for one of the methods `reFind`
models (see there). -/
def reFindRun (heap : Heap Val) (prog : List ReInst) (start ncap kind : Nat) (s : Val) : Res (Val × Heap Val) :=
  match s, strBytes heap s with
  | .str ref off _, some x =>
    let sub (a b : Nat) : Val := if b ≤ a then .str 0 0#64 0#64 else .str ref (off + BitVec.ofNat 64 a) (BitVec.ofNat 64 (b - a))
    match reSearch prog.toArray start ncap x.toArray with
    | none => .panic .modelStuck
    | some m =>
      match kind, m with
      | 0, _ => .ok (.bool m.isSome, heap)
      | 1, none => .ok (.slice 0 0#64 0#64 0#64, heap)
      | 1, some caps =>
        let groups := (List.range (ncap / 2)).map fun g =>
          match caps[2 * g]?, caps[2 * g + 1]? with
          | some (some a), some (some b) => sub a b
          | _, _ => .str 0 0#64 0#64
        let (p, heap) := heapAlloc heap (.arr groups)
        match p with
        | .ptr r _ => .ok (.slice r 0#64 (BitVec.ofNat 64 groups.length) (BitVec.ofNat 64 groups.length), heap)
        | _ => .panic .modelStuck
      | 2, none => .ok (.str 0 0#64 0#64, heap)
      | 2, some caps =>
        match caps[0]?, caps[1]? with
        | some (some a), some (some b) => .ok (sub a b, heap)
        | _, _ => .panic .modelStuck
      | 3, none => .ok (.slice 0 0#64 0#64 0#64, heap)
      | 3, some caps =>
        match caps[0]?, caps[1]? with
        | some (some a), some (some b) =>
          let (p, heap) := heapAlloc heap (.arr [.int 64 (BitVec.ofNat 64 a), .int 64 (BitVec.ofNat 64 b)])
          match p with
          | .ptr r _ => .ok (.slice r 0#64 2#64 2#64, heap)
          | _ => .panic .modelStuck
        | _, _ => .panic .modelStuck
      | _, _ => .panic .modelStuck
  | _, _ => .panic .modelStuck

/-- The FindAll methods (`reFind` kinds 4 FindAllString, 5
FindAllStringSubmatch, 6 FindAllStringIndex) on `s` with limit `n`: nil
without a match, as Go returns. -/
def reFindAllRun (heap : Heap Val) (prog : List ReInst) (start ncap kind : Nat) (s : Val) (n : Int) : Res (Val × Heap Val) :=
  match s, strBytes heap s with
  | .str ref off _, some x =>
    let sub (a b : Nat) : Val := if b ≤ a then .str 0 0#64 0#64 else .str ref (off + BitVec.ofNat 64 a) (BitVec.ofNat 64 (b - a))
    let slot (caps : Array (Option Nat)) (g : Nat) : Val :=
      match caps[2 * g]?, caps[2 * g + 1]? with
      | some (some a), some (some b) => sub a b
      | _, _ => .str 0 0#64 0#64
    let sliceOf (heap : Heap Val) (vs : List Val) : Val × Heap Val :=
      let (p, heap) := heapAlloc heap (.arr vs)
      match p with
      | .ptr r _ => (.slice r 0#64 (BitVec.ofNat 64 vs.length) (BitVec.ofNat 64 vs.length), heap)
      | _ => (.unit, heap)
    match reAll prog.toArray start ncap x.toArray n with
    | none => .panic .modelStuck
    | some [] => .ok (.slice 0 0#64 0#64 0#64, heap)
    | some ms =>
      let (elems, heap) := ms.foldl (fun (acc, heap) caps =>
        match kind with
        | 4 => (acc ++ [slot caps 0], heap)
        | 5 =>
          let (v, heap) := sliceOf heap ((List.range (ncap / 2)).map (slot caps))
          (acc ++ [v], heap)
        | _ =>
          let pos (i : Nat) : Val := .int 64 (BitVec.ofNat 64 ((caps[i]?).bind id |>.getD 0))
          let (v, heap) := sliceOf heap [pos 0, pos 1]
          (acc ++ [v], heap)) ([], heap)
      .ok (sliceOf heap elems)
  | _, _ => .panic .modelStuck

/-- End context `c` and, to depth `n`, its children: set the error code and
close the done channel (waking every receive on it). -/
def ctxCancelRec (code : Nat) : Nat → Heap Val → Val → Heap Val
  | 0, heap, _ => heap
  | n + 1, heap, c =>
    match ctxGet heap c with
    | some (ref, done, 0, cs) =>
      let heap := heap.set! ref (ctxObj done code cs)
      let heap := match chanGet heap done with
        | some (r, cap, buf, false, z) => heap.set! r (chanObj cap buf true z)
        | _ => heap
      cs.foldl (ctxCancelRec code n) heap
    | _ => heap

/-- A `sync.Cond`'s state, kept in its `notify` field (field 2): the next
ticket and the tickets still waiting. Any other value is the zero state. -/
def condState (heap : Heap Val) (c : Val) : Nat × List Nat :=
  match c with
  | .ptr r path =>
    match heapLoad heap (.ptr r (path ++ [2])) with
    | .ok (.tuple [.int _ n, .tuple ws]) => (n.toNat, ws.filterMap fun | .int _ t => some t.toNat | _ => none)
    | _ => (0, [])
  | _ => (0, [])

def setCondState (heap : Heap Val) (c : Val) (n : Nat) (ws : List Nat) : Res (Heap Val) :=
  match c with
  | .ptr r path =>
    heapStore heap (.ptr r (path ++ [2]))
      (.tuple [.int 64 (BitVec.ofNat 64 n), .tuple (ws.map fun t => .int 64 (BitVec.ofNat 64 t))])
  | _ => .panic .nilDereference

/-- The `ConcOp`s, on the heap. -/
def denoteConc (heap : Heap Val) : ConcOp → List Val → Res (Val × Heap Val)
  | .atomicLoad, [p] =>
    match heapLoad heap p with
    | .ok v => .ok (v, heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .atomicStore, [p, v] =>
    match heapStore heap p v with
    | .ok heap => .ok (.unit, heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .ctxBackground, [] => .ok (heapAlloc heap (ctxObj (.ptr 0 []) 0 []))
  | .ctxNew, [parent] =>
    let (ch, heap) := heapAlloc heap (chanObj 0 [] false (.structv []))
    let (c, heap) := heapAlloc heap (ctxObj ch 0 [])
    match ctxGet heap parent with
    | some (pref, pdone, pe, pcs) =>
      let heap := heap.set! pref (ctxObj pdone pe (pcs ++ [c]))
      .ok (c, if pe = 0 then heap else ctxCancelRec pe 16 heap c)
    | none => .ok (c, heap)
  | .ctxDone, [c] => .ok ((ctxGet heap c).elim (.ptr 0 []) (·.2.1), heap)
  | .ctxErr, [c] => .ok (.err (match ctxGet heap c with | some (_, _, e, _) => e != 0 | none => false), heap)
  | .ctxCancel code, [c] => .ok (.unit, ctxCancelRec code 16 heap c)
  | .condEnter, [c] =>
    let (n, ws) := condState heap c
    match setCondState heap c (n + 1) (ws ++ [n]) with
    | .ok heap => .ok (.int 64 (BitVec.ofNat 64 n), heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .condSignal all, [c] =>
    let (n, ws) := condState heap c
    match setCondState heap c n (if all then [] else ws.drop 1) with
    | .ok heap => .ok (.unit, heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .globalAddr key zero, [] =>
    let dir := match heap[0]? with | some (.arr es) => es | _ => []
    let found := dir.findSome? fun e => match e with
      | .tuple [.int _ k, p] => if k.toNat == key then some p else none
      | _ => none
    match found with
    | some p => .ok (p, heap)
    | none =>
      let (p, heap) := heapAlloc heap zero
      .ok (p, heap.set! 0 (.arr (dir ++ [.tuple [.int 64 (BitVec.ofNat 64 key), p]])))
  | _, _ => .panic .modelStuck

/-- Appends bytes to the string cell a `strings.Builder` pointer `p` holds;
yields `(n, nil)` as `WriteString` and `Write` do. -/
def sbAppend (heap : Heap Val) (p : Val) : Option (List Nat) → Res (Val × Heap Val)
  | some bs =>
    match heapLoad heap p with
    | .ok cur =>
      let old := match strBytes heap cur with | some x => x | none => []
      if old.length + bs.length > strLimit then .panic .modelStuck else
      let (v, heap) := strAlloc heap (old ++ bs)
      match heapStore heap p v with
      | .ok heap => .ok (.tuple [.int 64 (BitVec.ofNat 64 bs.length), .err false], heap)
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | .panic k => .panic k
    | .outOfFuel => .panic .modelStuck
  | none => .panic .modelStuck

/-- All instructions: heap-touching ones here, the rest via `denotePure`. -/
def denote (or : Oracle) (heap : Heap Val) : Op → List Val → Res (Val × Heap Val)
  | .alloc zero, [] => .ok (heapAlloc heap (match zero with | .arr es => mkArr es | z => z))
  | .makeMap, [] =>
    let (p, heap) := heapAlloc heap (.arr [])
    .ok (.boxed 1 p, heap)
  | .base64Encode, [.slice ref off len _, .int _ k] =>
    match strBytes heap (.str ref off len) with
    | some bs => .ok (strAlloc heap (base64EncK k.toNat bs))
    | none => .panic .modelStuck
  | .base64Decode, [s, .int _ k] =>
    match strBytes heap s with
    | some bs =>
      match base64DecK k.toNat bs with
      | some out =>
        let (p, heap) := heapAlloc heap (.arr (out.map fun b => .int 8 (BitVec.ofNat 8 b)))
        match p with
        | .ptr ref _ => .ok (.tuple [.slice ref 0#64 (BitVec.ofNat 64 out.length) (BitVec.ofNat 64 out.length), .err false], heap)
        | _ => .panic .modelStuck
      | none => .ok (.tuple [.slice 0 0#64 0#64 0#64, .err true], heap)
    | none => .panic .modelStuck
  | .jsonMarshal t root, [v] =>
    match jEnc t heap root v with
    | .ok bs =>
      let (p, heap) := heapAlloc heap (.arr (bs.map fun b => .int 8 (BitVec.ofNat 8 b)))
      match p with
      | .ptr ref _ => .ok (.tuple [.slice ref 0#64 (BitVec.ofNat 64 bs.length) (BitVec.ofNat 64 bs.length), .err false], heap)
      | _ => .panic .modelStuck
    | .error true => .ok (.tuple [.slice 0 0#64 0#64 0#64, .err true], heap)
    | .error false => .panic .modelStuck
  | .jsonEncodeTo t root html, [p, v] =>
    match jEnc t heap root v with
    | .ok bs =>
      let bs := (if html then bs else htmlUnescape bs) ++ [10]
      match heapLoad heap p with
      | .ok cur =>
        let old := match strBytes heap cur with | some x => x | none => []
        if old.length + bs.length > strLimit then .panic .modelStuck else
        let (s, heap) := strAlloc heap (old ++ bs)
        match heapStore heap p s with
        | .ok heap => .ok (.err false, heap)
        | .panic k => .panic k
        | .outOfFuel => .outOfFuel
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | .error true => .ok (.err true, heap)
    | .error false => .panic .modelStuck
  | .jsonValid, [.slice ref off len _] =>
    match strBytes heap (.str ref off len) with
    | some bs => .ok (.bool (match Lean.Json.parse (utf8Lossy bs) with | .ok _ => true | .error _ => false), heap)
    | none => .panic .modelStuck
  | .jsonDecode t root, [data, p] =>
    match data with
    | .slice ref off len _ =>
      match strBytes heap (.str ref off len) with
      | some bs =>
        match Lean.Json.parse (utf8Lossy bs) with
        | .error _ => .ok (.err true, heap)
        | .ok j =>
          match heapLoad heap p with
          | .ok cur =>
            match jDec t heap root j (jTrim bs) cur with
            | .ok (v, heap, err) =>
              match heapStore heap p v with
              | .ok heap => .ok (.err err, heap)
              | .panic k => .panic k
              | .outOfFuel => .outOfFuel
            | .error () => .panic .modelStuck
          | .panic k => .panic k
          | .outOfFuel => .outOfFuel
      | none => .panic .modelStuck
    | _ => .panic .modelStuck
  | .jsonUnmarshal kind tags, [data, p] =>
    match data with
    | .slice ref off len _ =>
      match strBytes heap (.str ref off len) with
      | some bs => jsonUnmarshalOp heap kind tags bs p
      | none => .panic .modelStuck
    | _ => .panic .modelStuck
  | .mapLookup commaOk zero, [m, k] =>
    let found : Option Val := match m with
      | .opaqueNil => some zero
      | _ => (mapEntries heap m).map fun (_, es) =>
        ((es.find? fun (k', _) => keyEq heap k k').map (·.2)).getD zero
    let hit : Bool := match mapEntries heap m with
      | some (_, es) => es.any fun (k', _) => keyEq heap k k'
      | none => false
    match found with
    | some v => .ok (if commaOk then .tuple [v, .bool hit] else v, heap)
    | none => .panic .modelStuck
  | .mapUpdate, [.opaqueNil, _, _] => .panic .nilMapWrite
  | .mapUpdate, [m, k, v] =>
    match mapEntries heap m with
    | some (p, es) =>
      let es := if es.any (fun (k', _) => keyEq heap k k') then
          es.map fun (k', v') => if keyEq heap k k' then (k', v) else (k', v')
        else es ++ [(k, v)]
      match heapStore heap p (mapObj es) with
      | .ok heap => .ok (.unit, heap)
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | none => .panic .modelStuck
  | .mapDelete, [.opaqueNil, _] => .ok (.unit, heap)
  | .mapDelete, [m, k] =>
    match mapEntries heap m with
    | some (p, es) =>
      match heapStore heap p (mapObj (es.filter fun (k', _) => !keyEq heap k k')) with
      | .ok heap => .ok (.unit, heap)
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | none => .panic .modelStuck
  | .mapLen, [.opaqueNil] => .ok (.int 64 0#64, heap)
  | .mapLen, [m] =>
    match mapEntries heap m with
    | some (_, es) => .ok (.int 64 (BitVec.ofNat 64 es.length), heap)
    | none => .panic .modelStuck
  | .mapRange, [m] =>
    let (p, heap) := heapAlloc heap (.tuple [m, .int 64 0#64])
    .ok (p, heap)
  | .mapNext zk zv, [it] =>
    match heapLoad heap it with
    | .ok (.tuple [m, .int 64 i]) =>
      match mapEntries heap m with
      | some (_, es) =>
        match es[i.toNat]? with
        | some (k, v) =>
          match heapStore heap it (.tuple [m, .int 64 (i + 1)]) with
          | .ok heap => .ok (.tuple [.bool true, k, v], heap)
          | .panic k => .panic k
          | .outOfFuel => .outOfFuel
        | none => .ok (.tuple [.bool false, zk, zv], heap)
      -- A nil map, or one the model knows nothing about: no entries.
      | none => .ok (.tuple [.bool false, zk, zv], heap)
    | _ => .panic .modelStuck
  | .loadPath, [p] =>
    match heapLoad heap p with
    | .ok v => .ok (v, heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .storePath, [p, v] =>
    match heapStore heap p v with
    | .ok heap => .ok (.unit, heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .strConst bs, [] => .ok (strAlloc heap bs)
  | .strConcat, [a, b] =>
    match strBytes heap a, strBytes heap b with
    | some x, some y => if x.length + y.length > strLimit then .panic .modelStuck else .ok (strAlloc heap (x ++ y))
    | _, _ => .panic .modelStuck
  | .eqDeep, [a, b] => match eqDeep heap a b with
    | some r => .ok (.bool r, heap) | none => .panic .modelStuck
  | .neDeep, [a, b] => match eqDeep heap a b with
    | some r => .ok (.bool !r, heap) | none => .panic .modelStuck
  | .deepEqualOk, [a, b] => .ok (.bool (reflectDeepEq heap deepEqLevels a b).isSome, heap)
  | .deepEqual, [a, b] => match reflectDeepEq heap deepEqLevels a b with
    | some r => .ok (.bool r, heap)
    | none => .panic .modelStuck
  | .sprintfOk, [f, args, .int _ bt, .arr us] =>
    match strBytes heap f, sliceElems heap args with
    | some fb, some xs => .ok (.bool ((sprintfBytes heap bt.toNat (natsOf us) fb xs).isSome), heap)
    | _, _ => .ok (.bool false, heap)
  | .sprintf, [f, args, .int _ bt, .arr us] =>
    match strBytes heap f, sliceElems heap args with
    | some fb, some xs =>
      match sprintfBytes heap bt.toNat (natsOf us) fb xs with
      | some r => if r.length > strLimit then .panic .modelStuck else .ok (strAlloc heap r)
      | none => .panic .modelStuck
    | _, _ => .panic .modelStuck
  | .hexEncode, [.slice ref off len _] =>
    match strBytes heap (.str ref off len) with
    | some x => .ok (strAlloc heap (hexDigits x))
    | none => .panic .modelStuck
  | .freshArr n, [] =>
    let k := heap.size
    let (_, heap) := heapAlloc heap .unit
    .ok (.arr ((List.range n).map fun i => .int 8 (BitVec.ofNat 8 (k / 256 ^ (n - 1 - i)))), heap)
  | .bytesToStr, [.slice ref off len _] =>
    match strBytes heap (.str ref off len) with
    | some x => .ok (strAlloc heap x)
    | none => .panic .modelStuck
  | .strToBytes, [s] =>
    match strBytes heap s with
    | some x =>
      let (p, heap) := heapAlloc heap (.arr (x.map fun b => .int 8 (BitVec.ofNat 8 b)))
      match p with
      | .ptr ref _ => .ok (.slice ref 0#64 (BitVec.ofNat 64 x.length) (BitVec.ofNat 64 x.length), heap)
      | _ => .panic .modelStuck
    | none => .panic .modelStuck
  | .strToRunes, [s] =>
    match strBytes heap s with
    | some x =>
      let rs := decodeAll x
      let (p, heap) := heapAlloc heap (.arr (rs.map fun r => .int 32 (BitVec.ofNat 32 r)))
      match p with
      | .ptr ref _ => .ok (.slice ref 0#64 (BitVec.ofNat 64 rs.length) (BitVec.ofNat 64 rs.length), heap)
      | _ => .panic .modelStuck
    | none => .panic .modelStuck
  | .runesToStr, [rs] =>
    match sliceElems heap rs with
    | some es =>
      match es.mapM (fun | .int _ v => some v.toNat | _ => none) with
      | some rs => .ok (strAlloc heap (rs.flatMap encodeRune))
      | none => .panic .modelStuck
    | none => .panic .modelStuck
  | .runeToStr, [.int _ v] => .ok (strAlloc heap (encodeRune v.toNat))
  | .utf16Encode, [rs] =>
    match sliceElems heap rs with
    | some es =>
      match es.mapM (fun | .int _ v => some v.toInt | _ => none) with
      | some rs =>
        let units := rs.flatMap fun r =>
          if (0 ≤ r && r < 0xD800) || (0xE000 ≤ r && r < 0x10000) then [r.toNat]
          else if 0x10000 ≤ r && r ≤ 0x10FFFF then
            let r' := r.toNat - 0x10000
            [0xD800 + r' / 0x400, 0xDC00 + r' % 0x400]
          else [0xFFFD]
        let (p, heap) := heapAlloc heap (.arr (units.map fun u => .int 16 (BitVec.ofNat 16 u)))
        match p with
        | .ptr ref _ => .ok (.slice ref 0#64 (BitVec.ofNat 64 units.length) (BitVec.ofNat 64 units.length), heap)
        | _ => .panic .modelStuck
      | none => .panic .modelStuck
    | none => .panic .modelStuck
  | .strNext, [p] =>
    match heapLoad heap p with
    | .ok (.tuple [s, .int _ pos]) =>
      match strBytes heap s with
      | some bs =>
        let i := pos.toNat
        if i < bs.length then
          let (r, n) := decodeRune bs i
          match heapStore heap p (.tuple [s, .int 64 (BitVec.ofNat 64 (i + n))]) with
          | .ok heap => .ok (.tuple [.bool true, .int 64 (BitVec.ofNat 64 i), .int 32 (BitVec.ofNat 32 r)], heap)
          | _ => .panic .modelStuck
        else .ok (.tuple [.bool false, .int 64 0#64, .int 32 0#32], heap)
      | none => .panic .modelStuck
    | _ => .panic .modelStuck
  | .strItoa, [.int _ v] => .ok (strAlloc heap (itoaBytes v.toInt))
  | .strParseInt, [s, .int _ base, .int _ bits] =>
    match strBytes heap s with
    | some x =>
      let (v, e) := parseIntGo x base.toInt bits.toInt
      .ok (.tuple [.int 64 (BitVec.ofInt 64 v), .err (e != 0)], heap)
    | none => .panic .modelStuck
  | .strParseUint, [s, .int _ base, .int _ bits] =>
    match strBytes heap s with
    | some x =>
      let (v, e) := parseUintGo x base.toInt bits.toInt
      .ok (.tuple [.int 64 (BitVec.ofNat 64 v), .err (e != 0)], heap)
    | none => .panic .modelStuck
  | .strParseFloat, [s] =>
    match strBytes heap s with
    | some x =>
      match parseFloatGo x with
      | some (v, e) => .ok (.tuple [v, .err e], heap)
      | none => .panic .modelStuck
    | none => .panic .modelStuck
  | .strParseFloatKnown, [s] =>
    match strBytes heap s with
    | some x => .ok (.bool (parseFloatGo x).isSome, heap)
    | none => .panic .modelStuck
  | .strParseBool, [s] =>
    match strBytes heap s with
    | some x =>
      let t := [[49], [116], [84], [84, 82, 85, 69], [116, 114, 117, 101], [84, 114, 117, 101]]
      let f := [[48], [102], [70], [70, 65, 76, 83, 69], [102, 97, 108, 115, 101], [70, 97, 108, 115, 101]]
      if t.contains x then .ok (.tuple [.bool true, .err false], heap)
      else if f.contains x then .ok (.tuple [.bool false, .err false], heap)
      else .ok (.tuple [.bool false, .err true], heap)
    | none => .panic .modelStuck
  | .strFormatInt, [.int _ v, .int _ b] =>
    if b.toInt < 2 || b.toInt > 36 then .panic .explicit
    else
      let ds := formatNatGo v.toInt.natAbs b.toNat
      .ok (strAlloc heap (if v.toInt < 0 then 45 :: ds else ds))
  | .strFormatUint, [.int _ v, .int _ b] =>
    if b.toInt < 2 || b.toInt > 36 then .panic .explicit
    else .ok (strAlloc heap (formatNatGo v.toNat b.toNat))
  | .randRead, [.slice _ _ len _] => .ok (.tuple [.int 64 len, .err false], heap)
  | .strToLower, [s] =>
    match strBytes heap s with
    | some x => .ok (strAlloc heap (x.map lowerByte))
    | none => .panic .modelStuck
  | .strToUpper, [s] =>
    match strBytes heap s with
    | some x => .ok (strAlloc heap (x.map upperByte))
    | none => .panic .modelStuck
  | .strReplacer pairs, [_, s] =>
    match strBytes heap s with
    | some x =>
      let r := replacePairs pairs x
      if r.length > strLimit then .panic .modelStuck else .ok (strAlloc heap r)
    | none => .panic .modelStuck
  | .strReplaceAll, [s, o, n] =>
    match strBytes heap s, strBytes heap o, strBytes heap n with
    | some x, some old, some new =>
      let r := replaceBytes x old new
      if r.length > strLimit then .panic .modelStuck else .ok (strAlloc heap r)
    | _, _, _ => .panic .modelStuck
  | .errNew, _ => .ok (.err true, heap)
  | .sbString, [p] =>
    match heapLoad heap p with
    | .ok s@(.str ..) => .ok (s, heap)
    | .ok _ => .ok (.str 0 0#64 0#64, heap) -- a zero Builder cell holds the empty string
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .sbLen, [p] =>
    match heapLoad heap p with
    | .ok (.str _ _ len) => .ok (.int 64 len, heap)
    | .ok _ => .ok (.int 64 0#64, heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .sbReset, [p] =>
    match heapStore heap p (.str 0 0#64 0#64) with
    | .ok heap => .ok (.unit, heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .sbWriteString, [p, s] => sbAppend heap p (strBytes heap s)
  -- A byte slice's backing array is laid out as a string's.
  | .sbWrite, [p, .slice ref off len _] => sbAppend heap p (strBytes heap (.str ref off len))
  | .sbWriteByte, [p, .int _ c] =>
    match heapLoad heap p with
    | .ok cur =>
      let old := match strBytes heap cur with | some x => x | none => []
      if old.length ≥ strLimit then .panic .modelStuck else
      let (v, heap) := strAlloc heap (old ++ [c.toNat % 256])
      match heapStore heap p v with
      | .ok heap => .ok (.err false, heap)
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .sbWriteRune, [p, .int _ r] =>
    match heapLoad heap p with
    | .ok cur =>
      let old := match strBytes heap cur with | some x => x | none => []
      let enc := encodeRune r.toNat
      if old.length + enc.length > strLimit then .panic .modelStuck else
      let (v, heap) := strAlloc heap (old ++ enc)
      match heapStore heap p v with
      | .ok heap => .ok (.tuple [.int 64 (BitVec.ofNat 64 enc.length), .err false], heap)
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .strRepeat, [s, .int _ n] =>
    if n.msb then .panic .explicit -- strings: negative Repeat count
    else
      match strBytes heap s with
      | some x =>
        if n.toNat * x.length > 4096 then .panic .modelStuck
        else .ok (strAlloc heap ((List.replicate n.toNat x).flatten))
      | none => .panic .modelStuck
  | .reFind prog start ncap kind, [s] => reFindRun heap prog start ncap kind s
  | .reFind prog start ncap kind, [s, .int _ n] => reFindAllRun heap prog start ncap kind s n.toInt
  | .reFindIn _ kind, [.boxed _ (.str r o l), s] =>
    match strBytes heap (.str r o l) >>= fun b => (reCompile b).toOption with
    | some (prog, start, ncap) => reFindRun heap prog start ncap kind s
    | none => .panic .modelStuck
  | .reFindIn _ kind, [.boxed _ (.str r o l), s, .int _ n] =>
    match strBytes heap (.str r o l) >>= fun b => (reCompile b).toOption with
    | some (prog, start, ncap) => reFindAllRun heap prog start ncap kind s n.toInt
    | none => .panic .modelStuck
  | .reFindIn progs kind, [.boxed _ (.int _ id), s] =>
    match progs.find? (fun (i, _) => i == id.toNat) with
    | some (_, prog, start, ncap) => reFindRun heap prog start ncap kind s
    | none => .panic .modelStuck
  | .reFindIn progs kind, [.boxed _ (.int _ id), s, .int _ n] =>
    match progs.find? (fun (i, _) => i == id.toNat) with
    | some (_, prog, start, ncap) => reFindAllRun heap prog start ncap kind s n.toInt
    | none => .panic .modelStuck
  | .strSplit, [s, sep] =>
    match strBytes heap s, strBytes heap sep with
    | some x, some y => .ok (allocStrSlice heap (splitBytes x y))
    | _, _ => .panic .modelStuck
  | .strFields, [s] =>
    match strBytes heap s with
    | some x => .ok (allocStrSlice heap (fieldsBytes x))
    | none => .panic .modelStuck
  | .strJoin, [elems, sep] =>
    match strSliceBytes heap elems, strBytes heap sep with
    | some xs, some y =>
      let r := (xs.intersperse y).flatten
      if r.length > strLimit then .panic .modelStuck else .ok (strAlloc heap r)
    | _, _ => .panic .modelStuck
  | .appendSlice zero, [s, t] =>
    match sliceElems heap t with
    | none => .panic .modelStuck
    | some [] => .ok (s, heap) -- append(s) is s, nil included
    | some ts =>
      match s with
      | .slice 0 _ _ _ =>
        let (p, heap) := heapAlloc heap (mkArr ts)
        match p with
        | .ptr ref _ => .ok (.slice ref 0#64 (BitVec.ofNat 64 ts.length) (BitVec.ofNat 64 ts.length), heap)
        | _ => .panic .modelStuck
      | .slice ref off len cap =>
        match heap[ref]? with
        | some (.arr es) =>
          let newLen := len.toNat + ts.length
          if newLen ≤ cap.toNat then
            -- Room in the backing array: write in place (aliases stay live).
            .ok (.slice ref off (BitVec.ofNat 64 newLen) cap,
                 heap.set! ref (.arr (writeAt es (off.toNat + len.toNat) ts)))
          else
            -- Grow: Go doubles small slices; the exact capacity is
            -- implementation-defined and only visible through `cap`.
            let newCap := max (2 * cap.toNat) newLen
            -- The model's size bound, like fuel: a run that would grow a
            -- slice past it ends out of fuel (no behaviour of the program).
            if newCap > sliceBound then .outOfFuel
            else
              let contents := (es.drop off.toNat).take len.toNat ++ ts ++ List.replicate (newCap - newLen) zero
              let (p, heap) := heapAlloc heap (mkArr contents)
              match p with
              | .ptr ref' _ => .ok (.slice ref' 0#64 (BitVec.ofNat 64 newLen) (BitVec.ofNat 64 newCap), heap)
              | _ => .panic .modelStuck
        | some (.arrA es) =>
          let newLen := len.toNat + ts.length
          if newLen ≤ cap.toNat then
            -- In place: the array is taken out of the heap first (#71).
            let heap := heap.set! ref .unit
            .ok (.slice ref off (BitVec.ofNat 64 newLen) cap,
                 heap.set! ref (.arrA (writeAtA es (off.toNat + len.toNat) ts)))
          else
            let newCap := max (2 * cap.toNat) newLen
            if newCap > sliceBound then .outOfFuel
            else
              let contents := (es.extract off.toNat (off.toNat + len.toNat)).toList ++ ts ++ List.replicate (newCap - newLen) zero
              let (p, heap) := heapAlloc heap (mkArr contents)
              match p with
              | .ptr ref' _ => .ok (.slice ref' 0#64 (BitVec.ofNat 64 newLen) (BitVec.ofNat 64 newCap), heap)
              | _ => .panic .modelStuck
        | _ => .panic .modelStuck
      | _ => .panic .modelStuck
  | .copySlice, [d, t] =>
    match sliceElems heap t, d with
    | some ts, .slice 0 _ _ _ => .ok (.int 64 (BitVec.ofNat 64 (min 0 ts.length)), heap)
    | some ts, .slice ref off len _ =>
      let n := min len.toNat ts.length
      match heap[ref]? with
      | some (.arr es) =>
        .ok (.int 64 (BitVec.ofNat 64 n), heap.set! ref (.arr (writeAt es off.toNat (ts.take n))))
      | some (.arrA es) =>
        let heap := heap.set! ref .unit
        .ok (.int 64 (BitVec.ofNat 64 n), heap.set! ref (.arrA (writeAtA es off.toNat (ts.take n))))
      | _ => .panic .modelStuck
    | _, _ => .panic .modelStuck
  | .conc c, args => denoteConc heap c args
  | .chanMake zero, [cap] =>
    match asIndex cap with
    | some c => if c.slt 0 then .panic .makeSliceLen else .ok (heapAlloc heap (chanObj c [] false zero))
    | none => .panic .modelStuck
  | .chanLen, [ch] =>
    match chanGet heap ch with
    | some (_, _, buf, _, _) => .ok (.int 64 (BitVec.ofNat 64 buf.length), heap)
    | none => if chanIsNil ch then .ok (.int 64 0, heap) else .panic .modelStuck
  | .chanCap, [ch] =>
    match chanGet heap ch with
    | some (_, c, _, _, _) => .ok (.int 64 c, heap)
    | none => if chanIsNil ch then .ok (.int 64 0, heap) else .panic .modelStuck
  | .onceExit, [p] =>
    match heapStore heap p (.int 64 2) with
    | .ok heap => .ok (.unit, heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | .makeSlice zero, [len, cap] =>
    match asIndex len, asIndex cap with
    | some len, some cap =>
      -- A negative length or capacity panics, as in Go.
      if len.slt 0 || cap.slt 0 then .panic .makeSliceLen else
      if len.ule cap && cap.toNat ≤ sliceBound then
        let (p, heap) := heapAlloc heap (mkArr (List.replicate cap.toNat zero))
        match p with
        | .ptr ref _ => .ok (.slice ref 0#64 len cap, heap)
        | _ => .panic .modelStuck
      -- Too large to materialise: the model's size bound, like fuel.
      else if len.ule cap then .outOfFuel
      else .panic .makeSliceLen
    | _, _ => .panic .modelStuck
  | op, args =>
    match denotePure or heap op args with
    | .ok v => .ok (v, heap)
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel

/-- A stuck JSON decode names the text it was decoding. -/
def stuckSite (heap : Heap Val) : Op → List Val → Option String
  | .jsonDecode _ root, [.slice ref off len _, _] =>
    (strBytes heap (.str ref off len)).map fun bs => s!"jsonDecode into entry {root}: {utf8Lossy (bs.take 240)}"
  | _, _ => none

/-- The under-approximations an op takes on these operands (recorded by
the core; see `Dialect.assumption` and docs/semantics.md). -/
def assumption (heap : Heap Val) : Op → List Val → Option String
  | .eq, [.err true, .err true] | .ne, [.err true, .err true] =>
    some "two non-nil errors the model does not tell apart compared equal"
  | .mapNext .., [it] =>
    match heapLoad heap it with
    | .ok (.tuple [.opaqueNil, _]) => none
    | .ok (.tuple [m, _]) =>
      match mapEntries heap m with
      | some _ => none
      | none => some "a range over a map the model knows nothing about ran zero times"
    | _ => none
  | _, _ => none

/-- The heap object a map or slice value lives in. -/
def objOf : Val → Val
  | .boxed _ p => p
  | .slice r _ _ _ => .ptr r []
  | v => v

/-- The heap accesses of an op, for the race detector: reads (0), writes
(1), atomic acquires (2) and releases (3). Maps and slices are one location
each (their backing object); a field or element through a pointer is its
own. -/
def accesses : Op → List Val → List (Val × Nat)
  | .loadPath, [p] => [(p, 0)]
  | .storePath, [p, _] => [(p, 1)]
  | .mapLookup _ _, m :: _ => [(objOf m, 0)]
  | .mapLen, [m] => [(objOf m, 0)]
  | .mapRange, [m] => [(objOf m, 0)]
  | .mapUpdate, m :: _ => [(objOf m, 1)]
  | .mapDelete, m :: _ => [(objOf m, 1)]
  | .appendSlice _, [s, t] => [(objOf t, 0), (objOf s, 1)]
  | .copySlice, [d, t] => [(objOf t, 0), (objOf d, 1)]
  | .bytesToStr, [b] => [(objOf b, 0)]
  | .conc .atomicLoad, [p] => [(p, 2), (p, 4)]
  | .conc .atomicStore, [p, _] => [(p, 5), (p, 3)]
  | _, _ => []

/-- `denote` with the heap owned (#71): the heap comes back on every
outcome, so the interpreter hands it over without keeping a reference of
its own, and stores and allocations update it in place (a shared heap is
copied whole by each of them, which makes a loop over a large array, or one
that allocates, quadratic). Other ops go through `denote`. `denoteH_spec`:
the results are `denote`'s. -/
def denoteH (or : Oracle) (heap : Heap Val) (o : Op) (args : List Val) : Res Val × Heap Val :=
  match o, args with
  | .alloc zero, [] =>
    let (p, heap) := heapAlloc heap (match zero with | .arr es => mkArr es | z => z)
    (.ok p, heap)
  | .makeMap, [] =>
    let (p, heap) := heapAlloc heap (.arr [])
    (.ok (.boxed 1 p), heap)
  | .storePath, [p, v] =>
    match storeCheck heap p with
    | some k => (.panic k, heap)
    | none =>
      match heapStore heap p v with
      | .ok heap => (.ok .unit, heap)
      | .panic k => (.panic k, #[])
      | .outOfFuel => (.outOfFuel, #[])
  | o, args =>
    match denote or heap o args with
    | .ok (v, heap') => (.ok v, heap')
    | .panic k => (.panic k, heap)
    | .outOfFuel => (.outOfFuel, heap)

theorem denoteH_spec (or : Oracle) (heap : Heap Val) (o : Op) (args : List Val) :
    denoteH or heap o args =
      match denote or heap o args with
      | .ok (v, heap') => (.ok v, heap')
      | .panic k => (.panic k, heap)
      | .outOfFuel => (.outOfFuel, heap) := by
  unfold denoteH
  split
  · simp [denote]
  · simp [denote]
  · rename_i p v
    have h := heapStore_of_storeCheck heap p v
    split at h <;> rename_i hc
    · obtain ⟨h', hh⟩ := h
      simp [denote, hc, hh]
    · simp [denote, hc, h]
  · rfl

instance : Dialect Op Val Oracle where
  denote := denote
  denoteH := denoteH
  accesses := accesses
  stuckSite := stuckSite
  assumption := assumption
  toBool
    | .bool b => some b
    | _ => none
  callee
    | .boxed 0 (.tuple (.int _ f :: bound)) => .known f.toNat bound
    | .opaqueNil => .nil
    | _ => .unknown

abbrev Prog := SSA.Prog Op
abbrev Func := SSA.Func Op
abbrev Block := SSA.Block Op
abbrev Instr := SSA.Instr Op

/-! The equation lemmas of the operations' semantics, made here (#94).
`simp` asks for them when a proof unfolds the dialect. A lemma made later,
in the module that asks, is made under that module's default heartbeat
limit (200000), not under the options of the proof, and `denotePure`'s
lemmas no longer fit in it. Made here, under this file's limit, they are
part of this module. -/
open Lean Meta in
run_meta for d in [``denotePure, ``denote, ``denoteConc] do discard <| getEqnsFor? d

end Go2Lean.GoSSA
