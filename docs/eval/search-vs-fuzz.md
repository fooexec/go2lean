# Evaluation 5: go2lean's search against go test -fuzz, same specs

Issue #37 asks what go2lean adds over simpler ways of finding defects. A
spec helps every method that can run it, so this run holds the spec fixed
and compares only what searches it: go2lean's model search (code
constants, small exhaustive domains, then replay in real Go) against
Go's native fuzzer. This page is the protocol, committed before any spec
is written; results are added afterwards and the protocol is not edited
once the run starts.

## Cases

Real wrong-result defects, from fix commits of open-source Go libraries:
- **Pool.** Commits whose subject reads like a fix, that change 1 to 3
  non-test Go files and add or change a test
  (`search-vs-fuzz/mine.py`, drawn by `search-vs-fuzz/draw.py` into
  `search-vs-fuzz/pool.jsonl`), from 54
  libraries cloned for earlier evaluations, at most 3 per library, in a
  seeded random order (seed 20261028). Network clients and servers
  (go-redis, fasthttp, the Prometheus client) are left out: their fixes are
  rarely reachable through generated values.
- **Screening.** Read in that order, until 30 are accepted. A case is
  accepted when:
  - the fix changes a result, not only a panic, a race, a leak or speed;
  - one exported function or method shows it on values a generator can
    build;
  - the parent commit's documentation, or a well-known reference, says
    what the right result is;
  - the fix's test fails on the parent and passes on the fix.

  An agent screens and writes two files: the public case list (repository,
  parent commit, package, target function, where its documentation or
  reference is) and the private ground truth (the fix, the bug, the
  triggering input). The spec writer reads only the first until the run
  ends. Every rejection is recorded with its reason.

## Specs

For each case, one spec of the target, written by one writer (Claude, in
the session that runs the evaluation):
- from the parent commit's documentation, signatures and types, or the
  reference, with the skill `.claude/skills/go2lean-spec`;
- without reading the target's body, the fix, its test, the commit log
  after the parent, or the issue tracker;
- as a `go2lean:spec` function in a `_test.go` file of the target's
  package, the documentation line it encodes quoted in its comment.

The writer knows the target has a defect; that is the same for both
arms. A spec that does not compile, or that cannot express the oracle, is
recorded as such and the case counts as not found by both arms.

## Arms

Both arms get the same spec file, on the parent commit, on the same
machine, one after the other.

- **A, go2lean.** `go2lean run -deps -budget 300 -prove=false`, the
  target's package only. Found: a confirmed counterexample of the spec.
- **B, fuzz.** `go2lean fuzz -fuzztime 5m` on the same spec. Found: a
  failing input of the spec.

Seeds: B starts from the package's existing fuzz corpus
(`testdata/fuzz`), if any; `go2lean fuzz` adds none. A draws from the
code's constants and the spec's literals. That difference is part of what
is measured: it is how each tool ships.

## Scoring

A failing input counts as the defect when:
- the spec holds for that input on the fix commit;
- or, where the spec is wrong for that input on both commits, it does not
  count, and the case is marked "spec wrong".

Per case and arm, the score is found, not found, or tool failure, with the
time to the first failure.

Reported:
- found by A only, by B only, by both, by neither;
- for "neither", whether the spec could catch the defect at all: does it
  fail on the triggering input from the ground truth?
- every tool failure of A (model gaps, from `scripts/gaps.go`), since
  those are what stage 1 is extended from;
- float cases separately, as the model treats floats as opaque.

The hypothesis to test: A finds defects that need a specific combination
of the code's constants (a boundary, a delimiter, an exact length) where
B's random mutation rarely lands, and B finds what A's model cannot run.
If A finds nothing B misses, that is the result.

## Tool

go2lean frozen at the commit that adds this page, built once into a
separate binary. Go and Lean as in the earlier evaluations.

## Cost

No LLM is called by either arm. The screening agent is one run, about
0.5 to 0.8 M tokens; the specs are written in the evaluating session.

## Results

Run on 2026-10-28 with the frozen tool (`e5feedc`). Screening examined all
135 candidates and accepted 29 (rejections in `search-vs-fuzz/screen.jsonl`).
Per case: `search-vs-fuzz/results.tsv`. The specs are in
`search-vs-fuzz/specs/` (as `.go.txt`), the cases and the ground truth in `cases.jsonl` and
`truth.jsonl`, and the scripts beside them.

**Found** means a failing input of the spec at the parent for which the spec
holds at the fix commit (`score.sh` replays it there).

