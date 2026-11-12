# How much of Go the compile stage translates (2026-11-11)

The trials on unseen projects found gaps one at a time. This audit measures
them all at once, on the one library every Go program shares: the standard
library. `scripts/audit-std.sh <ws>` compiles each of its 165 public,
non-runtime packages under `-deps`, as a user's run does. `audit-std.py`
then sorts every function the model does not translate completely by
cause. Rerun it after a change to see the numbers move.

## Where the model stands

16,909 functions in 165 packages; every package compiles. Test helpers
(functions taking a `*testing.T` and the like) are never targets and are
left out:

| functions | share | |
|---|---|---|
| 11,629 | **71.4%** | translated completely |
| 552 | 3.4% | **language**: Go the compile stage does not model yet |
| 1,281 | 7.9% | **boundary**: what Go stands on (the runtime, `unsafe`, assembly, the operating system) |
| 1,547 | 9.5% | a dependency function `-deps` did not reach. In a run, the [refinement loop](../pipeline.md#refinement-loop) translates such a function when a spurious verdict rests on it |
| 1,244 | 7.6% | a value of unknown origin, because the package was compiled out of context: a type assertion or a nil comparison on an interface parameter, a float input. In a run, the search supplies these values |

A function counts in the first row its causes reach, in this order:
language, boundary, dependency, unknown value.

Only the first two rows are work on the model. Both are finite, and each
item in them is done once for every program.

## Progress

Each change rerun on the audit (#207):

| change | complete | language | boundary |
|---|---|---|---|
| first audit (#205) | 71.4% | 3.4% | 7.9% |
| `clear`, `print`, `println`; debug switches as no-ops | 71.8% | 3.1% | 7.9% |
| `recover` of the panic's value and of a function's own operations (#209); `godebug.New` | 72.1% | 3.1% | 7.7% |
| complex numbers; float-to-integer conversions as amd64 does them | 72.2% | 2.7% | 7.8% |
| the audit's classification corrected (below) | 72.2% | **1.8%** | 7.9% |

The last row changes no translation. Two kinds of message were counted as
language gaps by mistake: the value read from a map of unknown origin (`map
lookup: assumed ...`, `len of map ... assumed below 16`), which always comes
with "a map the model knows nothing about", and floats of unknown origin,
whose description the report cut before the words its rule looked for.
Both are values compiled out of context (an input supplies them in a run).
So the earlier language shares were 0.9 points too high; the trend
between rows holds.

### What the 1.8% is now (291 functions)

| about | functions |
|---|---|
| package variables the model does not build: `flag` variables and `os.Args` (the OS), tables a call of another package fills | 64 |
| calls ignored, deferred calls ignored | 38 |
| `sync.Pool.Get` returns `New`'s object: an object `Put` back is not returned (one of Go's behaviours, the only one modelled) | 26 |
| `unsafe.Slice`, `unsafe.String`, `unsafe.SliceData` | 15 |
| `time.Timer.Reset` and other `time` calls | 13 |
| stores to package variables ignored | 12 |
| package variables used by address that the model has no value for (`&crctab[i]`, `zeroBlock[:]`) | ~60 |
| a long tail, none above 10 functions | ~63 |

## Translating more of the standard library

`GO2LEAN_STD_ALL=1` translates more standard-library packages than the
default list. Every package lifts the audit from 71.8% to 75.9% complete;
the packages that import no boundary package give 74.4%. On real projects,
each costs a spurious verdict and a lost finding, so it stays off: the
data and the project-by-project details are in
[std-translation.md](std-translation.md).

## The language gaps (3.4%)

| about | functions | |
|---|---|---|
| package variables whose value the model does not build (a table loaded as "assumed", a store ignored) | ~110 | as #176, #182 and #184 did for the cases met so far |
| complex numbers: `complex`, `real`, `imag`, `==`, conversions | ~110 | the dialect has no complex arithmetic |
| floats from unknown sources in conversions | ~60 | |
| the `clear` builtin | 43 | |
| `print`, `println` | 18 | |
| a long tail, none above 6 functions | ~210 | |

## The boundary (7.9%)

| about | functions | how |
|---|---|---|
| `os`, `syscall`, `internal/poll`, `os/exec` | ~360 | an environment model, as `osmodel` and `netloop` do for the environment and the network |
| `reflect` | ~290 | `Value` and `Type` are partly modelled (`Kind`, `IsNil`, `Call(nil)`, `TypeOf`, `Implements`, #200; `Interface`, `IsValid`, `Int`, `Uint`, `Bool`, `String`, `Len`, `Elem`, #215); the rest are `Value`'s setters, `Field`, `Index` and `Zero` |
| runtime internals, `internal/abi` | ~210 | |
| functions with no Go body (assembly, `linkname`) | 200 | a pure-Go build tag where one exists (`math/big` with `math_big_pure_go`, #171), else a model |
| `unsafe` | ~180 | conversions through `unsafe.Pointer`, `unsafe.Slice`/`String` |
| debug switches (`internal/godebug`, `internal/race`, `runtime.KeepAlive`, the FIPS switches) | ~120 | no-ops: the model can take them as such |
| `time` | ~100 | timers and the clock are modelled; the rest of `time` is not yet |

## What this means

- **The language gaps are a short list.** Closing the five largest items
  takes the language share from 3.4% to under 1.5%.
- **The boundary is a fixed list.** User code reaches the same runtime, OS
  and `unsafe` that the standard library does, so the list does not grow
  with what users run. Its largest items are an environment model and
  `reflect`'s `Value`.
- **The two other rows (17%) are not gaps.** They are what compiling one
  package out of context leaves unknown. A run resolves them with its
  refinement loop and its generated inputs.
- **This audit measures translation, not execution.** A function that
  translates completely can still get stuck when it runs, as `x/text`
  does (#183). The [semantics tests](../validation.md) (`sem-diff`,
  `json-diff`) check what the model computes against Go.
