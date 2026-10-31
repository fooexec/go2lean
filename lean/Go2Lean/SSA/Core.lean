import Std.Data.HashMap
/-!
# SSA core

A dialect-independent SSA program representation with an executable,
fuel-bounded semantics. The design follows lean-mlir's split between a
generic SSA core and dialects, but keeps only what go2lean needs:

* basic blocks with block parameters (phi nodes are lowered to them by the
  frontends), straight-line instructions and a terminator;
* a dialect that supplies the value type, the instruction set and the
  semantics of a single instruction; dialects cannot branch or call;
* explicit `havoc` instructions for values the frontend chose not to model
  (loads, unmodeled calls). Their values are extra inputs to the program, so a
  counterexample search can pick them and report them.

Every recursion is structural on `fuel`, so the interpreter is total and
usable from `#eval`, `decide` and, after unfolding, `bv_decide`.
-/

namespace Go2Lean.SSA

/-- Go runtime panic categories. `tag` matches `contract.PanicKind` on the Go side. -/
inductive PanicKind where
  | indexOutOfRange
  | divideByZero
  | nilDereference
  | negativeShift
  | sliceBounds
  | makeSliceLen
  /-- A failed single-result type assertion ("interface conversion"). -/
  | typeAssertion
  /-- A write to a nil map ("assignment to entry in nil map"). -/
  | nilMapWrite
  | explicit
  /-- Undefined behaviour of a C/LLVM program that is not one of the kinds
  above (signed overflow, a poison branch condition, `unreachable`). -/
  | undefinedBehavior
  /-- A send on a closed channel, a close of a closed or nil channel. -/
  | closedChannel
  /-- A fatal error of the `sync` package: unlock of an unlocked mutex,
  `RUnlock` without a reader. Not recoverable in Go. -/
  | syncFatal
  /-- The function under test is blocked and no goroutine can move: Go's
  "all goroutines are asleep - deadlock!". Reported by the concurrent
  machine like a panic. -/
  | deadlock
  /-- The function under test returned, and a goroutine it started is
  blocked with no goroutine left to unblock it. -/
  | goroutineLeak
  /-- Two goroutines access the same location, one of them writing, with
  neither access happening before the other (C2). -/
  | dataRace
  | other
  /-- Not a Go panic: the model got stuck (ill-typed operands, missing block).
  Signals a frontend bug; M3 reports it as inconclusive. -/
  | modelStuck
  deriving Repr, DecidableEq, Inhabited

/-- A stuck model says nothing about the program: `modelStuck` is an
ill-formed model, `other` is what a rejected callee's stub raises. -/
def PanicKind.isStuck : PanicKind → Bool
  | .modelStuck | .other => true
  | _ => false

def PanicKind.tag : PanicKind → String
  | .indexOutOfRange => "index_out_of_range"
  | .divideByZero => "divide_by_zero"
  | .nilDereference => "nil_dereference"
  | .negativeShift => "negative_shift"
  | .sliceBounds => "slice_bounds"
  | .makeSliceLen => "make_slice_len"
  | .typeAssertion => "type_assertion"
  | .nilMapWrite => "nil_map_write"
  | .explicit => "explicit"
  | .undefinedBehavior => "undefined_behavior"
  | .closedChannel => "closed_channel"
  | .syncFatal => "sync_fatal"
  | .deadlock => "deadlock"
  | .goroutineLeak => "goroutine_leak"
  | .dataRace => "data_race"
  | .other => "other"
  | .modelStuck => "model_stuck"

/-- Result of running a function. `outOfFuel` is an artefact of the bounded
semantics and never a real program behaviour: the run took more steps than
its fuel, or would allocate past the model's size bound (a slice of more
than `GoSSA.sliceBound` elements, `makeSlice` and `append`). -/
inductive Res (α : Type) where
  | ok (v : α)
  | panic (k : PanicKind)
  | outOfFuel
  deriving Repr, Inhabited

