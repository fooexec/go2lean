# Evaluation 7: recent concurrency bugs, go2lean against Go's own tools

Issue #37 asks what the Lean model adds over simpler ways of finding
defects. For functional defects, Go's fuzzer did as well or better on the
same specs (docs/eval/search-vs-fuzz.md). Fuzzing does not vary schedules,
so concurrency is where a model that enumerates schedules could add
something. GoKer (docs/concurrency.md) is small and from 2021; this run
uses concurrency bugs fixed since 2023 in projects people use now. This
page is the protocol, committed before any case is run; results are added
afterwards and the protocol is not edited once the run starts.

## Cases

- **Projects:** 22 actively maintained Go projects that use concurrency
  heavily, history since 2023-01-01 (`conc-recent/mine.py`):
  - grpc-go, etcd, nats-server, nats.go, pgx, go-sql-driver/mysql;
  - kafka-go, sarama, amqp091-go, quic-go, ants, pond;
  - hashicorp/raft, yamux, memberlist, badger, pebble;
  - go-redis, containerd, caddy, prometheus, sourcegraph/conc.
- **Pool.** Commits whose subject names a race, a deadlock, a leak, a hang
  or a synchronisation primitive and reads like a fix, that change 1 to 5
  non-test Go files and a test: 264 candidates. At most 5 per project are
  taken, in a seeded order (`conc-recent/draw.py`, seed 20261028), giving
  70 (`conc-recent/pool.jsonl`).
- **Screening.** Read in that order, until 25 are accepted. A case is
  accepted when:
  - the fix removes a concurrency defect in non-test code: a data race, a
    deadlock, a goroutine leak, a lost wake-up or hang, a misuse panic
    (closing a closed channel, a negative `WaitGroup`, unlocking an
    unlocked mutex), or an atomicity violation with a wrong result;
  - one test function of the package exercises it (the fix's own test, or
    an existing one) and runs without external services (databases,
    brokers, containers). In-process loopback networking is allowed and
    recorded.
- **Ground truth for each accepted case:**
  - **Observed:** at the parent commit the test fails because of the defect
    at least once in 200 runs or 10 minutes, under `-race` and with goleak
    for a leak, and at the fix it does not fail in the same budget.
  - **Forced:** when the parent's test never fails in that budget, the
    screener writes a private reproduction that forces the interleaving
    (channels, sleeps or hooks in a test). The case is accepted as forced.
    These are where a schedule search could find what repetition does not.

  Every rejection is recorded with its reason.

Neither arm writes specs, so no blinding is needed: the target is the test
function, and the property is that it does not deadlock, leak, race or
panic.

## Arms

All on the same 4-core machine, one case at a time, arms one after the
other.

- **A, go2lean.** `go2lean run -tests -run '^<Test>$' -budget 300` on the
  test's package, at the parent commit, with the go2lean of the commit that
  adds this page. Found: a confirmed verdict whose kind (deadlock, leak,
  race, panic) is the defect's.
- **B, Go's tools.** The test binary built with `-race`, the test function
  starting with `defer goleak.VerifyNone(t)` (as GoBench instruments it),
  run once at a time with `-test.timeout 30s`, until it fails or 10 minutes
  have passed. Found: a failure of the defect's kind: a race report, a
  leak, a hang, Go's deadlock detection, or the panic.

B gets more time than A's budget: it is the strongest baseline a project
has without new tools.

## Scoring

A finding counts when it does not occur at the fix commit:
- for A, the counterexample is replayed there (`go2lean poc`);
- for B, the test is run there again for the same budget.

Per case and arm: found or not, and the time to the first finding.
Reported:
- found by A only, by B only, by both, by neither;
- the same split for observed and forced cases;
- for A's misses, the model gap (`go2lean-spec/scripts/gaps.go`);
- for B's misses, how rare the schedule is (the forced cases).

