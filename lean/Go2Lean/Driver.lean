import Go2Lean.Search

/-! The native search driver: a JSON description of a search, written by
M2 (`internal/constraint`), decoded into the generators and run against a
model from the registry. Running the engines in a compiled executable
instead of `#eval` avoids interpreting the interpreter. -/

namespace Go2Lean.Search

open Lean

/-- Decode a zero value described by `gossa.zeroModel`. -/
partial def valOfJson (j : Json) : Except String GoSSA.Val := do
  match ← j.getObjValAs? String "k" with
  | "int" => let w ← j.getObjValAs? Nat "w"; return .int w 0
  | "bool" => return .bool false
  | "err" => return .err false
  | "str" => return .str 0 0#64 0#64
  | "nil" => return .opaqueNil
  | "ptr" => return .ptr 0 []
  | "slice" => return .slice 0 0#64 0#64 0#64
  | "struct" => return .structv (← ((← j.getObjValAs? (Array Json) "fields").toList.mapM valOfJson))
  | "arr" => return .arr (← ((← j.getObjValAs? (Array Json) "elems").toList.mapM valOfJson))
  | _ => return .unit

/-- Decode a generator; the shape mirrors `constraint.genJSON` on the Go side. -/
partial def genOfJson (t : GoSSA.JTable) (j : Json) : Except String Gen := do
  let k ← j.getObjValAs? String "k"
  let name := (j.getObjValAs? String "name").toOption.getD ""
  match k with
  | "int" => return .int name (← j.getObjValAs? Nat "w") (← j.getObjValAs? Bool "signed")
  | "bool" => return .bool name
  | "err" => return .err name
  | "slice" =>
    return .slice name (← j.getObjValAs? Nat "w") (← j.getObjValAs? Bool "signed") (← j.getObjValAs? Nat "maxLen")
  | "sliceOf" => return .sliceOf name (← genOfJson t (← j.getObjVal? "elem")) (← j.getObjValAs? Nat "maxLen")
  | "structv" => return .structv name (← (← j.getObjValAs? (Array Json) "fields").toList.mapM (genOfJson t))
  | "hot" => return .hot (← genOfJson t (← j.getObjVal? "inner"))
  | "sdkDoc" => return .sdkDoc name t (← j.getObjValAs? Nat "desc") (← genOfJson t (← j.getObjVal? "inner"))
  | "ptr" => return .ptr name (← genOfJson t (← j.getObjVal? "pointee"))
  | "link" => return .link name (← genOfJson t (← j.getObjVal? "pointee"))
  | "str" =>
    let enum := ((j.getObjValAs? (List String) "enum").toOption.getD []).map
      fun s => s.toUTF8.toList.map (·.toNat)
    return .str name (← j.getObjValAs? Nat "maxLen") enum
  | "nilptr" => return .nilptr name
  | "ref" => return .ref name (← j.getObjValAs? Nat "ty")
  | "opaque" =>
    if let .ok z := j.getObjVal? "zero" then
      return .opaque name (← valOfJson z)
    let v := match (j.getObjValAs? String "v").toOption with
      | some "nil" => .opaqueNil
      | some "nonnil" => .boxed 0 .unit
      | _ => .unit
    return .opaque name v
  | "arrOf" => return .arrOf name (← genOfJson t (← j.getObjVal? "elem")) (← j.getObjValAs? Nat "n")
  | "mapOf" =>
    return .mapOf name (← genOfJson t (← j.getObjVal? "key")) (← genOfJson t (← j.getObjVal? "val")) (← j.getObjValAs? Nat "maxLen")
  | "jsonText" =>
    let strs (key : String) : List (List Nat) :=
      ((j.getObjValAs? (List String) key).toOption.getD []).map fun s => s.toUTF8.toList.map (·.toNat)
    return .jsonText name (strs "keys") (strs "vals")
  | "anyOf" =>
    let alts ← (← j.getObjValAs? (Array Json) "alts").toList.mapM fun a => do
      return ((← a.getObjValAs? Nat "tag"), (← genOfJson t (← a.getObjVal? "gen")))
    return .anyOf name alts
  | other => throw s!"unknown generator kind {other}"

