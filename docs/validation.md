# Validation

[← Back to README](../README.md)

[Scope and evidence](#scope-and-evidence) · [Ground truth](#ground-truth)

## Scope and evidence

What it is: a bounded, search-based bug finder (random search steered by the program's
constants, exhaustive search of small domains), not a symbolic executor;
the pipeline proves over all inputs `no_panic` of integer and bool
functions and the specs of loop-free ones, and the postconditions `go2lean prove` is given: loop-free
functions alone, loops with invariants an agent writes
(`examples/invariants`, `examples/loops`: nested loops, bubble sort) or by hand (`lean/Proofs/Functional.lean`: up to
insertion sort, sorted and a permutation, for every slice). Each proof is
judged by Lean's comparator against go2lean's own statement; a red-team
corpus of 17 attacks on that judgement is rejected (in the on-demand
proofs workflow), and the
properties `go2lean prove` proves on the examples are checked again on the
real Go code, where a mutant of each proved function is told apart and
fails to prove ([trust](trust.md), [eval](eval/proof-trust.md)). 31 ground-truth packages run in CI (260 properties, each outcome
pinned, including every spec's mutation score; their Lean proofs only in
the proofs workflow). The design is in
[docs/design.md](../docs/design.md); the Go semantics modelled, and how
faithfully, in [docs/semantics.md](../docs/semantics.md); what it does not
deliver, in [docs/limitations.md](../docs/limitations.md). There are no
releases yet: the CLI, the workspace formats and the model's semantics
still change from one pull request to the next.

A [first run on four unseen projects](../docs/eval/first-run.md)
(go-humanize, golang-lru, cast, gjson), as a new user runs it with the
default flags, gave real panics with PoCs in two of them. It also showed
what got in the way: dependencies not translated by default, no
end-of-run summary, generic packages skipped silently, and `doctor`
misreading `GOTOOLCHAIN`, all fixed since.
A [second run on seven more](../docs/eval/second-run.md) (pflag, toml,
mux, backoff, decimal, go-shellquote, xid) confirmed panics in five. It
also fixed what stopped two of them at the hour cap: exhaustive domains
built whole before the budget check, and PoCs whose real run never ends.
Since then, `math/big`, package initializers, run-time regexps and calls
through a nil interface are modelled (#177 to #180). decimal, mux and
backoff now run with no spurious or inconclusive verdict. The noise from
unexported functions remains.
A [third run on seven more](../docs/eval/third-run.md) (uuid, lo,
go-runewidth, semver, cron, ulid, go-wordwrap) finished each project within
six minutes. Its spurious findings point to the next gaps: `recover`,
interface-typed package variables, `golang.org/x/text`, tables filled by
`init` functions, and constructors named `New`.
A [fourth run on seven more](../docs/eval/fourth-run.md) (chi, envconfig,
msgpack, conc, go-multierror, go-toml, golang-lru) led `run` to search
only the API's templates by default (`-unexported` for the rest), which
cut the properties by two thirds and the confirmed count with them, and
to model `sync/atomic.Pointer`. After the fixes, two verdicts in the seven
projects are spurious or inconclusive.
A [fifth run on seven more](../docs/eval/fifth-run.md) (go-version, btree,
pkg/errors, logrus, websocket, goldmark, groupcache), with the tool
installed as a user installs it, gave no spurious verdict on five of
them. logrus's came from library calls not modelled yet (`sync.Pool` in a
field, `fmt.Sprintln`), groupcache's from protobuf's text rendering
(#215). With those modelled, none of the seven gives a spurious verdict.
A [sixth run](../docs/eval/sixth-run.md) (dateparse, govaluate, regexp2,
gomarkdown) found govaluate's open crash on a trailing backslash with no
spec, at tier 1; the others showed three problems in the tool (#232, #233,
#234), two of them fixed since.
A [seventh run](../docs/eval/seventh-run.md) on five more parsers found
a goroutine leak on every parse error in magiconair/properties, with no
spec; it is not in the project's tracker.
An [eighth run](../docs/eval/eighth-run.md) on five string and CSS
libraries found no new bug, and two replay problems, fixed.
An [audit of the standard library](../docs/eval/std-audit.md) measures the
compile stage on all of its 165 public packages: 71.4% of its functions
translate completely, 3.4% are held back by Go the model does not cover
yet (a short list: complex numbers, `clear`, package variables it does not
build), and 7.9% by the runtime, `unsafe`, assembly and the OS.

Evidence beyond the codebase it was built on comes from blind runs with a
protocol committed before each run and the tool frozen:
[Masterminds/semver](../docs/eval/semver.md) (templates alone found no bug;
the agent's blind specs found one, half new, and Go's own fuzzer on the
same specs found one more; most counterexamples were spurious from one
model gap, fixed since and reported separately) and
[gobwas/glob](../docs/eval/glob.md) (6 new bugs and 1 known, all from the
agent's specs or its reading of the source; the most serious one, a wrong
match, was found by reading and reached by no search; templates again
found none) and [grpc-go](../docs/eval/grpc.md) (templates over 209 packages
confirmed 153 panics, none a bug; the agent's specs on six packages led
to one: `MaxConnectionAge` below 10 ns crashes the server; the run found
seven problems in the tool, fixed since)
and [concurrent code](../docs/eval/conc.md) (eapache/go-resiliency
and patrickmn/go-cache: every concurrency result confirmed was real, the
one bug, a data race in a package's own test, new, and found as well by
`go test -race`; the run found two frontend bugs, fixed since). `go2lean prove`
on real code ([three string libraries](../docs/eval/prove-blind.md)) proved
none of 12 functions' `no_panic` contracts. One function really panics: a
divide by zero in `smetrics.Ukkonen`, confirmed in Go. For the other 11, the
contract is false only in the model, through its own over-approximations:
untyped havoc values, harmless recorded assumptions, a size bound. These
are fixed next. The 85 tingly-box findings
were triaged by hand ([go2lean-tingly-box](https://github.com/fooexec/go2lean-tingly-box)).

## Ground truth

`go2lean run -deps -workspace out-reg ./examples/...` runs every row
(`-deps` models the generic helpers of package `slices` the examples call).
`scripts/regress.sh out-reg` does the same and compares every property's
outcome (confirmed, certified, none) with
[`testdata/regress/examples.golden`](../testdata/regress/examples.golden);
`scripts/json-diff.sh out-jd` checks the `encoding/json` model against Go,
`PKG=testdata/rediff scripts/json-diff.sh out-rd` the `regexp` model, and
`PKG=testdata/convdiff` the `strconv` model. All run in CI on every change
to code, `regress.sh` there with `PROVE=0`: without the Lean proofs, which
take most of its time, a property Lean proves is expected at its
exhaustive result. The full run, with the proofs, the proof judge's
red-team corpus and the mutants, is the proofs workflow
(`.github/workflows/proofs.yml`), started by hand.
`scripts/recompile-diff.sh out-rc` (run by hand) checks the model's own
regexp compiler, for patterns built at run time, against Go: which
patterns Go rejects, and the submatch indices of the rest on a set of
strings ([`testdata/recompile`](../testdata/recompile/gen.go)).

| package | planted bug | result |
|---|---|---|
| `examples/overflow` | `Sum` wraps on large inputs | `SpecSum` violated; confirmed with `ret = MinInt64` |
| `examples/oob` | `Last` has an off-by-one | confirmed `index out of range`; `Clamp` certified |
| `examples/divzero` | `Avg` divides by a caller-controlled count | confirmed `integer divide by zero`; `Ratio` (incl. `MinInt/-1`) certified |
| `examples/magic` | `Decode` indexes an empty payload only when `tag == 0x5EC4E7` | confirmed — reachable because literals are mined into the value pool |
| `examples/checked` | `Double` returns `(int, error)` and misses overflow for negative inputs | `SpecDoubleHalves` violated; confirmed |
| `examples/alias` | `Deposit(*Account, int)` overshoots its limit; `Withdraw` accepts negative amounts; `Fill([]int, int)` misses the last element | all confirmed; specs observe the mutated struct and slice. Nil receivers and pointer arguments are tried only with `-nil-inputs` |
| `examples/strs` | `Scheme` slices with an unchecked `strings.Index`; `ParsePort` has no upper bound | both confirmed (`"port:2147483648"`); `Short` slices a `fmt.Sprintf("%s-%d", ...)` label to 8 bytes: confirmed (`slice bounds out of range [:8] with length 6`), and a spec on `Label` (the formatted string) certified |
| `examples/list` | `Sum(*Node)` skips the head cell of a linked list | confirmed with a materialised `&Node{...}` |
| `examples/recs` | `First`/`Longest`/`Dedup` index slices of structs and strings without a length check | all confirmed with `[]Item{...}` literals; `Total`, `Names` (built with `append`) and `Push` satisfy their specs |
| `examples/opaque` | `(*Cache).Share` divides by a struct field next to a map; `Lookup(ctx, map, keys)` indexes `keys[0]` | both confirmed: the map and the context are opaque to the model and the test passes `context.Background()` and a nil map |
| `examples/runes` | `(*Counter).Bump` indexes a fixed array with `n%4` under a mutex and a deferred unlock; `Head` returns a 3-index slice with `max < len` | both confirmed (`index out of range [-2]`); `Reverse` (`[]rune` round trip) and `Initials` (`range` over a string) are certified |
| `examples/builder` | `Join` builds a string with `strings.Builder` and indexes an empty result at -1 | confirmed; `Parse`'s `fmt.Errorf` paths and `Tag`'s builder growth are certified |
| `examples/xpkg` | `Share` guards negative totals, then calls `money.Pct` in another package, which divides by a zero total | confirmed through the linked model of both packages; `Bounded` (via `money.Clamp`) certified; `Units` divides by an unexported field of `money.Amount` plus one, which no test outside `money` can set: certified (the field is its zero), while `Amount.PerScale~NewAmount` inside `money` is confirmed (`NewAmount` accepts a scale of -1); `Post` takes a `*money.Ledger`, a handle `money.NewLedger` builds: certified (a ledger assembled field by field, with a nil map, is not an input) |
| `examples/iface` | `Mixed` puts a string into a `[]any` queue whose `Sum` asserts every item to `int`; `Unwrap` asserts a nil `any` | all confirmed as `interface conversion` (`Sum` also on a queue holding nil items); `Count` (comma-ok assertions), a spec on `Push` and a spec on `Pipe.Keep` with a nil filter hook certified; `Ratio` divides by the area of a `Shape` built as a square of side `n` (a method call on an interface): confirmed; specs on `Area` (value and pointer receivers through an interface), `Name` (an assertion to an interface and a call through it, like `param.IsOmitted`) and `Set` (`reflect.ValueOf(v).IsZero()`) certified; `Restart` calls `Reset` on a nil interface no type implements: confirmed as a nil dereference (#174) |
| `examples/maps` | `Pick` indexes `names[pos[want]-1]` with the position of a missing or first name; `Counter.Share` divides by a limit `NewCounter` accepts as 0 | both confirmed (`index out of range [-1]`; `integer divide by zero` through `Counter.Share~NewCounter`, the receiver built by the constructor); `Registry.Put` is targeted as `Registry.Put~NewRegistry` and certified (a registry no constructor made has a nil map: a misuse, not a target); `Distinct` (`len` of a built map) and `Sum` (`delete`, `range`) certified |
| `examples/globals` | `At` indexes a package-level slice nothing fills; `Slot` indexes four slots with a generator the package initializer sets to one that yields 7 (only a `...ForTest` helper replaces it) | both confirmed (`index out of range`): a package-level variable nothing writes loads as its zero value, one only the initializer sets to a constant or function loads as that; `Blank` (`==` on `[4]byte` against such a variable) certified |
| `examples/closures` | `Last` returns the last element a closure appended, and indexes `-1` when it never ran | confirmed (`index out of range [-1]`); `HasNeg` (a function passed as a value), `HasAbove` (a closure through `slices.ContainsFunc`) and `Total` (a closure updating a captured variable) certified; `anyOf` with a nil func confirmed as `nil_opaque_input` |
| `examples/stream` | a converter that assembles content-block events into one message drops the open block's text when a second `Start` arrives; properties of the event sequence, written as `go2lean:sequence` specs | `SeqComplete` (every byte received is in the message at the end of the stream) confirmed on a 7-event sequence found by random search; `SeqBounded` (an invariant after every event) holds on all 538,084 sequences of up to 3 events |
| `examples/replaydep` | `WriteWhileClosing` (after alitto/pond's dispatcher, evaluation 7's k024): a write that saw the dispatcher open sends on its channel after a concurrent close; the write's queue is guarded by a mutex in the dependency `replaydep/queue`, and the close sets an atomic flag before it closes | confirmed by replaying the model's schedule (Go's scheduler rarely takes the interleaving): the dependency's lock passes ungated and the closing goroutine is held until the model starts it (#56) |
| `examples/conc` | goroutines, channels and locks: `Notify` sends on an unbuffered channel nobody reads when `n > 10`; `First` returns before reading a second result; `AddMore` locks a held mutex; `Drain` closes a channel twice; `ReadWhileWriting` takes a read lock while a writer waits (writer preference); `Bump` locks a package-level mutex twice; `WithDeadline` and `Timeout` leave a sender blocked when the deadline or the `time.After` timer wins; `LostWakeup` waits on a `sync.Cond` once, after the `Signal` was sent; `RacyCount`, `CountHits` (a package variable), `FillAndPeek` (a map read without the lock), `SnapshotRacy` (an atomic add against a plain copy) and `AddInside` (`WaitGroup.Add` inside the goroutine it counts) race, and `PeekEarly` reads a result before waiting for it | `Notify`, `AddMore`, `Bump`, `LostWakeup` (deadlock), `First`, `WithDeadline`, `Timeout` (goroutine leak) and `Drain` (`close of closed channel`) confirmed by `go test`; the six races confirmed by `go test -race`; `ReadWhileWriting` found by the model and confirmed, by Go's scheduler or, when it does not take the interleaving, by replaying the model's schedule; `Pipe`, `Sum`, `TrySend` (`select` with `default`), `Once`, `SumGen` (`range` over a channel `gen` returns), `CountAtomic`, `BumpAll`, `Cancelled` (a worker that watches `ctx.Done()`), `TimeoutBuffered`, `Poll` (a ticker), `Handoff` (`Cond.Wait` in a loop), `LockedCount`, `AtomicCount`, `Publish` and `FillAndRead` (race-free), `SnapshotAtomic`, `AddBefore`, `LazyClose` (a channel made on first use, closed after a nil check) and the rest hold on every input, most under every schedule (C3) |
| `examples/portable` | constructs that gave spurious or missed results on one-api and spf13/cast: `URL` slices the `BaseURL` of a `*meta.Meta` (a struct of exported fields from another package, which has a function returning one); `Pick` indexes with `rand.Intn(len(xs))`; `Parse` and `parse` (ids differ only in case) divide by `strconv.Atoi`'s result; `Port` calls the generic `Must`; `Unit` reads group 2 of a one-group regexp; `Minor` divides by group 2 of a version, read through a helper that takes the regexp; `Head` divides by the first element after checking a sentinel error; `Per` divides by a `uint8` case of a type switch on `any`; `Label` reads a field of a handle (`*meta.Session`), then divides by a name's length; `Tokens` divides through a pointer only `InitEncoder` sets; `ring.push` would panic on a ring with an empty buffer, which only a receiver built field by field has; `Shrink` and `Budget` use `min`/`max`; `Drain` divides by an `atomic.Int64`; `Kind.String` and `Label2` read a stringer's index table | `URL`, `Pick` (Go's own `invalid argument to Intn`), `Parse`, `parse`, `Port` (via `Must[int]`), `Head`, `Per` (with `uint8(0)`), `Unit`, `Minor` (`"v0.0"`, a string the search drew from the pattern), `Label`, `Shrink`, `Drain`, `Label2` and `Tokens` confirmed (`Tokens` with the note `reads_unknown_state`: the program may call `InitEncoder` first, as one-api's `CountToken*` rely on `InitTokenEncoders`); `Count` (`errors.Is` on the sentinel `first` returns covers the empty slice), `Digit` (`rand.Intn` stays below its bound), `IntPart` (a one-group match has two elements), `PerMB` (`var mb = kb * 1024` folded), `PerMax` (`Atoi` of an out-of-range number is `MaxInt64`, not 0) and `Month` (both ends of a regexp group checked) certified (`Fill`, through `newRing`, `Budget` and `Kind.String` too); `ring.push` is no target (its unexported type is built by `newRing` only); the helper `group` as a target of its own is inconclusive (a `*regexp.Regexp` input cannot be generated) |

### Differential gate completeness

`scripts/json-diff.sh <workspace> <seeds>` fails on compile, property,
search or replay command errors. Its comparator requires at least one
differential property, confirmed model-versus-Go comparisons for every
property and each seed in `1..seeds`, and zero different, inconclusive or
missing verdicts. Zero samples and duplicate artifacts do not pass.
`scripts/test_json_diff_compare.py` exercises these failure paths in CI.
A passing run remains evidence only for those modeled probes and inputs;
it is not a correctness proof or a real-project release evaluation.

The strengthened gate exposed a missing case in the number round-trip suite:
`numdiff.Any` previously produced only stuck trials when re-encoding decoded
interface numbers, while the old script reported success using `Field` alone.
The encoder now handles the model's canonical `knownFloat` representation
inside interfaces. The strengthened six-seed run completed all 12 real
comparisons (both properties), with zero different or other results.
Existing partial results are not full-suite evidence.

### Sandbox integration gate

`GO2LEAN_REQUIRE_SANDBOX_TESTS=1 go test ./internal/sandbox ./internal/poc -count=1 -v`
requires working bubblewrap and prlimit, with no replay or Lean isolation
opt-out. CI runs this separately so a machine without user namespaces
cannot silently pass by skipping the isolation tests. The tests check replay
copy/write isolation, hidden home directories, private Lean temporary
storage, absence of inherited credential variables in both execution paths,
and replay wall-clock termination. This is evidence for those boundaries,
not a security audit or a full release gate. Local unit tests without this
flag may still skip unavailable integration cases.

### Generated-source artifact completeness

The experimental ordering mode has a separate mandatory integration gate:
`GO2LEAN_REQUIRE_BUILTIN_TESTS=1 GO2LEAN_REQUIRE_SANDBOX_TESTS=1 go test
./internal/pipeline -run TestBuiltinSearchReplayFromPersistedSnapshot -count=1 -v`.
It uses actual Lean search to generate a witness for a deliberately invalid
unit comparator and independently replays it in sandboxed Go from the persisted
snapshot, without passing the original overlay. No witness is supplied by the
test. This proves the unit path works, **not** that an external project's
contract was inferred or a release gate was met. Snapshot tests separately
cover changed source/embed data/configuration, model/property tampering, moved
identical clones, immutable compilation and stale cached results.

The mandatory isolation gate also runs `./internal/poc`. Its trusted packaging
fixture first builds/runs a generated oracle through the normal replay path,
copies the delivered bundle to a different directory, removes the original
target and workspace, and discards the in-memory overlay. It then builds the
bundle's standalone preparer, prepares an overlay for a fresh source clone,
builds the observation test with Go, and runs it under real isolation. The
test requires the actual oracle/result observation, not just exit zero.
A harness-only negative control must fail compilation without the helpers.
The fixture's oracle **holds**: these are spurious candidates, not invented
confirmed bugs or external-project release evidence.

Other tests cover all package-local helpers, exclusion of unrelated package
overlays, rewrite precedence, `-race` flag preservation, invalidation of an old
manifest after a failed update, hash mismatches, omitted/missing files,
unsupported manifest versions/fields, duplicate destinations, unsafe relative
paths, symlinks and refusal to overwrite output. Portable preparation ignores
the legacy absolute-path schedule overlay.

This verifies the package-local Go-source delivery mechanism described in
[the pipeline](pipeline.md#generated-support-sources), not arbitrary dependency
overlays, a hostile-filesystem race boundary, model correctness, resumed
command bindings, or the still-missing real external non-panic release case.

### Offline dependency diagnostics

`go2lean doctor -deps ./...` inspects the target's package and test import
graph, without compiling or executing it. Unit/integration tests cover missing
test dependencies, command and JSON failures, partially empty package
selections, host download/build-setting overrides, bounded output, and local
workspace `use`/`replace` resolution without changing module or workspace
files. Real Go metadata probes use an `init` marker and a cgo `pkg-config`
marker to check that neither target execution nor target build tooling runs.
These fixtures test the diagnostic, not the release requirement to find a
real project's non-panic functional defect.

The installed packaging gate (`scripts/install-smoke.sh <external-module>
<new-results-directory> [--require-external-deps] [--compare-order Function]`) installs the binary with a
fresh Go module/build cache and an empty HOME. It then provisions target dependencies into a separate
fresh module cache, with target files read-only, before running compilation
with the checkout hidden and networking disabled. The diagnostics (`doctor`)
run on the host instead, offline through Go's settings: they check that the
host can give go2lean its own sandboxes, which a host that forbids nested user
namespaces (GitHub's runners) would refuse inside the hiding sandbox. All stages use
an allowlisted environment, ignoring host Go configuration, workspace selection,
credentials and sandbox opt-outs. Installation and dependency preparation may
use the network; their logs are separate from offline doctor/compile logs.
`scripts/test_install_smoke.py` checks environment allowlisting, separate
initially empty caches, online/offline phase selection, and rejection of
reused result directories using mock tools. Those tests check orchestration,
not real namespace isolation or successful dependency downloads.

With `--require-external-deps`, tool/isolation prerequisites must first pass.
Offline dependency diagnostics against the initially empty target cache must
then fail with a dependency diagnostic. A successful diagnostic or an unrelated
failure is not accepted as missing-dependency evidence. Only then does the gate
provision dependencies and require offline diagnostics and compilation to pass.
Mock tests cover both rejected cases as well as the expected failure/success
transition. A missing module can stop `go list` before it emits package JSON;
the gate accepts that path only with the explicit offline module-lookup
diagnostic, not an arbitrary command failure.

CI uses the pinned external uuid and stdr modules; stdr exercises the required
missing-dependency transition. Go, elan, the pinned Lean toolchain,
system tools and their PATH remain preinstalled prerequisites; other host paths
remain readable. This tests clean Go dependency/cache setup and standalone
packaging, **not** installation of OS/Lean prerequisites on a clean machine,
every target build configuration, or the full release gate. By default it
does not search/replay. The optional `--compare-order Function` explicitly
asserts the experimental comparator role and additionally runs a fresh
seed-42 ordering search/replay and resume, offline with the checkout hidden.
Inspect the saved hunt and verdict records: command success alone does not
mean a defect was found or the comparator passed. The
[natural-order evaluation](evaluations/ordering-natural-20261028.md) records
a real installed non-panic positive with this option; it is not the full
acceptance matrix.

Local checkpoint (2026-10-27): the gate passed on uuid revision
`2d3c2a9cc518326daf99a383f07c4d3c44317e4d`, with deliberately invalid host
`GOFLAGS`, `GOENV` and `GOWORK`, host sandbox opt-outs, and a credential
sentinel. Doctor reported 143 locally available packages and working replay,
Lean and comparator isolation. The external checkout remained unchanged.
The fresh **tool installation** downloaded go2lean's dependencies; this uuid
revision has no module dependencies to download, so it does not exercise
provisioning a third-party target dependency. Compilation still reports
unsupported model operations; successful packaging is not full model support.

The same checkpoint passed on stdr v1.2.2, revision
`521af2addfa7c81c8a65d0e85ed34bb6bb6dc262`, with the same hostile host settings
and `--require-external-deps`. Tool/isolation prerequisites passed; offline
doctor first rejected the empty cache because `github.com/go-logr/logr@v1.2.2`
was unavailable. After network-enabled preparation populated the separate
target cache, offline doctor reported 131 available packages and compilation
passed with the checkout hidden. The external checkout remained unchanged.
The compile report includes unmodeled calls/state, so this is dependency
provisioning and packaging evidence only, not functional validation of stdr.
CI retains both packaging cases' logs on success as well as failure.