| | cases |
|---|---|
| found by A (go2lean) | 7 |
| found by B (fuzz) | 11 (3 of them floats) |
| both | 4: c029, c032, c056, c094 |
| A only | 3: c053, c108, c121 |
| B only | 7: c039, c069, c088 (floats), c062, c104, c118, c126 |
| neither | 15 |

Of the 15 found by neither, 8 specs could not have caught the defect: the
trigger lies outside what the spec generates (no trailing newline, non-ASCII
punctuation, a nested array path, and so on). That was judged by reading each
trigger against the spec, after the run.

What decided most cases was not the search:
- **A's counterexample was often a model artifact.** 11 of A's
  counterexamples did not reproduce in Go (spurious). Each read havoc or
  took an under-approximation (an unmodelled `encoding/binary.Uvarint`,
  `regexp`, `reflect.DeepEqual`, floats, a stuck `strings.Builder`): the
  random engine reports such a counterexample only as a fallback, when none
  of its 2,000 trials gave a clean one. The model gaps on the target's
  path, not an early stop, are the cause. B never has this problem: it runs
  the real code.
- **B's first failure was often a wrong spec.** 14 of B's failing inputs
  also fail at the fix commit, most of them at the fuzzer's first, zero-value
  input. A hit a wrong spec 3 times.
- **Both stop at the first failure.** So a wrong corner of a spec, or a
  model artifact, hides whether the defect itself would have been found:
  in all three A-only cases B's first failure was a wrong spec, and in one
  of B's (c126) A's was.

Where A found what B did not: a multibyte pattern equal to the data
(c053), a key present only in a nested object (c108), and https with port
443 (c121). The first two came from the spec's own literals and the
code's constants combined; the third from the code's constants.

Floats: A found none of the three float defects; B found all three.

### Deviations

- The screening agent put the area of some bugs into the public oracle
  field ("omit the scheme's default port", "testParseExtensions"), so those
  specs were not fully blind. Both arms got the same spec. One gjson body
  was glimpsed (two lines of `GetMany`) while reading its docs.
- `go2lean fuzz` skips specs of variadic targets: for c015 and c094, B is a
  hand-written fuzz target over the same spec (`search-vs-fuzz/manual/`, as `.go.txt`),
  fuzzed 5 minutes.
- Old test files fail Go 1.24's `vet`, which `go test` runs: in 7 cases the
  replay (A) and the fuzzing build (B) failed. They were rerun with
  `GOFLAGS=-vet=off`; c104 also with the right import path.
- c118's A run hit the 30-minute limit: `go2lean run` searches every
  template property of the package with the same budget before the spec.

### Tool issues (stage 1 and harness, from this run)

1. Model gaps on the targets' paths (11 spurious results, each a
   havoc-dependent fallback after a clean search found nothing).
2. `go test`'s vet is not turned off in the replay and the fuzz export; the
   failure is reported as "the target exited" (7 cases).
3. `go2lean fuzz` skips variadic targets (2 cases).
4. `go2lean run` cannot be limited to the specs; template properties take
   the budget first (c118 timed out, c024 took 12 minutes).
5. The model runs out of fuel on byte-wise parsers (`ansi.Truncate`: 1940 of
   2000 trials).
6. Unmodelled in the specs' paths: `encoding/binary.Uvarint`, `regexp`
   (`FindIndex`), `reflect.DeepEqual`, `fmt.Sprintf` widths, floats.
7. Before the run, on a toy package: a spec's call to a generic function of
   the package under test was havoc without `-deps`, and
   `io.ReadAll(strings.NewReader(s))` disagreed with Go under `-deps`.

### What this says about the question

On the same specs, Go's fuzzer found more defects (11 against 7), all of
the float ones, and none of its finds were model artifacts. go2lean found 3
that the fuzzer did not reach within 5 minutes; each needed a specific
combination of literals. The larger lesson is that both searches mostly
stopped at the wrong thing: model artifacts for go2lean, wrong specs for the
fuzzer. Closing the model gaps on the targets' paths, and checking specs
against trivial inputs before searching, would have changed more outcomes
than either search.

## Rerun after the fixes (2026-10-28)

The same 29 cases and specs, with go2lean at `c2d5416` (#41: vet off in
the harness, `package_build_error`, variadic targets in `go2lean fuzz`,
`-specs-only`, `encoding/binary` from source), A with `-specs-only`.
Scores per case: `search-vs-fuzz/rerun-scores.txt`.

| | first run | rerun |
|---|---|---|
| found by A (go2lean) | 7 | 9 |
| found by B (fuzz) | 11 | 14 |
| both | 4 | 8 |
| A only | 3 | 1 (c108) |
| B only | 7 | 6 |

