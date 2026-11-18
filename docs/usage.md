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
Neither weak mode is safe for arbitrary code. A replay runs in a copy of
the package's directory: its files, `testdata`, and the subdirectories of
data its tests read by relative path (no Go file or `go.mod`, 64 MB at
most). Build steps still run outside
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
go install github.com/fooexec/go2lean/cmd/go2lean@latest
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
git clone https://github.com/fooexec/go2lean.git
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
compiled github.com/fooexec/go2lean/examples/overflow: 2 functions (2 complete, 0 partial, 0 rejected; -v lists them)
== Spec & search stage: properties
  2 properties to search (-v lists them)
== Spec & search stage: hunt
  p_overflow_sum_specsum       random: found at trial 12 -> cx_overflow_sum_specsum_random_s42
      inputs map[xs_len:5 ...] memory map[xs:[1 0 0 0 9223372036854775807]] model ok -9223372036854775808
== Replay stage: generate PoC and verify
  cx_overflow_sum_specsum_random_s42  confirmed    real: map[ret:-9223372036854775808]
      PoC: out/poc/cx_overflow_sum_specsum_random_s42
== Summary
  2 properties checked
  1 confirmed finding a caller can reach or that violate a spec, 1 place:
  confirmed: Sum
    spec violated: Sum satisfies the spec-in-Go oracle SpecSum
    real: map[ret:-9223372036854775808]
    PoC: out/poc/cx_overflow_sum_specsum_random_s42
  0 spurious, 0 inconclusive
  full summary: out/summary.md
```

Read the summary at the end first. It lists every
violated spec, and the confirmed findings a caller of the package's API can
reach, one entry per line where the program fails, with runtime errors before the package's own
`panic` calls. The other confirmed findings are only counted. They are in
unexported functions, need a nil no caller passes, or rest on package state
the test forged. `out/summary.md` ranks every finding by how plausible its
input is.

`run -v` lists every compiled function with its status (and why one is
`partial`: the calls and values the model does not know), every property,
and every property's search.

### Reading the results

Every counterexample the model finds is run as a real `go test` (the
PoC), and gets one of three verdicts:

| verdict | meaning | what to do |
|---|---|---|
| **confirmed** | Go fails the same way on the same input | a real finding: read its rank (below) and the PoC |
| **spurious** | Go does not fail on that input: the model was wrong | ignore it for the code. Its `why` lines name what the model guessed (an unmodelled call, an assumption). Please report it with `out/summary.md` attached: it is a model gap to close |
| **inconclusive** | the real run neither confirmed nor refuted it (a timeout, out of memory, a race not observed) | look at its `why` lines; rerunning may settle it |

A confirmed finding's **rank** says how plausible its input is for real callers:

| tier | the input | listed in the run's summary |
|---|---|---|
| 1 | any caller of the package's API can pass it, or a caller in the module passes the same nil, or (in an unexported function) it comes from a parameter of the public API through no check that may stop it, the path named (`grpc.KeepaliveParams(kp.MaxConnectionAge) → ... → transport.getJitter(v)`) | yes |
| 2 | depends on the callers (an unexported function's arguments, a nil a caller may pass, a receiver built field by field where the package has a constructor for its type, an argument of the package's own struct type, or an interface argument holding one, whose unexported fields the test set, as regexp2's `FindNextMatch(m *Match)` on a forged match or blackfriday's `Markdown` on a forged `*Html` renderer) | counted |
| 3 | no caller in the module passes it | counted |
| 4 | the test forged it (an opaque value left nil, package state set up first) | counted |

A violated spec is always listed. `out/summary.md` has every finding with
its rank, input, real output and PoC.

A finding raised by the package's own explicit `panic` says "check whether
its documentation says it panics there": many APIs panic on bad arguments
by design (`MustParse`, a negative size).

No counterexample does **not** mean correct: the search is bounded, and
the summary says which properties were proved, exhausted (every input in
a small domain) or only searched.

### User models

A spurious verdict whose `why` names calls the model does not look into
(an SDK client, a cgo binding, an operating-system call) can be fixed
without changing go2lean. Write what the call should return, in Go, in
the module's `.go2lean/models` directory (or one given with
`run -models dir`), and mark it with the function it stands for:

```go
package models

import "example.com/acme/sdk"