The claim to test: the model's search over schedules finds defects that
repeated runs under the race detector and goleak do not find in 10
minutes. If A finds nothing that B misses, that is the result, and the
README will say so.

## Cost

The screening agent: one run, about 0.4 to 0.6 M tokens, and CPU time to
rerun tests. Neither arm calls an LLM.

## Amendment before the run (2026-10-29)

Screening accepted 25 cases (22 observed, 3 forced). In 22 of them the
fix's own test fails at the parent in 90% or more of its runs under
`-race`: maintainers write a regression test to reproduce the bug
reliably. Run against that test, arm B would find nearly every case on its
first run, and the evaluation would measure the test, not the tools. The
arms are changed before either has run, to ask what matters in use: can a
tool find the defect before anyone wrote a test for it?

- Both arms run at the parent commit **without** the fix's test files and
  without the screener's tests (`custom/`).
- **A, go2lean:** `go2lean run -tests -budget 60` on the package: its
  existing tests and the `no_panic` templates of its exported functions
  and methods, with a 20-minute limit per case.
- **B, Go's tools:** the package's existing tests, `go test -race
  -count=1 -timeout 5m`, again and again until a failure or 10 minutes.
- **Found:** a finding (a race report, a deadlock, a leak, a hang or a
  panic) whose stacks name a function the fix changes, and which does not
  occur at the fix commit. Each match is checked by reading the report.
- The fix's test (or the screener's reproduction) stays the ground truth:
  it shows the defect is real.

The cases with in-process loopback networking (6) are reported apart.

## Second amendment before the run (2026-10-29)

