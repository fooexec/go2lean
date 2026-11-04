# Project reference

[← Back to README](../README.md)

[Supported Go subset](#supported-go-subset) · [Layout](#layout) · [Roadmap](#roadmap)

## Supported Go subset

Legend: ✅ exact (the model follows Go's semantics; a counterexample
reproduces, and a bounded search finds what Go would do on its domain) · 🟡 havoc or
assumption (may give spurious counterexamples, which `go test` rejects, or
miss behaviours) · ❌ the function is rejected and listed in the model report.
Construct-by-construct detail: [docs/semantics.md](../docs/semantics.md).

**Values**
- ✅ Integers of every width (wrapping arithmetic, shifts, conversions, `min`/`max`), `bool`
- ✅ Strings: indexing, slicing, concatenation, `==`, `range` rune by rune, `[]byte` / `[]rune` conversions
- ✅ Structs, pointers, recursive types, arrays, slices (`make`, `new`, `append`, `copy`, 3-index slicing)
- ✅ Maps the program builds (insertion order for `range`: 🟡)
- ✅ Interfaces holding values the program boxed: type assertions, type switches, method calls
- ✅ Closures, function values, method values; methods; multiple results
- ✅ `error` nil-ness; sentinel errors (`errors.New` / `fmt.Errorf` of a constant, `var errX = &T{...}`) with `==`; `errors.Is`, `errors.As`, `errors.Unwrap` through the program's own error types (`Unwrap`, `Is`)
- ✅ Floats (IEEE 754 float64 and float32: arithmetic, comparisons, conversions, `math` from source, generated inputs; #148); `Exp` and `Log` under an assumption on amd64 (#150); a computed float's `json.Marshal` and `fmt` output 🟡
- 🟡 Complex numbers (opaque)
- 🟡 Interfaces, maps and funcs the model did not build (results of unmodelled calls)
- ❌ Ordered string comparison (`s < t`)

**Control and effects**
- ✅ Loops, branches, recursion (fuel-bounded), calls within the package, calls into dependencies (`-deps`)
- ✅ Panics: index out of range, slice bounds, nil dereference, division by zero, negative shift, `make` length, type assertion, nil map write, `panic(v)`
- ✅ Package variables with constant initializers, constant lookup tables
- 🟡 Other package variables (havoc), `init` functions (not run), stores to globals (ignored)
- 🟡 `defer` of an unmodelled call (ignored); `recover` (havoc)
- ✅ `defer` of a modelled function, in a loop too
- 🟡 Goroutines, channels, `select`, `sync.Mutex` `RWMutex` `WaitGroup` `Once`: deadlocks, goroutine leaks and misuse panics under schedules the search picks ([concurrency.md](../docs/concurrency.md)); `context`, timers (`time.After`, `NewTimer`, `NewTicker`, `AfterFunc`) and `sync.Cond` too; data races (vector clocks), confirmed under `go test -race`

**Standard library and SDKs** (✅ unless noted)
- `strings`: `HasPrefix` `HasSuffix` `Contains` `Index*` `LastIndex*` `Count` `Cut` `Trim*` `ReplaceAll` `Split` `Fields` `Join` `Repeat` `Builder` `Replacer` (literal pairs); `TrimSpace` `ToLower` `ToUpper` `EqualFold` ASCII only 🟡
- `strconv`: `Atoi` `Itoa` `ParseInt` `ParseUint` `ParseBool` `ParseFloat` (decimal; inf/NaN/hex 🟡) `FormatInt` `FormatUint`
- `unicode/utf8`, `unicode/utf16`, `net/url`, `encoding/binary` (the functions a package calls, such as the varints), `container/list`, `bufio`, `io`: translated from their Go source
- `encoding/json`: `Marshal`, `Unmarshal`, `Valid`, `Encoder` to a `strings.Builder`; `encoding/base64` standard encodings; `encoding/hex.EncodeToString`
- `regexp` with constant patterns, and patterns built at run time (common Perl syntax); `fmt.Sprintf` with `%s %d %v %t` 🟡 otherwise; `reflect` `ValueOf` `IsZero` `Kind` `IsNil`
- `sync/atomic` integer types and functions (`LoadInt32`, `AddInt64`, `CompareAndSwapUint32`, ...)
- anthropic-sdk-go and openai-go JSON unions, `Message.Accumulate` (under `-deps`)
- 🟡 `math/rand` (any value in range), clocks, environment, I/O, network (havoc)

On tingly-box (7539 functions and methods, including agent-written specs)
this models 7112 (2025 completely); most of the 427 rejected are test
helpers taking `*testing.T`, `select` statements and `defer` of modelled
functions.

## Layout

```
cmd/go2lean/           CLI
internal/contract/     shared JSON records and the workspace layout
internal/frontend/     compile frontends: gossa (go/ssa), llvm (go-llvm, -tags llvm18)
internal/ir, ssaout/   program representation, validation, Lean printer
internal/constraint/   spec & search: property templates and engine runners
internal/agent/        spec-writing agent: LLM interface, Claude, mock, prompts, loop
internal/poc/          replay: harness generation, execution, verdicts
internal/llvmhunt/     the LLVM line: model, search, C replay under ASan/UBSan
internal/pipeline/     the steps the CLI and the agent share
lean/                  Lean 4 project: SSA core, GoSSA dialect, search engines
lean-llvm/             Lean 4 project: LLVM dialect over VeIR, its search driver
<workspace>/lean/      a workspace's own Lean project: its models and search driver
examples/              ground-truth packages
docs/design.md         design and roadmap
docs/semantics.md      the Go semantics modelled, by construct, with status
```

## Roadmap

Completion is counted in items; ✅ done, 🚧 in progress, ⬜ not started.

**Go line (go/ssa → GoSSA)**: 17 / 20
- ✅ SSA core with an executable, fuel-bounded semantics; GoSSA dialect
- ✅ Heap model: pointers, slices, arrays, structs, writes through aliases
- ✅ Maps, interfaces, closures, function values, methods
- ✅ Standard library and SDK models (list above); dependencies from source (`-deps`)
- ✅ Engines: random search, exhaustive bounded certificates
- ✅ Replay stage: every counterexample replayed as a `go test`, verdict with PoC
- ✅ Spec-in-Go properties and probes (model value vs real value)
- ✅ LLM agent: proposes specs, runs them, reports
- ✅ Regression gate in CI (260 properties); benchmark of hand-found bugs (34/34)
- ✅ Differential tests of the JSON, regexp, UTF-16 and number models against Go
- ✅ Proof prototype: a postcondition proved for all 64-bit inputs by hand (`lean/Proofs`)
- ✅ Real-world run: tingly-box, 82 bugs found and confirmed ([go2lean-tingly-box](https://github.com/andreasfoo/go2lean-tingly-box))
- ✅ Proofs over all inputs in the pipeline for `no_panic` on integer and bool functions, each with its trust base (10 on the examples, all kernel-checked), and, through the weakest precondition, for specs and integer slices of loop-free functions (5 more on the examples, each judged by Lean's comparator)
- ✅ Sequence specs (`go2lean:sequence`): properties of a sequence of calls, bounded model checking over short sequences
- ✅ Spec mutation check: each spec that held is run against mutants of the code under test; a spec no mutant fails is flagged as checking nothing
- ✅ Specs as Go fuzz targets (`go2lean fuzz`, and in `agent`): the same properties searched by `go test -fuzz` on the real code
- ✅ Go fuzz targets (`FuzzXxx`) read as specs (`go2lean run`, `-fuzz-specs`): a project with fuzz targets needs no spec writing
- 🚧 Proofs of postconditions: `go2lean prove` states a function's contract from a postcondition and proves loop-free functions alone; loops by an agent that writes the invariants under the kernel's check (10 of 10 goals, nested loops and bubble sort included, [eval](../docs/eval/proof-agent.md)) or by hand (up to insertion sort); a Go spec can be the postcondition; every proof judged by Lean's comparator in a sandbox, with a certificate ([trust](trust.md), [#22](https://github.com/andreasfoo/go2lean/issues/22))
- 🚧 `init` functions and package state (stores an `init` makes on every path, since 2026-10-23); floats ✅ (#148)
- 🚧 Concurrency (C1, C2, [concurrency.md](../docs/concurrency.md)): goroutines, channels, `select`, `sync`; deadlocks and leaks found and confirmed in 60 of GoKer's 68 blocking kernels (`context`, timers, `sync.Cond` included), with the model's schedule replayed in Go when its scheduler does not take it; data races found and confirmed under `-race` in 24 of its 35 non-blocking kernels; a certificate under every schedule for small configurations (C3)

**LLVM line (C, LLVM IR, TinyGo → VeIR semantics)**: 7 / 11
- ✅ `lean-llvm/`: LLVM dialect over the same core with [VeIR](https://github.com/opencompl/veir)'s integer and memory semantics
- ✅ C frontend (clang + mem2reg + go-llvm); random and exhaustive search; replay under ASan/UBSan
- ✅ TinyGo IR (LLVM 22): Go's ABI, runtime panics, aggregates, function values, globals, callees from the standard library and runtime lowered from their IR
- ✅ `go2lean tinygo -compare`: the same package through both lines, side by side
- ✅ Cross-check on all 22 example packages: 73 of 94 functions agree (29 on the same confirmed bug), no semantic disagreement; the rest are TinyGo-line gaps (design doc §4.4)
- ✅ Replay of TinyGo-line counterexamples with `go test`, in the sandbox
- ✅ Package `init` functions and string constants as inputs in the TinyGo line
- 🚧 Inputs for interfaces and maps; `sync.Once`, `strconv`, `encoding/json` on the TinyGo line
- ⬜ Specs in C; properties beyond "no undefined behaviour"
- ⬜ Floats, `ptrtoint`/`inttoptr`, more intrinsics, `malloc`/`free`
- ⬜ CI for the LLVM line (needs LLVM development files)

**Agent and scale**: 2 / 4
- ✅ Resumable full passes over a module; parallel searches
- ✅ Specs from an issue tracker's bug classes (tingly-box: 28 confirmed, 11 new bugs)
- ⬜ Ranking targets by expected yield; `ModelGap` feedback into the frontend
- ⬜ Generalising a confirmed bug across a package
