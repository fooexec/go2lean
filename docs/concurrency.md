# Concurrency in the model (C1 to C3)

Status, 2026-10-24: the machine, the frontend lowering and the replay
stage checks are built (C1a), `context`, timers and `sync.Cond` (C1b), the
deterministic replay of the model's schedule (C1c), data races (C2), and
the enumeration of every schedule for a certificate (C3). "Built" below
lists what landed and how it differs from the plan; the rest of the page is the plan. What other
projects offer for it (Goose's channel model, Veil, Iris-Lean) is in
[related-work.md](related-work.md).

## Built (C1a)

- `Go2Lean.GoConc`: the small-step machine below; `Term.spawn` and
  `Term.sync` in the core; `SyncOp` covers channels (send, receive with or
  without `ok`, close, `select`), `Mutex`, `RWMutex`, `WaitGroup`, `Once`
  and a yield (`time.Sleep`, `runtime.Gosched`). The search engines run a
  function in the machine when it can synchronise (`Func.sync`), else as
  before. Semantics by construct: [semantics.md §6](semantics.md#6-concurrency).
- Frontend: `go`, channel operations, `select`, `close`, `len`/`cap` of a
  channel, the `sync` methods (also deferred), `sync/atomic`'s functions,
  method values (`go x.run()`), direction-only channel conversions.
  Package variables holding sync state with no initializer (`var mu
  sync.Mutex`, a struct embedding one) are cells every goroutine shares
  (`globalAddr`).
- End of a run: `deadlock`, `goroutine_leak` (not for a goroutine blocked
  on a channel the function returns), and the panics `closed_channel`,
  `sync_fatal`; the report names where each goroutine waits.
- Targets: `-tests` makes `TestXxx(t *testing.T)` targets, as GoKer's
  kernels are written; `t` is the harness's own in the replay stage.
- Replay stage: a deadlock or leak runs the call up to 200 times under a 2-second
  watchdog, a leak is a goroutine of the package blocked on a channel or
  lock after the call returned (not one alive before); a panic in a
  goroutine the call started ends the test binary and is judged from Go's
  report on stderr, also when the call returned first (`Deadline.Run`
  timing out before its goroutine panics, #15).
- Not as planned: there is no separate `no_block` property; `no_panic`
  covers deadlocks and leaks (they end the run like a panic). Fuel counts
  goroutine steps, and the schedule is one havoc site.
- Ground truth: `examples/conc` (6 planted bugs in `conc.go`, 3 more with contexts, timers and a condition variable in `timeouts.go`);
  `scripts/goker.sh` runs GoKer.

## Built (C1b)

- `context`: a context is a heap object (done channel, error, children);
  `WithCancel`, `WithTimeout` and `WithDeadline` make a child, the cancel
  func is a synthetic function of the program (`$context.cancel`) that
  closes the done channel and its children's, and a timeout or deadline is
  a goroutine that cancels at any scheduling point (`$context.deadline`):
  the deadline may pass at any time.
- Timers: `time.After` and `NewTimer` are a one-value channel and a
  goroutine that sends on it at any point without blocking (`$time.fire`);
  `NewTicker` a daemon goroutine (`$time.tick`, `Func.daemon`) that does
  not keep a run alive; `AfterFunc` a goroutine running the function.
- `sync.Cond`: a ticket queue in the cond's `notify` field (`condEnter`,
  `condSignal`, `SyncOp.condWait`); `Wait` unlocks and relocks `L` through
  `sync.Locker`, whose `Lock`/`Unlock` dispatch on the boxed type (a
  `*Mutex`, a `*RWMutex`'s writer side, its `RLocker()`'s reader side).
- Also: deferred calls through func values (`defer cancel()`), `go` of an
  interface method, `go close(ch)`; wrappers around synchronising methods
  (`T.M~NewT`) now get frames; the writer-preference rule blocks new
  readers only while a writer waits for readers to leave (a writer that
  could take the lock is not waiting yet); the replay stage leak scan waits while a
  goroutine of the package is still running or sleeping.

## Built (C2)

- Data races in the model: vector clocks per goroutine and per
  synchronisation object, and a FastTrack shadow per location (last write,
  reads since); semantics in [semantics.md §6](semantics.md#6-concurrency).
  The shadow is a hash map by location: an access checks its own location
  and the enclosing ones by lookup, and the ones inside it only when the
  object has deeper ones (a list scan made a setup of 16384 maps take
  minutes per run, #71).
  The dialect lists each operation's heap accesses (`Dialect.accesses`),
  the machine checks them after every goroutine step. The interpreter
  records them only when its `track` argument is set: the concurrent
  machine sets it, also for the callees it runs big-step; sequential runs
  and the prover pass a literal `false`, which `simp` folds away. A race ends the run with
  the panic kind `data_race`, naming the location and both goroutines.
- `sync/atomic`'s functions are `atomicLoad`/`atomicStore`, which acquire
  and release their location: no race between atomics, and an atomic
  publishes the writes before it. An atomic access is also checked against
  plain ones (since C3: an atomic add and a plain read of the same field
  race, as `go test -race` says).
- Package variables written by a function other than `init`, in a package
  that starts goroutines, are cells every goroutine shares (as the
  sync-holding ones of C1a), starting at their zero value or at a constant
  initializer (`var interval = 10 * time.Second`), so a race on a package
  counter or setting is seen.
- Replay stage: a `data_race` finding runs `go test -race` up to 20 times;
  when no run reports a race in the package's code, the model's schedule
  is replayed under `-race` (C1c). The replay controller is invisible to
  the race detector (`//go:norace`, `runtime.RaceDisable` around its own
  lock), and a report whose first program frame is the controller or the
  harness is ignored, so the report the PoC shows is the program's race.
- Ground truth: `examples/conc/races.go`, 4 planted races (an unguarded
  counter, a read before the channel that orders it, a map read without
  its `RWMutex`, a package counter) next to their race-free twins (mutex,
  atomic, channel hand-over, `RLock`).

## Built (C3)

- The exhaustive engine enumerates schedules: for each input whose run
  drew from the schedule site, it reruns the input under every schedule,
  depth first. Each draw records how many choices it had (the ready
  goroutines, or a `select`'s ready cases; `ConcState.arity`), a schedule
  is the prefix of choices forced through the havoc function (the first
  option after it), and the next one advances the last draw with an
  untried option.
- What it certifies: a goroutine runs atomically between two
  synchronisation points, so the choices at those points are every
  interleaving of race-free code; the accesses in between are checked by
  C2's race detector, which reports any two unordered ones. An input whose
  schedules all ran, none out of fuel and with no other havoc read, is
  certified for every schedule: a property that held on every such input
  is `exhausted`, where before it was `exhausted_havoc` (sampled
  schedules).
- Bounds: 2000 schedules per input and 10,000 per property (and the
  search budget); an input past them counts as sampled, as before, and
  the summary says how many. There is no partial-order reduction yet, so
  a function with many goroutines or a large input domain (`Sum` over
  slices, `BumpAll`) stays sampled; a test or a GoKer kernel (one input)
  is where it pays. A schedule that spins a goroutine out of fuel (a
  polling loop) is one Go's scheduler does not keep taking: no finding,
  no certificate.
- The concurrent machine's fields of the run state (schedule, choice
  counts, race detector) are one sub-structure, `ConcState`, so the
  sequential interpreter's state stays small for the prover; the prover
  is not tried on a function whose schedules were enumerated.
- A certificate is only as good as the model under it, and certifying
  every schedule made two gaps visible on GoKer, where every kernel has a
  bug: istio 8214 races an atomic add with a plain read of the struct,
  which C2 did not check (now it does, and the race is confirmed), and
  istio 8144 calls `sync.Map`, whose calls were dropped as harmless "in a
  single goroutine", a note from before C1 that recorded no assumption
  (now an assumption: `exhausted_assuming`). Both were `exhausted_havoc`
  before, so the sampled-schedule status had hidden them.
- `examples/conc`: of its 27 properties that hold, 23 are now
  `exhausted` (2 before, the rest `exhausted_havoc`); `Sum`, `BumpAll`,
  `Poll` (a ticker: a schedule spins) and `cache.put` stay sampled.
  `SnapshotRacy` (an atomic add against a plain copy) is a race,
  `SnapshotAtomic` its race-free twin.

## Built since C3 (C1 leftovers)

- External test packages (`package x_test`): with `-tests` their tests are
  targets too; the replay stage builds them through their directory's
  package and writes the PoC in package `x_test`. GoKer's syncthing 5795,
  a kernel written as one, is now confirmed.
- `WaitGroup` races: an `Add` concurrent with a `Wait`, as Go's race
  detector reports it (semantics.md §6). `examples/conc`: `AddInside` (a
  race), `AddBefore` (race-free).
- A test that reached `t.Error` (or any method of `*testing.T` through its
  embedded `common`) got stuck in the model: go/ssa takes `&t.common`
  first, and the test handle is opaque. Its fields are a placeholder now,
  as the calls on it are ignored; 17 of go-cache's tests that were never
  explored now run (docs/eval/conc.md, tool issue 5).
- Where a run gets stuck is named by function and block, in the
  concurrent machine too. A synchronisation operation the machine cannot
  fire (a lock on a pointer it does not know) names its own goroutine's
  frame. A callee that got stuck earlier and was skipped leaves no place
  behind (#71: k009's stuck lock was named after xxhash's `Sum64`).
- **Setup snapshots (#87).** A concurrent sequence harness (a
  `go2lean:sequence` function with a `go` statement) often builds its
  object before its goroutines start. The frontend reorders the harness's
  entry block so that everything before the first read of a parameter or
  the first `go` statement comes first, then ends it with a `setupEnd`
  sync op.
  - **Captured parameters.** A store of a parameter into a local cell
    moves after the cut; a read of that cell counts as a read of the
    parameter.
  - **The marker.** `setupEnd` completes at once, draws no schedule choice
    and is not in the trace the replay follows.
  - **The snapshot.** The search runs the setup once, on placeholder
    arguments and an empty heap, and snapshots the machine at `setupEnd`
    with the main goroutine still alone. Every trial and every schedule
    the exhaustive engine enumerates then resumes from that snapshot, with
    its arguments in the entry frame and its input objects allocated on
    top of the snapshot's heap.
  - **What changes for a trial.** Its setup's havoc reads, if any, take
    the same values in every trial (one draw, recorded as an assumption);
    relevance cannot perturb them.
  - **When there is no snapshot.** If the setup chose between schedules,
    got stuck, or did not reach `setupEnd` alone, there is no snapshot and
    each trial runs from the start. The search's JSON says which
    (`setup_snapshot`).
  - **Effect.** Evaluation 7's k016 (prometheus' agent DB, three
    16384-element stripe arrays) took 28 s per trial, nearly all of it
    setup. With the snapshot, 200 trials take 43 s (the setup about 38 s
    of it), and 5000 take 240 s. The bug was still not found: it needs two
    appenders with equal labels and a narrow interleaving, which wants
    schedules enumerated for a fixed input.
  - **Example.** `examples/conc/setup.go`.

## GoKer

`scripts/goker.sh <gobench> <out>` on all 103 kernels (GoBench at
2e91eb1), each kernel's test function as the target, default search
budget, 3 kernels at a time, 2026-10-24, with C3. The kernels are the
ground truth: each blocking kernel has a blocking bug its test can
trigger; most non-blocking ones have a data race.

| | blocking (68) | non-blocking (35) |
|---|---|---|
| bug found by the model, confirmed by `go test` | **60** (21 deadlocks, 37 leaks, 1 double unlock, 1 data race) | **27** (24 data races, 2 `closed_channel`, 1 nil dereference) |
| found by the model, refuted by `go test` (spurious) | 1 | 3 |
| nothing found | 7 | 5 (holds) |

How it grew: blocking 35 with C1a alone (no `context`, timers or `Cond`;
16 kernels got stuck on a channel the model did not know), 53 with C1b,
when 6 findings stayed unreproduced by Go's scheduler, 59 with C1c (the
replay of the model's schedule); non-blocking 6 before C2, 25 with C2,
26 with C3's atomic accesses (istio 8214), 27 with `WaitGroup` races
(cockroach 4407); blocking 60 with external test packages (syncthing
5795). A confirmed
finding is the real kernel deadlocking, leaking, panicking or racing,
not the model. About 6 blocking ones (cockroach 7504, 10214, 16167, istio
16224, serving 2137, and, depending on the run, cockroach 1462, etcd 5509
or kubernetes 1321, 62464) only show up with the model's schedule
replayed; which ones moves a little between runs, as Go's scheduler
varies.

- Data races: every race the model reports, `go test -race` confirms,
  most on the plain run, the rest with the model's schedule replayed. In
  4 kernels the race is the first bug the model meets, before the leak it
  reported with C1 (moby 27782, istio 8967, kubernetes 79631, 88331), and
  etcd 3077 (C1c's unreproduced deadlock) and etcd 8194 (C1c's spurious
  slice bound) turn out to be races, and istio 8214 (an atomic add
  against a plain read) since atomics are accesses. Missed: `sync.Map`
  (istio 8144, now an assumption) and a package map built by its
  initializer (istio 16742).
- Spurious, each rejected by the replay stage: `recover` (etcd 7443
  recovers its send on a closed channel), unmodelled `os/exec` and a
  callback (moby 18412, serving 6472), a data race the model's
  sequentially consistent steps turn into a panic before the race
  detector sees it (kubernetes 13058). Hugo 3251 is counted confirmed
  here but moves between runs: the model's `sync_fatal` (a mutex from a
  map the model does not know) stands in for the kernel's real deadlock,
  which shows up in one real run often but not always under load.
- Not found: `fmt` calling a `String` method that takes a read lock
  (cockroach 6181), `io.ReadFull` (grpc 1275), `cond.Wait` passed as a
  func value (kubernetes 11298), a sync-holding package variable set by
  `init` (moby 29733), and three whose trials all run out of fuel (grpc
  862, istio 18454, kubernetes 58107: polling loops). Non-blocking: a
  channel misuse (grpc 1687) and two testing-library bugs (serving 4908,
  6171).

## Why

go2lean's properties so far need a spec, except `no_panic`. Concurrency
gives spec-free properties that matter: a test or a function that spawns
goroutines must not deadlock, must not leave goroutines blocked forever
(a leak), and must not panic from misusing a channel or a lock. Existing
Go tools split the space:

- `go test -race` finds data races in the interleavings a run happens to
  take; it says nothing about blocking bugs.
- Static checkers (GCatch, Gomela, dingo-hunter) model channel and lock
  operations but abstract data away, so a blocking bug that depends on a
  value (a full buffer, an error path that skips an `Unlock` or a send)
  is missed or misreported.

go2lean already models the data: the program's own code, values and heap.
Adding goroutines and synchronisation to that model lets the search vary
inputs and schedules together, and lets the exhaustive engine state
"no deadlock, no leak, no panic under every schedule" for a bounded
configuration: a certificate neither `-race` nor fuzzing gives.

Ground truth: GoKer, the kernel half of GoBench (Yuan et al., CGO 2021):
103 small programs (median 86 lines) extracted from real concurrency bugs
in Kubernetes, etcd, gRPC, Docker, CockroachDB and others, 68 blocking
and 35 non-blocking, each with a test that can trigger the bug. 31 of
the blocking and 16 of the non-blocking kernels use only goroutines,
channels, `select`, `Mutex`, `RWMutex`, `WaitGroup` and `Once`; most of
the rest add `time` and `context`.

## Scope of C1

| Go | model |
|---|---|
| `go f(x)`, `go func(){...}()` | a new goroutine, runnable |
| `make(chan T)`, `make(chan T, n)` | a channel object on the heap: capacity, buffer, closed flag |
| `ch <- v`, `<-ch`, `v, ok := <-ch`, `close(ch)`, `len(ch)`, `cap(ch)` | Go's semantics: unbuffered is a rendezvous, a send on a closed channel and a close of a closed or nil channel panic, a receive on a closed channel gets the zero value and `ok == false`, a nil channel blocks forever |
| `for v := range ch` | receives until closed |
| `select` with send and receive cases, with or without `default` | one enabled case, chosen by the schedule; blocks when none is enabled and there is no default |
| `sync.Mutex` `Lock`, `Unlock`, `TryLock` | a lock word in place of the field; `Unlock` of an unlocked mutex is a fatal error (reported as a panic) |
| `sync.RWMutex` | writer preference, as Go: a blocked `Lock` stops new `RLock`s (the classic read–write–read deadlock) |
| `sync.WaitGroup` `Add`, `Done`, `Wait` | a counter; negative is a panic; `Wait` blocks until zero |
| `sync.Once` `Do` | done / running / not started; a second `Do` blocks while the first runs |
| `time.Sleep`, `runtime.Gosched` | a scheduling point (any goroutine may run next); time itself is not modelled |

Later in C1: `sync.Cond`; `time.After`, `NewTimer`, `NewTicker` (a
channel a timer goroutine may send on at any scheduling point);
`context.WithCancel`, `WithTimeout` (a done channel closed by `cancel` or
by a timer goroutine). Out of C1: data races (C2: happens-before in the
model), `sync/atomic` ordering, `runtime.NumGoroutine`.

## The machine

The existing interpreter (`Go2Lean.SSA.runFunc`) is big-step: a call runs
to its end. It stays as it is, for sequential code and for the prover.
Concurrent code runs in a small-step machine (`Go2Lean.SSA.Conc`):

- **State**: the heap (channels and lock words live there), the
  goroutines, and the havoc state. A goroutine is a stack of frames
  (function, block, environment, where the caller continues) and a
  status: runnable, blocked at a synchronisation term, or done.
- **Atomic steps**: a goroutine runs from one scheduling point to the
  next. Instructions and calls to functions that cannot reach a
  synchronisation operation (the frontend marks them) run with the
  big-step interpreter, atomically. Calls to functions that can push a
  frame.
- **Scheduling points**: every synchronisation term (`go`, channel
  operations, `select`, lock and wait-group operations, `Once.Do`,
  `Sleep`). At each, the schedule picks which runnable goroutine moves
  next: the next value of a dedicated havoc site, modulo the number of
  enabled goroutines. So a schedule is part of a trial's input, the
  random engine varies it, a counterexample records it, and the
  exhaustive engine enumerates it for small bounds.
- **Blocking**: a goroutine at a term that cannot fire (a receive on an
  empty channel, a `Lock` of a held mutex) is blocked; it is enabled
  again when the term can fire. An unbuffered send blocks offering its
  value; a receive (or a `select` case) takes an offer and completes the
  sender's term.
- **End of a run**: when the main goroutine (the function under test)
  returns, the others run until none is enabled. Then:
  - all done: the run returns the main goroutine's result;
  - some blocked: **leak** (a goroutine blocked forever);
  - the main goroutine blocked and none enabled: **deadlock** (Go's
    "all goroutines are asleep").
  A panic in any goroutine ends the run with that panic, as in Go.

Fuel counts steps across all goroutines, so an unfair schedule that spins
one goroutine runs out of fuel instead of looping (reported as such, not
as a finding).

## Frontend

- `go`, channel operations, `select`, and calls of the `sync` methods
  above lower to synchronisation terms instead of being rejected.
- Each function is classified: it *can synchronise* when it, or a
  function it calls (statically, through a known func value, or by
  interface dispatch to a modelled method), performs one. Only those get
  frames in the machine.
- Lock words: a `sync.Mutex` field is a heap slot like any field; its
  zero value (today an opaque value) reads as unlocked.

## Properties and targets

- `no_block`: the function returns, and when it does, no goroutine it
  started is blocked (no deadlock, no leak). Generated for every function
  that can synchronise, like `no_panic`, and for test functions
  (`TestXxx(t *testing.T)`, `t` unused or used only through the methods
  the fuzz-target import already rewrites), which is how GoKer's kernels
  are written.
- `no_panic` on such functions now covers the panics of misusing a
  channel, a mutex or a wait group.

## Checking against Go (replay stage)

Built as planned, in two steps:

1. The target runs under `go test` up to 200 times (a 2-second watchdog
   per run, a goroutine dump for leaks); most findings show up here.
2. When none does: deterministic replay (C1c, `internal/replay`). The
   model records its schedule, one entry per step: the goroutine that
   moved, the operation it fired (`select:k` with the case taken) and the
   rendezvous partners it completed. The package is rewritten through an
   overlay so that every synchronisation operation first passes a gate of
   a replay controller: a receive `<-ch` becomes `<-gate("recv", ch)`, a
   `mu.Lock()` passes its gate with the receiver bound, a `select`
   evaluates its channels, passes its gate and nils every channel but the
   model's case, `go f(x)` evaluates `f` and `x`, passes the spawn gate and
   registers the goroutine with the next model id, and timers, deadlines
   and `sync.Cond` are the controller's own. The controller lets each
   goroutine through in the schedule's order, waits until it reaches its
   next gate, and checks that the operation there is the one the model
   fires. A new goroutine is held at a start gate until the model's step
   that starts it, so the code before its first operation (an atomic
   store, say) runs when the model's does. Only the target package is
   rewritten: each schedule step names the package of its code, and a
   step of another package (a dependency's lock, under `-deps`) passes
   without a gate, the goroutine firing it on its way to its next gated
   operation (#56). A goroutine started in such a package cannot be
   followed (its model id is unknown), and the replay says so. When the schedule ends, the goroutines it left blocked are
   released in the order they arrived and Go's runtime decides: a
   deadlock or leak that shows up then is the real program's, under the
   model's interleaving. The PoC carries the rewritten files and the
   overlay, so `go test -overlay ...` reproduces it.

A panic or a false result the model found in a run with more than one
goroutine goes through the same two steps (#52): one run under Go's
scheduler does not refute a schedule-dependent finding. If no run violates
the property, the verdict is inconclusive (`schedule_not_reproduced`) until
the replay decides; a replay that ends in the model's panic confirms it. If the replay follows the whole schedule and the
property still holds, the verdict is spurious (`holds_on_model_schedule`),
a disagreement between the model and Go.

## Measures

On GoKer: kernels modelled (all their functions complete), blocking bugs
found by the model, confirmed by `go test`, found with or without the
deterministic replay, time; non-blocking kernels are C2's. Fixed versions
of the kernels (from the linked patches) measure false positives.