The first amended design was started on 7 cases (k001 to k014) and
stopped. Neither arm found a defect, for reasons that would hold for the
other cases too:
- `go2lean run -tests` did not finish on these packages: it hit the
  20-minute limit in most cases and ran out of memory in one (#45). Its
  `no_panic` templates call one function at a time, so they never run two
  methods of one object concurrently.
- The packages' existing tests had passed the projects' CI with the
  defect present; repeating them under `-race` failed only on tests
  unrelated to the fix (an environment-dependent proxy test, a flaky
  timing test).

So that run measured whether a test exercising the defect exists, not
what either search adds. The design now follows evaluation 5: both arms
search the same program, and only the search differs.

- **Harness.** For each case, one `go2lean:sequence` function in a test
  file of the package at the parent commit (`conc-recent/harness/`): it
  builds one object the way a user would, runs two of its methods
  concurrently (three where the defect needs three parties), takes small
  ints, strings or bytes as inputs, and returns whether the result is one
  some sequential order of the calls could give. It has no sleeps, yields
  or hooks, and nothing of the fix. Harnesses are written by subagents
  following `conc-recent/harness/BRIEF.md`, then reviewed.
- **Not blind.** The writer reads the fix to choose which methods to run
  together, so the harness points at the defect's area. Both arms get the
  same harness; what is measured is the search over inputs and
  schedules, not finding the area.
- **A, go2lean:** `go2lean run -deps -specs-only -budget 300` on the
  harness's package, with the frozen tool. Found: a confirmed
  counterexample (a race, a deadlock, a panic or a false result). A
  counterexample the replay did not reproduce (spurious) is not a find; it
  is counted apart, with its kind, since the defect may still be real.
- **B, Go's fuzzer:** `go2lean fuzz -fuzztime 10m` on the property A
  recorded (the same harness), with `GOFLAGS=-race`. Found: a failing
  input. Go's fuzzer does not check for leaked goroutines, so B finds a
  leak only if it makes the run fail.
- **Scoring** (`conc-recent/score8.sh`). Each arm's failing input is
  tried at the fix commit with the same harness: A's counterexample is
  replayed with `go2lean poc` (repeated `-race` runs and the model's
  schedule), B's failing input is run 20 times under `-race`. It counts
  when it does not fail there and the parent's report is of the defect's
  kind, checked by reading it. The runner is `conc-recent/run8.sh`; the 25
  harnesses, each with its sanity results in `meta.json`, are in
  `conc-recent/harness/` (as `.go.txt`).
- **Sanity runs.** Each harness was run 200 times at the parent and at the
  fix while it was written. A harness that fails at the fix is reworked or
  dropped. A harness that already fails at the parent in a plain loop shows
  the defect needs no search; such cases are reported apart, since either
  arm will find them.
- **Pilot.** The runner was tried on k024 and k030 with a 30-second budget
  and 20 seconds of fuzzing, to check that both arms run; it showed that
  `-deps` and the import path were needed. Both cases are rerun with the
  full settings.
- Cases with loopback networking or real time are reported apart, as
  before.

## Results

Run on 2026-10-29 with the frozen tool (`90dc83c`), A and B one after the
other per case, on a 4-core machine. Per case: `conc-recent/results.tsv`;
what each arm reported, with A's havoc sites and assumptions:
`conc-recent/ledger.txt`. The runner (`run8.sh`), the rerun with its
adjustments (`run8b.sh`), the scorer (`score8.sh`) and the ledger script
(`ledger7.py`) are beside them.

| | cases |
|---|---|
| found by A (go2lean) | 6: four races (k001, k017, k030, k031), two deadlocks (k015, k047) |
| found by B (Go's fuzzer, `-race`) | 22 |
| both | 6 |
| A only | 0 |
| B only | 16 |
| neither | 3: k009 (a leak, which the fuzzer does not check), k023 (A out of memory, so B had no property), k033 (a rare schedule) |

- **By kind.** A found 4 of 9 races and 2 of 7 deadlocks or hangs, none of
  the 6 wrong results and neither panic. B missed only the leak, the
  out-of-memory case and k033.
- **Rare schedules.** Of the five harnesses that a plain loop of 200 runs
  did not fail at the parent (k007, k033, k038, k045, k047), B found four
  and A one (k047). The fuzzer's repeated runs under the race detector
  reached schedules the hypothesis expected only a schedule search to reach.
- **Networking (6) and forced cases (3).** A found one networking case
  (k031) and no forced case; B found four and two.

### Why A missed

Every miss traces to the model or the tool, not to the search over
schedules. Each class is an issue:

| class | cases | issue |
|---|---|---|
| a spurious counterexample built on havoc from code the model does not run: sockets, `reflect`, `os/exec` | k007, k009, k033, k043, k045, k050 | #70 |
| `sync/atomic`'s typed values are havoc | k015 (also), k039 | #67 |
| pure-Go standard library not translated: `container/list`, `bufio`, `errors.Is`/`As` | k010, k014, k028, k039, k050 | #69 |
| a race the model reports that `-race` does not, even on the model's schedule | k012, k014 | #68 |
| out of fuel, stuck callees, a domain too large for the exhaustive engine | k010, k016, k024, k038, k042, k044 | #71, #56 |
| stores to a package variable ignored | k004 | #55 (partly fixed in #57) |
| time | k028, k050 | #59 |
| out of memory on nats-server | k023 | #45 |

Two tool bugs surfaced too:
- the package's own fuzz targets stopped k044 from loading (#64, fixed);
- the replay reported a race inside the harness's deadline helper as the
  program's (#66). In k031 that made the score at the fix wrong; at the
  parent, A's confirmed race was the defect's.

### Deviations

- k018, k042, k043 and k044 were rerun (`run8b.sh`):
  - k018 lost its fuzzing build to a cache cleanup of ours;
  - k042 ran out of disk;
  - k043 needed the harness's `go get` of nats-server v2.15.2, which wants
    Go 1.26 (`GOTOOLCHAIN=auto`);
  - k044 needed `-fuzz-specs=false` in A (#64).

  Their first-pass logs are kept.
- **Scoring at the fix, read by hand.** Three of A's results needed the
  reports read to score them:
  - k031: the race at the fix was the harness's own, so it counts as
    found;
  - k015 and k047: no deadlock at the fix in 200 runs and an inconclusive
    replay, so they count as found;
  - k018: blocked past the replay's 2-second watchdog at the fix too,
    because the fix drops a blocked confirmation only after 5 seconds, so
    it does not count. B's check at the fix allows 10 minutes, so this
    case is scored unevenly.

### What this says about the question

On recent concurrency bugs, with the same harness for both, Go's fuzzer
under the race detector found almost everything (22 of 25), including
most of the rare schedules. go2lean found 6, all also found by the fuzzer.
The hypothesis, that a search over schedules finds what repeated runs do
not, is not supported at go2lean's current state: its schedule search
never got the chance, because the model of the code around each bug
(sockets, atomics, untranslated standard library, fuel) failed first. The
next work is that model (the roadmap, #53). This evaluation is rerun with
the same harnesses after each of those fixes.

## Rerun after review 2 (2026-10-29)

The same 25 harnesses, arm A only, with the development build after the
fixes of review 2 (`ef8d59f`: #75 to #80). B is not rerun (the fuzzer did
not change; its scores stand). The scoring is the first run's, applied by
`score9.sh`: a counterexample confirmed at the parent counts as found when
it does not fail at the fix, an inconclusive replay at the fix included
(k015 and k047 in the first run). `run9.sh` ran A; the container
restarted twice during the run, and `resume9.sh` resumed it at the cases
without a result. Per case: [results-rerun.tsv](conc-recent/results-rerun.tsv).

| | first run | rerun |
|---|---|---|
| found by A | 6 | 10 |
| A only (B missed) | 0 | 0 |

- **Found now, missed before (6).**
  - k012: a race on `ProducerMessage.Headers` (#68; package variables set by
    a call, #75, #76).
  - k014: a race (#69: `errors.Is` on sarama's own error type).
  - k004: a race on a package-level logger (#55: stores to package
    variables as shared cells).
  - k039: a wrong result under the model's schedule (#67 atomics, #69
    `container/list`).
  - k010: a wrong result (#69, #71: `bufio` and `io` translated, the slice
    bound raised).
  - k024: send on a closed channel. Go's scheduler took the interleaving
    this time; the replay of the model's schedule diverged on this build,
    and with #81 it confirms the finding on the schedule.
- **Found before, missed now (2).**
  - k001: the model held. #75 made an array field of a pointer sentinel
    (grpc's `bdpPing`, `&ping{data: [8]byte{...}}`) havoc at every load,
    where it was silently zero before, so `calculate`'s comparison with it
    returned early. Constant array fields are now constants (#71); k001 is
    found again with that change.
  - k017: the model's race was not seen by `-race` in its 20 runs (the
    first run's runs were luckier), and the replay of the model's schedule
    diverged in a dependency (pond's `internal/dispatcher`), the case #81
    fixes.
- **Still missed.** k016 needs its setup run once rather than in every
  trial: with enough fuel (256000) one trial of its 16384-stripe setup
  takes minutes (#71). k007 and k033 are inconclusive, k009, k038, k043,
  k045 and k050 spurious, and k023, k028, k042, k044 give no
  counterexample; their issues are those of the first run's ledger.


## Refinement by hand (#85, 2026-10-30)

Arm A's spurious and inconclusive cases, rerun after refining the sites
their counterexamples named: the reads `relevant_havoc` (#84) found
relevant, or else the assumptions the run took. Same harnesses and
budget as the rerun (`run11.sh`). Round 1 bounds `Read`/`Write` counts and
`hash.Hash` writes by their contracts and makes a `[]byte` constant global
known. Round 2 adds the bounds on `Len`/`Cap` and `runtime.Callers`, and
`rand.Perm`. k043 (a NATS server started in setup, #87) and k050
(protobuf's `unsafe` internals, #70) were not refined.

| case | rerun | site refined | round 1 | round 2 |
|---|---|---|---|---|
| k045 | spurious: `bufio` "negative read" | `Read` count | **holds** (2000 trials, 1 stuck) | — |
| k009 | spurious | `hash.Hash` write | spurious, at `Digest.Sum64` (`reflect.Value.Len` < 0) | **holds**, but 1566 of 2000 trials stuck |
| k038 | spurious: `redact` slice bounds | `[]byte` constant global | spurious, at `runtime.Callers` | spurious, a havoc field of a pooled printer |
| k007 | inconclusive | `io.ReadAtLeast` count | inconclusive, at `rand.Perm`'s length | inconclusive, at `Sdump` (assumptions only) |
| k033 | inconclusive | — | inconclusive, at `GetOrRegister` | inconclusive, at `Sdump` |

Every refinement removed the violation it targeted, and the next
counterexample was further along the run, naming the next site: the
CEGAR loop's progress, done by hand. Most sites were havoc outside a
contract the callee documents. None of the cases turned confirmed: their
bugs need the schedule and setup the model does not yet reach (#87).

## k016 found (#87, 2026-10-30)

k016 (prometheus' agent DB: two concurrent appends of one label set make
two series) was missed by every run above. Three changes found it:
- **Setup snapshots** (#90). Its harness builds three 16384-stripe arrays
  before the goroutines start, 28 s a trial; that setup now runs once.
- **Zero-copy views** (#91). In the model every `Append` had failed:
  prometheus' labels encode a label set with
  `unsafe.String(unsafe.SliceData(b), len(b))`, which the model took as
  an empty string. It is now `string(b)`.
- **Equal arguments** (#91). One trial in four, two parameters of one kind
  get the same value, so two appends share a label set.

**Result.** The fourth trial (`a = b`) violates the spec. Replayed at the
parent under the model's schedule, it is confirmed (`ret: false`). At the
fix the same input gives `ret: true`; the verdict there is inconclusive,
because the fix rewrote `getOrCreate` and the schedule no longer applies.

**Cost.** About 100 s a trial, mostly the harness's final loop over 16384
stripe read locks.

## Rerun at main (2026-10-30)

The same 25 harnesses and budget, arm A only, with main at `c8d1176`
(contract havoc #89, setup snapshots #90, zero-copy views and equal
arguments #91, the relevance time guard #92). Scored as the rerun above.
`run14.sh` ran A; six cases first failed on a full disk and were rerun by
`redo14.sh`, which trims Go's build cache while it runs. Per case: the
"rerun 2" columns of [results-rerun.tsv](conc-recent/results-rerun.tsv).

| | first run | rerun | rerun 2 |
|---|---|---|---|
| found by A | 6 | 10 | 11 |
| A only (B missed) | 0 | 0 | 0 |
| spurious counterexamples | 10 | 5 | 2 |

- **Found again (1).** k001: constant array fields of pointer sentinels
  (#80), as predicted above. No case found by the rerun was lost.
- **Spurious to none (2).** k045 holds over 2000 trials (contract havoc on
  `Read` counts, #89). k009 gives no counterexample, but 1566 of its 2000
  trials are stuck at a lock in `Digest.Sum64` (#71).
- **Spurious to inconclusive (1).** k050: protobuf's `unsafe` internals
  (#70); the counterexample now rests on assumptions only.
- **k016 still missed at this budget.** All 2000 trials run out of the
  default fuel. It is found with 256000 fuel and about 100 s a trial (the
  section above), far over the 300 s budget.
- **k023 regressed in cost.** The Lean build of nats-server's `server`
  package was killed after 12 minutes at 13 GB (the container has 15 GB);
  the rerun's build fit. It gave no counterexample either time (#45).

Time: 2.1 h for the 25 cases, against 1.9 h for the rerun.

## Hang detection on the fuel cases (#86, 2026-10-31)

Arm A's out-of-fuel runs, rerun with the state watched for a cycle (#96,
#98), concurrent runs included (#100): main at `f62721d`, the same
harnesses and budget, by `run15all.sh`.

- **Fixed earlier.** k010, k038 and k044, the fuel cases of the first run,
  no longer run out of fuel: k010 and k038 give a counterexample, k044
  none, as in the rerun at main above.
- **Still out of fuel, no cycle.** k009 (434 of 2000 trials), k016 (2000)
  and k042 (2000). None of 20 out-of-fuel runs per case comes back to a
  state. k016 needs more fuel (above); k009 is stuck at otel's registry
  (#71). k042, prometheus' out-of-order compaction deadlock, runs out
  of fuel inside one goroutine step in all its runs. That step is the
  harness's setup (`NewHead`, `h.Init`, from `@SeqConcOOOCompactionHead#8`),
  whose sequential work before the first scheduling point exceeds the fuel.
  It is a setup cost like k016's, not a hang.

So hang detection finds nothing in this evaluation. Its findings are
`examples/hang`'s loops and livelock, confirmed in Go.

## Rerun at main after M3 (2026-11-03)

Arm A on the same harnesses, budget and scoring, at two points:

- **rerun 3**: main at `e875114` plus #121, after the init cells, the clock and virtual time (#107 to #121). Reported in review 5 on #53.
- **rerun 4**: main at `15e8aaf`, after loopback networking (#127), raising the fuel (#132), the run order and hunt budget (#133), subtests (#134), `Sprintf` widths (#135), `reflect.DeepEqual` (#136), run-time regexps (#137), the memory limit (#138) and change-first order (#139). `run21.sh` ran it.

Per case: the "rerun 3" and "rerun 4" columns of [results-rerun.tsv](conc-recent/results-rerun.tsv). As in review 5, a schedule-forced find that the fix's new synchronisation stops (k015, k024, k039, k047) counts as found ("adjudicated"); "strict" counts only `FOUND`.

| | rerun 2 | rerun 3 | rerun 4 |
|---|---|---|---|
| found, strict | 6 | 7 | 6 |
| found, adjudicated | 11 | 11 | 10 |
| spurious counterexamples | 2 | 1 | 2 |
| time without k023 | 2.1 h | 1.6 h | 2.7 h |

- **k023 runs to the end.** Under the memory limit, nats-server's model builds and is searched in 1140 s. Before, it was killed at 13 GB (#45), or it needed more disk than the container had. The model's counterexample is spurious: a slice bound that fails on results it chose for `base32` calls it does not model.
- **k031 lost (#142).** The bisection points to #127's constant `net.ErrClosed`. With it, quic-go's `Unwrap` methods return a constant where they returned a havoc read, and the Close/Dial race is no longer reached. It is open.
- **k007 and k033: no counterexample** where rerun 3 had an inconclusive one. Under the loopback model (#127), the sarama harnesses hold over 2000 trials and the exhaustive inputs. The mock-broker round trip is still not reached, as review 5's note on #53 says.
- **Time.** The fuel raise (#132) spends the 300 s budget on harnesses whose trials all run out of fuel: k042 (1890 s) and k016 (1320 s), against about 470 s and 270 s before. Neither is found. k007 and k033 cost more for the same reason.
- **Disk.** The first pass of k023 and k014 failed on a full disk. `go2lean poc` reported k014's failed link as `target_exited` (#140, fixed here). Both were rerun.


## Rerun after the setup redraw and floats (2026-11-04)

**rerun 5**: arm A at #154's head (`e440aec`, merged as `58f2f49`). New since rerun 4:
- the setup redraw (#144);
- `recover` and `sort` (#145);
- regexp pattern variables (#147);
- floats (#149, #151, #152);
- composite-keyed tables (#154).

It used the same harnesses, budget and scoring. `run22.sh` ran it and `score22.sh` scored it. Per case: the "rerun 5" columns of [results-rerun.tsv](conc-recent/results-rerun.tsv).

| | rerun 4 | rerun 5 |
|---|---|---|
| found, strict | 6 | 7 |
| found, adjudicated | 10 | 12 |
| spurious counterexamples | 2 | 1 (k023 not run) |
| time without k023 | 2.7 h | 2.2 h |

- **k031 found again.** The redraw (#144) redraws the setup's havoc values every block of trials (#142). The Close/Dial race comes back at trial 15, and the replay confirms it at the parent with `-race`. At the fix it is not observed.
- **k010 adjudicated.** Its counterexample now replays at the parent: the real run returns false, where rerun 4's replay was inconclusive. At the fix, the model's schedule no longer replays. The fix adds new synchronisation there: `Close` sets a `closeInit` atomic, and `reconnectChannel` checks it once it holds the lock. So k010 counts as adjudicated, like k015, k024, k039 and k047.
- **k045 changed.** The search finds nothing, as before. But now no mutant of the code fails the spec, where 8 of 22 did; the model's notes on the code are the same. It ran on go1.26.6 (below), where rerun 4 ran it on go1.24.7. The cause is open.
- **k023 not run.** nats-server's workspace passed 3.6 GB, and the build cache grew at the same time, so the disk filled twice. Each time the run was stopped and the case's output removed.
- **One toolchain from k023 on.** Until then each case ran the Go that its `go.mod` asked for: a downloaded go1.25.x or go1.26.x, or the system go1.24.7 for older `go` lines. The container held eight toolchains, plus their build caches. From k023 on, `GOTOOLCHAIN=go1.26.6+auto` (`go env -w`) runs every case on go1.26.6, which every case's `go` line allows. k001 to k018 kept their earlier toolchain.
- **Toolchain comparison: k001 only.** The rerun of k001 to k018 on go1.26.6 got through k001 (the same counterexample, confirmed) before the disk filled in k004.

## Rerun of the fuel cases after in-place arrays (2026-11-05)

**rerun 6**: arm A on k009, k016, k023 and k042 at #156's head (`a0a56a2`, merged as `41b0e4b`). #156 made heap arrays of more than 256 elements an `Array` updated in place, and raised the fuel up to 256 times the first (#71). Same harnesses, flags and budget as rerun 5, on a fresh machine. `run23.sh` ran it and `score23.sh` scored it. The bench was rebuilt from `pool.jsonl`, each parent being the fix's first parent, and every case ran on go1.26.6 (k009 and k016 ran on their own toolchain in rerun 5). Per case: the "rerun 6" columns of [results-rerun.tsv](conc-recent/results-rerun.tsv); the other cases were not run.

| case | rerun 5 | rerun 6 | secs (5 → 6) |
|---|---|---|---|
| k016 | missed, all trials out of fuel | **found, adjudicated** | 1342 → 1258 |
| k042 | missed, out of fuel | no result: stopped at the run's 2400 s limit | 1709 → 2400 |
| k009 | none, trials stuck | none, 970 of 2000 out of fuel | 342 → 435 |
| k023 | not run (disk) | spurious | – → 1245 |

- **k016 found.** All 182 trials ran out of fuel at 64000. At 256000, the random search finds the violation at trial 3: two appends of one label set give two series. The replay at the parent confirms it (`ret: false`). At the fix, the real runs return true, and the model's schedule does not replay (`schedule_not_reproduced`). The fix replaces the separate `GetByHash` and `Set` with one `SetUnlessAlreadySet` under the stripe lock: new synchronisation, so k016 counts as adjudicated, like k010.
- **k042 slower.** Each fuel raise starts a new random search with the full 300 s budget, and the budget is checked between trials only. The fifth search, at 256000, took the hunt past the run's 2400 s limit, and nothing was recorded. The setup reads `os.MkdirTemp`, which the model does not know, so it is not snapshotted and every trial redoes the 16384-stripe fill.
- **k009 is not a fuel case.** The same 970 trials run out at 256000 as at 64000, inside one goroutine step from `registry.load`. The harness sets `OTEL_TRACES_EXPORTER=none`, but `os.Getenv` is havoc in the model (`docs/semantics.md`). otel's `autoexport` then falls back to `otlp`, and `registry.load` builds `otlptracehttp.New` while it holds its mutex, one long step.
- **k023 fits.** nats-server's workspace and build fitted in 19 GB free, with one toolchain. The random search finds a nil dereference at trial 4. The replay calls it spurious: it rests on the result the model chose for `os.MkdirTemp` (the harness's `StoreDir`), and the real run returns true.
- **Next.** `os.MkdirTemp` (k023, k042) and `os.Getenv`/`os.Setenv` (k009) are what block three of the four cases. Bounding the fuel raises by one budget per property would keep k042's cost in check, but k016 needs about 1250 s at the 300 s budget.

## Rerun after the environment model and the early stop (2026-11-05)

**rerun 7**: arm A on the same four cases at #158's head (`ec96503`, merged as `d90f9f4`). #158 models the environment and `os.MkdirTemp` (`osmodel`), marks callers of concurrent functions in other packages concurrent, and stops a search early when the fuel is short and can still be raised. Same harnesses, flags and budget as rerun 6 (`run23.sh` with that build, `score23.sh`). Per case: the "rerun 7" columns of [results-rerun.tsv](conc-recent/results-rerun.tsv).

| case | rerun 6 | rerun 7 | secs (6 → 7) |
|---|---|---|---|
| k016 | found, adjudicated | **found, adjudicated** | 1258 → 448 |
| k042 | no result (2400 s limit) | no result (2400 s limit) | 2400 → 2400 |
| k009 | none, out of fuel in `registry.load` | inconclusive (race not observed) | 435 → 206 |
| k023 | spurious (`os.MkdirTemp`) | spurious (a stuck `Read`) | 1245 → 783 |

- **k016 faster.** Each level up to 64000 stops after 20 trials, all out of fuel, and the search at 256000 finds the violation at trial 3, as in rerun 6. The case takes 448 s instead of 1258 s.
- **k009 past the environment.** `OTEL_TRACES_EXPORTER=none` now reads back, and the trials no longer build the OTLP exporter. The model then reports a data race at trial 0. The replay does not observe it under `-race` (`race_not_observed`), so it is inconclusive. It rests on 622 havoc reads and on otel's attribute hashing, where the model is stuck: `xxhash.NewWithSeed` and `Digest.Sum64`. The leak that is k009's defect is not reached.
- **k023 spurious for another reason.** Its counterexample no longer rests on `os.MkdirTemp`. It now rests on a `Read` that `io.ReadAtLeast` dispatched to a modelled reader that got stuck. The stuck-callee fallback gives `n` and `err` free havoc, and the `io.Reader` contract that unmodelled `Read`s keep (#85) is not applied there.
- **k042: mutation was the cost.** A rerun with each engine timed (default flags, budget 300) gives:

  | step | time |
  |---|---|
  | compile and model build | 3.5 min |
  | fuel climb to 64000 | 43 s |
  | random search at 256000 | 327 s, 12 trials, none found |
  | exhaustive engine | 300 s |
  | mutation | 32 min |

  Mutation gave each of its 24 mutants a share of the budget, but checked it only before each trial. Each mutant ran at least one trial, and those trials do not resume from the setup snapshot, so each one redoes k042's 16384-stripe setup at 256000 fuel. Mutation now stops at the whole budget (#159). Rerun at #159's head, k042 finishes in 1202 s instead of 2400. Mutation stops after 4 of 24 mutants. k042 is still not found: at 256000 fuel the random search runs 12 trials in its 300 s, about 25 s each, and the exhaustive engine 158 of its 294 inputs. The cost of one trial is the next thing to cut.
