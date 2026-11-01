import Go2Lean.Dialect.GoSSA

/-! The concurrent machine (`docs/concurrency.md`, issue #2): goroutines,
channels, `select` and the `sync` primitives over the GoSSA dialect.

The big-step interpreter (`Go2Lean.SSA.runFunc`) stays for sequential code;
a function that can reach a synchronisation operation (`Func.sync`) runs
here. A goroutine is a stack of frames and runs from one scheduling point
(a `spawn` or `sync` terminator) to the next in one step; calls of functions
that cannot synchronise run to their end inside that step. At each step the
schedule (the havoc site `Prog.schedSite`) picks one of the goroutines that
can move. Channels live on the heap (`chanObj`); a mutex, read-write mutex,
wait group or once is a word in place of its field (any value but the ones
these operations store reads as the zero state).

A run ends when no goroutine can move: with the main goroutine's result when
every goroutine is done, `goroutineLeak` when the main goroutine returned
and another is blocked forever, `deadlock` when the main goroutine itself
is blocked. A goroutine blocked on a channel the main goroutine returns is
not a leak: the caller may still use the channel (`gen` returning `<-chan`). A panic in any goroutine ends the run with it, as in Go. -/

namespace Go2Lean.GoConc

open Go2Lean.SSA Go2Lean.GoSSA

abbrev Func := Go2Lean.GoSSA.Func
abbrev Prog := Go2Lean.GoSSA.Prog

/-- A call frame. `atTerm`: the block's instructions ran and its terminator
(a `spawn` or `sync`) waits. `dst`, `next`, `nargs`: where the caller
continues with this frame's result. -/
structure Frame where
  fn : Func
  bid : BlockId
  env : Env Val
  atTerm : Bool := false
  dst : Var := 0
  next : BlockId := 0
  nargs : List Var := []

instance : Inhabited Frame :=
  ⟨{ fn := { name := "", params := [], nvals := 0, entry := 0, blocks := #[] }, bid := 0, env := #[] }⟩

structure Gor where
  frames : List Frame
  done : Bool := false
  result : Val := .unit
  /-- Runs a `Func.daemon` function (a ticker). -/
  daemon : Bool := false
  /-- Its vector clock, for the race detector. -/
  vc : Array Nat := #[]
  /-- At a `time.Sleep(d)` (a `yield [d]`): the nanoseconds left, once
  another sleeper's waking moved time on; none: all of `d` (#59). -/
  sleep : Option Int := none

instance : Inhabited Gor := ⟨{ frames := [] }⟩

/-- The frame a call of `fn` on `args` starts in. -/
def entryFrame (fn : Func) (args : List Val) : Frame :=
  { fn, bid := fn.entry, env := bindParams (Array.replicate fn.nvals default) fn.params args }

/-! ## One goroutine, up to its next scheduling point -/

inductive Stop where
  | atSync (frames : List Frame)
  | returned (v : Val)
  | panicked (k : PanicKind)
  | outOfFuel

/-- The frames after the top one returned `v`: the caller continues. -/
def returnTo (fr : Frame) (v : Val) : List Frame → Option (List Frame)
  | c :: rest =>
    let env := enter c.fn fr.next (c.env.set! fr.dst v) fr.nargs
    some ({ c with env := env, bid := fr.next, atTerm := false } :: rest)
  | [] => none

/-- Run the top frame's blocks until the goroutine reaches a `spawn` or
`sync`, returns from its first frame, or panics. -/
def advance (p : Prog) (or : Oracle) (h : Havoc Val) : List Frame → HState Val → Nat → Stop × HState Val
  | [], s, _ => (.panicked .modelStuck, s)
  | _, s, 0 => (.outOfFuel, s)
  | fr :: rest, s, fuel + 1 =>
    if fr.atTerm then (.atSync (fr :: rest), s) else
    match fr.fn.blocks[fr.bid]? with
    | none => (.panicked .modelStuck, s)
    | some blk =>
      match execInstrs (Op := Go2Lean.GoSSA.Op) or h true blk.instrs.toList fr.env s with
      | (.panic k, s) =>
        -- Name the function and block (once: the innermost site).
        let s := match s.panicAt with
          | some site => if site.startsWith "@" then s else { s with panicAt := some s!"@{fr.fn.name}#{fr.bid}: {site}" }
          | none => { s with panicAt := some s!"@{fr.fn.name}#{fr.bid}" }
        (.panicked k, s)
      | (.outOfFuel, s) => (.outOfFuel, s)
      | (.ok env, s) =>
        let goto := fun (b : BlockId) (env : Env Val) (args : List Var) =>
          { fr with bid := b, env := enter fr.fn b env args, atTerm := false } :: rest
        -- A call: a frame of its own for a function that can synchronise,
        -- else the big-step interpreter, as in `runBlock`.
        let call := fun (f : FuncId) (vals : List Val) (dst next : BlockId) (nargs : List Var) (fb : BlockId) =>
          match p.funcs[f]? with
          | none => (.panicked .modelStuck, s)
          | some callee =>
            if callee.sync then
              advance p or h ({ entryFrame callee vals with dst, next, nargs } :: { fr with env } :: rest) s fuel
            else
              match runFunc p or h true f vals s fuel with
              | (.ok v, s') => advance p or h (goto next (env.set! dst v) nargs) s' fuel
              | (.panic k, s') =>
                if k.isStuck then
                  -- Forget where the callee got stuck too: a later stuck
                  -- site would be reported at the callee's place (#71).
                  let s'' := { s' with heap := s.heap, stuck := s.stuck, panicAt := s.panicAt }
                  advance p or h (goto fb env []) (s''.assume (stuckCallNote p f)) fuel
                else (.panicked k, s')
              | (.outOfFuel, s') => (.outOfFuel, s')
        match blk.term with
        | .ret v =>
          match returnTo fr env[v]! rest with
          | some frames => advance p or h frames { s with exit := some (fr.fn.name, fr.bid) } fuel
          | none => (.returned env[v]!, { s with exit := some (fr.fn.name, fr.bid) })
        | .jump b args => advance p or h (goto b env args) s fuel
        | .br c t ta f fa =>
          match Dialect.toBool (Op := Go2Lean.GoSSA.Op) env[c]! with
          | some true => advance p or h (goto t env ta) s fuel
          | some false => advance p or h (goto f env fa) s fuel
          | none => (.panicked .modelStuck, s)
        | .panic k => (.panicked k, s)
        | .call f args dst next nargs fb => call f (args.map (env[·]!)) dst next nargs fb
        | .callv fv args dst next nargs fb =>
          match Dialect.callee (Op := Go2Lean.GoSSA.Op) env[fv]! with
          | .nil => (.panicked .nilDereference, s)
          | .unknown => advance p or h (goto fb env [])
              (s.assume "a call through a function value the model does not know: its effects are not modelled") fuel
          | .known f bound => call f (bound ++ args.map (env[·]!)) dst next nargs fb
        | .xcall _ _ _ _ _ _ fb =>
          advance p or h (goto fb env [])
            (s.assume "a call into a package not linked into the model: its effects are not modelled") fuel
        | .spawn .. | .sync .. => (.atSync ({ fr with env, atTerm := true } :: rest), s)

/-! ## Pending operations -/

/-- The `sync` terminator goroutine `g` waits at, with its argument values. -/
def pendingSync (g : Gor) : Option (SyncOp × List Val) :=
  match g.frames with
  | f :: _ =>
    if !f.atTerm then none else
    match f.fn.blocks[f.bid]? with
    | some blk =>
      match blk.term with
      | .sync op args _ _ => some (op, args.map (f.env[·]!))
      | _ => none
    | none => none
  | [] => none

/-- Complete the `spawn` or `sync` goroutine `g` waits at, with result `r`:
it moves on to the terminator's successor and can run again. -/
def complete (g : Gor) (r : Val) : Gor :=
  match g.frames with
  | f :: rest =>
    match f.fn.blocks[f.bid]? with
    | some blk =>
      match blk.term with
      | .sync _ _ dst next =>
        { g with frames := { f with env := enter f.fn next (f.env.set! dst r) [], bid := next, atTerm := false } :: rest,
                 sleep := none }
      | .spawn _ _ next =>
        { g with frames := { f with env := enter f.fn next f.env [], bid := next, atTerm := false } :: rest }
      | _ => g
    | none => g
  | [] => g

def samePtr : Val → Val → Bool
  | .ptr r p, .ptr r' p' => r == r' && p == p'
  | _, _ => false

def valNat : Val → Nat
  | .int _ v => v.toNat
  | _ => 0

/-- The cases of a `select`: send flag, channel, value (unit for a receive). -/
def selectCases : List Bool → List Val → List (Bool × Val × Val)
  | true :: ss, ch :: v :: rest => (true, ch, v) :: selectCases ss rest
  | false :: ss, ch :: rest => (false, ch, .unit) :: selectCases ss rest
  | _, _ => []

/-- go/ssa's result of a `select`: (index, recvOk, the received value of each
receive case, zero for the ones not chosen). -/
def selectResult (heap : Heap Val) (cases : List (Bool × Val × Val)) (idx : Int) (ok : Bool)
    (got : Val) : Val :=
  let recvs := (cases.zipIdx).filterMap fun ((send, ch, _), i) =>
    if send then none else
    some (if (i : Int) = idx then got else
      match chanGet heap ch with | some (_, _, _, _, z) => z | none => .unit)
  .tuple (.int 64 (BitVec.ofInt 64 idx) :: .bool ok :: recvs)

/-- Goroutine `g` waits to receive on channel `ref`: the result its pending
operation gets for a received value `v`. -/
def recvWaiter (heap : Heap Val) (ref : Nat) (g : Gor) : Option (Val → Val) :=
  match pendingSync g with
  | some (.recv withOk, [ch]) =>
    match chanGet heap ch with
    | some (r, _, _, _, _) => if r == ref then some fun v => if withOk then .tuple [v, .bool true] else v else none
    | none => none
  | some (.select sends _, args) =>
    let cases := selectCases sends args
    match (cases.zipIdx).find? fun ((send, ch, _), _) =>
        !send && (match chanGet heap ch with | some (r, _, _, _, _) => r == ref | none => false) with
    | some (_, i) => some fun v => selectResult heap cases i true v
    | none => none
  | _ => none

/-- Goroutine `g` waits to send on channel `ref`: the value, and the result
its pending operation gets once the value is taken. -/
def sendWaiter (heap : Heap Val) (ref : Nat) (g : Gor) : Option (Val × Val) :=
  match pendingSync g with
  | some (.send, [ch, v]) =>
    match chanGet heap ch with
    | some (r, _, _, _, _) => if r == ref then some (v, .unit) else none
    | none => none
  | some (.select sends _, args) =>
    let cases := selectCases sends args
    match (cases.zipIdx).find? fun ((send, ch, _), _) =>
        send && (match chanGet heap ch with | some (r, _, _, _, _) => r == ref | none => false) with
    | some ((_, _, v), i) => some (v, selectResult heap cases i false .unit)
    | none => none
  | _ => none

/-- The first goroutine other than `i` that satisfies `w`. -/
def findOther {α : Type} (gs : Array Gor) (i : Nat) (w : Gor → Option α) : Option (Nat × α) :=
  (List.range gs.size).findSome? fun j =>
    if j == i then none else
    match gs[j]? with
    | some g => if g.done then none else (w g).map (j, ·)
    | none => none

/-- The outcome of trying goroutine `i`'s pending operation. -/
inductive Fire where
  | done (gs : Array Gor) (heap : Heap Val)
  | blocked
  | panic (k : PanicKind)

instance : Inhabited Fire := ⟨.blocked⟩

def setChan (heap : Heap Val) (ref : Nat) (cap : BitVec 64) (buf : List Val) (closed : Bool) (z : Val) : Heap Val :=
  heap.set! ref (chanObj cap buf closed z)

/-- A send of `v` on `ch` by goroutine `i`, completed with `res`. -/
def fireSend (heap : Heap Val) (gs : Array Gor) (i : Nat) (ch v res : Val) : Fire :=
  if chanIsNil ch then .blocked else
  match chanGet heap ch with
  | none => .panic .modelStuck
  | some (ref, cap, buf, closed, z) =>
    if closed then .panic .closedChannel
    else if cap.toNat > 0 then
      if buf.length < cap.toNat then
        .done (gs.modify i (complete · res)) (setChan heap ref cap (buf ++ [v]) false z)
      else .blocked
    else
      match findOther gs i (recvWaiter heap ref) with
      | some (j, k) => .done ((gs.modify j (complete · (k v))).modify i (complete · res)) heap
      | none => .blocked

/-- A receive on `ch` by goroutine `i`; `res v ok` is its result. -/
def fireRecv (heap : Heap Val) (gs : Array Gor) (i : Nat) (ch : Val) (res : Val → Bool → Val) : Fire :=
  if chanIsNil ch then .blocked else
  match chanGet heap ch with
  | none => .panic .modelStuck
  | some (ref, cap, buf, closed, z) =>
    match buf with
    | v :: rest => .done (gs.modify i (complete · (res v true))) (setChan heap ref cap rest closed z)
    | [] =>
      match (if cap.toNat = 0 then findOther gs i (sendWaiter heap ref) else none) with
      | some (j, (v, sres)) => .done ((gs.modify j (complete · sres)).modify i (complete · (res v true))) heap
      | none => if closed then .done (gs.modify i (complete · (res z false))) heap else .blocked

/-- The lock word at `ptr`: any value these operations did not store is the
zero state. -/
def wordAt (heap : Heap Val) (ptr : Val) : Nat :=
  match heapLoad heap ptr with
  | .ok (.int _ v) => v.toNat
  | _ => 0

def setWord (heap : Heap Val) (ptr : Val) (n : Int) : Option (Heap Val) :=
  match heapStore heap ptr (.int 64 (BitVec.ofInt 64 n)) with
  | .ok heap => some heap
  | _ => none

/-- A read-write mutex: (readers, writer). -/
def rwAt (heap : Heap Val) (ptr : Val) : Nat × Bool :=
  match heapLoad heap ptr with
  | .ok (.tuple [.int _ r, .bool w]) => (r.toNat, w)
  | _ => (0, false)

def setRW (heap : Heap Val) (ptr : Val) (r : Nat) (w : Bool) : Option (Heap Val) :=
  match heapStore heap ptr (.tuple [.int 64 (BitVec.ofNat 64 r), .bool w]) with
  | .ok heap => some heap
  | _ => none

/-- A goroutine other than `i` waits for the writer side of `ptr`. -/
def writerWaiting (gs : Array Gor) (i : Nat) (ptr : Val) : Bool :=
  (findOther gs i fun g => match pendingSync g with
    | some (.wLock, [q]) => if samePtr q ptr then some () else none
    | _ => none).isSome

/-- What a `sync.Locker` holds: 1 for a `*RWMutex` (boxed with `rwTag`), 2
for its `RLocker()` (`rTag`), 0 for a `*Mutex`; and the pointer. -/
def lockerTarget (rwTag rTag : Nat) : Val → Nat × Val
  | .boxed t p => (if t == rwTag then 1 else if t == rTag then 2 else 0, p)
  | p => (0, p)

/-- The key of the model's clock cell (`gossa.clockKey`, #59). -/
def clockKey : Nat := 1775612383

/-- The heap with the clock cell, if the run made one, moved on by `d`
nanoseconds. -/
def advanceClock (heap : Heap Val) (d : Int) : Heap Val :=
  if d ≤ 0 then heap else
  let dir := match heap[0]? with | some (.arr es) => es | _ => []
  let cell := dir.findSome? fun e => match e with
    | .tuple [.int _ k, p] => if k.toNat == clockKey then some p else none
    | _ => none
  match cell with
  | some p =>
    match heapLoad heap p with
    | .ok (.int 64 v) =>
      match heapStore heap p (.int 64 (BitVec.ofInt 64 (v.toInt + d))) with
      | .ok heap => heap
      | _ => heap
    | _ => heap
  | none => heap

/-- The heap with the clock cell set to 0, so that states that differ only
in the time compare equal (hang detection, #59). -/
def withoutClock (heap : Heap Val) : Heap Val :=
  let dir := match heap[0]? with | some (.arr es) => es | _ => []
  let cell := dir.findSome? fun e => match e with
    | .tuple [.int _ k, p] => if k.toNat == clockKey then some p else none
    | _ => none
  match cell with
  | some p =>
    match heapStore heap p (.int 64 0) with
    | .ok heap => heap
    | _ => heap
  | none => heap

/-- The nanoseconds goroutine `g` still sleeps, when it waits at a
`time.Sleep` (a `yield [d]`). -/
def sleepLeft (g : Gor) : Option Int :=
  if g.done then none else
  match pendingSync g with
  | some (.yield, [.int _ d]) => some (g.sleep.getD d.toInt)
  | _ => none

/-- Try goroutine `i`'s pending operation; `choice` picks among ready
`select` cases. -/
def fire (heap : Heap Val) (gs : Array Gor) (i : Nat) (choice : Nat) : Fire :=
  let done1 := fun (r : Val) (heap : Heap Val) => Fire.done (gs.modify i (complete · r)) heap
  let withWord := fun (o : Option (Heap Val)) (r : Val) => match o with
    | some heap => done1 r heap
    | none => Fire.panic .modelStuck
  match gs[i]? >>= pendingSync with
  | none => .blocked
  | some (op, args) =>
    match op, args with
    | .send, [ch, v] => fireSend heap gs i ch v .unit
    | .recv withOk, [ch] => fireRecv heap gs i ch fun v ok => if withOk then .tuple [v, .bool ok] else v
    | .close, [ch] =>
      if chanIsNil ch then .panic .closedChannel else
      match chanGet heap ch with
      | none => .panic .modelStuck
      | some (ref, cap, buf, closed, z) =>
        if closed then .panic .closedChannel else done1 .unit (setChan heap ref cap buf true z)
    | .select sends blocking, args =>
      let cases := selectCases sends args
      let tries := (cases.zipIdx).filterMap fun ((send, ch, v), k) =>
        let r := if send then fireSend heap gs i ch v (selectResult heap cases k false .unit)
          else fireRecv heap gs i ch fun x ok => selectResult heap cases k ok x
        match r with
        | .blocked => none
        | r => some r
      match tries with
      | [] => if blocking then .blocked else done1 (selectResult heap cases (-1) false .unit) heap
      | _ => tries[choice % tries.length]!
    | .setupEnd, [] => done1 .unit heap
    | .lock, [ptr] => if wordAt heap ptr = 1 then .blocked else withWord (setWord heap ptr 1) .unit
    | .tryLock, [ptr] =>
      if wordAt heap ptr = 1 then done1 (.bool false) heap else withWord (setWord heap ptr 1) (.bool true)
    | .unlock, [ptr] => if wordAt heap ptr = 1 then withWord (setWord heap ptr 0) .unit else .panic .syncFatal
    | .wLock, [ptr] =>
      let (r, w) := rwAt heap ptr
      if r = 0 && !w then withWord (setRW heap ptr 0 true) .unit else .blocked
    | .wUnlock, [ptr] =>
      let (r, w) := rwAt heap ptr
      if w then withWord (setRW heap ptr r false) .unit else .panic .syncFatal
    | .rLock, [ptr] =>
      let (r, w) := rwAt heap ptr
      -- Writer preference: a writer waiting for the readers to leave stops
      -- new readers, as in Go. With no reader in, a writer at its `Lock` is
      -- not waiting yet (it could take the lock): either may go first.
      if !w && (r = 0 || !writerWaiting gs i ptr) then withWord (setRW heap ptr (r + 1) false) .unit else .blocked
    | .rUnlock, [ptr] =>
      let (r, w) := rwAt heap ptr
      if r > 0 then withWord (setRW heap ptr (r - 1) w) .unit else .panic .syncFatal
    | .wgAdd, [ptr, .int _ d] =>
      let n := (wordAt heap ptr : Int) + d.toInt
      if n < 0 then .panic .explicit else withWord (setWord heap ptr n) .unit
    | .wgWait, [ptr] => if wordAt heap ptr = 0 then done1 .unit heap else .blocked
    | .onceEnter, [ptr] =>
      match wordAt heap ptr with
      | 0 => withWord (setWord heap ptr 1) (.bool true)
      | 1 => .blocked
      | _ => done1 (.bool false) heap
    | .yield, [.int _ d] =>
      -- A sleep (#59): time moves on to this goroutine's waking only when
      -- no other sleeper is due before it; it may move on at any
      -- scheduling point, as time passes while the others run. Waking
      -- moves every other sleeper and the clock on as far.
      let mine := (gs[i]? >>= sleepLeft).getD d.toInt
      let earlier := (List.range gs.size).any fun j =>
        j != i && match gs[j]? >>= sleepLeft with
          | some r => r < mine
          | none => false
      if mine > 0 && earlier then .blocked else
      let step := max mine 0
      let gs := gs.mapIdx fun j g =>
        if j == i then complete g .unit
        else match sleepLeft g with
          | some r => { g with sleep := some (r - step) }
          | none => g
      Fire.done gs (advanceClock heap step)
    | .yield, _ => done1 .unit heap
    | .condWait, [c, .int _ t] =>
      if (condState heap c).2.contains t.toNat then .blocked else done1 .unit heap
    | .lockL tag rtag, [l] =>
      match lockerTarget tag rtag l with
      | (1, ptr) =>
        let (r, w) := rwAt heap ptr
        if r = 0 && !w then withWord (setRW heap ptr 0 true) .unit else .blocked
      | (2, ptr) =>
        let (r, w) := rwAt heap ptr
        if !w && (r = 0 || !writerWaiting gs i ptr) then withWord (setRW heap ptr (r + 1) false) .unit else .blocked
      | (_, ptr) => if wordAt heap ptr = 1 then .blocked else withWord (setWord heap ptr 1) .unit
    | .unlockL tag rtag, [l] =>
      match lockerTarget tag rtag l with
      | (1, ptr) =>
        let (r, w) := rwAt heap ptr
        if w then withWord (setRW heap ptr r false) .unit else .panic .syncFatal
      | (2, ptr) =>
        let (r, w) := rwAt heap ptr
        if r > 0 then withWord (setRW heap ptr (r - 1) w) .unit else .panic .syncFatal
      | (_, ptr) => if wordAt heap ptr = 1 then withWord (setWord heap ptr 0) .unit else .panic .syncFatal
    | _, _ => .panic .modelStuck

/-! ## The scheduler -/

/-- Can goroutine `i` move? -/
def ready (heap : Heap Val) (gs : Array Gor) (i : Nat) : Bool :=
  match gs[i]? with
  | none => false
  | some g =>
    if g.done then false else
    match g.frames with
    | f :: _ =>
      if !f.atTerm then true else
      match f.fn.blocks[f.bid]? with
      | some blk =>
        match blk.term with
        | .spawn .. => true
        | .sync .. => match fire heap gs i 0 with | .blocked => false | _ => true
        | _ => false
      | none => false
    | [] => false

/-- The goroutines that can move now (#59): time passes only when nothing
else can move, as in Go's `testing/synctest` (code runs in no time), so a
sleeper with time left is a candidate only when no goroutine can move
without it. -/
def movable (heap : Heap Val) (gs : Array Gor) : List Nat :=
  let cands := (List.range gs.size).filter (ready heap gs)
  let now := fun (j : Nat) => match gs[j]? >>= sleepLeft with
    | some r => r ≤ 0
    | none => true
  if cands.any now then cands.filter now else cands

/-- Value `v` holds channel `ch`, itself or in a field (to depth `n`). -/
def reaches (ch : Val) : Nat → Val → Bool
  | 0, _ => false
  | n + 1, v => samePtr v ch || match v with
    | .structv fs | .tuple fs => fs.any (reaches ch n)
    | .boxed _ w => reaches ch n w
    | _ => false

/-- Goroutine `g` waits on a channel the main goroutine returned in `res`. -/
def waitsOnResult (res : Val) (g : Gor) : Bool :=
  match pendingSync g with
  | some (.send, ch :: _) | some (.recv _, ch :: _) => reaches ch 4 res
  | some (.select ss _, args) => (selectCases ss args).any fun (_, ch, _) => reaches ch 4 res
  | _ => false

/-- Where the goroutines that cannot move wait, for a deadlock or leak report. -/
def describeBlocked (gs : Array Gor) : String :=
  let parts := (gs.toList.zipIdx).filterMap fun (g, i) =>
    if g.done || g.daemon then none else
    match g.frames with
    | f :: _ =>
      let op := match pendingSync g with
        | some (op, _) => (s!"{repr op}".take 40).toString
        | none => "?"
      some s!"goroutine {i} at {f.fn.name}#{f.bid}: {op}"
    | [] => none
  String.intercalate "; " parts

/-- The next schedule choice, among `arity` options (recorded, so the
exhaustive engine can enumerate every schedule). -/
def schedChoice (p : Prog) (h : Havoc Val) (s : HState Val) (arity : Nat) : Nat × HState Val :=
  match p.schedSite with
  | some site =>
    let (v, s) := s.take h site
    (valNat v, { s with conc.arity := s.conc.arity.push arity })
  | none => (0, s)

/-- How many ways goroutine `i`'s pending operation can fire: the ready
cases of a `select`, else one. -/
def fireArity (heap : Heap Val) (gs : Array Gor) (i : Nat) : Nat :=
  match gs[i]? >>= pendingSync with
  | some (.select sends _, args) =>
    let cases := selectCases sends args
    let ready := (cases.zipIdx).countP fun ((send, ch, v), k) =>
      let r := if send then fireSend heap gs i ch v (selectResult heap cases k false .unit)
        else fireRecv heap gs i ch fun x ok => selectResult heap cases k ok x
      match r with
      | .blocked => false
      | _ => true
    max ready 1
  | _ => 1

/-- The model's bound on the goroutines a run starts (finished ones
included): one more ends the run out of fuel, as an allocation past
`GoSSA.sliceBound` does. Every step looks at every goroutine and the race
detector's clocks have one entry per goroutine, so a mutant or a bug that
starts goroutines in a loop without end would otherwise slow a run to a
crawl (#58). -/
def goroutineBound : Nat := 128

/-- Run goroutine `i` (now able to move) to its next scheduling point. -/
def stepGor (p : Prog) (or : Oracle) (h : Havoc Val) (gs : Array Gor) (i : Nat) (s : HState Val)
    (fuel : Nat) : Except PanicKind (Array Gor × HState Val) × HState Val :=
  -- First the pending terminator, if any.
  let started : Except PanicKind (Array Gor × HState Val) :=
    match gs[i]? with
    | none => .error .modelStuck
    | some g =>
      match g.frames with
      | f :: _ =>
        if !f.atTerm then .ok (gs, s) else
        match f.fn.blocks[f.bid]? with
        | some blk =>
          match blk.term with
          | .spawn fv args _ =>
            let gs := gs.modify i (complete · .unit)
            match Dialect.callee (Op := Go2Lean.GoSSA.Op) f.env[fv]! with
            | .nil => .error .nilDereference
            | .unknown => .ok (gs, s.assume "a goroutine running a function value the model does not know is not modelled")
            | .known fid bound =>
              match p.funcs[fid]? with
              | some callee =>
                if gs.size ≥ goroutineBound then .error .other else
                .ok (gs.push { frames := [entryFrame callee (bound ++ args.map (f.env[·]!))], daemon := callee.daemon }, s)
              | none => .error .modelStuck
          | .sync op .. =>
            let (c, s) := if op == .setupEnd then (0, s) else schedChoice p h s (fireArity s.heap gs i)
            match fire s.heap gs i c with
            | .done gs heap => .ok (gs, { s with heap })
            | .blocked => .error .modelStuck
            | .panic k => .error k
          | _ => .error .modelStuck
        | none => .error .modelStuck
      | [] => .error .modelStuck
  match started with
  | .error .modelStuck =>
    -- Say which operation: a channel or lock word the model does not know.
    let note := match gs[i]? >>= pendingSync with
      | some (op, args) => (s!"{repr op} on {repr args}".take 200).toString
      | none => "a go statement"
    -- And where: the goroutine's innermost frame.
    let place := match gs[i]? >>= (·.frames.head?) with
      | some fr => some s!"@{fr.fn.name}#{fr.bid}"
      | none => none
    let s := if s.stuck.isNone then { s with panicAt := place <|> s.panicAt } else s
    (.error .modelStuck, { s with stuck := s.stuck <|> some note })
  | .error .other =>
    -- Past the bound on goroutines: no behaviour of the program.
    (.error .other, { s with stuck := s.stuck <|> some s!"more than {goroutineBound} goroutines (the model's bound)" })
  | .error k => (.error k, s)
  | .ok (gs, s) =>
    match gs[i]? with
    | none => (.error .modelStuck, s)
    | some g =>
      match advance p or h g.frames s fuel with
      | (.atSync frames, s) => (.ok (gs.set! i { g with frames }, s), s)
      | (.returned v, s) => (.ok (gs.set! i { g with frames := [], done := true, result := v }, s), s)
      | (.panicked k, s) => (.error k, s)
      | (.outOfFuel, s) =>
        -- Where the step started: the loop a goroutine spins in without
        -- reaching a scheduling point (#86).
        let place := match g.frames.head? with
          | some fr => s!" step from @{fr.fn.name}#{fr.bid}"
          | none => ""
        (.error .other, { s with stuck := s.stuck <|> some s!"out of fuel in a goroutine{place}" })

/-- The operation goroutine `g` waits at, named for the schedule trace
("" when it is not at one). -/
def pendingName (g : Gor) : String :=
  match g.frames with
  | f :: _ =>
    if !f.atTerm then "" else
    match f.fn.blocks[f.bid]? with
    | some blk =>
      match blk.term with
      | .spawn .. => "spawn"
      | .sync op _ _ _ => (s!"{repr op}".replace "Go2Lean.SSA.SyncOp." "").takeWhile (· != ' ') |>.toString
      | _ => ""
    | none => ""
  | [] => ""

/-- The Go package of the function goroutine `g` runs (its top frame), for
the schedule trace. -/
def pendingPkg (g : Gor) : String :=
  match g.frames with
  | f :: _ => f.fn.pkg
  | [] => ""

/-- For a `select` goroutine `i` fires with `choice`: the case taken (-1
for the default), as "select:k" for the schedule trace. -/
def selectTaken (heap : Heap Val) (gs : Array Gor) (i choice : Nat) : Option String :=
  match gs[i]? with
  | some { frames := f :: _, .. } =>
    match f.fn.blocks[f.bid]? with
    | some { term := .sync (.select ..) _ dst _, .. } =>
      match fire heap gs i choice with
      | .done gs' _ =>
        match gs'[i]? with
        | some { frames := f' :: _, .. } =>
          match f'.env[dst]? with
          | some (.tuple (.int _ k :: _)) => some s!"select:{k.toInt}"
          | _ => none
        | _ => none
      | _ => none
    | _ => none
  | _ => none

/-- The goroutines other than `i` that waited at an operation in `before`
and no longer do in `after`: the partners of a rendezvous. -/
def completedBy (before after : Array Gor) (i : Nat) : List Nat :=
  (List.range before.size).filter fun j =>
    j != i && pendingName before[j]! != "" && (after[j]?.map pendingName) == some ""

/-! ## Data races (C2)

Happens-before with vector clocks. Every synchronisation operation joins
the clocks of the goroutines taking part with the clock of the object
(channel, lock word, wait group, once, cond), in both directions: an
over-approximation of Go's happens-before (a buffered send is ordered
with every receive of its channel, readers of a `RWMutex` with each
other), so a race reported is one Go's own detector also has, and the
search may miss some. A spawn orders the parent's past with the child;
`sync/atomic` operations acquire and release their location. Plain heap
accesses (`Dialect.accesses`, recorded per step) are checked against the
last write and the reads since of their location (FastTrack). -/

def vcGet (v : Array Nat) (i : Nat) : Nat := v.getD i 0

def vcJoin (a b : Array Nat) : Array Nat :=
  (Array.range (max a.size b.size)).map fun i => max (vcGet a i) (vcGet b i)

def vcTick (v : Array Nat) (i : Nat) : Array Nat :=
  let v := if i < v.size then v else v ++ Array.replicate (i + 1 - v.size) 0
  v.set! i (v[i]! + 1)

/-- The heap location a pointer (or a locker holding one) names. -/
def locKey : Val → Option (Nat × List Nat)
  | .ptr r p => if r = 0 then none else some (r, p)
  | .boxed _ (.ptr r p) => if r = 0 then none else some (r, p)
  | _ => none

def syncGet (s : HState Val) (k : Nat × List Nat) : Array Nat :=
  s.conc.syncVC.getD k #[]

def syncSet (s : HState Val) (k : Nat × List Nat) (v : Array Nat) : HState Val :=
  { s with conc.syncVC := s.conc.syncVC.insert k v }

/-- The object a fired operation synchronises on: its first argument, or
the case a select took. -/
def syncObj (g : Gor) (name : String) : Option (Nat × List Nat) :=
  match pendingSync g with
  | some (.select ss _, args) =>
    match (name.drop 7).toString.toInt? with
    | some k =>
      if k < 0 then none else
      ((selectCases ss args)[k.toNat]?).bind fun (_, ch, _) => locKey ch
    | none => none
  | some (.yield, _) => none
  | some (_, a :: _) => locKey a
  | _ => none

/-- Goroutine `g` with clock `vc` reads or writes `k` (atomically or not):
the race it makes, or the shadow state with the access added. Two atomic
accesses never race. -/
def checkAccess (s : HState Val) (vc : Array Nat) (g : Nat) (k : Nat × List Nat) (write atomic : Bool) :
    Except String (HState Val) :=
  let unordered := fun (h c : Nat) (a : Bool) => h != g && c > vcGet vc h && !(atomic && a)
  let check := fun (e : Option (Nat × Nat × Bool) × List (Nat × Nat × Bool)) =>
    let byWrite := match e.1 with
      | some (h, c, a) => if unordered h c a then some (h, "wrote") else none
      | none => none
    let byRead := if write then e.2.findSome? fun (h, c, a) => if unordered h c a then some (h, "read") else none else none
    byWrite <|> byRead
  -- Taken out of `s`, so the maps are updated in place.
  let shadow := s.conc.shadow
  let s := { s with conc.shadow := {} }
  let (depth, paths) := shadow.getD k.1 (0, {})
  let shadow := shadow.erase k.1
  -- The location and its enclosing ones, then, when deeper paths were
  -- accessed, the ones inside it.
  let race := ((List.range (k.2.length + 1)).findSome? fun n => (paths.get? (k.2.take n)).bind check) <|>
    (if depth > k.2.length then
      paths.fold (fun r q e => r <|> (if q.length > k.2.length && k.2.isPrefixOf q then check e else none)) none
    else none)
  match race with
  | some (h, what) =>
    .error s!"data race on object {k.1} at {k.2}: goroutine {g} {if write then "writes" else "reads"} it, goroutine {h} {what} it, neither ordered before the other"
  | none =>
    let clk := vcGet vc g
    let (w, rs) := paths.getD k.2 (none, [])
    let entry := if write then (some (g, clk, atomic), []) else (w, (g, clk, atomic) :: rs.filter (·.1 != g))
    .ok { s with conc.shadow := shadow.insert k.1 (max depth k.2.length, paths.insert k.2 entry) }

/-- Go's race annotations of `sync.WaitGroup`, as accesses to a location of
its own next to the counter: an `Add` that raises the counter from zero
reads it, a `Wait` that finds the counter above zero (and blocks) writes
it, so the two race unless ordered (an `Add` concurrent with a `Wait`).
The fired operation of goroutine `g` (before the step) and the one `g'`
arrives at (after it), with the heap after the step. -/
def wgAccesses (heap : Heap Val) (g g' : Gor) : List (Val × Nat) × List (Val × Nat) :=
  let sema := fun (p : Val) => match p with
    | .ptr r path => some (Val.ptr r (path ++ [4294967295]))
    | _ => none
  let add := match pendingSync g with
    | some (.wgAdd, [p, .int _ d]) =>
      if d.toInt > 0 && (wordAt heap p : Int) == d.toInt then (sema p).toList.map (·, 0) else []
    | _ => []
  let wait := match pendingSync g' with
    | some (.wgWait, [p]) => if wordAt heap p > 0 then (sema p).toList.map (·, 1) else []
    | _ => []
  (add, wait)

/-- After goroutine `i` stepped from `gs` to `gs'` (firing `name`, completing
`partners`): its synchronisation, then its accesses, in order. -/
def raceStep (gs gs' : Array Gor) (i : Nat) (name : String) (partners : List Nat) (s : HState Val) :
    Except String (Array Gor × HState Val) :=
  let vcOf := fun (gs : Array Gor) (j : Nat) => (gs[j]?.map (·.vc)).getD #[]
  let (gs', s) :=
    if name == "spawn" then
      let child := gs.size
      let gs' := gs'.modify child fun g => { g with vc := vcTick (vcOf gs i) child }
      (gs'.modify i fun g => { g with vc := vcTick (vcOf gs i) i }, s)
    else if name == "" then (gs', s) else
    match gs[i]? >>= (syncObj · name) with
    | none => (gs', s)
    | some k =>
      let parts := i :: partners
      let joined := parts.foldl (fun v j => vcJoin v (vcOf gs j)) (syncGet s k)
      (parts.foldl (fun gs' j => gs'.modify j fun g => { g with vc := vcTick joined j }) gs', syncSet s k joined)
  let rec go : List (Val × Nat) → Array Nat → HState Val → Except String (Array Nat × HState Val)
    | [], vc, s => .ok (vc, s)
    | (p, kind) :: rest, vc, s =>
      match locKey p with
      | none => go rest vc s
      | some k =>
        match kind with
        | 2 => go rest (vcJoin vc (syncGet s k)) s
        | 3 => go rest (vcTick vc i) (syncSet s k (vcJoin (syncGet s k) vc))
        | _ =>
          match checkAccess s vc i k (kind == 1 || kind == 5) (kind ≥ 4) with
          | .ok s => go rest vc s
          | .error e => .error e
  let (wgAdd, wgWait) := match gs[i]?, gs'[i]? with
    | some g, some g' => wgAccesses s.heap g g'
    | _, _ => ([], [])
  match go (wgAdd ++ s.conc.acc.toList ++ wgWait) (vcOf gs' i) s with
  | .ok (vc, s) => .ok (gs'.modify i fun g => { g with vc }, { s with conc.acc := #[] })
  | .error e => .error e

/-- The machine where the main goroutine, still the only one, reached its
`setupEnd` (#87): the goroutines, the state, and the schedule steps and
idle count `schedule` continues with. -/
structure Snap where
  gs : Array Gor
  s : HState Val
  n : Nat
  idle : Nat

/-- Schedule until no goroutine can move. `outOfFuel` when the steps run out
(an unfair schedule spinning one goroutine ends here, not as a finding).
Daemon goroutines (tickers) always can: when only they can, `idle` counts
their steps, and once each had two without waking another goroutine the run
ends as if they were done. With `stop`, the run instead ends with a
snapshot where the main goroutine, alone, reaches a `setupEnd`. -/
def scheduleS (p : Prog) (or : Oracle) (h : Havoc Val) (stepFuel : Nat) (stop : Bool) :
    Array Gor → HState Val → Nat → Nat → Snap ⊕ (Res Val × HState Val)
  | _, s, 0, _ => .inr (.outOfFuel, s)
  | gs, s, n + 1, idle =>
    if stop && gs.size == 1 && pendingName gs[0]! == "setupEnd" then .inl { gs, s, n := n + 1, idle } else
    let cands := movable s.heap gs
    let live := cands.filter fun i => !(gs[i]!.daemon)
    let daemons := cands.length - live.length
    if live.isEmpty && (daemons = 0 || idle ≥ 2 * daemons) then
      match gs[0]? with
      | some g0 =>
        if g0.done then
          if gs.all (fun g => g.done || g.daemon || waitsOnResult g0.result g) then .inr (.ok g0.result, s)
          else .inr (.panic .goroutineLeak, { s with panicAt := some (describeBlocked gs) })
        else .inr (.panic .deadlock, { s with panicAt := some (describeBlocked gs) })
      | none => .inr (.panic .modelStuck, s)
    else
      -- A lone goroutine at `setupEnd` draws no choice: the schedule's draws
      -- stay those of the program without it.
      let atSetupEnd := cands.length == 1 && pendingName gs[cands[0]!]! == "setupEnd"
      let (c, s) := if atSetupEnd then (0, s) else schedChoice p h s cands.length
      let i := cands[c % cands.length]!
      -- The case a select takes: stepGor draws the same choice from `s`.
      let name := (selectTaken s.heap gs i (schedChoice p h s 1).1).getD (pendingName gs[i]!)
      let pkg := pendingPkg gs[i]!
      match stepGor p or h gs i { s with conc.acc := #[] } stepFuel with
      | (.ok (gs', s), _) =>
        let partners := completedBy gs gs' i
        -- `setupEnd` is no operation of the Go program: not in the trace
        -- the replay follows.
        let s := if name == "setupEnd" then s else { s with conc.sched := s.conc.sched.push (i, name, partners, pkg) }
        match raceStep gs gs' i name partners s with
        | .ok (gs', s) => scheduleS p or h stepFuel stop gs' s n (if live.isEmpty then idle + 1 else 0)
        | .error e => .inr (.panic .dataRace, { s with panicAt := some e })
      | (.error .other, s) => .inr (.outOfFuel, s)
      | (.error k, s) => .inr (.panic k, s)

/-- `scheduleS` to the end. -/
def schedule (p : Prog) (or : Oracle) (h : Havoc Val) (stepFuel : Nat)
    (gs : Array Gor) (s : HState Val) (n idle : Nat) : Res Val × HState Val :=
  match scheduleS p or h stepFuel false gs s n idle with
  | .inr r => r
  | .inl _ => (.panic .modelStuck, s)

/-- Run function `fid` on `args` in the concurrent machine: the main
goroutine and every goroutine it starts. -/
def runConc (p : Prog) (or : Oracle) (h : Havoc Val) (fid : FuncId) (args : List Val) (fuel : Nat)
    (heap : Heap Val := #[]) : Res Val × HState Val :=
  match p.funcs[fid]? with
  | none => (.panic .modelStuck, { heap })
  | some fn => schedule p or h fuel #[{ frames := [entryFrame fn args], vc := #[1] }] { heap } fuel 0

/-- Does function `fid` mark where its setup ends? -/
def hasSetupEnd (p : Prog) (fid : FuncId) : Bool :=
  match p.funcs[fid]? with
  | some fn => fn.sync && fn.blocks.any fun b => match b.term with
    | .sync .setupEnd .. => true
    | _ => false
  | none => false

/-- Run `fid`'s setup once, on `heap` (the initializers', or empty) and placeholder arguments (the
setup reads none), and snapshot the machine at its `setupEnd` (#87). The
setup's havoc reads, if any, are then the same in every trial (`h`'s, as
the exhaustive engine draws them anyway), recorded as an assumption. An
error says why there is no snapshot: the run did not get there with the
main goroutine alone, or chose between schedules on the way. -/
def captureSetup (p : Prog) (or : Oracle) (h : Havoc Val) (fid : FuncId) (fuel : Nat)
    (heap : Heap Val := #[]) : Except String Snap :=
  if !hasSetupEnd p fid then .error "no setupEnd" else
  match p.funcs[fid]? with
  | none => .error "no function"
  | some fn =>
    let args := fn.params.map fun _ => Val.unit
    match scheduleS p or h fuel true #[{ frames := [entryFrame fn args], vc := #[1] }] { heap } fuel 0 with
    | .inl snap =>
      if !snap.s.conc.arity.all (· == 1) then .error "the setup chose between schedules"
      else if let some site := snap.s.stuck then .error s!"the setup got stuck: {site}"
      else if snap.s.log.all (fun (site, _, _) => some site == p.schedSite) then .ok snap
      else
        let s := snap.s.assume "the setup's havoc reads are the same in every trial (it runs once, #87)"
        .ok { snap with s }
    | .inr (r, _) =>
      let how := match r with
        | .ok _ => "returned"
        | .panic k => s!"panic {repr k}"
        | .outOfFuel => "out of fuel"
      .error s!"the setup did not reach setupEnd alone ({how})"

/-- Continue `fid` from snapshot `snap` with the trial's arguments, whose
objects the trial allocated in `heap` (on top of the snapshot's heap). -/
def resumeConc (p : Prog) (or : Oracle) (h : Havoc Val) (fid : FuncId) (args : List Val) (fuel : Nat)
    (heap : Heap Val) (snap : Snap) : Res Val × HState Val :=
  match p.funcs[fid]?, snap.gs[0]? with
  | some fn, some g0 =>
    match g0.frames with
    | [fr] =>
      let g0 := { g0 with frames := [{ fr with env := bindParams fr.env fn.params args }] }
      schedule p or h fuel (snap.gs.set! 0 g0) { snap.s with heap } snap.n snap.idle
    | _ => (.panic .modelStuck, { heap })
  | _, _ => (.panic .modelStuck, { heap })

/-- Run `fid`: in the concurrent machine when it can synchronise, else with
the big-step interpreter. -/
def runAny (p : Prog) (or : Oracle) (h : Havoc Val) (fid : FuncId) (args : List Val) (fuel : Nat)
    (heap : Heap Val := #[]) (snap : Option Snap := none) : Res Val × HState Val :=
  match p.funcs[fid]?, snap with
  | some fn, some snap => if fn.sync then resumeConc p or h fid args fuel heap snap else run p or h fid args fuel heap
  | some fn, none => if fn.sync then runConc p or h fid args fuel heap else run p or h fid args fuel heap
  | none, _ => run p or h fid args fuel heap

end Go2Lean.GoConc
