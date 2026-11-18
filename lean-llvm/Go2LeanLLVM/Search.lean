import Lean.Data.Json
import Go2LeanLLVM.Dialect

/-!
# Search over LLVM models

The engines of the Go line, for functions of an LLVM model: seeded random
trials and, when the domain is small enough, exhaustive enumeration. The
property is that the function runs without undefined behaviour. A pointer
parameter points to a fresh object (a buffer of elements); an integer
parameter the frontend paired with it is its element count, so a caller
that passes the buffer's real length is all the search tries.
-/

namespace Go2Lean.LLVM.Search

open Go2Lean.SSA Go2Lean.LLVM Lean

/-- The C type of an object a pointer parameter points to, from the debug
information (#269): a struct is its size and members at their offsets; a
member pointer points to another typed object, unrolled to a fixed depth
(past it, `null`); `zero` is a member the search leaves at zero bytes
(floats, unions, bit-fields, function pointers). -/
inductive CTy where
  | int (bytes : Nat)
  /-- A NUL-terminated string (`char *`). -/
  | str
  | ptr (t : CTy)
  | null
  | struct (size : Nat) (fields : List (Nat × CTy))
  | zero (bytes : Nat)
  deriving Repr, Inhabited

/-- A drawn typed object: the value of a `CTy`. -/
inductive TVal where
  | int (bytes : Nat) (v : Int)
  | str (bytes : List Nat)
  | null
  /-- A pointer to a fresh object holding `v`. -/
  | ptr (v : TVal)
  | struct (size : Nat) (fields : List (Nat × TVal))
  | zero (bytes : Nat)
  deriving Repr, Inhabited

/-- How to generate one parameter. Mirrors the frontend's ABI. -/
inductive Gen where
  | int (name : String) (w : Nat)
  /-- A pointer to a fresh object of `n ≤ maxLen` elements of `elemBytes`
  bytes each. -/
  | buf (name : String) (elemBytes : Nat) (maxLen : Nat)
  /-- The element count of buffer parameter `of` (an index into the
  generators), as a `w`-bit integer. -/
  | len (name : String) (w : Nat) (of : Nat)
  /-- A null pointer (TinyGo's closure context of a plain function). -/
  | null (name : String)
  /-- A pointer to one fresh object of `size` bytes: random bytes when
  `random`, else zero (an object holding pointers). -/
  | obj (name : String) (size : Nat) (random : Bool)
  /-- An aggregate passed by value (a Go array or struct): random
  integers, null pointers. -/
  | agg (name : String) (t : Ty)
  /-- A buffer whose elements are zero (they hold pointers, or have a size
  that is not an integer width). -/
  | zbuf (name : String) (elemBytes : Nat) (maxLen : Nat)
  /-- A value of a C type from the debug information (a pointer to a
  struct, built member by member). -/
  | typed (name : String) (t : CTy)
  /-- A `w`-bit float (32 or 64) holding a drawn integer, so the C harness
  can pass the same integer. -/
  | fp (name : String) (w : Nat)
  deriving Repr, Inhabited

def Gen.name : Gen → String
  | .int n _ | .buf n _ _ | .len n _ _ | .null n | .obj n _ _ | .agg n _ | .zbuf n _ _ | .typed n _ | .fp n _ => n

/-- A generated argument before it is placed in memory. -/
inductive Arg where
  | int (w : Nat) (v : Int)
  | buf (elemBytes : Nat) (elems : List Int)
  | null
  | obj (bytes : List Nat)
  | val (v : Val)
  /-- `n` zero elements of `elemBytes` bytes. -/
  | zbuf (elemBytes n : Nat)
  | tree (v : TVal)
  /-- A `w`-bit float of the integer `v`. -/
  | fp (w : Nat) (v : Int)
  deriving Repr, Inhabited

/-- The bit pattern of the `w`-bit float nearest to `v`. -/
def fpBits (w : Nat) (v : Int) : Val :=
  if w == 32 then .int w (.val (BitVec.ofNat w (Float32.ofInt v).toBits.toNat))
  else .int w (.val (BitVec.ofNat w (Float.ofInt v).toBits.toNat))

/-- Interesting values of a `w`-bit integer. -/
def specials (w : Nat) : List Int :=
  let lim := (2 : Int) ^ (w - 1)
  [0, 1, -1, 2, lim - 1, -lim, lim / 2, 7, 255, 256, 65535, 1024]

def randInt (g : StdGen) (w : Nat) (extra : List Int := []) : Int × StdGen :=
  let (k, g) := randNat g 0 9
  if k < 2 && !extra.isEmpty then
    let (i, g) := randNat g 0 (extra.length - 1)
    ((BitVec.ofInt w extra[i]!).toInt, g)
  else if k < 4 then
    let sp := specials w
    let (i, g) := randNat g 0 (sp.length - 1)
    (sp[i]!, g)
  else if k < 8 then
    let (v, g) := randNat g 0 16
    let (s, g) := randNat g 0 1
    (if s == 0 then v else -(v : Int), g)
  else
    let (v, g) := randNat g 0 (2 ^ w - 1)
    ((BitVec.ofNat w v).toInt, g)

/-- Draw a value of a C type: a pointer is null a third of the time, a
string one of the program's string constants half the time. -/
partial def genTVal (g : StdGen) (extra : List Int) (strs : List (List Nat)) : CTy → TVal × StdGen
  | .int b =>
    let (v, g) := randInt g (b * 8) extra
    (.int b v, g)
  | .str =>
    let (k, g) := randNat g 0 1
    if k == 0 && !strs.isEmpty then
      let (i, g) := randNat g 0 (strs.length - 1)
      (.str strs[i]!, g)
    else
      let (n, g) := randNat g 0 4
      let (bs, g) := (List.range n).foldl (fun (acc, g) _ =>
        let (b, g) := randNat g 1 127; (acc ++ [b], g)) ([], g)
      (.str bs, g)
  | .null => (.null, g)
  | .zero b => (.zero b, g)
  | .ptr t =>
    let (k, g) := randNat g 0 2
    if k == 0 then (.null, g) else
    let (v, g) := genTVal g extra strs t
    (.ptr v, g)
  | .struct size fs =>
    let (vs, g) := fs.foldl (fun (acc, g) (off, t) =>
      let (v, g) := genTVal g extra strs t; (acc ++ [(off, v)], g)) ([], g)
    (.struct size vs, g)

/-- The smallest values of a C type, for the exhaustive engine: a null
pointer, and a pointer to an object of zeros and null pointers. -/
partial def CTy.zeroVal : CTy → TVal
  | .int b => .int b 0
  | .str => .str []
  | .null | .ptr _ => .null
  | .zero b => .zero b
  | .struct size fs => .struct size (fs.map fun (off, t) => (off, t.zeroVal))

def CTy.choices : CTy → List TVal
  | .ptr t => [.null, .ptr t.zeroVal]
  | t => [t.zeroVal]

/-- A typed value as JSON, for the counterexample (the C harness rebuilds
it). -/
partial def TVal.toJson : TVal → Json
  | .int b v => Json.mkObj [("k", "int"), ("b", b), ("v", toString v)]
  | .str bs => Json.mkObj [("k", "str"), ("v", Json.arr (bs.map fun (b : Nat) => (b : Json)).toArray)]
  | .null => Json.mkObj [("k", "null")]
  | .ptr v => Json.mkObj [("k", "ptr"), ("v", v.toJson)]
  | .struct size fs => Json.mkObj [("k", "struct"), ("size", size),
      ("fields", Json.arr (fs.map fun (off, v) => Json.mkObj [("off", off), ("v", v.toJson)]).toArray)]
  | .zero b => Json.mkObj [("k", "zero"), ("b", b)]

/-- Random arguments: buffers first (their lengths feed `len`). -/
def genArgs (gens : List Gen) (g : StdGen) (extra : List Int := []) (strs : List (List Nat) := []) :
    List Arg × StdGen := Id.run do
  let mut g := g
  let mut out : Array Arg := #[]
  for gen in gens do
    match gen with
    | .int _ w =>
      let (v, g') := randInt g w extra
      g := g'; out := out.push (.int w v)
    | .buf _ eb maxLen =>
      -- A byte buffer (a string): half the time one of the program's own
      -- string constants, so that comparisons against them can succeed.
      let pool := strs.filter (·.length ≤ maxLen)
      let (k, g') := randNat g 0 1
      g := g'
      if eb == 1 && k == 0 && !pool.isEmpty then
        let (i, g') := randNat g 0 (pool.length - 1)
        g := g'; out := out.push (.buf 1 (pool[i]!.map Int.ofNat))
        continue
      let (n, g') := randNat g 0 maxLen
      g := g'
      let mut es : Array Int := #[]
      for _ in [0:n] do
        let (v, g') := randInt g (eb * 8) extra
        g := g'; es := es.push v
      out := out.push (.buf eb es.toList)
    | .len _ w _ => out := out.push (.int w 0)
    | .null _ => out := out.push .null
    | .agg _ t =>
      let rec genTy (g : StdGen) : Ty → Val × StdGen
        | .int w =>
          let (v, g) := randInt g w extra
          (.int w (.val (BitVec.ofInt w v)), g)
        | .ptr => (.ptr Veir.Data.LLVM.Ptr.null, g)
        | .agg fs =>
          let (vs, g) := fs.foldl (fun (acc, g) f => let (v, g) := genTy g f; (acc ++ [v], g)) ([], g)
          (.agg vs, g)
      let (v, g') := genTy g t
      g := g'; out := out.push (.val v)
    | .zbuf _ eb maxLen =>
      let (n, g') := randNat g 0 maxLen
      g := g'; out := out.push (.zbuf eb n)
    | .obj _ size random =>
      let mut bs : Array Nat := #[]
      for _ in [0:size] do
        if random then
          let (b, g') := randNat g 0 255
          g := g'; bs := bs.push b
        else bs := bs.push 0
      out := out.push (.obj bs.toList)
    | .typed _ t =>
      let (v, g') := genTVal g extra strs t
      g := g'; out := out.push (.tree v)
    | .fp _ w =>
      let (v, g') := randInt g 16 extra
      g := g'; out := out.push (.fp w v)
  -- Each length is its buffer's element count.
  let fixed := (gens.zip out.toList).map fun (gen, a) =>
    match gen with
    | .len _ w of => match out[of]? with
      | some (.buf _ es) => Arg.int w es.length
      | some (.zbuf _ n) => Arg.int w n
      | _ => a
    | _ => a
  return (fixed, g)

/-- Allocate the globals and write their initial bytes and pointers. -/
def initGlobals (globals : List GlobalInit) : Option (Veir.MemoryState × Array Val) := do
  let mut m := Veir.MemoryState.empty
  let mut ptrs : Array Veir.Data.Pointer := #[]
  for g in globals do
    let (m', p) ← (match m.alloc g.size.toUInt64 with | .ok r => some r | _ => none)
    m := m'
    let bs := g.bytes ++ List.replicate (g.size - g.bytes.length) 0
    if g.size > 0 then
      m ← (match m.store p (ByteArray.mk (bs.map (·.toUInt8)).toArray) with | .ok m => some m | _ => none)
    ptrs := ptrs.push p
  for (g, p) in globals.zip ptrs.toList do
    for (off, j, addend) in g.relocs do
      let t ← ptrs[j]?
      let q : Veir.Data.LLVM.Ptr := .val ⟨t.object, UInt64.ofInt (t.address.toNat + addend)⟩
      m ← (match m.storeByte64 ⟨p.object, p.address + off.toUInt64⟩ q.toByte with | .ok m => some m | _ => none)
  return (m, ptrs.map fun p => .ptr (.val p))

/-- The bytes a typed value takes where it is stored: a pointer, a
string's pointer included, is 8. -/
def TVal.size : TVal → Nat
  | .int b _ | .zero b => b
  | .str _ | .null | .ptr _ => 8
  | .struct size _ => size

mutual
/-- Write a typed value at `addr` (the object is zero already). -/
partial def writeTVal (m : Veir.MemoryState) (addr : Veir.Data.Pointer) : TVal → Option Veir.MemoryState
  | .int b v =>
    match m.llvmStore addr (.int (b * 8) (.val (BitVec.ofInt (b * 8) v))) with
    | .ok m => some m | _ => none
  | .zero _ => some m
  | .null =>
    match m.storeByte64 addr (Veir.Data.LLVM.Ptr.null).toByte with
    | .ok m => some m | _ => none
  | .ptr v => do
    let (m, q) ← allocTVal m v
    let q' : Veir.Data.LLVM.Ptr := .val q
    match m.storeByte64 addr q'.toByte with | .ok m => some m | _ => none
  | .str bs => do
    let (m, q) ← (match m.alloc (bs.length + 1).toUInt64 with | .ok r => some r | _ => none)
    let m ← (match m.store q (ByteArray.mk ((bs ++ [0]).map (fun (b : Nat) => b.toUInt8)).toArray) with | .ok m => some m | _ => none)
    let q' : Veir.Data.LLVM.Ptr := .val q
    match m.storeByte64 addr q'.toByte with | .ok m => some m | _ => none
  | .struct _ fs => fs.foldlM (fun m (off, v) =>
      writeTVal m ⟨addr.object, addr.address + off.toUInt64⟩ v) m

/-- A fresh zeroed object holding a typed value; its address. -/
partial def allocTVal (m : Veir.MemoryState) (v : TVal) : Option (Veir.MemoryState × Veir.Data.Pointer) := do
  let n := v.size
  let (m, p) ← (match m.alloc n.toUInt64 with | .ok r => some r | _ => none)
  let m ← if n > 0 then
      (match m.store p (ByteArray.mk (Array.replicate n 0)) with | .ok m => some m | _ => none)
    else some m
  let m ← writeTVal m p v
  return (m, p)
end

/-- Place the arguments: buffers become fresh objects in memory, after the
globals. -/
def materialise (args : List Arg) (globals : List GlobalInit := []) : Option (List Val × Heap Val) := do
  let (m0, gptrs) ← initGlobals globals
  let mut m := m0
  let mut vals : Array Val := #[]
  for a in args do
    match a with
    | .int w v => vals := vals.push (.int w (.val (BitVec.ofInt w v)))
    | .fp w v => vals := vals.push (fpBits w v)
    | .buf eb es =>
      let (m', p) ← (match m.alloc (eb * es.length).toUInt64 with | .ok r => some r | _ => none)
      m := m'
      let mut i := 0
      for e in es do
        let q : Veir.Data.Pointer := ⟨p.object, p.address + (i * eb).toUInt64⟩
        m ← (match m.llvmStore q (.int (eb * 8) (.val (BitVec.ofInt (eb * 8) e))) with
          | .ok m => some m | _ => none)
        i := i + 1
      vals := vals.push (.ptr (.val p))
    | .null => vals := vals.push (.ptr Veir.Data.LLVM.Ptr.null)
    | .val v => vals := vals.push v
    | .zbuf eb n =>
      let (m', p) ← (match m.alloc (eb * n).toUInt64 with | .ok r => some r | _ => none)
      m := m'
      if eb * n > 0 then
        m ← (match m.store p (ByteArray.mk (Array.replicate (eb * n) 0)) with | .ok m => some m | _ => none)
      vals := vals.push (.ptr (.val p))
    | .obj bs =>
      let (m', p) ← (match m.alloc bs.length.toUInt64 with | .ok r => some r | _ => none)
      m := m'
      if !bs.isEmpty then
        m ← (match m.store p (ByteArray.mk (bs.map (·.toUInt8)).toArray) with | .ok m => some m | _ => none)
      vals := vals.push (.ptr (.val p))
    | .tree .null => vals := vals.push (.ptr Veir.Data.LLVM.Ptr.null)
    | .tree (.ptr v) =>
      let (m', p) ← allocTVal m v
      m := m'
      vals := vals.push (.ptr (.val p))
    | .tree (.str bs) =>
      let (m', p) ← (match m.alloc (bs.length + 1).toUInt64 with | .ok r => some r | _ => none)
      m := m'
      m ← (match m.store p (ByteArray.mk ((bs ++ [0]).map (fun (b : Nat) => b.toUInt8)).toArray) with
        | .ok m => some m | _ => none)
      vals := vals.push (.ptr (.val p))
    | .tree (.int b v) => vals := vals.push (.int (b * 8) (.val (BitVec.ofInt (b * 8) v)))
    | .tree _ => vals := vals.push (.ptr Veir.Data.LLVM.Ptr.null)
  return (vals.toList, #[.unit, .mem m] ++ gptrs)

/-- Havoc values for unmodelled calls: `w`-bit integers from the seed. -/
def havocOf (seed : Nat) (widths : Array Nat) : Havoc Val := fun site iter =>
  let w := widths[site]?.getD 64
  let g := mkStdGen (seed * 1000003 + site * 7919 + iter)
  .int w (.val (BitVec.ofInt w (randInt g w).1))

inductive Outcome where
  | holds
  | ub (k : PanicKind)
  | stuck
  | outOfFuel

def runOnce (p : Prog) (globals : List GlobalInit) (fid : FuncId) (args : List Arg) (seed fuel : Nat)
    (init : Option FuncId := none) (spec : Bool := false) : Outcome × HState Val :=
  match materialise args globals with
  | none => (.stuck, {})
  | some (vals, heap) =>
    let h := havocOf seed p.havocWidths
    let s : HState Val := { heap := heap }
    -- The package's initialisation first (TinyGo's `pkg.init(context)`):
    -- package variables set at run time hold their values.
    let s := match init with
      | some i => match runFunc p () h false i [.ptr Veir.Data.LLVM.Ptr.null] s fuel with
        | (.ok _, s') => some s'
        | _ => none
      | none => some s
    match s with
    | none => (.stuck, {})
    | some s =>
    match runFunc p () h false fid vals s fuel with
    -- A spec (#270) that returns 0: its property is violated.
    | (.ok (.int _ (.val v)), s) => if spec && v.toNat == 0 then (.ub .explicit, s) else (.holds, s)
    | (.ok _, s) => (.holds, s)
    | (.panic k, s) => (if k.isStuck then .stuck else .ub k, s)
    | (.outOfFuel, s) => (.outOfFuel, s)

def argJson (gen : Gen) (a : Arg) : List (String × Json) × List (String × Json) :=
  match gen, a with
  | _, .int _ v | _, .fp _ v => ([(gen.name, Json.str (toString v))], [])
  | _, .buf _ es => ([], [(gen.name, Json.arr (es.map (Json.str ∘ toString)).toArray)])
  | _, .null => ([], [])
  | _, .obj bs => ([], [(gen.name, Json.arr (bs.map (Json.str ∘ toString)).toArray)])
  | _, .val v => ([(gen.name, Json.str v.str)], [])
  | _, .zbuf _ n => ([], [(gen.name, Json.arr (List.replicate n (Json.str "0")).toArray)])
  | _, .tree v => ([(gen.name, Json.str v.toJson.compress)], [])

def report (gens : List Gen) (args : List Arg) (k : PanicKind) (s : HState Val)
    (trial seed : Nat) (engine : String) : Json :=
  let parts := (gens.zip args).map fun (g, a) => argJson g a
  Json.mkObj [("found", true), ("engine", engine), ("trial", trial), ("seed", seed),
    ("inputs", Json.mkObj (parts.flatMap (·.1))), ("memory", Json.mkObj (parts.flatMap (·.2))),
    ("model_output", Json.mkObj [("res", "panic"), ("panic", k.tag)]),
    ("panic_at", (s.panicAt.map Json.str).getD Json.null),
    ("havoc", Json.arr (s.log.map fun (site, iter, v) =>
      Json.mkObj [("idx", site), ("iter", iter), ("value", (repr v).pretty)])),
    ("assumptions", Json.arr (s.assumed.map Json.str))]

/-- A run that read no havoc value and took no under-approximation: its
counterexample is the model's own, not a guess about unmodelled code. -/
def clean (s : HState Val) : Bool := s.log.isEmpty && s.assumed.isEmpty

/-- Small values for exhaustive enumeration. -/
def smallInts (w : Nat) (extra : List Int := []) : List Int :=
  let lim := (2 : Int) ^ (w - 1)
  if w ≤ 2 then (List.range (2 ^ w)).map (fun n => (BitVec.ofNat w n).toInt)
  else
    let base := [0, 1, -1, 2, lim - 1, -lim]
    base ++ ((extra.map fun e => (BitVec.ofInt w e).toInt).filter (!base.contains ·)).eraseDups

def tuples (xs : List α) : Nat → List (List α)
  | 0 => [[]]
  | n + 1 => xs.flatMap fun x => (tuples xs n).map (x :: ·)

def genChoices (exLen : Nat) (extra : List Int := []) (strs : List (List Nat) := []) : Gen → List Arg
  | .int _ w => (smallInts w extra).map (.int w)
  | .buf _ eb maxLen =>
    (List.range (min exLen maxLen + 1)).flatMap (fun n =>
      (tuples [0, 1, -1] n).map (.buf eb)) ++
    (if eb == 1 then (strs.filter (·.length ≤ maxLen)).map fun s => .buf 1 (s.map Int.ofNat) else [])
  | .len _ w _ => [.int w 0]
  | .null _ => [.null]
  | .obj _ size _ => [.obj (List.replicate size 0)]
  | .agg _ t => [.val t.zero]
  | .zbuf _ eb maxLen => (List.range (min exLen maxLen + 1)).map (.zbuf eb)
  | .typed _ t => t.choices.map .tree
  | .fp _ w => (smallInts 16 extra).map (.fp w)

def domainSize (exLen : Nat) (gens : List Gen) (extra : List Int := []) (strs : List (List Nat) := []) : Nat :=
  gens.foldl (fun acc g => acc * (genChoices exLen extra strs g).length) 1

def enumArgs (exLen : Nat) (gens : List Gen) (extra : List Int := []) (strs : List (List Nat) := []) :
    List (List Arg) :=
  let raw := gens.foldr (fun g acc => (genChoices exLen extra strs g).flatMap fun a => acc.map (a :: ·)) [[]]
  raw.map fun args => (gens.zip args).map fun (gen, a) =>
    match gen with
    | .len _ w of => match args[of]? with
      | some (.buf _ es) => Arg.int w es.length
      | some (.zbuf _ n) => Arg.int w n
      | _ => a
    | _ => a

structure Spec where
  func : FuncId
  gens : List Gen
  trials : Nat := 2000
  seed : Nat := 42
  fuel : Nat := 5000
  engine : String := "all"
  exhaustLen : Nat := 3
  exhaustLimit : Nat := 300000
  /-- Constants of the program, tried as integer values. -/
  extra : List Int := []
  /-- String constants of the program (bytes), tried as byte buffers. -/
  strs : List (List Nat) := []
  /-- A spec function (#270): a run that returns 0 is a violation. -/
  spec : Bool := false
  /-- The package's initialisation function, run before each trial. -/
  init : Option FuncId := none

def search (p : Prog) (globals : List GlobalInit) (sp : Spec) : Json := Id.run do
  let mut stuck := 0
  let mut fuelOut := 0
  let mut sites : List (String × Nat) := []
  -- The first counterexample that went through havoc or an assumption is
  -- kept while the search looks for one that did not (as the Go line's
  -- engines do), and reported, marked, only if none turns up.
  let mut fallback : Option Json := none
  if sp.engine != "exhaustive" then
    let mut g := mkStdGen sp.seed
    for t in [0:sp.trials] do
      let (args, g') := genArgs sp.gens g sp.extra sp.strs
      g := g'
      match runOnce p globals sp.func args (sp.seed + t) sp.fuel sp.init sp.spec with
      | (.ub k, s) =>
        let j := report sp.gens args k s t sp.seed "random"
        if clean s then return j
        if fallback.isNone then fallback := some j
      | (.stuck, s) =>
        stuck := stuck + 1
        let site := (s.stuck.getD "argument setup").take 120 |>.toString
        sites := match sites.lookup site with
          | some n => sites.map fun (k, c) => if k == site then (k, n + 1) else (k, c)
          | none => if sites.length < 20 then (site, 1) :: sites else sites
      | (.outOfFuel, _) => fuelOut := fuelOut + 1
      | _ => pure ()
  let dom := domainSize sp.exhaustLen sp.gens sp.extra sp.strs
  let mut exhausted := false
  -- Under-approximations the exhaustive runs took (a callee the model got
  -- stuck in, whose effects are dropped).
  let mut assumed : Array String := #[]
  if sp.engine != "random" && dom ≤ sp.exhaustLimit then
    let mut i := 0
    let mut clean' := true
    for args in enumArgs sp.exhaustLen sp.gens sp.extra sp.strs do
      match runOnce p globals sp.func args 0 sp.fuel sp.init sp.spec with
      | (.ub k, s) =>
        let j := report sp.gens args k s i 0 "exhaustive"
        if clean s then return j
        if fallback.isNone then fallback := some j
        clean' := false
      | (.holds, s) =>
        -- A run that read the result of an unmodelled call held for the
        -- values guessed, not for every value: no exhaustion.
        if !s.log.isEmpty then
          clean' := false
          if assumed.size < 32 && !assumed.contains "values of unmodelled calls" then
            assumed := assumed.push "values of unmodelled calls"
        for a in s.assumed do
          if !assumed.contains a && assumed.size < 32 then assumed := assumed.push a
      | _ => clean' := false
      i := i + 1
    exhausted := clean'
  if let some j := fallback then
    return j.setObjVal! "havoc_dependent" true
  let top := (sites.toArray.qsort (fun a b => a.2 > b.2)).toList.take 3
  return Json.mkObj [("found", false), ("trials", sp.trials), ("stuck", stuck),
    ("out_of_fuel", fuelOut), ("domain", dom), ("exhausted", exhausted),
    ("assumptions", Json.arr (assumed.map Json.str)),
    ("stuck_sites", Json.mkObj (top.map fun (k, n) => (k, Json.num n)))]

partial def tyOfJson (j : Json) : Except String Ty := do
  match ← j.getObjValAs? String "k" with
  | "int" => return .int (← j.getObjValAs? Nat "w")
  | "ptr" => return .ptr
  | "agg" => return .agg (← (← j.getObjValAs? (Array Json) "fields").toList.mapM tyOfJson)
  | k => throw s!"unknown type {k}"

partial def ctyOfJson (j : Json) : Except String CTy := do
  match ← j.getObjValAs? String "k" with
  | "int" => return .int (← j.getObjValAs? Nat "b")
  | "str" => return .str
  | "null" => return .null
  | "zero" => return .zero (← j.getObjValAs? Nat "b")
  | "ptr" => return .ptr (← ctyOfJson (← j.getObjVal? "t"))
  | "struct" =>
    let fs ← (← j.getObjValAs? (Array Json) "fields").toList.mapM fun f => do
      return ((← f.getObjValAs? Nat "off"), (← ctyOfJson (← f.getObjVal? "t")))
    return .struct (← j.getObjValAs? Nat "size") fs
  | k => throw s!"unknown C type {k}"

/-- Decode a generator; the shape mirrors the frontend's ABI. -/
def genOfJson (j : Json) : Except String Gen := do
  let name ← j.getObjValAs? String "name"
  match ← j.getObjValAs? String "k" with
  | "int" => return .int name (← j.getObjValAs? Nat "w")
  | "buf" => return .buf name (← j.getObjValAs? Nat "eb") (← j.getObjValAs? Nat "maxLen")
  | "len" => return .len name (← j.getObjValAs? Nat "w") (← j.getObjValAs? Nat "of")
  | "null" => return .null name
  | "obj" => return .obj name (← j.getObjValAs? Nat "eb") ((j.getObjValAs? Bool "random").toOption.getD false)
  | "zbuf" => return .zbuf name (← j.getObjValAs? Nat "eb") (← j.getObjValAs? Nat "maxLen")
  | "agg" => return .agg name (← tyOfJson (← j.getObjVal? "ty"))
  | "typed" => return .typed name (← ctyOfJson (← j.getObjVal? "ty"))
  | "fp" => return .fp name (← j.getObjValAs? Nat "w")
  | k => throw s!"unknown generator {k}"

def specOfJson (j : Json) : Except String Spec := do
  let gens ← (← j.getObjValAs? (Array Json) "gens").toList.mapM genOfJson
  let opt (k : String) (d : Nat) : Nat := (j.getObjValAs? Nat k).toOption.getD d
  return { func := ← j.getObjValAs? Nat "func", gens,
           trials := opt "trials" 2000, seed := opt "seed" 42, fuel := opt "fuel" 5000,
           engine := (j.getObjValAs? String "engine").toOption.getD "all",
           exhaustLen := opt "exhaustLen" 3, exhaustLimit := opt "exhaustLimit" 300000,
           extra := ((j.getObjValAs? (Array Int) "extra").toOption.getD #[]).toList,
           strs := ((j.getObjValAs? (Array String) "strs").toOption.getD #[]).toList.map
             fun s => s.toUTF8.toList.map (·.toNat),
           init := (j.getObjValAs? Nat "init").toOption,
           spec := (j.getObjValAs? Bool "spec").toOption.getD false }

/-- The driver: `go2lean-llvm-search <spec.json>` with the spec naming a
model of the registry. -/
def driverMain (registry : List (String × Prog × List GlobalInit)) (args : List String) : IO UInt32 := do
  let [path] := args | IO.eprintln "usage: go2lean-llvm-search <spec.json>"; return 2
  let j ← IO.ofExcept (Json.parse (← IO.FS.readFile path))
  let model ← IO.ofExcept (j.getObjValAs? String "model")
  let some (p, globals) := registry.lookup model | IO.eprintln s!"no model {model}"; return 2
  let sp ← IO.ofExcept (specOfJson j)
  IO.println (search p globals sp).compress
  return 0

end Go2Lean.LLVM.Search