abbrev Var := Nat
abbrev BlockId := Nat
abbrev FuncId := Nat

/-- The heap: objects by reference. Index 0 is reserved for `nil`. -/
abbrev Heap (Val : Type) := Array Val

/-- What a call through a func value runs: a function of the program with
the values its closure captured, a nil func (Go panics), or a value the
model knows nothing about (the call is unmodelled). -/
inductive Callee (Val : Type) where
  | known (f : FuncId) (bound : List Val)
  | nil
  | unknown

/-- What a dialect must provide. `Val` is the universe of runtime values,
`Op` the instruction set, `denote` the meaning of one instruction applied to
its operands. `Oracle` is read-only ambient state the dialect may consult
(for GoSSA: the contents of memory), fixed for a whole run so that two reads
of the same location agree. `toBool` reads a branch condition. -/
class Dialect (Op : Type) (Val Oracle : outParam Type) where
  /-- One instruction: reads the oracle, may read and write the heap. -/
  denote : Oracle → Heap Val → Op → List Val → Res (Val × Heap Val)
  toBool : Val → Option Bool
  /-- The function a func value calls. -/
  callee : Val → Callee Val := fun _ => .unknown
  /-- Where an op got stuck, said better than its `repr` (which a large
  operand, such as a JSON table, fills before the cut). -/
  stuckSite : Heap Val → Op → List Val → Option String := fun _ _ _ => none
  /-- The under-approximation an op takes on these operands, if any (a
  `range` over a map the model knows nothing about runs zero times): the
  run records it, so a result that went through it is not a certificate. -/
  assumption : Heap Val → Op → List Val → Option String := fun _ _ _ => none
  /-- The heap locations an op reads (0) or writes (1), the atomic
  locations it acquires (2) or releases (3), and its atomic reads (4) and
  writes (5): the concurrent machine's race detector (C2) checks them
  against happens-before (two atomic accesses never race; an atomic and a
  plain one can). -/
  accesses : Op → List Val → List (Val × Nat) := fun _ _ => []