def propertyOfJson (j : Json) : Except String Property := do
  match ← j.getObjValAs? String "kind" with
  | "noPanic" => return .noPanic (← j.getObjValAs? Nat "f")
  | "postcondition" => return .postcondition (← j.getObjValAs? Nat "f") (← j.getObjValAs? Nat "spec")
  | "sequence" => return .sequence (← j.getObjValAs? Nat "f")
  | "noHang" => return .noHang (← j.getObjValAs? Nat "f")
  | other => throw s!"unknown property kind {other}"

/-- One search: everything `run` or `exhaust` takes, plus the model's key. -/
structure Spec where
  prog : String
  engine : String
  prop : Property
  gens : List Gen
  extra : List Nat
  strs : List String
  table : TypeTable
  allowNil : Bool
  seed : Nat
  trials : Nat
  fuel : Nat
  maxLen : Nat
  limit : Nat
  budgetMs : Nat
  /-- For the mutation engine: the functions whose mutants it tries. -/
  mutate : List Nat := []
  maxMutants : Nat := 24

/-- `tables`: each model's JSON table, for SDK values generated as decoded
documents (`sdkDoc`). -/
def specOfJson (tables : List (String × GoSSA.JTable)) (j : Json) : Except String Spec := do
  let t := (tables.lookup (← j.getObjValAs? String "prog")).getD { descs := #[], zeros := #[] }
  let gens ← (← j.getObjValAs? (Array Json) "gens").toList.mapM (genOfJson t)
  let table ← (← j.getObjValAs? (Array (Array Json)) "table").mapM fun fs => fs.toList.mapM (genOfJson t)
  return {
    prog := ← j.getObjValAs? String "prog"
    engine := ← j.getObjValAs? String "engine"
    prop := ← propertyOfJson (← j.getObjVal? "prop")
    gens, table
    extra := ← j.getObjValAs? (List Nat) "extra"
    strs := ← j.getObjValAs? (List String) "strs"
    allowNil := ← j.getObjValAs? Bool "allowNil"
    seed := ← j.getObjValAs? Nat "seed"
    trials := ← j.getObjValAs? Nat "trials"
    fuel := ← j.getObjValAs? Nat "fuel"
    maxLen := ← j.getObjValAs? Nat "maxLen"
    limit := ← j.getObjValAs? Nat "limit"
    budgetMs := (j.getObjValAs? Nat "budgetMs").toOption.getD 0
    mutate := (j.getObjValAs? (List Nat) "mutate").toOption.getD []
    maxMutants := (j.getObjValAs? Nat "maxMutants").toOption.getD 24 }

def runSpec (p : GoSSA.Prog) (s : Spec) : IO UInt32 := do
  match s.engine with
  | "random" => run p s.prop s.gens s.extra s.seed s.trials s.fuel s.strs s.table s.allowNil s.budgetMs; return 0
  | "mutate" => mutate p s.prop s.mutate s.gens s.extra s.seed s.trials s.fuel s.strs s.table s.allowNil s.budgetMs s.maxMutants; return 0
  | "exhaustive" => exhaust p s.prop s.gens s.extra s.maxLen s.limit s.fuel s.strs s.table s.allowNil s.budgetMs; return 0
  | other => IO.eprintln s!"unknown engine {other}"; return 2

-- Linking ---------------------------------------------------------------------

/-- The models a program calls into through `xcall` terminators. -/
def xrefs (p : GoSSA.Prog) : List String :=
  p.funcs.foldl (init := []) fun acc fn =>
    fn.blocks.foldl (init := acc) fun acc blk =>
      let acc := blk.instrs.foldl (init := acc) fun acc i =>
        match i with
        | .op _ (.xfuncRef pkg _) _ => if acc.contains pkg then acc else pkg :: acc
        | _ => acc
      match blk.term with
      | .xcall pkg .. => if acc.contains pkg then acc else pkg :: acc
      | _ => acc

/-- The functions of `p` renumbered for a merged program: FuncIds move up by
`fbase`, havoc sites by `hbase`, and an `xcall` into a model whose base
`base` knows becomes a plain `call`. -/
def shiftFuncs (p : GoSSA.Prog) (fbase hbase : Nat) (base : String → Option Nat) :
    Array GoSSA.Func :=
  p.funcs.map fun fn =>
    { fn with blocks := fn.blocks.map fun blk =>
      { blk with
        instrs := blk.instrs.map fun
          | .havoc dst site => .havoc dst (site + hbase)
          | .op dst (.funcRef f) args => .op dst (.funcRef (f + fbase)) args
          | .op dst (.xfuncRef pkg f) args =>
            match base pkg with
            | some b => .op dst (.funcRef (f + b)) args
            | none => .op dst (.xfuncRef pkg f) args
          | i => i
        term := match blk.term with
          | .call f args dst next nargs fb => .call (f + fbase) args dst next nargs fb
          | .xcall pkg f args dst next nargs fb =>
            match base pkg with
            | some b => .call (f + b) args dst next nargs fb
            | none => .xcall pkg f args dst next nargs fb
          | t => t } }

/-- The models reachable from `todo` through `xcall`s, in discovery order. -/
partial def closure (registry : List (String × GoSSA.Prog)) (todo seen : List String) :
    List String :=
  match todo with
  | [] => seen.reverse
  | pkg :: rest =>
    if seen.contains pkg then closure registry rest seen
    else match registry.lookup pkg with
      | none => closure registry rest seen
      | some p => closure registry (rest ++ xrefs p) (pkg :: seen)

/-- Merge the models a search of `main` needs into one program: `main`
first, so its FuncIds and havoc sites keep their numbers (the property and
the reports refer to them), then every model it reaches through `xcall`,
renumbered. An `xcall` into a model the registry does not hold stays as it
is and gets the interpreter stuck, as an unmodelled call would. -/
def link (registry : List (String × GoSSA.Prog)) (main : String) : Option GoSSA.Prog := do
  let _ ← registry.lookup main
  let order := closure registry [main] []
  let (bases, _, _) := order.foldl (init := (([] : List (String × Nat × Nat)), 0, 0))
    fun (acc, fb, hb) pkg =>
      match registry.lookup pkg with
      | some p => ((pkg, fb, hb) :: acc, fb + p.funcs.size, hb + p.havocWidths.size)
      | none => (acc, fb, hb)
  let fbaseOf := fun pkg => (bases.lookup pkg).map (·.1)
  let funcs := order.foldl (init := #[]) fun acc pkg =>
    match registry.lookup pkg, bases.lookup pkg with
    | some p, some (fb, hb) => acc ++ shiftFuncs p fb hb fbaseOf
    | _, _ => acc
  let widths := order.foldl (init := #[]) fun acc pkg =>
    match registry.lookup pkg with
    | some p => acc ++ p.havocWidths
    | none => acc
  let origin := order.foldl (init := #[]) fun acc pkg =>
    match registry.lookup pkg with
    | some p => acc ++ (Array.range p.havocWidths.size).map (pkg, ·)
    | none => acc
  -- The schedule: the first model that has one, its site renumbered.
  let sched := order.findSome? fun pkg =>
    match registry.lookup pkg, bases.lookup pkg with
    | some p, some (_, hb) => p.schedSite.map (· + hb)
    | _, _ => none
  -- The initializers: the models `main` reaches first, as Go initializes
  -- a package's imports before it.
  let inits := order.reverse.foldl (init := #[]) fun acc pkg =>
    match registry.lookup pkg, bases.lookup pkg with
    | some p, some (fb, _) => acc ++ p.inits.map (· + fb)
    | _, _ => acc
  return { funcs, havocWidths := widths, siteOrigin := origin, schedSite := sched, inits }

/-- Entry point of the `go2lean-search` executable: `go2lean-search spec.json`,
with the models in `registry`. -/
def driverMain (registry : List (String × GoSSA.Prog)) (tables : List (String × GoSSA.JTable)) (args : List String) : IO UInt32 := do
  match args with
  | [path] =>
    let txt ← IO.FS.readFile path
    match Json.parse txt >>= specOfJson tables with
    | .error e => IO.eprintln s!"{path}: {e}"; return 2
    | .ok spec =>
      match link registry spec.prog with
      | none => IO.eprintln s!"{path}: no model named {spec.prog} in the registry (recompile)"; return 3
      | some p => runSpec p spec
  | _ => IO.eprintln "usage: go2lean-search <spec.json>"; return 2

end Go2Lean.Search
