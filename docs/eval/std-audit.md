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
| `reflect` | ~290 | `Value` and `Type` are partly modelled (`Kind`, `IsNil`, `Call(nil)`, `TypeOf`, `Implements`, #200); the rest are `Value`'s setters and accessors |
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