inductive Instr (Op : Type) where
  /-- `dst := op args` -/
  | op (dst : Var) (o : Op) (args : List Var)
  /-- `dst := havoc site` — a fresh non-deterministic value for havoc site
  `site`. Sites are numbered per program; iterations are counted at run time. -/
  | havoc (dst : Var) (site : Nat)
  /-- Record `v` under `label` in the run state (a spec's `go2leanProbe`),
  so a counterexample reports the values the model computed. -/
  | probe (label : String) (v : Var)
  /-- The run takes an under-approximation the frontend marked here (a
  store to a global dropped, a call without result ignored): recorded in
  the run state. -/
  | assume (note : String)
  deriving Repr

/-- A synchronisation operation: a scheduling point of the concurrent
machine (`Go2Lean.GoConc`). Its arguments are the term's variables. -/
inductive SyncOp where
  /-- `[ch, v]` -/
  | send
  /-- `[ch]`: the value, or with `withOk` the pair (value, ok). -/
  | recv (withOk : Bool)
  /-- `[ch]` -/
  | close
  /-- One case per entry, true for a send; the arguments are each case's
  channel, then its value for a send. The result is go/ssa's tuple (index,
  recvOk, the values of the receive cases); index -1 is the default case,
  present when `blocking` is false. -/
  | select (sends : List Bool) (blocking : Bool)
  /-- `[ptr]` to a `sync.Mutex`; `tryLock` yields a bool. -/
  | lock | unlock | tryLock
  /-- `[ptr]` to a `sync.RWMutex`: the writer side, then the reader side. -/
  | wLock | wUnlock | rLock | rUnlock
  /-- `wgAdd` `[ptr, delta]`, `wgWait` `[ptr]` on a `sync.WaitGroup`. -/
  | wgAdd | wgWait
  /-- `[ptr]` to a `sync.Once`: true when this caller runs the function (it
  then sets the state done with the `onceExit` op); blocks while another
  caller runs it. -/
  | onceEnter
  /-- `time.Sleep`, `runtime.Gosched`: any goroutine may run next. -/
  | yield
  /-- `[cond, ticket]`: a `sync.Cond.Wait`'s wait, until `Signal` or
  `Broadcast` releases the ticket `condEnter` gave it. -/
  | condWait
  /-- `[locker]`: `Lock`, `Unlock` through a `sync.Locker`; a `*RWMutex`
  (boxed with tag `rwTag`) takes its writer side, its `RLocker()` (tag
  `rTag`) its reader side, anything else is a `*Mutex`. -/
  | lockL (rwTag rTag : Nat) | unlockL (rwTag rTag : Nat)
  /-- `[]`: where a concurrent harness's setup ends, before anything it does
  reads its parameters (#87). It completes at once and is not a schedule
  step; the search snapshots the machine here and starts later trials from
  the snapshot. -/
  | setupEnd
  deriving Repr, DecidableEq

inductive Term where
  | ret (v : Var)
  | jump (b : BlockId) (args : List Var)
  | br (cond : Var) (t : BlockId) (targs : List Var) (f : BlockId) (fargs : List Var)
  | panic (k : PanicKind)
  /-- `dst := call f args; jump next nargs`. When the callee gets stuck
  (a rejected stub, an op the model has no rule for) the block `fb` runs
  instead on the heap as it was before the call: it binds `dst` to a havoc
  value, so the caller continues as if the call were unmodelled. -/
  | call (f : FuncId) (args : List Var) (dst : Var) (next : BlockId) (nargs : List Var) (fb : BlockId)
  /-- A call into function `f` of the model registered as `pkg`; the driver
  links it into a `call` when it merges the models, so the interpreter only
  sees one when a package was not compiled alongside. -/
  | xcall (pkg : String) (f : FuncId) (args : List Var) (dst : Var) (next : BlockId) (nargs : List Var) (fb : BlockId)
  /-- A call through the func value in `fv`: the function it names, with
  its captured values before `args`; a nil func panics, and a func value
  the model knows nothing about takes the fallback. -/
  | callv (fv : Var) (args : List Var) (dst : Var) (next : BlockId) (nargs : List Var) (fb : BlockId)
  /-- `go fv(args)`, then `jump next`. Only the concurrent machine runs it. -/
  | spawn (fv : Var) (args : List Var) (next : BlockId)
  /-- `dst := op args`, then `jump next`. Only the concurrent machine runs it. -/
  | sync (op : SyncOp) (args : List Var) (dst : Var) (next : BlockId)
  deriving Repr

structure Block (Op : Type) where
  /-- Variables bound by this block's parameters, in order. -/
  params : List Var
  instrs : Array (Instr Op)
  term : Term
  deriving Repr

structure Func (Op : Type) where
  name : String
  /-- Variables bound by the function parameters, in order. -/
  params : List Var
  /-- Number of variables; the environment is an array of this size. -/
  nvals : Nat
  entry : BlockId
  blocks : Array (Block Op)
  /-- It can reach a synchronisation operation (`spawn`, `sync`): the
  concurrent machine gives its calls frames of their own; a call of any
  other function runs to its end in one step. -/
  sync : Bool := false
  /-- A goroutine running it does not keep a run alive (a ticker): the run
  ends when only such goroutines can move and moving them wakes no other. -/
  daemon : Bool := false
  /-- The Go package it is from: the schedule trace names it with each
  operation, so the replay can tell the ones it does not gate (#56). -/
  pkg : String := ""
  deriving Repr

structure Prog (Op : Type) where
  funcs : Array (Func Op)
  /-- Width hint per havoc site, consumed by the search driver to generate values. -/
  havocWidths : Array Nat := #[]
  /-- For a program the driver linked from several models: the model and
  local index each havoc site came from (empty for a single model). -/
  siteOrigin : Array (String × Nat) := #[]
  /-- The havoc site the concurrent machine reads its schedule from (which
  goroutine moves next, which ready `select` case fires); none for a
  program without concurrency. -/
  schedSite : Option Nat := none
  /-- The package initializers' slices the search runs, in order, before
  the trials: they set the package variables that are cells to what the
  initializers build (#55). -/
  inits : Array Nat := #[]
  deriving Repr

/-- Havoc oracle: site → iteration → value. -/
abbrev Havoc (Val : Type) := Nat → Nat → Val

/-- What the concurrent machine (`Go2Lean.GoConc`) records during a run. -/
structure ConcState (Val : Type) where
  /-- The schedule, one entry per step: the goroutine that moved, the
  operation it fired ("" when it only ran code: a goroutine that just
  started, or whose operation a partner completed) and the goroutines whose
  operations it completed (a rendezvous), and the Go package of the code
it ran. The replay stage replays it. -/
  sched : Array (Nat × String × List Nat × String) := #[]
  /-- How many choices each draw of the schedule site had, in order (the
  ready goroutines, or a `select`'s ready cases): the exhaustive engine
  enumerates schedules by it. -/
  arity : Array Nat := #[]
  /-- The heap accesses of the ops run, when `execInstrs` tracks them,
  checked for data races after each step. -/
  acc : Array (Val × Nat) := #[]
  /-- The race detector's state: the vector clock of each synchronisation
  object (by heap location) and, per location, its last write and the reads
  since (goroutine, clock, whether the access was atomic). -/
  syncVC : Std.HashMap (Nat × List Nat) (Array Nat) := {}
  /-- Per heap object: the depth of its deepest path accessed, and its
  accessed paths. An access checks its own path and its prefixes by lookup,
  and scans the object only when a deeper path was accessed (a scan of every
  location was quadratic in a long setup's accesses, #71). -/
  shadow : Std.HashMap Nat (Nat × Std.HashMap (List Nat) (Option (Nat × Nat × Bool) × List (Nat × Nat × Bool))) := {}

/-- Run-time bookkeeping for havoc: how often each site fired and which
values were handed out, so a counterexample can report them. -/
structure HState (Val : Type) where
  counts : Array Nat := #[]
  log : Array (Nat × Nat × Val) := #[]
  /-- Objects allocated before or during the run; `heap[0]` is the nil slot. -/
  heap : Heap Val := #[]
  /-- The op (with its operands) at which the model got stuck, for the
  engines' diagnostics. -/
  stuck : Option String := none
  /-- The function and block of the last `ret` executed: after a spec ran,
  the spec's own return, so the engines can tell which `return` a trial
  left through (a guard's `return true` or the real check). -/
  exit : Option (String × Nat) := none
  /-- Values recorded by probes, in the order they ran. -/
  probes : Array (String × Val) := #[]
  /-- The under-approximations the run took (distinct notes, in order). -/
  assumed : Array String := #[]
  /-- Where the run panicked: the function, block and op (or terminator),
  set by the first panic, for diagnosing a counterexample. -/
  panicAt : Option String := none
  /-- The concurrent machine's state (schedule, race detector): one field,
  so the sequential interpreter's state updates stay small for the prover. -/
  conc : ConcState Val := {}

namespace HState

def take (h : Havoc Val) (s : HState Val) (site : Nat) : Val × HState Val :=
  let counts :=
    if site < s.counts.size then s.counts
    else s.counts ++ Array.replicate (site + 1 - s.counts.size) 0
  let iter := counts[site]!
  let v := h site iter
  (v, { s with counts := counts.set! site (iter + 1), log := s.log.push (site, iter, v) })

/-- Record an under-approximation (at most 32 distinct notes). -/
def assume (s : HState Val) (note : String) : HState Val :=
  if s.assumed.contains note || s.assumed.size ≥ 32 then s
  else { s with assumed := s.assumed.push note }

end HState

variable {Op Val Oracle : Type} [Dialect Op Val Oracle] [Inhabited Val] [Repr Op] [Repr Val]

abbrev Env (Val : Type) := Array Val

/-- Bind block/function parameters to argument values. -/
def bindParams (env : Env Val) : List Var → List Val → Env Val
  | p :: ps, v :: vs => bindParams (env.set! p v) ps vs
  | _, _ => env

/-- Execute the straight-line part of a block. With `track`, record each
op's heap accesses in `acc`; an argument rather than a state field, so the
sequential interpreter's `false` folds away before the prover sees it. -/
def execInstrs (or : Oracle) (h : Havoc Val) (track : Bool) :
    List (Instr Op) → Env Val → HState Val → Res (Env Val) × HState Val
  | [], env, s => (.ok env, s)
  | .op dst o args :: rest, env, s =>
    let vals := args.map (env[·]!)
    let s := match Dialect.assumption s.heap o vals with
      | some note => s.assume note
      | none => s
    -- Taken out of `s` before it grows, so it is unique and grows in place.
    let s := if track then
        let acc := s.conc.acc
        let s := { s with conc.acc := #[] }
        { s with conc.acc := (Dialect.accesses o vals).foldl (·.push ·) acc }
      else s
    match Dialect.denote or s.heap o vals with
    | .ok (v, heap) => execInstrs or h track rest (env.set! dst v) { s with heap := heap }
    | .panic .modelStuck =>
      -- Record where (op and operand shapes, truncated) for the engines.
      let site := match Dialect.stuckSite s.heap o vals with
        | some d => (d.take 300).toString
        | none => (s!"{repr o} {repr vals}".take 160).toString
      (.panic .modelStuck, { s with stuck := s.stuck <|> some site })
    | .panic k => (.panic k, { s with panicAt := s.panicAt <|> some (s!"{repr o}".take 160).toString })
    | .outOfFuel => (.outOfFuel, s)
  | .probe label v :: rest, env, s =>
    execInstrs or h track rest env { s with probes := s.probes.push (label, env[v]!) }
  | .havoc dst site :: rest, env, s =>
    let (v, s) := s.take h site
    execInstrs or h track rest (env.set! dst v) s
  | .assume note :: rest, env, s => execInstrs or h track rest env (s.assume note)

/-- Bind the parameters of block `b` to the values of `args` in `env`. -/
def enter (fn : Func Op) (b : BlockId) (env : Env Val) (args : List Var) : Env Val :=
  match fn.blocks[b]? with
  | some blk => bindParams env blk.params (args.map (env[·]!))
  | none => env

/-- The note for a callee the model got stuck in: its effects are dropped. -/
def stuckCallNote (p : Prog Op) (f : FuncId) : String :=
  s!"the model got stuck in {(p.funcs[f]?.map (·.name)).getD "a callee"}: its result is havoc, its effects are dropped"

mutual

/-- Run block `bid` of `fn` in environment `env`. Each block entry and each
call consumes one unit of fuel. `track` as in `execInstrs`. -/
def runBlock (p : Prog Op) (or : Oracle) (h : Havoc Val) (track : Bool) (fn : Func Op) (bid : BlockId)
    (env : Env Val) (s : HState Val) : (fuel : Nat) → Res Val × HState Val
  | 0 => (.outOfFuel, s)
  | fuel + 1 =>
    match fn.blocks[bid]? with
    | none => (.panic .modelStuck, s)
    | some blk =>
      match execInstrs or h track blk.instrs.toList env s with
      | (.panic k, s) =>
        -- Name the function and block of the op the instructions recorded
        -- (once: callers see the innermost site).
        let s := match s.panicAt with
          | some site => if site.startsWith "@" then s else { s with panicAt := some s!"@{fn.name}#{bid}: {site}" }
          | none => { s with panicAt := some s!"@{fn.name}#{bid}" }
        (.panic k, s)
      | (.outOfFuel, s) => (.outOfFuel, s)
      | (.ok env, s) =>
        match blk.term with
        | .ret v => (.ok env[v]!, { s with exit := some (fn.name, bid) })
        | .panic k => (.panic k, { s with panicAt := s.panicAt <|> some s!"@{fn.name}#{bid}: panic" })
        | .jump b args =>
          runBlock p or h track fn b (enter fn b env args) s fuel
        | .br c t targs f fargs =>
          match Dialect.toBool (Op := Op) env[c]! with
          | some true => runBlock p or h track fn t (enter fn t env targs) s fuel
          | some false => runBlock p or h track fn f (enter fn f env fargs) s fuel
          | none => (.panic .modelStuck, s)
        | .call f args dst next nargs fb =>
          match runFunc p or h track f (args.map (env[·]!)) s fuel with
          | (.ok v, s') =>
            let env := env.set! dst v
            runBlock p or h track fn next (enter fn next env nargs) s' fuel
          | (.panic k, s') =>
            if k.isStuck then
              -- The callee could not be followed: forget what it did to the
              -- heap and take the fallback, which havocs the result.
              let s'' := { s' with heap := s.heap, stuck := s.stuck }
              runBlock p or h track fn fb (enter fn fb env []) (s''.assume (stuckCallNote p f)) fuel
            else (.panic k, s')
          | (.outOfFuel, s') => (.outOfFuel, s')
        | .callv fv args dst next nargs fb =>
          match Dialect.callee (Op := Op) env[fv]! with
          | .nil => (.panic .nilDereference, s)
          | .unknown => runBlock p or h track fn fb (enter fn fb env []) (s.assume "a call through a function value the model does not know: its effects are not modelled") fuel
          | .known f bound =>
            match runFunc p or h track f (bound ++ args.map (env[·]!)) s fuel with
            | (.ok v, s') =>
              let env := env.set! dst v
              runBlock p or h track fn next (enter fn next env nargs) s' fuel
            | (.panic k, s') =>
              if k.isStuck then
                let s'' := { s' with heap := s.heap, stuck := s.stuck }
                runBlock p or h track fn fb (enter fn fb env []) (s''.assume (stuckCallNote p f)) fuel
              else (.panic k, s')
            | (.outOfFuel, s') => (.outOfFuel, s')
        | .spawn .. | .sync .. =>
          -- Concurrency needs the machine; run alone, the function is stuck.
          (.panic .modelStuck, { s with stuck := s.stuck <|> some s!"{fn.name}: a goroutine or synchronisation operation outside the concurrent machine" })
        | .xcall _ _ _ _ _ _ fb =>
          -- Not linked (the package was not compiled alongside): unmodelled.
          runBlock p or h track fn fb (enter fn fb env []) (s.assume "a call into a package not linked into the model: its effects are not modelled") fuel

/-- Run function `fid` on `args`. -/
def runFunc (p : Prog Op) (or : Oracle) (h : Havoc Val) (track : Bool) (fid : FuncId) (args : List Val)
    (s : HState Val) : (fuel : Nat) → Res Val × HState Val
  | 0 => (.outOfFuel, s)
  | fuel + 1 =>
    match p.funcs[fid]? with
    | none => (.panic .modelStuck, s)
    | some fn =>
      let env := bindParams (Array.replicate fn.nvals default) fn.params args
      runBlock p or h track fn fn.entry env s fuel

end

/-- Entry point: run a function with fresh havoc state and an initial heap
(the objects the arguments point to; `#[]` when there are none). -/
def run (p : Prog Op) (or : Oracle) (h : Havoc Val) (fid : FuncId) (args : List Val) (fuel : Nat)
    (heap : Heap Val := #[]) : Res Val × HState Val :=
  runFunc p or h false fid args { heap := heap } fuel

end Go2Lean.SSA
