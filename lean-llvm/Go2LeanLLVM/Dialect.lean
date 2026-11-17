import Go2Lean.SSA.Core
import Veir.Interpreter.Memory

/-!
# The LLVM dialect

LLVM IR over go2lean's SSA core. The meaning of each instruction is VeIR's:
integers are `Veir.Data.LLVM.Int` (a bit vector or poison) with VeIR's
arithmetic, flags and UB conditions, and memory is a `Veir.MemoryState`
(byte-addressed objects whose bytes may be poison; an access outside its
object is undefined behaviour). The core's heap holds that memory state in
slot 1 (slot 0 is the core's nil slot).

Undefined behaviour stops the run as a core panic: an access through null
is `nilDereference`, one outside its object `indexOutOfRange`, a division by
zero `divideByZero`, and every other kind `undefinedBehavior`. For C sources
the frontend sets `cUB` on arithmetic with `nsw` / `nuw`: C's signed overflow
is undefined behaviour where it happens, while LLVM only makes the result
poison.
-/

namespace Go2Lean.LLVM

open Go2Lean.SSA

abbrev LInt := Veir.Data.LLVM.Int
abbrev LPtr := Veir.Data.LLVM.Ptr
abbrev IntPred := Veir.Data.LLVM.IntPred

inductive Val where
  | int (w : Nat) (v : LInt w)
  | ptr (p : LPtr)
  /-- The memory state (only ever in heap slot 1). -/
  | mem (m : Veir.MemoryState)
  /-- A struct or array value in a register (TinyGo passes Go slices,
  strings and interfaces as such aggregates). -/
  | agg (fields : List Val)
  /-- A function of the program (a function pointer the frontend
  resolved). -/
  | fn (f : Nat)
  | unit
  deriving Inhabited

def intStr {w : Nat} : LInt w → String
  | .val b => toString b.toInt
  | .poison => "poison"

partial def Val.str : Val → String
  | .int w x => s!"i{w} {intStr x}"
  | .ptr (.val p) => s!"ptr {p.object}+{p.address}"
  | .ptr .poison => "ptr poison"
  | .mem _ => "<mem>"
  | .agg fs => "{" ++ ", ".intercalate (fs.map Val.str) ++ "}"
  | .fn f => s!"func #{f}"
  | .unit => "()"

instance : Repr Val where
  reprPrec v _ := v.str

/-- The type of a register value, for zero values of aggregates. -/
inductive Ty where
  | int (w : Nat)
  | ptr
  | agg (fields : List Ty)
  deriving Repr

partial def Ty.zero : Ty → Val
  | .int w => .int w (.val 0)
  | .ptr => .ptr Veir.Data.LLVM.Ptr.null
  | .agg fs => .agg (fs.map Ty.zero)

/-- Field `path` of an aggregate. -/
def extractPath : Val → List Nat → Option Val
  | v, [] => some v
  | .agg fs, i :: rest => fs[i]? >>= (extractPath · rest)
  | _, _ => none

/-- The aggregate with field `path` replaced by `x`. -/
def insertPath : Val → List Nat → Val → Option Val
  | _, [], x => some x
  | .agg fs, i :: rest, x => do
    let f ← fs[i]?
    return .agg (fs.set i (← insertPath f rest x))
  | _, _, _ => none

inductive Op where
  | const (w : Nat) (v : Int)
  | poison (w : Nat)
  | null
  /-- A pointer made from an integer constant (TinyGo's GC layout tags).
  Its value has no object: an access through it is stuck, never UB. -/
  | opaque
  /-- Arithmetic. `cUB`: overflow the flags make poison is C's undefined
  behaviour. -/
  | add (nsw nuw cUB : Bool)
  | sub (nsw nuw cUB : Bool)
  | mul (nsw nuw cUB : Bool)
  | udiv (exact : Bool)
  | sdiv (exact : Bool)
  | urem
  | srem
  | shl (nsw nuw : Bool)
  | lshr (exact : Bool)
  | ashr (exact : Bool)
  | and
  | or
  | xor
  | icmp (p : IntPred)
  /-- `icmp` on two pointers: their addresses compared. -/
  | icmpPtr (p : IntPred)
  | select
  | trunc (w : Nat)
  | zext (w : Nat)
  | sext (w : Nat)
  /-- `alloca` of `bytes` bytes. -/
  | alloca (bytes : Nat)
  /-- Load of a `w`-bit integer. -/
  | load (w : Nat)
  | loadPtr
  /-- `store v, p`: args `[v, p]`. -/
  | store
  /-- `getelementptr`: args `p :: idxs`; the address is `p + offset +
  Σ idx * stride` with each index signed. -/
  | gep (offset : Int) (strides : List Int)
  /-- A branch condition: undefined behaviour when poison. -/
  | cond
  /-- `unreachable` reached. -/
  | unreachable
  /-- The zero value of an aggregate (`zeroinitializer`). -/
  | zero (t : Ty)
  /-- `extractvalue agg, path`. -/
  | extract (path : List Nat)
  /-- `insertvalue agg, v, path`: args `[agg, v]`. -/
  | insert (path : List Nat)
  /-- Go's allocation (TinyGo's `runtime.alloc`): a fresh object of `size`
  bytes, zeroed. Args `[size]`. -/
  | goAlloc
  /-- C's `free(p)` (#269): null is a no-op; otherwise `p` must be the
  start of a live object, whose lifetime then ends: it holds no bytes, so
  every later access through it is out of bounds (a use after free), and a
  second `free` is undefined behaviour (a double free). -/
  | cfree
  /-- C's `realloc(p, size)`: a fresh object of `size` bytes holding the
  old one's first bytes (zero past them), and the old one freed. -/
  | realloc
  /-- A pointer to global variable `i` (its object is set up before the
  run; the pointer is in heap slot `2 + i`). -/
  | global (i : Nat)
  /-- The function a function pointer points to: args `[p]`; the table pairs
  the heap slot of each function's object with the function. A pointer to
  no function is unknown (the call takes its fallback). -/
  | resolveFn (table : List (Nat × Nat))
  /-- `llvm.memcpy` / `llvm.memmove`: args `[dst, src, len]`. -/
  | memcpy (move : Bool)
  /-- `llvm.memset`: args `[dst, byte, len]`. -/
  | memset
  /-- `llvm.smin` `smax` `umin` `umax` (`which` 0–3). -/
  | minmax (which : Nat)
  /-- `llvm.abs`: args `[x, isIntMinPoison]`. -/
  | abs
  /-- `llvm.ctlz` (0), `llvm.cttz` (1), `llvm.ctpop` (2); ctlz and cttz
  take `[x, isZeroPoison]`. -/
  | bitCount (which : Nat)
  /-- `llvm.fshl`: args `[a, b, c]`, the high half of `a:b << c % w`. -/
  | fshl
  /-- An instruction the frontend does not lower: the model is stuck. -/
  | unsupported (what : String)
  deriving Repr

/-- End the lifetime of the object `p` starts (`free`): none when `p` is
not the start of a live object (a double free, or a pointer into the
middle of one). A freed object keeps its index with no bytes; the
`globals` map records it, under a key no symbol has. -/
def freeObj (m : Veir.MemoryState) (p : Veir.Data.Pointer) : Option Veir.MemoryState := do
  let obj ← m.objects[p.object]?
  let key := s!"#freed{p.object}"
  if m.globals.contains key || p.address != obj.base then none
  return { m with objects := m.objects.set! p.object ⟨obj.base, 0⟩, globals := m.globals.insert key p.object }

/-- The memory state, from heap slot 1. -/
def memOf (heap : Heap Val) : Option Veir.MemoryState :=
  match (heap[1]? : Option Val) with
  | some (.mem m) => some m
  | _ => none

def setMem (heap : Heap Val) (m : Veir.MemoryState) : Heap Val :=
  if heap.size > 1 then heap.set! 1 (.mem m) else #[.unit, .mem m]

/-- A run of VeIR's interpreter monad as a core result. -/
def ofInterp (k : PanicKind) : Veir.Interp α → Res α
  | .ok a => .ok a
  | .ub _ => .panic k
  | .fail _ => .panic .modelStuck

/-- The largest `runtime.alloc` the model performs (bytes). -/
def maxAlloc : Nat := 1 <<< 20

/-- Leading zeros (0), trailing zeros (1) or set bits (2) of `x`. -/
def bitCount (which : Nat) {w : Nat} (x : LInt w) (zeroPoison : Bool) : LInt w :=
  match x with
  | .poison => .poison
  | .val b =>
    let n := b.toNat
    if zeroPoison && n == 0 && which < 2 then .poison else
    let bits := List.range w
    let r := match which with
      | 0 => if n == 0 then w else w - (Nat.log2 n + 1)
      | 1 => (bits.find? (fun i => n.testBit i)).getD w
      | _ => bits.countP (fun i => n.testBit i)
    .val (BitVec.ofNat w r)

def fshl {w : Nat} : LInt w → LInt w → LInt w → LInt w
  | .val a, .val b, .val c =>
    let k := c.toNat % w
    .val (if k == 0 then a else (a <<< k) ||| (b >>> (w - k)))
  | _, _, _ => .poison

/-- Binary integer ops of equal widths. -/
def bin (f : {w : Nat} → LInt w → LInt w → LInt w) (heap : Heap Val) : List Val → Res (Val × Heap Val)
  | [.int w a, .int w' b] => if h : w' = w then .ok (.int w (f a (b.cast h)), heap) else .panic .modelStuck
  | _ => .panic .modelStuck

/-- Arithmetic whose flags make overflow poison; with `cUB`, a poison result
from operands that were not poison is C's undefined behaviour. -/
def flagged (f : {w : Nat} → LInt w → LInt w → LInt w) (cUB : Bool) (heap : Heap Val) :
    List Val → Res (Val × Heap Val)
  | [.int w a, .int w' b] =>
    if h : w' = w then
      let b := b.cast h
      match f a b with
      | .poison =>
        if cUB && a != .poison && b != .poison then .panic .undefinedBehavior
        else .ok (.int w .poison, heap)
      | r => .ok (.int w r, heap)
    else .panic .modelStuck
  | _ => .panic .modelStuck

/-- The little-endian number in `bytes`. -/
def leNat (bytes : ByteArray) : Nat :=
  bytes.toList.foldr (fun b acc => acc * 256 + b.toNat) 0

/-- Check an access of `size` bytes at `p`, naming the kind of UB: through
null, or outside the object. -/
def checkAccess (m : Veir.MemoryState) (p : LPtr) (size : Nat) : Res Veir.Data.Pointer :=
  match p with
  | .poison => .panic .undefinedBehavior
  | .val p =>
    if p.object == 0 then .panic .nilDereference else
    match m.getObject? p with
    | some obj => if size == 0 || obj.holds p.address size then .ok p else .panic .indexOutOfRange
    | none => .panic .modelStuck

def loadIntAt (heap : Heap Val) (w : Nat) (p : LPtr) : Res (Val × Heap Val) :=
  match memOf heap with
  | some m =>
    if w % 8 != 0 then .panic .modelStuck else
    match checkAccess m p (w / 8) with
    | .ok p =>
      match ofInterp .indexOutOfRange (do
          let bytes ← m.load p (w / 8).toUInt64
          let poisoned ← m.hasPoison p (w / 8).toUInt64
          return (bytes, poisoned)) with
      | .ok (bytes, poisoned) =>
        .ok (.int w (if poisoned then .poison else .val (BitVec.ofNat w (leNat bytes))), heap)
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | none => .panic .modelStuck

def storeAt (heap : Heap Val) (v : Val) (p : LPtr) : Res (Val × Heap Val) :=
  match memOf heap with
  | some m =>
    let (rv, size) : Veir.RuntimeValue × Nat := match v with
      | .int w x => (.int w x, w / 8)
      | .ptr q => (.addr q, 8)
      | _ => (.int 0 .poison, 0)
    if size == 0 then .panic .modelStuck else
    match checkAccess m p size with
    | .ok p =>
      match ofInterp .indexOutOfRange (m.llvmStore p rv) with
      | .ok m => .ok (.unit, setMem heap m)
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | .panic k => .panic k
    | .outOfFuel => .outOfFuel
  | none => .panic .modelStuck

def denote (_ : Unit) (heap : Heap Val) : Op → List Val → Res (Val × Heap Val)
  | .const w v, [] => .ok (.int w (.val (BitVec.ofInt w v)), heap)
  | .poison w, [] => .ok (.int w .poison, heap)
  | .null, [] => .ok (.ptr Veir.Data.LLVM.Ptr.null, heap)
  | .opaque, [] => .ok (.unit, heap)
  | .add nsw nuw cUB, args => flagged (fun a b => Veir.Data.LLVM.Int.add a b nsw nuw) cUB heap args
  | .sub nsw nuw cUB, args => flagged (fun a b => Veir.Data.LLVM.Int.sub a b nsw nuw) cUB heap args
  | .mul nsw nuw cUB, args => flagged (fun a b => Veir.Data.LLVM.Int.mul a b nsw nuw) cUB heap args
  | .udiv exact, [.int w a, .int w' b] =>
    if h : w' = w then
      let b := b.cast h
      if b == .val 0 then .panic .divideByZero
      else if Veir.Data.LLVM.Int.isUnsignedDivisionUB b then .panic .undefinedBehavior
      else .ok (.int w (Veir.Data.LLVM.Int.udiv a b exact), heap)
    else .panic .modelStuck
  | .sdiv exact, [.int w a, .int w' b] =>
    if h : w' = w then
      let b := b.cast h
      if b == .val 0 then .panic .divideByZero
      else if Veir.Data.LLVM.Int.isSignedDivisionUB a b then .panic .undefinedBehavior
      else .ok (.int w (Veir.Data.LLVM.Int.sdiv a b exact), heap)
    else .panic .modelStuck
  | .urem, [.int w a, .int w' b] =>
    if h : w' = w then
      let b := b.cast h
      if b == .val 0 then .panic .divideByZero
      else if Veir.Data.LLVM.Int.isUnsignedDivisionUB b then .panic .undefinedBehavior
      else .ok (.int w (Veir.Data.LLVM.Int.urem a b), heap)
    else .panic .modelStuck
  | .srem, [.int w a, .int w' b] =>
    if h : w' = w then
      let b := b.cast h
      if b == .val 0 then .panic .divideByZero
      else if Veir.Data.LLVM.Int.isSignedDivisionUB a b then .panic .undefinedBehavior
      else .ok (.int w (Veir.Data.LLVM.Int.srem a b), heap)
    else .panic .modelStuck
  | .shl nsw nuw, args => bin (fun a b => Veir.Data.LLVM.Int.shl a b nsw nuw) heap args
  | .lshr exact, args => bin (fun a b => Veir.Data.LLVM.Int.lshr a b exact) heap args
  | .ashr exact, args => bin (fun a b => Veir.Data.LLVM.Int.ashr a b exact) heap args
  | .and, args => bin (fun a b => Veir.Data.LLVM.Int.and a b) heap args
  | .or, args => bin (fun a b => Veir.Data.LLVM.Int.or a b) heap args
  | .xor, args => bin (fun a b => Veir.Data.LLVM.Int.xor a b) heap args
  | .icmp p, [.int w a, .int w' b] =>
    if h : w' = w then .ok (.int 1 (Veir.Data.LLVM.Int.icmp a (b.cast h) p), heap) else .panic .modelStuck
  | .icmpPtr p, [.ptr a, .ptr b] =>
    match a, b with
    | .val a, .val b => .ok (.int 1 (.val (if p.eval a.address.toBitVec b.address.toBitVec then 1#1 else 0#1)), heap)
    | _, _ => .ok (.int 1 .poison, heap)
  | .select, [.int 1 c, x, y] =>
    match c with
    | .poison => .ok (match x with | .int w _ => .int w .poison | _ => .ptr .poison, heap)
    | .val b => .ok (if b == 1#1 then x else y, heap)
  | .trunc w', [.int w a] =>
    if h : w > w' then .ok (.int w' (Veir.Data.LLVM.Int.trunc a w' false false h), heap) else .panic .modelStuck
  | .zext w', [.int w a] =>
    if h : w < w' then .ok (.int w' (Veir.Data.LLVM.Int.zext a w' false h), heap) else .panic .modelStuck
  | .sext w', [.int w a] =>
    if h : w < w' then .ok (.int w' (Veir.Data.LLVM.Int.sext a w' h), heap) else .panic .modelStuck
  | .alloca bytes, [] =>
    match memOf heap with
    | some m =>
      match ofInterp .undefinedBehavior (m.alloc bytes.toUInt64) with
      | .ok (m, p) => .ok (.ptr (.val p), setMem heap m)
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | none => .panic .modelStuck
  | .load 1, [.ptr p] =>
    -- An i1 in memory is a byte.
    match loadIntAt heap 8 p with
    | .ok (.int 8 b, heap) => .ok (.int 1 (match b with
        | .poison => .poison
        | .val x => .val (if x == 0 then 0#1 else 1#1)), heap)
    | r => r
  | .load w, [.ptr p] => loadIntAt heap w p
  | .loadPtr, [.ptr p] =>
    match memOf heap with
    | some m =>
      match checkAccess m p 8 with
      | .ok p =>
        match ofInterp .indexOutOfRange (m.loadByte64 p) with
        | .ok b => .ok (.ptr (m.ptrOfByte b), heap)
        | .panic k => .panic k
        | .outOfFuel => .outOfFuel
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | none => .panic .modelStuck
  | .store, [.int 1 b, .ptr p] =>
    storeAt heap (.int 8 (match b with
      | .poison => .poison
      | .val x => .val (if x == 1#1 then 1#8 else 0#8))) p
  | .store, [v, .ptr p] => storeAt heap v p
  | .gep offset strides, .ptr p :: idxs =>
    match p with
    | .poison => .ok (.ptr .poison, heap)
    | .val p =>
      let step : Option Int → (Val × Int) → Option Int := fun acc (i, stride) =>
        match acc, i with
        | some a, .int _ (.val x) => some (a + x.toInt * stride)
        | _, _ => none
      match (idxs.zip strides).foldl step (some (p.address.toNat + offset)) with
      | some addr => .ok (.ptr (.val ⟨p.object, UInt64.ofInt addr⟩), heap)
      | none => .ok (.ptr .poison, heap)
  | .cond, [.int 1 c] =>
    match c with
    | .poison => .panic .undefinedBehavior
    | c => .ok (.int 1 c, heap)
  | .unreachable, [] => .panic .undefinedBehavior
  | .zero t, [] => .ok (t.zero, heap)
  | .extract path, [a] =>
    match extractPath a path with
    | some v => .ok (v, heap)
    | none => .panic .modelStuck
  | .insert path, [a, x] =>
    match insertPath a path x with
    | some v => .ok (v, heap)
    | none => .panic .modelStuck
  | .goAlloc, [.int _ (.val size)] =>
    -- A huge allocation is beyond what the model runs (Go may not panic).
    if size.toNat > maxAlloc then .panic .modelStuck else
    match memOf heap with
    | some m =>
      match ofInterp .undefinedBehavior (do
          let (m, p) ← m.alloc size.toNat.toUInt64
          let m ← if size.toNat == 0 then pure m else m.store p (ByteArray.mk (Array.replicate size.toNat 0))
          return (m, p)) with
      | .ok (m, p) => .ok (.ptr (.val p), setMem heap m)
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | none => .panic .modelStuck
  | .cfree, [.ptr p] =>
    match p with
    | .poison => .panic .undefinedBehavior
    | .val q =>
      if q.object == 0 then .ok (.unit, heap) else
      match memOf heap with
      | some m =>
        match freeObj m q with
        | some m => .ok (.unit, setMem heap m)
        | none => .panic .undefinedBehavior
      | none => .panic .modelStuck
  | .realloc, [.ptr p, .int _ (.val size)] =>
    if size.toNat > maxAlloc then .panic .modelStuck else
    match memOf heap with
    | some m =>
      match ofInterp .undefinedBehavior (do
          let (m, q) ← m.alloc size.toNat.toUInt64
          let m ← if size.toNat == 0 then pure m else m.store q (ByteArray.mk (Array.replicate size.toNat 0))
          return (m, q)) with
      | .ok (m', q) =>
        match p with
        | .val old =>
          if old.object == 0 then .ok (.ptr (.val q), setMem heap m') else
          match m'.objects[old.object]? with
          | some obj =>
            let n := min obj.size size.toNat
            let bytes := (List.range n).foldl (fun bs i =>
              bs.insert (q.address + i.toUInt64) (m'.byte (old.address + i.toUInt64))) m'.bytes
            match freeObj { m' with bytes } old with
            | some m'' => .ok (.ptr (.val q), setMem heap m'')
            | none => .panic .undefinedBehavior
          | none => .panic .undefinedBehavior
        | .poison => .panic .undefinedBehavior
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | none => .panic .modelStuck
  | .resolveFn table, [.ptr p] =>
    match p with
    | .val q =>
      if q.object == 0 then .ok (.ptr p, heap) else
      match table.find? (fun (slot, _) => match (heap[slot]? : Option Val) with
          | some (.ptr (.val r)) => r == q
          | _ => false) with
      | some (_, f) => .ok (.fn f, heap)
      | none => .ok (.unit, heap)
    | .poison => .panic .undefinedBehavior
  | .memcpy move, [.ptr d, .ptr s, .int _ (.val n)] =>
    if n == 0 then .ok (.unit, heap) else
    match memOf heap with
    | some m =>
      match checkAccess m d n.toNat, checkAccess m s n.toNat with
      | .ok d, .ok s =>
        match ofInterp .indexOutOfRange (if move then m.memmove d s n.toNat else m.memcpy d s n.toNat) with
        | .ok m => .ok (.unit, setMem heap m)
        | .panic k => .panic k
        | .outOfFuel => .outOfFuel
      | .panic k, _ | _, .panic k => .panic k
      | _, _ => .outOfFuel
    | none => .panic .modelStuck
  | .memset, [.ptr d, .int 8 b, .int _ (.val n)] =>
    if n == 0 then .ok (.unit, heap) else
    match memOf heap with
    | some m =>
      match checkAccess m d n.toNat with
      | .ok d =>
        match ofInterp .indexOutOfRange (m.memset d (.int 8 b) n.toNat) with
        | .ok m => .ok (.unit, setMem heap m)
        | .panic k => .panic k
        | .outOfFuel => .outOfFuel
      | .panic k => .panic k
      | .outOfFuel => .outOfFuel
    | none => .panic .modelStuck
  | .minmax which, args =>
    bin (fun a b => match which with
      | 0 => Veir.Data.LLVM.Int.smin a b
      | 1 => Veir.Data.LLVM.Int.smax a b
      | 2 => Veir.Data.LLVM.Int.umin a b
      | _ => Veir.Data.LLVM.Int.umax a b) heap args
  | .abs, [.int w a, .int 1 p] =>
    .ok (.int w (Veir.Data.LLVM.Int.abs a (p == .val 1#1)), heap)
  | .bitCount which, [.int w a, .int 1 p] => .ok (.int w (bitCount which a (p == .val 1#1)), heap)
  | .fshl, [.int w a, .int w₁ b, .int w₂ c] =>
    if h : w₁ = w ∧ w₂ = w then .ok (.int w (fshl a (b.cast h.1) (c.cast h.2)), heap) else .panic .modelStuck
  | .bitCount which, [.int w a] => .ok (.int w (bitCount which a false), heap)
  | .global i, [] =>
    match (heap[2 + i]? : Option Val) with
    | some v@(.ptr _) => .ok (v, heap)
    | _ => .panic .modelStuck
  | _, _ => .panic .modelStuck

/-- The bytes a string of hex digits spells (how the frontend writes the
initial bytes of globals: long list literals exhaust the elaborator). -/
def hexBytes (s : String) : List Nat :=
  let digit (c : Char) : Nat :=
    if c.isDigit then c.toNat - '0'.toNat else c.toNat - 'a'.toNat + 10
  let rec go : List Char → List Nat
    | a :: b :: rest => (digit a * 16 + digit b) :: go rest
    | _ => []
  go s.toList

/-- A global variable's initial memory: `size` bytes, of which `bytes` are
the start, and pointers `(offset, global, addend)` into other globals.
A function's object (what a pointer to it points to) is a global of one
byte. -/
structure GlobalInit where
  size : Nat
  bytes : List Nat
  relocs : List (Nat × Nat × Int)

instance : Dialect Op Val Unit where
  denote := denote
  toBool
    | .int 1 (.val b) => some (b == 1#1)
    | _ => none
  callee
    | .fn f => .known f []
    | .ptr (.val p) => if p.object == 0 then .nil else .unknown
    | _ => .unknown

abbrev Prog := Go2Lean.SSA.Prog Op
abbrev Func := Go2Lean.SSA.Func Op

end Go2Lean.LLVM
