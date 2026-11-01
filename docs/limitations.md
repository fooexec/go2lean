# Limitations and open problems

What go2lean does not deliver yet, stated against what the design promises.
Written from an outside review (2026-10-21); each item names what exists, the
gap, and the planned work. The README and the design doc should not claim
more than this page allows.

## 1. Proofs cover a narrow class of functions

The design promises two outputs: a confirmed PoC, or a Lean theorem. Most
properties still end in a bounded search, and `exhausted` is a search, not
a proof: `go2lean-search` (compiled Lean, the same interpreter that finds
counterexamples) computes it, and no kernel checks it.

What is proved, and how ([trust](trust.md) for the exact claims):

- **`no_panic` of integer and bool functions**, in the pipeline: a
  bounded theorem by case split and `bv_decide`, with the trust base read
  from `#print axioms` (`kernel`, or `compiler` when `bv_decide`'s
  certificate check uses `Lean.ofReduceBool`). One fixed oracle, runs
  within a fuel.
- **`no_panic` and specs of loop-free functions** (integers, bools,
  strings, integer slices), in the pipeline, by `wp_auto`.
- **Postconditions given to `go2lean prove`**, loops included, with
  invariants an agent or a person writes.

The second and third are the function's contract for every input, every
value the model does not know, and every run that ends. They are judged
by Lean's comparator against go2lean's own statement, in a sandbox, with a
certificate.

CI's regression gate runs without the proofs (`PROVE=0`), so a change that
breaks a proof is caught only by a local `scripts/regress.sh` run with
`PROVE=1`. #94 went unnoticed this way. One more operation made the dialect's
semantics too large for the equation lemmas `simp` needs. Lean makes these
lemmas under the default heartbeat limit of the module that asks for them,
not under the proof's options. `GoSSA.lean` now makes them itself, under
its own limit.

The hand proofs (`lean/Proofs/Functional.lean`) are the agent's worked
examples. The proofs workflow (run by hand) checks them by `#print axioms`,
not by the judge.

Missing:

- **Termination.** The theorems are about runs that end; a spec that does
  not end satisfies any.
- **Spare capacity.** A slice is taken with capacity equal to its length,
  so `append` into spare capacity is not covered, though the search
  generates it.