- **A's two new finds came from the model gap closed:** c062 and c104,
  through `encoding/binary.Uvarint`.
- **B's three new finds:** c015 because variadic targets now export; c053
  and c121 because a fuzzer's run differs from run to run.
- **Still spurious for A:** 9, on `reflect.DeepEqual`, `regexp`, floats and
  `strings.Builder`.

The conclusion stands: on functional defects, with the same specs, Go's
fuzzer finds as much or more, and go2lean's own contribution is the rare
defect that needs a combination of literals. The next question is
concurrency, where a fuzzer does not vary schedules
(docs/eval/conc-recent.md).

## The spurious cases again, after the #46 fixes (2026-11-03)

The 13 cases in #46's gap ledger (A spurious, or A blocked by fuel), arm A
only, at main `91afeb2`. The fixes merged since #46 was filed are:
`reflect.DeepEqual` (#136), regexps built at run time (#137),
`strings.Builder.Write`, `fmt.Sprintf` widths (#135), init cells for
package variables (#94, #121) and the fuel raise (#132). `run46.sh`
rebuilds each case from `cases.jsonl`, `truth.jsonl` and `specs/`, runs A
and scores its spec counterexample at the fix, as `score.sh` does. Scores:
`search-vs-fuzz/rerun46-scores.txt`.

| case | #46 gap | rerun A | now |
|---|---|---|---|
| c079 | `reflect.DeepEqual` | spurious | **FOUND** |
| c093 | `reflect.DeepEqual` | spurious | spec fails on the fix (as B) |
| c130 | `regexp` built at run time | spurious | spurious: a package-level `regexp.MustCompile` variable is havoc |
| c039 | floats | – | – (every trial stuck at `math.Mod`) |
| c069 | floats | spurious | **FOUND** |
| c088 | floats | – | – (every trial stuck at `math.Mod`) |
| c097 | `strings.Builder.Write` | spurious | spurious: a call through a function value the model does not know |
| c022 | opaque map lookup | spurious | spurious: `sort.Search` fixed (#145); a map literal keyed by an array is still opaque |
| c109 | `growSlice` | spurious | spec fails on the fix (as B), with `recover()` nil (#145) |
| c008, c116 | fuel | – | spec fails on the fix (as B) |
| c118 | `fmt.Sprintf` widths | – | **FOUND** (with `-specs-only`) |
| c024 | `[]byte` globals | spurious | spurious: `html.UnescapeString` unmodelled, `escapeHTML` stuck |

- **3 new finds** (c069, c079, c118). Five more counterexamples are no
  longer spurious: they fail on the fix as well, as the fuzzer's do (the
  spec, not the model, is wrong there).
- **Spurious: 8 → 4** of the rerun's 9 (c063, not in the ledger, was not
  rerun). Each of the four rests on a different gap. The floats (c039,
  c088) give no counterexample at all.
- **Deviation.** The rerun above used `-specs-only`; `run46.sh` searched
  the templates too, and c118 and c024 hit the 1800 s limit before the
  replay. Both were run again with `-specs-only` (c118 is found in 32 s).
  In the other 11 the spec is searched first (#133) with the same budget.

### The floats (#148, 2026-11-04)

After floats became IEEE values (#149), `math` was translated from source
(#151) and float inputs were generated (#152), the three float cases again,
with `-specs-only` (`run46.sh`, a tool at #152's head):

| case | before | now |
|---|---|---|
| c039 (`DistanceCIE94`) | – (every trial stuck at `math.Mod`) | **FOUND** at trial 1 |
| c069 (`SorensenDice.Compare`) | FOUND | **FOUND** at trial 5 |
| c088 (`BlendHcl`) | – (every trial stuck at `math.Mod`) | **FOUND** at trial 1 (t = 1.0) |

c039 and c088 were two of the fuzzer's seven "B only" finds in the first
run: go2lean finds them now. Of #46's ledger, four cases remain
spurious, each on its own gap (above).

### Tables keyed by arrays (#46, c022, 2026-11-04)

c022's spurious counterexample came from `grandfatheredMap`, a
package-level `map[[maxLen]byte]int16` literal. go/ssa builds each array
key as a local literal, so the table was an unmodelled load and its havoc
value indexed `altTagIndex` out of range at trial 1. The frontend now
folds such keys (arrays and structs of integer and bool constants) and
builds the table. Rerun with `-specs-only`: no counterexample in 2000
trials. The bug itself stays out of reach of this spec: it compares
`String()`, which the bug leaves right (`Macro.Canonicalize("cmn-u-co-stroke")`
prints `zh-u-co-stroke` but its `Extensions()` is empty). Spurious: 4 → 3
(c024, c097, c130).