//go2lean:model example.com/acme/sdk.(*Client).Get
func ClientGet(c *sdk.Client, key string) (string, error) {
	if key == "" {
		return "", sdk.ErrNotFound
	}
	return "value", nil
}
```

- **Targets.** A function is `pkg/path.Func`, and a method is
  `pkg/path.(*T).Method` or `pkg/path.T.Method`. A method's receiver is
  the model's first parameter, and the signatures must otherwise match.
  The run stops with an error naming both signatures if they don't.
- **Effect.** Every static call of the target goes to the model, which is
  translated like the package's own code. A model can keep state in
  package variables, and can call the function it stands for: calls made
  inside the models are not redirected.
- **The models only shape the search.** Replay runs the real code, so a
  wrong model can cost spurious or missed findings, but a confirmed finding
  is still a real `go test` failure.
- **Limits.**
  - A call through an interface has no static target to replace.
  - A model cannot reach another package's unexported fields.
  - User models need `-deps`, which is on by default.

### Time and memory

The first run on a machine builds go2lean's Lean runtime once: a few
minutes, which it announces. After that, the trials on 28 unseen
projects took from 7 seconds to 12 minutes each (`docs/eval/`), with the
default 10 s search budget per property and a 10 s timeout per PoC.
`-run-budget s` caps the whole search stage, and `-run regexp` searches
only the functions it names.

`run ./...` takes several packages side by side: at most `-jobs`
searches and replays run at once (default: one per CPU), and each
package's output is printed whole, in order. Before any search, the
workspace's search executable is built against every package's model
once, and the run prints how long that took. On a large module such as
grpc-go, that build is the largest part of the run: 198 s of 317 s on
its 24 balancer packages.

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

Each spec file the agent writes is first built as `go test` builds the
package's tests (`go test -c`), since the compile stage tolerates type
errors; the compiler's errors and the specs the frontend rejects go back to
the agent, for up to `-repairs` rounds. A call to the LLM that fails (a
dropped connection) is tried twice more. Every prompt and response is
written to `out/agent/<package>/`, and the report lands in `out/agent/<package>/report.md`. A
revised spec file in the report round triggers one more pass.

## Commands

```text
go2lean version                                   the release tag, or the commit it was built from
go2lean compile [-workspace out] <package>        Go -> Lean model, ABI, targets
go2lean props   [-workspace out] -package <path>  template properties from the model report
go2lean hunt    [-workspace out] [-property id] [-engine random|exhaustive|all] [-jobs n] [-budget s]
                [-seed n] [-trials n] [-fuel n] [-maxlen n] [-exhaust-len n] [-exhaust-limit n]
go2lean poc     [-workspace out] -counterexample id [more ids...]   one build per package, one process per counterexample
go2lean run     [-workspace out] [-v] [-nil-inputs] [-deps=false] [-generics=false] [-unexported] [-specs-only] [-run regexp] [-run-budget s] [-changed rev] [-mem-limit mb] <packages...>   compile, props, hunt, poc (./... allowed)
go2lean agent   [-workspace out] [-llm claude|mock:<path>] [-model id] [-base-url url] [-thinking]
                [-repairs n] [-report] <package>
go2lean prove   [-workspace out] -goals goals.json | -func F -post P  [-llm claude-code|claude|mock:<path>]
                [-rounds n] [-lean-tools] [-paranoid] [-deps] [-out proofs.json] <package>