- **Other parameter kinds:** structs, pointers, maps, methods.
- **The LLVM line.**
- **Real code.** On three string libraries no function's contract was
  provable ([eval](eval/prove-blind.md)): the contract counted the model's
  own over-approximations as failures. Since then a havoc value has its
  site's type, `Grow` is modelled, the model's slice bound (16384 elements, raised
  from 1024 so `bufio`'s 4096-byte buffers fit, #71) ends a
  run like fuel (so, like fuel, it proves nothing about larger slices),
  and `prove` links dependencies. Real code is not re-evaluated yet.

Every theorem is about the model; how faithfully the model follows Go is
§5's question. The differential checks of proved properties bear on it,
but no proof answers it.

## 2. Under-approximations are not tracked mechanically

P1 says the model may over-approximate, never under-approximate.
`docs/semantics.md` lists places where it does under-approximate: a loop over
an unknown map runs zero times, an update to one is dropped, `len` of one is
havoc below 16; writes to globals are dropped, `init` does not run (except the stores it makes on every path, since 2026-10-23), `defer`
of an unmodelled function and calls without results are ignored; two non-nil
errors compare equal; a slice from an unmodelled function has 0 to 3 zero
elements. A spec verdict that read a havoc value is reported as unknown, but
the other assumptions flow silently through a run, and the exhaustive engine
does not enumerate havoc sites.

A counterexample that read havoc says which reads its violation depends on
(`relevant_havoc`, #84): the model reruns it once per read, that read
changed (an integer one up, one down and zero, a bool or an error the other way)
and the others kept, and a read whose change makes the violation go away
is relevant. Strings and assumptions are fixed guesses and are not
perturbed (`havoc_perturbed` counts the reads that were), and the
schedule's draws are left to the replay. A run that took more than 5 s is
not rerun per read (`relevance_skipped`): evaluation 7's k016 takes about
100 s a run and reads dozens. An empty set is evidence only when
the run took no assumption: on evaluation 7's spurious cases, three had a
few relevant reads (in `creack/pty`, sarama, containerd's streaming proxy)
and three had none, their violations coming from assumptions instead (a
global taken as an empty slice, test options taken as zero, a stuck
`xxhash.NewWithSeed`); both name what to model next (#85).

Refining what those sites named, by hand (#85's first experiment), moved
every case: each refinement removed its violation, and the search either
found none or a new one further on, the next site to refine. Most sites
were havoc outside a documented contract (a negative count from `Read`, a
negative `Len`, a `rand.Perm` of the wrong length), so unmodelled calls
with such a contract now keep their havoc within it, and a global holding
`[]byte` of a constant is known; k045 then holds, k009 holds but with most
runs stuck. The loop itself is not automated yet.

So an `exhausted` result means: no counterexample on a small finite domain,
havoc sites not enumerated, and the run may have taken an
under-approximating branch. Which assumptions invalidate a given certificate
is not recorded.

Since 2026-10-21 under-approximations are recorded as the run takes them.
The frontend marks each site it lowers to one (a dropped store to a
global or into a handle, an ignored call or `defer`, a map update on an
unknown map, an assumed pointer, slice or zero value for an unmodelled
result, a length kept below 16) with an `assume` instruction; the core
records a callee the model got stuck in (its effects dropped) and calls
through unknown function values or into packages not linked; the GoSSA
dialect records the run-time ones (a `range` over an unknown map that ran
zero times, two unknown non-nil errors compared equal). An exhaustive pass
whose runs took any of them is recorded as `exhausted_assuming`, with the
list, instead of `exhausted`; counterexamples carry the list too. On the
example packages 4 of 101 certificates changed (a store to a global
dropped, a callee the model got stuck in, an assumed `errors.As` target).

Still open: time itself is not modelled (a timer or a deadline may fire
at any scheduling point; `time.Now` is havoc), and `context.WithValue`,
`ctx.Value` and `ctx.Deadline` are havoc, with a note in the model report
(§7); havoc sites are still sampled, not enumerated
(`exhausted_havoc`); and the list is not yet a hypothesis of any theorem.

## 3. Confirmed is not the same as actionable

The replay stage shows that the model's counterexample reproduces in Go, not that a real
caller can build that input. In the sixth full pass, 741 of 912 confirmed
results involved a nil input; in the seventh, 231 came from constructors
handed forged third-party structs (a `*gorm.DB` whose internal maps are
nil), a class "handles" (docs/history.md §9b) later removed, one heuristic at a
time. In
the blind-spec experiment, 5 of 39 confirmed results came from wrong specs:
confirmed measures a violated oracle, and sometimes the oracle is the bug.
The 82 bugs published for tingly-box are the result of hand triage; the
tool itself does not rank. `nil_opaque_input` is a first tag, not a ranking.

Since 2026-10-21 every confirmed verdict carries a `rank`
(`internal/poc/rank.go`, `nilflow.go`; `go2lean rank` re-ranks an existing
workspace, `go2lean summary` groups the findings by tier):

| tier | input |
|---|---|
| 1 | plain values any caller of the package's API can pass; a caller in the module can pass the same nil; or every caller passes constants and these are among them |
| 2 | plain values to an unexported function whose callers pass variables, a nil a caller may pass (a field, a global: not traced to a nil), or a hand-written spec violated |
| 3 | a nil no caller in the module can pass, an unexported function whose callers all pass other constants, or an agent-written spec violated |
| 4 | an input the test forged: an opaque value left nil, package state the program may initialise first |

Nil-ness is traced through the callers' SSA (since 2026-10-22): a nil
constant through phis and conversions, a caller's parameter up to three
callers up, the results of functions that return nil; a call reached only
past `v != nil` (a dominating check) is not counted; a nil receiver is also
traced to the places a nil pointer of its type is stored in an interface
(a typed nil). On the 53 confirmed results of an older tingly-box workspace
(`out-tingly6`) this gives 1 / 10 / 42 / 0: 33 are nil receivers of methods
called only through an interface (mostly `Error()`) where no nil of that
type is ever boxed; 5 have call sites none of which can pass nil; 4 are
`truncate(s, n)` helpers whose callers all pass constants such as 30 or
200, so a negative `n` is not reachable; 5 have callers passing a field
whose nil-ness is not traced. The first version (literal `nil` at the call
site only) had marked one finding tier 1 that a dominating `e != nil`
check rules out.

## 4. Evidence comes from one codebase

The heuristics of docs/history.md §9c (documented enums, Stainless SDK `param.Opt` and
`apijson`, SDK unions, GORM handles, lookup tables, Gin contexts) were
written for tingly-box, and the benchmarks (17/17, 31/34) are bugs found by
hand in the same codebase: read, find, write a spec, tune go2lean until it
finds it. one-api and spf13/cast were used only to collect constructs
(`examples/portable`), with no yield reported.

Planned: a blind run with a frozen tool version and a protocol fixed in
advance on codebases of other kinds (a CLI, a pure-Go database, a Kubernetes
controller), with no tool changes during the measurement.

First blind run (2026-10-22, `docs/eval/semver.md`): Masterminds/semver,
protocol committed before the run. Templates alone: 8 confirmed, no bug.
The agent's 21 blind specs: one bug confirmed (bare wildcards with `^`
and `<=`; the `<=` half new), and Go's own fuzzer, given the same specs,
found it plus one more new bug in seconds. One model gap (package
`init()` not run, so regexps compiled there are unknown) made 44 of 53
counterexamples spurious. The value on that package was in the specs,
not in go2lean's search.

## 5. Hand-written semantics; the cross-check

About 2,860 lines of GoSSA dialect and hand reimplementations of strings,
`strconv`, `regexp`, `encoding/json`, `apijson`, `net/url` are checked only
by sampled differential tests (`scripts/json-diff.sh`). Several model bugs
gave wrong answers rather than getting stuck (`RawMessage` HTML escaping,
`omitzero`, `apijson` coercion); each such bug can hide a real one.

The language core is checked against Go on Goose's semantics tests
(`testdata/goosesem`, `scripts/sem-diff.sh`, in CI): 111 small programs
that return true in Go, after Goose/Perennial's way of keeping their model
honest. On 2026-10-29 the model agreed on 102. Fixing what the other 9
showed (#60: `==` on interface values was havoc; #61: an interface call on
`*T` did not reach a method declared on `T`) also exposed a wrong answer on
modelled code: a type alias got a type tag of its own, so
`any((*B)(nil)) == any((*A)(nil))` with `type B = A` was false. With
those fixed, the model agrees on 110. The last one was a `defer` in a
loop, which the compile stage rejected (#62); it now runs on a defer
stack. go2lean adds its own tests to the suite (`go2lean_*.go`) for each
gap it closes (`sync/atomic`'s typed values #67, `container/list` and
`bufio` #69 #71, package initializers #55, defers in loops #62,
`errors.Is`/`As` and `io` #69): the model agrees on all 137, and
`expected.txt` is empty.

The independent check is the TinyGo line (design §4.4): the same package
compiled by TinyGo to LLVM IR, with the standard library and runtime taken
from their real source and run with VeIR's semantics, so it shares none of
the hand-written models. It compares verdicts per function today; it does
not share `ir.Prog`, and it does not yet compare return values on the same
inputs, which is what would catch a model that runs but computes the wrong
value.

First result (2026-10-21, design §4.4): on the 94 exported functions of
the example packages the two lines agree on 73, 29 of them on the same
confirmed bug; none of the 6 differences was the two models computing
different values, and 15 functions the TinyGo line does not model yet
(stuck in `strconv`, `encoding/json`, `regexp`). The examples are small and
were written for the Go line, so this says little about the hand-written
models on real code; running the comparison on tingly-box packages, and
comparing return values, are next.

## 6. Execution isolation and its limits

The history records `log.Fatal`/`os.Exit` killing a test batch (four
rounds), functions writing files, a 14 GB build cache filling the disk and a
9 GB search killed by the OOM killer. The docs advise a scratch clone; the
original tool did not enforce isolation. The `agent` subcommand has an LLM
write specs that are compiled with the user's repository.

Since 2026-10-21 (`internal/sandbox`): the harness joins the package
through `go test -overlay` (nothing is written into the package), the test
binary is built with `-c` and runs in a fresh copy of the package
directory under [bubblewrap](https://github.com/containers/bubblewrap)
(`bwrap`, the unprivileged sandbox Flatpak uses: the file system read-only
except that copy, a private `/tmp`, no network, its own PID namespace,
killed with go2lean) and `prlimit` (8 GB address space, 300 s CPU, 256 MB
files, 10 min wall clock). Without a usable `bwrap` (not installed, or
user namespaces disabled) replay now refuses execution by default.
`GO2LEAN_SANDBOX=rlimits` explicitly permits limits-only execution if prlimit
is available; `GO2LEAN_SANDBOX=off` disables confinement and limits. These
opt-outs are for trusted code only. Replay also hides the host home directory
in bwrap mode and starts from a minimal environment, not the host's
credentials. Agent Lean checks clear the inherited environment too, retaining
only PATH, ELAN_HOME, a private HOME/TMPDIR and a fixed locale. The rest of
the host remains read-only but readable, so this is not a boundary for all
host secrets. `go2lean doctor` checks prerequisites. The C and TinyGo
replays use the same runner (ASan runs without the address-space limit).
The build step itself (`go test -c`) still runs cgo and the toolchain
outside the sandbox, as any build does. Not covered: the `agent`
subcommand's own LLM calls, and the search, which runs only the
generated Lean model.

## 7. Smaller items

- Unit tests are thin (7 test files for 35 Go files); the gates are the
  end-to-end regression and differential scripts, which are slow.
- Concurrency (C1, [concurrency.md](concurrency.md)): goroutines, channels,
  `select`, mutexes, wait groups and `Once` are modelled, and the search
  finds deadlocks, leaks, misuse panics and data races (C2), with
  `context`, timers and `sync.Cond`; but happens-before is
  over-approximated (some races are missed), the machine is sequentially
  consistent (no weak-memory reorderings), time is not modelled (a
  timeout may fire at any point, so a finding may need a deadline to
  pass early), a certificate
  for a concurrent function covers sampled schedules only
  (`exhausted_havoc`), and the replay of the model's schedule needs the
  package rewritten (an overlay), so a bug Go's own scheduler never shows
  is confirmed under that rewrite, which the PoC carries. Properties are about one run of a function;
  properties of a sequence of calls over a finite input are written as
  `go2lean:sequence` specs (design §5.2) and checked by bounded model
  checking, which covers "always" after each step and "eventually" at the
  end of a finite stream, not infinite traces or fairness.
- The agent comparison (Opus vs MiniMax-M3) rests on single runs, without
  repetitions, cost accounting or ablations.
- Resource limits are ad hoc (64 KB strings, 8 GB cache sweep, 24 leaves
  for exhaustive search, 64 type-switch cases).
- `docs/design.md` mixed the current design, superseded plans and a
  development log; since 2026-10-21 the log and the old plans are in
  `docs/history.md`. Some sections (5.3, 5.4) still read as a record of
  how they were built.
