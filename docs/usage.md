# Usage guide

[← Back to README](../README.md)

[Quick start](#quick-start) · [The agent](#the-agent) · [Commands](#commands) · [On a real module](#on-a-real-module) · [C and LLVM IR](#c-and-llvm-ir)

## Quick start

### 1. Prerequisites

Requirements: Git, Go 1.26 and [elan](https://github.com/leanprover/elan). The Lean
toolchain pinned in `lean/lean-toolchain` is fetched on the first build.
Required for isolated replay on Linux: [bubblewrap](https://github.com/containers/bubblewrap)
(`apt install bubblewrap`, plus `prlimit` from util-linux), which isolates tests
during the replay stage. Missing isolation refuses replay by default. For
**trusted code only**, `GO2LEAN_SANDBOX=rlimits` explicitly permits resource
limits without isolation, and `GO2LEAN_SANDBOX=off` permits unconfined execution.
Neither weak mode is safe for arbitrary code. Build steps still run outside
confinement (including cgo), so a scratch clone is not a complete security boundary.

Run `go2lean doctor` to check local tools, Go 1.26 or newer (with automatic
toolchain downloads disabled for the version probe), the bundled Lean toolchain pin, and usable
isolation, including the `--clearenv` capability required by Lean's comparator
(older bubblewrap may support replay but not proof judging).
`COMPARATOR_BWRAP` selects the comparator sandbox executable, as in Lake. It returns nonzero when prerequisites are missing or isolation is
unavailable. It does not download tools, load target packages or execute target
code by default. Add `-deps` to inspect target package and test dependencies:

```sh
go2lean doctor -deps ./...
```

This runs offline `go list` metadata loading with the local toolchain, module
proxy/checksum/VCS downloads disabled, host GOFLAGS/GOENV overrides ignored,
and `-mod=readonly`. Workspace resolution is preserved through a disposable
copy of `go.work` (with absolute local paths) and its sums, since Go may update
workspace sums even in readonly mode. Target module/workspace files are not
rewritten. Missing dependencies, package-loading errors and empty matches fail
the check; use trusted tooling to prepare dependencies separately. It does not
compile or run target code, check types, install dependencies, inspect every
build-tag configuration, or establish model support. Cache metadata may be
written. The metadata command has a 30-second timeout and a 16 MiB output
limit; use narrower package patterns if either limit is reached.
It is not a clean-machine installation test.

### 2. Install, or clone and run the included example

For an explicitly selected pure/total string ordering comparator, the
[experimental ordering mode](builtins.md#experimental-usage) supplies a
non-panic law without handwritten spec code. It requires a dedicated workspace
and default native build configuration; it is not automatic contract inference
or a completed real-project release evaluation.

```sh
go install github.com/andreasfoo/go2lean/cmd/go2lean@latest
cd /path/to/your/go/module
go2lean compile ./...
```

The binary embeds the trusted Lean core sources. It materializes them in
`$GO2LEAN_CACHE` (default: the user cache directory's `go2lean/`) and builds
there; no go2lean checkout is required. Changes to embedded sources require
rebuilding the binary. The target module's `lean/` directory is never used
as the trusted runtime. Compilation alone does not run the search or replay.
Lean/Lake must still be installed for search and proof commands.
[Standalone packaging evaluation](eval/install.md) records the isolated
external-module compile check and its limits.

Relative `-workspace` paths are resolved from the invoking directory.
Without `-module`, package loading uses the enclosing Go module. With
`-module /path/to/module`, commands can also be invoked outside any Go module.
The latter flag does not change how workspace paths are resolved.

To run the included example instead:

```sh
git clone https://github.com/andreasfoo/go2lean.git
cd go2lean
go run ./cmd/go2lean run ./examples/overflow
```

No LLM API key is needed. This example includes a Go spec: when all inputs
are non-negative, the sum should be non-negative. Integer overflow can
violate that rule without panicking.

### 3. Inspect the result

Illustrative output (search details may vary):

```
== Compile stage
compiled github.com/andreasfoo/go2lean/examples/overflow -> out/lean/Go2Lean/Generated/gossa/overflow.lean
  SpecSum      complete  func(xs []int, ret int) bool  [spec for Sum]
  Sum          complete  func(xs []int) int
== Spec & search stage: properties
  p_overflow_sum_nopanic       no_panic       Sum does not panic on any input
  p_overflow_sum_specsum       postcondition  Sum satisfies the spec-in-Go oracle SpecSum
== Spec & search stage: hunt
  p_overflow_sum_nopanic       random: none in 2000 trials; exhaustive: holds on 9724 inputs (recorded as exhausted)
  p_overflow_sum_specsum       random: found at trial 12 -> cx_overflow_sum_specsum_random_s42
      inputs map[xs_len:5 ...] memory map[xs:[1 0 0 0 9223372036854775807]] model ok -9223372036854775808
== Replay stage: generate PoC and verify
  cx_overflow_sum_specsum_random_s42  confirmed    real: map[ret:-9223372036854775808]
      PoC: out/poc/cx_overflow_sum_specsum_random_s42
```

The PoC directory holds a ready-to-run Go test and a README with the
reproduction command. Everything the modules exchange is a JSON record under
`out/` (types in `internal/contract`).

## The agent

```sh
export ANTHROPIC_API_KEY=...        # or `ant auth login`
go run ./cmd/go2lean agent ./examples/oob
go run ./cmd/go2lean agent -llm mock:internal/agent/testdata/oob ./examples/oob   # offline replay
```

The LLM (Claude Opus 5.5 through the Anthropic SDK by default) is behind a
one-method interface. `-model` and `-base-url` point the same client at any
Anthropic-compatible endpoint (`-thinking=false` if the provider rejects the
parameter); `mock:<dir>` replays scripted responses (`round-1.md`,
`round-2.md`, …) for tests and offline runs. Replace `model_name` below with
a model ID supported by your chosen endpoint.

```sh
go run ./cmd/go2lean agent -model model_name -base-url https://api.minimaxi.com/anthropic ./examples/oob
```

Every prompt and response is
written to `out/agent/<package>/`, and the report lands in `out/agent/<package>/report.md`. A
revised spec file in the report round triggers one more pass.

## Commands

```text
go2lean compile [-workspace out] <package>        Go -> Lean model, ABI, targets
go2lean props   [-workspace out] -package <path>  template properties from the model report
go2lean hunt    [-workspace out] [-property id] [-engine random|exhaustive|all] [-jobs n] [-budget s]
                [-seed n] [-trials n] [-fuel n] [-maxlen n] [-exhaust-len n] [-exhaust-limit n]
go2lean poc     [-workspace out] -counterexample id [more ids...]   one go test per package
go2lean run     [-workspace out] [-nil-inputs] [-deps=false] [-specs-only] [-run regexp] [-run-budget s] [-changed rev] [-mem-limit mb] <packages...>   compile, props, hunt, poc (./... allowed)
go2lean agent   [-workspace out] [-llm claude|mock:<path>] [-model id] [-base-url url] [-thinking]
                [-repairs n] [-report] <package>
go2lean prove   [-workspace out] -goals goals.json | -func F -post P  [-llm claude-code|claude|mock:<path>]
                [-rounds n] [-lean-tools] [-paranoid] [-deps] [-out proofs.json] <package>
```

`run` and `compile` model the dependencies the packages call by default
(`-deps`): the dependency packages' functions, and the standard-library
code the model translates from source (`strings`, `strconv`, `math`'s
portable code, `slices`, the encodings, the environment and
`crypto/rand`). `-deps=false` models only the compiled packages; their
calls into anything else are then unmodelled, and their results havoc,
which gives more spurious counterexamples.

`-run` selects targets as `go test -run` selects tests, and a suite's
method is a target of its own (`-run 'TestY'`). `-run 'TestX/sub'` selects
`TestX` and, within it, the subtests whose name `sub` matches (spaces in
the name read as `_`, as in Go), in the search and in replay; one subtest
level only. Only the `t.Run` calls the test makes on its own `t` are
filtered: a helper's or a nested subtest's run as before (#48).

`run` searches the properties whose target synchronises (goroutines,
channels, locks) first, then, in each group, specs, test functions and
the other templates. With `-run-budget s`, no property starts once the
hunt stage has taken `s` seconds; the ones not searched are listed, and
`-resume` searches them later (#45).
With `-changed REV`, the properties whose target reaches (through the
package's calls) a function `git diff REV` touches come before all others:
the working tree against `REV`, untracked files included, so
`-changed origin/main` searches what a branch changed first. The hunt
stage says how many properties that is.

The searches a run starts in parallel share a memory limit, `-mem-limit`
megabytes (default: four fifths of the container's memory, the smaller of
its cgroup limit and the machine's; negative: none). go2lean samples
their memory: a new search waits while three quarters of the limit are in
use, and over the limit the largest one is stopped. Its property reads
`stopped, out of memory (...)`, is listed at the end of the hunt stage and
is not recorded, so `-resume` (with fewer `-jobs` or a higher limit)
searches it again; the rest of the run goes on (#45).

`-fuel` is where a hunt starts: when a quarter of the random trials run
out of fuel and none of them loops forever (the runs are long, not hung),
the hunt searches again with four times the fuel, up to 256 times the
first, and the exhaustive engine, the counterexample and the certificate
use the raised fuel; the hunt summary says so (#45). While the fuel can
still be raised, a search stops as soon as its trials show the fuel
short (a quarter of at least 20), so the budget goes to the search at the
raised fuel (#71). The reruns that look for a loop in out-of-fuel trials
stop at the budget too.

`prove` states, for each goal, the function's contract as a Lean theorem
(for every input, every value the model does not know, and every run that
ends: no panic, no under-approximation, a final state satisfying the
postcondition) and proves it. A proof counts only when Lean's comparator
accepts it in a project of its own against go2lean's statement, with
Lean's standard axioms only, replayed by Lean's kernel and (`-paranoid`,
the default) every independent checker bundled with Lean; each proved
goal gets a certificate (`<workspace>/judge/.../results.json`). What a
proof asserts, and what it rests on: [trust](trust.md). A goal is a
function and a postcondition: in Lean over the parameters, `ret` and `s'`
(`"post"`), or one of the function's `go2lean:spec` functions (`"spec"`),
which is then run in the model on the arguments and the result, so the
theorem is about the same property the search checks
(`examples/proofs/goals.json`, `examples/loops/goals.json`). Goals are
proved in order, each with the earlier ones as contracts at its calls; a
goal's `"name"` names its theorem. Parameters may be integers, bools,
strings and slices of integers; a string or slice is quantified by its
contents (a `List (BitVec w)` under the parameter's name, with `x_len`
and `x_ref`), and go2lean adds a companion theorem per goal for the nil
slice or empty string, with `"empty_post"` as its postcondition when the
goal's speaks of what a nil slice lacks (its backing array). A parameter
named `s`, `ret` or like a Lean keyword is `s_` in the postcondition.
With `-deps` (the default) the model is linked with the packages it
calls, as `run -deps` translates them, so their functions are modelled,
not havoc. A value the model does not know is any value of its type
(`h_hv` in the theorem). Each goal is checked on its own, the earlier ones
assumed; an open goal lists the issues in the model of its function and
callees that may keep it open, and a companion is not attempted while its
main goal is open. Loop-free functions are proved
with no proof text; a function with loops needs an invariant per loop.
With `-llm`, an agent writes them: it answers with invariants and a proof,
Lean checks, and its errors go back for up to `-rounds` attempts
(`claude-code` runs the `claude` CLI on its own login; with
[lean-lsp-mcp](https://github.com/oOo0oOo/lean-lsp-mcp) on `PATH`, e.g.
`uv tool install lean-lsp-mcp`, it also checks its answers in Lean itself
before giving them, `-lean-tools`, on by default). Lean text an agent
writes runs only in [bubblewrap](https://github.com/containers/bubblewrap)
(no network, nothing writable outside a scratch copy), so `prove` needs
`bwrap`; `GO2LEAN_LEAN_SANDBOX=off` runs it unconfined, and the
certificates say so. `-out` saves the
proofs that worked, which `prove -goals` checks again without the agent
([results](eval/proof-agent.md)).

## On a real module

```sh
go run ./cmd/go2lean compile -workspace out-tingly -module ../tingly-box ./...   # 156 packages, 15 s
go run ./cmd/go2lean agent -workspace out-tingly -module ../tingly-box -model model_name \
    -base-url https://api.minimaxi.com/anthropic github.com/tingly-dev/tingly-box/remote/scenario/builtin/claudecode
```

On tingly-box (7539 functions and methods) the model covers 7112; a full
pass over its 156 packages (7095 properties) takes about three hours at
four jobs and ends with 140 confirmed findings (80 of them not involving an
input the test could only pass as nil), 152 spurious counterexamples and
2779 exhaustive certificates (see docs/history.md, §9b): `truncate(s, n)`
helpers that panic on a negative `n`, `strings.Repeat` with a negative
count, writes to nil maps, slices of short hashes, and many nil
dereferences of fields a caller is expected to set. The agent on
`internal/typ` wrote 63 specs and found `pickWithinTier` indexing an
empty slice.

A package's model is one Lean module, which Lean elaborates and compiles
in one process. A model over 4 MB is written as part modules of about
2 MB that the package's module imports, so lake builds them in parallel
in bounded memory. nats-server's `server` package (35 MB of model) built
as one module was killed at 13 GB. As 17 parts it builds in 11 minutes,
using at most 4.8 GB in one Lean process and 9.3 GB across the four jobs
(#45).

The tingly-box case study (hand-written specs, the rounds of hunting,
PoCs, full-pass history and one page per finding) lives in its own
repository, [andreasfoo/go2lean-tingly-box](https://github.com/andreasfoo/go2lean-tingly-box).
Its first finding: a hand-written postcondition on
`ConvertAnthropicBetaToOpenAIRequest` is refuted by the search and confirmed by the replay stage:
tool_use ids of 43 to 51 characters reach the assistant `tool_calls[].id`
untruncated while the tool-result side is truncated to 40 (tingly-box
issue #1924).

## C and LLVM IR

The LLVM line runs C (or any LLVM IR) through the same SSA core, with
LLVM's integer and memory semantics taken from
[VeIR](https://github.com/opencompl/veir): integers that can be poison,
byte-addressed objects, and undefined behaviour where LLVM (and, for
signed overflow, C) says so. It needs LLVM's development files and the
sanitizer runtime (`llvm-18-dev`, `libclang-rt-18-dev` on Ubuntu) and a
build with the LLVM tag:

```
go build -tags llvm18 -o go2lean ./cmd/go2lean
./go2lean llvm -workspace out examples/llvm/oob.c
```

```
  sum      random: model index_out_of_range ... -> confirmed ERROR: AddressSanitizer: heap-buffer-overflow
  avg      random: model undefined_behavior ...  -> confirmed runtime error: signed integer overflow: 2147483647 + 7 ...
  max_of   no UB in 2000 trials; exhaustive: no UB on all 40 inputs
```

Each function is searched for inputs with undefined behaviour, assuming a
pointer parameter followed by an integer is a buffer and its length; a
counterexample is replayed as a C program under AddressSanitizer and
UndefinedBehaviorSanitizer. The first build of `lean-llvm/` fetches VeIR and
compiles the part of it the dialect imports (under two minutes); later runs
reuse it. What is modelled so far is in
[docs/design.md §4.4](../docs/design.md).