```

`run` and `compile` model the dependencies the packages call by default
(`-deps`): the dependency packages' functions, and the standard-library
code the model translates from source: every standard-library package
that imports no boundary package (the runtime, `unsafe`, `reflect`, `os`,
`syscall`, `net`) and has no semantics of its own in the model, such as
`strings`, `strconv`, `math`'s portable code, `slices`, the encodings,
`compress/*` and `text/tabwriter`
([std-translation.md](eval/std-translation.md)). `GO2LEAN_STD_ALL=0`
keeps to the shorter list used before. `-deps=false` models only the compiled packages; their
calls into anything else are then unmodelled, and their results havoc,
which gives more spurious counterexamples.

`run` searches the packages' exported generic functions too (`-generics`,
on by default). A generic function has no model until something
instantiates it, so `run` writes, into a test-file overlay
(`zz_go2lean_generics_test.go`, never written into the package), a wrapper
that calls each one at the first simple type arguments its constraints
accept (`int`, `string`, `float64`, `bool`), and searches the wrapper. A
finding names the instance: `Last[int]`, `Map[int, string]`. Methods of
generic types, unexported generic functions and functions whose
constraints none of those types satisfies are listed as not searched.
A method of a generic type is modelled per instance, named with its type
arguments (`LRU[int, int].Keys`), for its callers (#196).

`run` searches the `no_panic` and `no_hang` templates of the package's API
only: exported functions, and exported methods of exported types. An
unexported function's finding depends on what its callers pass, and the
exported functions that call it are searched, so its own templates are
skipped, and the hunt stage says how many. `-unexported` searches them
too, except those with an input that holds a pointer to a struct the
test cannot fill in, of a package outside the standard library (a
receiver whose field is a `*grpc.ClientConn`): the test can only pass `new(T)`, which no caller
does, and the hunt stage says how many it skipped (#230). `-run` searches
the functions it names, exported or not. On grpc-go, `-unexported` over
the whole module took 58% longer and tripled the findings below tier 1
for one more tier-1 finding ([evaluation](eval/grpc.md)); run it on the
packages that hold the logic. Specs,
fuzz targets and test functions are always searched (#172).

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
repository, [fooexec/go2lean-tingly-box](https://github.com/fooexec/go2lean-tingly-box).
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

Each externally visible function is searched for inputs with undefined
behaviour (`-internal` adds the `static` ones, whose callers' invariants
are not in their parameters). Parameters are generated from their C types,
read from the debug information of a `-g` build:
- a pointer to a struct is that struct, member by member, its pointer
  members null or a further object, to depth 2;
- a `char *` with no length after it is a NUL-terminated string;
- a `T **` is a slot holding a null or a pointer to a `T`;
- a pointer to one scalar (`int *`, `size_t *`, `double *`) with no
  length after it is an out parameter: one object of its type, never null;
- any other pointer followed by an integer is a buffer and its length.

glibc's `ctype` classifiers (`isdigit`, `isalpha`, `isspace`, `tolower`
and the rest, through `__ctype_b_loc` / `__ctype_tolower_loc` /
`__ctype_toupper_loc`) are modelled as the real C-locale tables, so a
parser that uses them runs in the model instead of getting stuck
(`examples/llvm/ctype.c`).

`malloc`, `calloc`, `realloc` and `free` are modelled (unless the file
defines them): a freed object holds no bytes, so a later access is a use
after free, and a second `free` is a double free (`examples/llvm/heap.c`),
also when they are reached through a table of function pointers
(`hooks.c`). `strlen`, `strcmp`, `strncmp`, `memcmp`, `strchr`, `strcpy`,
`tolower` and `toupper` run byte by byte over the model's memory, so a
string without its terminator is read out of bounds (`cstr.c`).

`inttoptr` gives back a pointer into the live object that holds the
address, so a buffer aligned by arithmetic on its address keeps its bounds
(`align.c`); 0 is null. Where trials get stuck, the log names the site, so
the next thing to model is visible.

A counterexample is replayed as a C program that builds the same objects,
under AddressSanitizer and UndefinedBehaviorSanitizer.

`-fuzz 60s` adds libFuzzer. It runs on each function the model search did
not settle:
- it held without exhausting its domain;
- trials were stuck, or it read results of unmodelled calls;
- its violation did not reproduce in the real program.

A target decodes the parameters from the fuzzer's bytes and calls the
function under ASan and UBSan; it is written to
`out/fuzz/<key>/<function>/`. A crash input is run once more and recorded
as a counterexample with its verdict:

```
  weight_of
    random: model index_out_of_range ...
      rests on: 1 value(s) of unmodelled calls
    -> spurious
    libfuzzer: crash after 19772 runs, input .../fuzz/m_fuzz/weight_of/testdata/crash-...
    -> confirmed fuzz.c:15:12: runtime error: index -4 out of bounds
```

`-memory` looks for memory errors only: out-of-bounds reads and writes,
use after free, double free. Signed arithmetic wraps (`-fwrapv`) in the
model, the replay and the fuzzer, and they run AddressSanitizer and the
bounds checks without the rest of UBSan. So an overflow without a memory
error neither stops a search nor counts as a finding. Each confirmed
finding is tagged with its class, most severe first: a write out of
bounds, a use after free or double free, a read out of bounds. The agent
on a C file uses this mode by default.

A library of several files is checked through a spec file that includes
its headers:

```bash
go2lean llvm -memory -specs-only -fuzz 60s -lib 'src/*.c' -cflags -Iinclude specs.c
```

The model links all the files (`llvm-link`); the replay and the fuzzer
link them as separate translation units (`examples/llvm/multi/`).

The model's search is the one that can exhaust a domain. The fuzzer runs
the real program, so it reaches what the model does not run (here
`strtol`) and values the search rarely draws (`examples/llvm/fuzz.c`).
Parameters a target cannot decode yet (a pointer to a struct) are listed
as not fuzzed.

### Specs in C

A spec is a C function that returns an `int`, non-zero when its property
holds, marked with clang's `annotate` attribute:

```c
#define SPEC __attribute__((annotate("go2lean:spec")))

SPEC int spec_round_trip(uint8_t n) {
    char buf[4];
    format_u8(n, buf);
    return parse_u8(buf) == n;
}
```

The search treats a spec's 0 as a violation, as it treats undefined
behaviour. The replayed program prints `go2lean: spec violated` and aborts
when the spec returns 0, which confirms it (`examples/llvm/spec.c`). A spec
says how the code is meant to be called, so it avoids the inputs that the
direct search of a function has to guess: `format_u8(n, out)` searched on
its own gets a one-byte `out` and overflows it, which the spec never does.

`go2lean agent` writes such specs when its target is a C file:

```bash
go2lean agent -guidance docs/specs/cors.md -workspace out lib.c
```

It writes `go2lean_specs.c` next to `lib.c`, prepending `#include "lib.c"`
so the specs see its static functions and types. A violation whose
undefined behaviour is in a spec's own body (its arithmetic on its
parameters) is the spec's error, `ub_in_spec`, not a finding; the agent
gets those reports and one round to fix the specs. `-fuzztime` also runs
libFuzzer on the specs the model search did not settle. It repairs the file until
clang accepts it, searches only the specs (`go2lean llvm -specs-only`), and
writes its report to `out/agent/<key>/report.md`. It needs a build with the
LLVM tag. `scripts/llvm-regress.sh <ws>` runs every C
example and compares each function's outcome with
`testdata/regress/llvm.golden`, as CI's `llvm` job does. The first build of `lean-llvm/` fetches VeIR and
compiles the part of it the dialect imports (under two minutes); later runs
reuse it. What is modelled so far is in
[docs/design.md §4.4](../docs/design.md).
