# Pipeline and specifications

[← Back to README](../README.md)

[How it works](#how-it-works) · [Writing a spec](#writing-a-spec)

## How it works

![The go2lean pipeline: human- or agent-written Go specs compile with the source; built-in specs also guide Lean search; candidate inputs are replayed with go test.](../docs/images/how-it-works.svg)

**Specs enter in two ways:** built-in properties such as `no_panic` are
generated for eligible targets; business specs are Go functions written by
a human or the optional agent. Go specs pass through the compile stage alongside
the target source, then both kinds of properties guide the spec & search stage.
A model counterexample is only a candidate until the replay stage checks it
with the real Go toolchain.

### Compile stage

`golang.org/x/tools/go/ssa` lowers the package; the
frontend turns it into a frontend-independent IR (blocks with parameters,
one dialect op per instruction) and prints it as a Lean term over the `GoSSA`
dialect. The Lean side is a small SSA core in the spirit of
[lean-mlir](https://github.com/opencompl/lean-mlir): a dialect-parametric
program representation with a fuel-bounded, total interpreter. Slice contents
come from a read-only memory oracle fixed per run; values the frontend cannot
model are explicit havoc inputs. The model may over-approximate; it never
silently guesses. Lean core only — no Mathlib.

### Spec & search stage

Three property shapes:

- **`no_panic`**, generated for every modelled function.
- **`postcondition`**, for every **spec-in-Go** function: a Go function with
  a `// go2lean:spec Target` doc comment and signature
  `func Spec(<Target params>..., ret <result>) bool`, compiled into the
  model with the same pipeline as the target, so the Lean property and the
  Go oracle agree by construction.
- **`go2lean:sequence`**: what must hold over a sequence of calls (an
  invariant after every event, a condition at the end of the stream),
  checked by bounded model checking over short sequences (design doc §5.2).

Two engines run per property:

- **Seeded random search**, with per-trial value profiles and the target's
  own integer literals, so magic-constant branches are reachable.
- **Exhaustive enumeration** of a small domain (narrow signatures only).
  When it passes, it records an `exhausted` certificate:
  - `exhausted_havoc` when some inputs ran through values the model does
    not know, of which it tried only a few;
  - `exhausted_assuming`, with the list, when some runs took an
    under-approximation such as a dropped store to a global.

A spec whose verdict depended on a havoc value (it looked inside an opaque
value, or called something unmodelled) is reported as unknown, never as a
counterexample or a certificate.

Both engines run in `go2lean-search`, a native executable built by lake
from the Lean engines and every generated model, driven by a JSON
description of the search. Properties are searched in parallel, and each
engine stops at a wall-clock budget (10 s by default, 120 s under `agent`),
so one function with expensive inputs cannot stall a sweep. go2lean keeps
its own memory under a third of physical memory (a soft limit; `GOMEMLIMIT`
overrides it); `GO2LEAN_MEMPROFILE=<file>` writes a heap profile near a
run's peak.

The agent sits on top:

1. it reads the source and the model report;
2. writes specs into `<pkg>/go2lean_specs_test.go` and repairs the ones the
   frontend rejects;
3. runs the pipeline;
4. writes a report from the verdicts.

### Proofs

An `exhausted` certificate covers a finite domain. Where it can, the spec &
search stage then tries a theorem over every input, recorded `proved`:

| Route | Functions | The theorem | Checked by |
|---|---|---|---|
| bounded | `no_panic` of integer and bool parameters | every input, one fixed oracle, runs within a fuel that read no havoc value and take no under-approximation (`simp`, `split`, `bv_decide`) | `#print axioms`: trust `kernel`, or `compiler` when `Lean.ofReduceBool` appears |
| weakest precondition | `no_panic` and specs of loop-free functions (integers, bools, strings, integer slices) | the function's contract for every input, every value the model does not know, every run that ends, with no under-approximation (`wp_auto`) | Lean's comparator against go2lean's statement, with a certificate |

`go2lean prove` states the same contract from a postcondition you give
(in Lean, or one of your Go specs), loops included. A loop needs an
invariant, written by the optional proof agent or by hand. What a `proved`
record asserts, and what it rests on, is in [trust](trust.md); the
commands are in the [usage guide](usage.md).

### Replay stage

A counterexample becomes a Go test in the target package,
which executes the real function with the generated inputs: run go2lean on
a scratch clone, since a function that writes files will write them. The
test never fails by itself; it prints what happened and the `poc` package
judges: `confirmed` (the real run violates the oracle the way the property
says), `spurious` (it does not — with a diagnosis of what the model got
wrong), or `inconclusive`. This is the only source of trust: a bug is
reported only with a verdict from here.

For a deadlock or goroutine leak the call runs up to 200 times under a
watchdog; when Go's scheduler never takes the model's interleaving, the
replay stage replays the model's schedule: the package is rewritten
through an overlay so that each channel, lock and `select` operation waits
for its turn, and the real runtime then shows the deadlock or leak. The
PoC carries the overlay ([concurrency.md](concurrency.md)).

### Refinement loop

With `-deps`, a `spurious` or `inconclusive` verdict can name the
dependency function whose unmodelled call it read, for example one past
the four calls `-deps` translates. Only the reads the violation depends
on count (`relevant_havoc`). The loop then translates that function
(`gossa.Options.Translate`, whatever its depth), recompiles, and searches
and replays that property again (#85):

```
== Refinement round 1: translating github.com/andreasfoo/go2lean/testdata/refinedeep.Level5
  refine_digit_nopanic random: none in 2000 trials (0 out of fuel, 0 moot/stuck); ...
```

The old counterexample is dropped. A property the refined model still
refutes gets a new one, which is replayed in turn. The loop stops when no
verdict names a function not yet translated, or after `-refine` rounds
(default 2; `-refine 0` turns it off).

Some calls cannot be translated this way: standard-library calls,
interface methods, sockets, `reflect`. They stay as they are, so their
verdicts stay. Those sites need a contract (#89) or a stub.
`examples/refine` is the ground truth: a digit the dependency computes
five calls deep.

### Generated support sources

When replay uses package-local generated Go helpers (including imported fuzz
oracles), the delivered artifact includes them rather than only a test that
references absent functions. Schedule-rewritten sources take precedence over
general overlays, matching the actual replay.

Such artifacts contain `bundle.json`, source SHA-256 hashes, `support/` and/or
`replay/` sources, and a standalone standard-library-only `prepare.go`. From
the bundle directory, use it to rebase an overlay onto a disposable clone of
the same target revision:

```sh
GOWORK=off go run prepare.go -bundle . \
  -module /absolute/scratch-clone \
  -output /absolute/new-replay-overlay.json
```

The preparer validates the source inventory, hashes, package-relative paths
and absence of symlinks within the bundle/clone. It creates only the requested
new JSON file; it does not build or execute target code, replace target source,
or overwrite an existing output. The bundle's README gives the subsequent
local `go test -overlay ...` command, including `-race` when needed. That manual
command is **not sandboxed**, unlike pipeline replay: trust the artifact,
source and dependencies and use a disposable isolated environment. Tests
print observations; exit zero alone does not confirm a finding.

The bundle includes generated/replaced Go sources, not the target repository
or its dependencies. Current replay applies overlays only to the target
package; arbitrary dependency-package overlays and non-Go support overlays
are not supported by this portable delivery path. The target package must be
inside the selected module directory. Hashes detect inconsistent files, not
an untrusted producer or changes to the underlying target revision. These
bundles are not persisted builtin bindings or resume authorizations.
Artifacts requiring no support sources retain the single-test instructions.
If a package already contains copied `zz_go2lean_poc_*_test.go` observations,
replay refuses with cleanup instructions rather than deleting source files.

## Writing a spec

You do not need to write Lean. There are two different ways to state a claim:

| Claim | How it is represented | What violates it |
|---|---|---|
| The function must not panic | Auto-generated `no_panic` property; no spec function required | A panic during the target call, confirmed by real Go replay |
| The result must satisfy a business rule | A Go function marked `// go2lean:spec Target` | The spec returns `false`, confirmed by real Go replay |

For example, the included [`Sum` spec](../examples/overflow/overflow.go) says
that adding non-negative integers should produce a non-negative result:

```go
// go2lean:spec Sum
func SpecSum(xs []int, ret int) bool {
    for _, x := range xs {
        if x < 0 {
            return true // This rule does not constrain inputs with negative elements.
        }
    }
    return ret >= 0
}
```

The annotation associates the spec with `Sum`. Its arguments are the target's
inputs followed by its result; `true` means the rule holds for that execution.
Place the spec in the target package; the agent writes its specs to
`go2lean_specs_test.go`. The compile stage translates the spec alongside the
target; the spec & search stage looks for inputs that make it return `false`;
the replay stage checks the same oracle in Go.

**Why not just use `no_panic`?** Go integer overflow wraps without panicking.
The business spec detects a wrong result that a panic check cannot catch.
Conversely, a target panic is checked by `no_panic`, not by pretending the
postcondition returned `false`.

**A claim is not a proof.** Finding no counterexample in a bounded search
is not a guarantee for all inputs. See [limitations](../docs/limitations.md)
for model boundaries and the restricted cases supporting all-input proofs.
