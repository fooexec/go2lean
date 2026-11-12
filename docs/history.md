# go2lean development history

The record of how go2lean got where it is: the runs on tingly-box and what
each one changed in the model, the benchmark of hand-found bugs, and notes
kept from earlier plans. Nothing here is the current design; that is
[design.md](design.md), with the limitations in
[limitations.md](limitations.md). Section numbers are those the design
doc used, so older references (`§9b`, `§9c`) still point here.

## 4.3b Stage B: the original heap design notes

Written before the heap model was built; the parts not done yet are
still open.

- **Heap.** `Heap := Ref → Obj` with `Obj := .array (elems : Array Val) |
  .struct (fields : Array Val) | .string (bytes : Array (BitVec 8))`. The
  heap is part of the run state next to `HState`, not of the oracle: the
  oracle keeps supplying contents of *inputs* (slices, strings and pointed-to
  values the caller owns), the heap holds what the function allocates or
  writes. A read goes to the heap when the `Ref` was allocated or written
  during the run, else to the oracle. This keeps stage-A models valid and
  makes writes exact for aliasing: two slices with the same `Ref` see each
  other's stores.
- **Values.** `Val` gains `.ptr (ref : Nat) (path : List Nat)` (a pointer to
  a field/element path inside an object; `nil` is `ref = 0`) and
  `.structv (fields : List Val)` for struct values. Pointer comparison is
  `ref`+`path` equality; `nil` dereference panics `nilDereference`.
- **Ops.** `alloc (kind)`, `load/store` generalised to a path, `field i`,
  `fieldAddr i`, `indexAddr`, `makeSlice`, `append` (grow-and-copy with a
  fresh `Ref` when `cap` is exceeded, as Go does), `copy`. Because `denote`
  stays branch-free and call-free, these are single steps; the loop in
  `copy`/`append` is a bounded fold over `len`, which is acceptable for
  the random and exhaustive engines and is where `bv_decide` would need a
  summary lemma instead.
- **Frontend.** `Alloc` → `alloc`; `FieldAddr`/`IndexAddr` → address values
  (paths), `Store` → `store`; struct parameters are flattened by the ABI
  into their fields (as TinyGo does for LLVM, so the two frontends agree on
  the ABI); pointer parameters are generated as either `nil` or a fresh
  `Ref` whose pointee comes from the oracle.
- **M3.** The harness materialises structs from the flattened fields and
  pointers as `&T{...}` or `nil`; a counterexample's `memory` section grows
  per-`Ref` object dumps.
- **Strings (after the heap).** `string` is `(ref, len)` over immutable
  bytes; `len`, indexing, slicing and `==` are ops (equality is a bounded
  byte fold); `strings.Contains/HasPrefix/Index/TrimSpace`, `bytes.*` and
  `strconv.Atoi/Itoa` get hand-written models in a `Go2Lean/Dialect/GoSSA/
  Stdlib.lean` table keyed by import path, because a havoc result for
  every `strings` call makes a string function's model meaningless.

Acceptance for stage B: `examples/alias` (writes through aliased slices),
a struct-returning constructor with an invariant spec, and a re-sweep of
tingly-box with the pointer/struct rejection counts as the metric.


## 4.3c What lean-mlir does for memory (read 2026-10-17, `SSA/Projects/SLLVM`)

lean-mlir's *structured LLVM* dialect is the closest prior art and confirms
the shape of our heap model while pointing at two things to borrow:

- **Memory is a map from block ids to blocks**, a pointer is `(block id,
  offset)`, a block holds its contents as one wide bitvector plus a
  live/freed flag; loads and stores are range-checked against the block and
  raise UB otherwise. That is our `ref` + path model at byte granularity.
  Go has no pointer arithmetic, so the typed path form is right for GoSSA;
  the byte form is what the LLVM dialect (§4.4) should port directly,
  including `PoisonOr` for UB where we use `Res.panic`/`modelStuck`.
- **Every memory op exists in two forms**: an *impure* one that runs in an
  effect monad (`load : ptr → m (bitvec w)`), and a *pure* one that takes
  and returns the memory as an explicit SSA value (`loadPure : (mem, ptr) →
  (mem, bitvec w)`, "memory SSA"). Pure programs stay in the fragment that
  `simp`/`bv_decide` handle. Our `Dialect.denote : Oracle → Heap → Op → args →
  Res (Val × Heap)` is exactly the pure form, threaded by the interpreter
  rather than appearing as a variable; when the proof engines arrive, an
  explicit `mem` value in the generated program is the representation to
  switch to, and lean-mlir shows it costs nothing in the dialect.
- `EffectKind` (`pure`/`impure`) tags each op so the framework knows which
  ops touch state; our `denotePure`/`denote` split is the same idea and
  should become an `Op.effect` function so the unfolding lemmas can skip
  heap threading for pure ops.
- Recursive data types are not a dialect concern in lean-mlir either: they
  are pointers into blocks. The type-directed generation of bounded linked
  structures (§4.3b, the ABI type table) is go2lean-specific because our
  inputs are Go values, not memory images.
- Their model has no deallocation and models a single function body; ours
  allocates across calls and never frees, which Go's GC makes sound.


## 9b. Smoke test on a real module (2026-10-17)

`tingly-box` (156 packages, 1152 Go files, an LLM gateway) was compiled with
the go/ssa frontend in one pass (15 s). What it taught:

| finding | consequence |
|---|---|
| 3783 top-level functions, **15 complete, 2 partial, 3766 rejected** — all at the signature | stage A's subset is the ceiling, not the engines; bodies were never reached |
| rejections by type kind: pointer 1324, string 897, named struct/interface 727, other slices 272, map 126, `[]string` 104, `context.Context` 91, interface 86, multiple results 51, `error` 35 | order of work: (1) multiple results + opaque `error` (cheap, unlocks the `(T, error)` idiom); (2) strings as `(ref, len)` over the memory oracle; (3) the heap model for pointers, structs, maps — the bulk |
| the module's `replace` target was a git submodule missing from a shallow clone; the tool said only "package has errors" | load errors are now reported with their causes |
| per-package loads re-type-checked the whole dependency graph (minutes per package) | `compile ./...` loads once; 156 packages in 15 s |
| packages sharing a base name (`types`, `util`) overwrote each other's records and Lean modules | workspace records and Lean modules are keyed by the full import path |
| `init#N` package initialisers were modelled as functions | skipped |
| the modelled functions are byte/rune classifiers, `gcd`, `max`, small budget arithmetic — exactly the shape the engines handle | real bugs in such helpers are rare; the value of go2lean on real code depends on the type-coverage work above |
| named integer types (`time.Duration`, `logrus.Level`) passed the frontend but broke the engines (which parsed type names) and the harness (no import) | the ABI carries width, signedness and the type's import path |
| **first real finding**: the agent (MiniMax-M3 through the Anthropic-compatible endpoint) proposed `SpecBudgetDurationNonNegative` for `claudecode.budgetDuration(seconds int) time.Duration`; the random engine found `seconds = MaxInt64` at trial 12; the real run returned `-1s` — `time.Duration(seconds) * time.Second` wraps for `seconds ≥ 9 223 372 037` | the loop works on real code once a function is modelled; the agent's report named the line and the threshold |
| the agent needs `import "time"` for `time.Duration` in spec signatures; the prompt forbade imports | the rule is now "imports only for the types in spec signatures"; package-level variables of the target are explicitly out of the subset |
| the model put one `go2lean:spec` marker at the top of the file, separated by a blank line; go/ssa saw no doc comment and the specs became targets with `no_panic` properties | a function `Spec<X>...` whose parameters are X's parameters plus `ret` of X's result is taken as a spec for X regardless of the marker; the prompt shows the marker placement |
| agent logs of a second package overwrote the first's | one log directory per package under `<workspace>/agent/` |
| after adding multiple results and opaque `error` values, the sweep models 19 real functions (was 15); `(T, error)` alone does not unlock much because the same functions also take strings, pointers or structs | type coverage has to come as a package: the heap model (§4.3b) with structs and pointers, then strings with a modelled core of `strings`/`bytes`/`strconv` |
| heap model (structs, pointers, slice writes) built; `examples/alias` confirmed end to end | 65 complete + 9 partial |
| methods of named types enumerated (the denominator becomes 7498 functions) | 89 complete + 11 partial |
| strings as heap byte arrays with a hand-written `strings`/`strconv` core and literal-driven generation | 464 complete + 35 partial |
| recursive named structs through an ABI type table with depth-budgeted generation (the earlier nil cut-off was wrong: Lean models linked structures fine; only the generator needed a budget) | `examples/list` confirmed |
| unmodelled results of string type havoc'd, other unsupported results take their zero/opaque value as a recorded assumption; arrays; opaque interfaces and maps (covers the `...any` varargs behind every `fmt.Sprintf`) | **468 complete + 389 partial = 857 of 7498 (11.4%)**; remaining signature blockers are interfaces, maps, funcs and `context.Context` |
| first full pipeline over the 111 packages with a model (855 properties): 53 confirmed, 146 spurious, 30 inconclusive | 43 of the confirmed were nil receivers/arguments; 112 of the spurious were stuck models reported as panics; the inconclusive were harness compile errors (name clashes, unused imports, internal/unexported types) |
| after: stuck is moot, nil inputs opt-in, harness fixes, `strings.Split`/`Fields`/`Join`/`Repeat`/`Trim*`/`ToLower`/`ToUpper`/`ReplaceAll`/`IndexByte`/`LastIndex`/`Count`/`EqualFold` modelled, unmodelled pointer results point at fresh zero objects, one `go test` per package | **11 confirmed, 26 spurious, 0 inconclusive**. Confirmed: seven `truncate(s, n)` helpers that slice with a negative `n`, two `strings.Repeat` calls with a negative count, `blobPath` slicing a 3-byte hash to 4, and `budgetDuration` wrapping to a negative duration. The remaining spurious cases are unmodelled calls (see the attribution in the run log) |
| `len`/`cap` on slices of anything but integers was havoc'd (random lengths, spurious index panics); shift counts ≥ 64 made Lean compute `2^k` | fixed; rerunning the 16 packages that had spurious verdicts: **9 confirmed, 5 spurious, 0 inconclusive**; the five that remain depend on `crypto/rand`, `sha256`, `base64`, an OAuth registry and `fmt` — genuine over-approximation that M3 filters. Three more real findings surfaced (`shortenPath`, `sliceLines`, `detectShellOperator`, all slicing with an unchecked length) |
| the largest signature blocker after that was slices of non-integer elements (484 rejections), and `append` rejected every function building a result | slice elements are now any modelled type (`Gen.sliceOf`, `elem_param` in the ABI, `[]T{...}` literals in the harness) and `append`/`copy` are dialect ops; `examples/recs` confirms three planted bugs. **1191 of 7498** |
| 4950 of the remaining rejections were named types: receivers and parameters pointing at structs with a map, interface, func, mutex or channel field, plus `context.Context`, `any` and `*testing.T` | *opaque values*: interfaces, maps, funcs and channels are representable as a value the model never looks into (`Gen.opaque`, `.unit`); reads through them (map lookup, type assertion, interface comparison, calls) are havoc, map updates are ignored, and M3 passes the zero value (`context.Background()` for contexts). Functions taking a `testing` type are test helpers and skipped. A zero array of structs is printed as `List.replicate`, and generated modules set `maxRecDepth` for blocks with hundreds of instructions. **3423 of 7498 (45.7%): 1402 complete, 2021 partial** |
| the engines ran under `#eval` in a fresh `lean` process per property: the interpreter interpreting the interpreter, 50–80 s per property on tingly-box functions with large inputs | `go2lean-search`: a `lean_exe` linking the engines and every generated model (a registry M1 regenerates), driven by a JSON search description (`lean/Go2Lean/Driver.lean`); the same property takes 9 s, the ground-truth packages 10 s each including compile, and properties run in parallel (`-jobs`, default: CPUs). A `*Config` receiver with hundreds of fields still took 27 s per property, so each engine has a wall-clock budget (`-budget`, default 10 s) and reports how far it got |
| floats (1659 struct fields), `unsafe.Pointer`, fixed arrays, `defer mu.Unlock()`, `range` over strings and maps, `json.RawMessage <-> []byte`, 3-index slices | floats and `unsafe.Pointer` are opaque (arithmetic and comparisons havoc); arrays are values (`Gen.arrOf`, `[N]T{...}` in M3; longer than 64 opaque in the ABI); deferred calls the model does not look into are ignored, `defer` of a modelled function still rejects; `range` over a string is a heap iterator advanced by `strNext` (UTF-8 decoded as Go does), over a map runs zero times as a recorded assumption; name-only conversions alias; `[]rune` conversions; `reslice3`; `Index` on strings (new go/ssa) fixed; foreign structs with no exported field (`sync.Mutex`) are opaque parameters; `examples/runes`. **6198 of 7498 (82.7%): 1873 complete, 4325 partial**; 304 of the 1300 rejected are `*testing.T` helpers, the rest closures, `select`, `defer` of modelled functions and results of foreign struct types |
| first pass on the enlarged model (38 packages, 917 properties): 22 confirmed, 5 spurious, 31 inconclusive. Most confirmed were nil dereferences of pointer *fields* the generator left nil; the inconclusive were the harness refusing `internal/` types (a wrong reading of Go's rule: the test lives inside the module) and `[]float64{nil}` | nested pointers non-nil by default (recursive links stay nullable), Go's internal rule applied with the target's package, opaque zero literals, unexported fields of foreign structs opaque. The agent on `internal/routing` (MiniMax-M3, thinking off: with thinking on it spent its whole 32k output budget thinking): 37 specs, all modelled |
| in partial functions most trials were *moot*: `HState.take` (a havoc read) rebuilt the state without `s with`, so every havoc instruction reset the heap and the next load got stuck | fixed; the interpreter now records the op and operand shapes where the model got stuck, the random engine tallies them and `hunt` prints the most frequent site, so the next model gaps come from the engines rather than from guessing |
| with the heap fix, `internal/routing` (37 agent specs): the "recursive links may be nil" rule had made `gin.Context.Request` nullable (five "Request is nil" dereferences); `SpecNewAffinityStage` compared opaque interfaces, so its verdict was a coin flip (four spurious); the exhaustive engine spent minutes building the inputs of a receiver with hundreds of leaves; every remaining moot run was a call into a function rejected for `MakeClosure` | the Go side tells a back-edge (`link`, nullable at every depth) from any other pointer (`ptr`, non-nil by default) along the ABI path; a spec verdict that read havoc is `specUnknown` (moot, never a refutation or a certificate); the exhaustive engine only enumerates signatures with at most 24 leaf generators; closures are opaque func values. The rerun: one confirmed (a nil interface field, the class M3 cannot populate), one spurious, no inconclusive |
| a workspace's `models/` reached 1.7 GB: every occurrence of a nested named struct (gin.Context inside http.Request inside ...) was expanded in place, and a full compile next to a second one ran the machine out of disk and then of memory | every named struct is described once in the ABI's type table and referred to by name (`type_ref`), as recursive types already were; `ResolveSessionID`'s record went from 324 KB to 54 KB. One full compile at a time: go/ssa over all 156 packages with their test variants takes several GB |
| the agent loop on `internal/typ` (MiniMax-M3): 63 specs, one repair round, a report round that revised the file and ran again | two confirmed: `TierTactic.pickWithinTier` indexes an empty slice (defensive: every caller guards it, and the report says so) and a postcondition on `RecordingMode.Has` that ignored `ParseRecordingMode`'s normalisation (the spec was wrong, not the code: the report hedges between the two readings). The loop works end to end on a real package; judging which confirmed spec violations are code bugs is still the reader's job |
| **full pass on the fixed core** (156 packages, 7076 properties, about two hours at four jobs): **97 confirmed, 114 spurious, 167 inconclusive, 2754 exhaustive certificates**. Confirmed by panic: 45 nil dereferences (mostly server handlers reading an interface field the test cannot populate), 18 index out of range, 11 slice bounds, 4 writes to a nil map, 4 negative `strings.Repeat` counts, two explicit panics. The inconclusive were harness compile errors in five classes (imports of types reached through the type table, `_` and `json` as parameter names, variadic parameters, func results under vet, unexported element types), all fixed after the pass; the spurious are over-approximation through havoc values (nil pointers, explicit panics and slice bounds that the real run with those inputs does not reach) | the next pass validates the harness fixes. Coverage at this point: **7112 of 7539 functions (2025 complete, 5087 partial)**, the extra functions being the agent's spec files; models/ is 378 MB |
| the unmodelled callees of the full pass, by call site: `fmt.Errorf` 1533, `fmt.Sprintf` 843, `strings.Builder` methods 454, `fmt.Println/Printf` 615, `json.Unmarshal/Marshal` 497, `time.Now` 230, `errors.New` 83; every spurious counterexample of the pass had used a havoc value | `errors.New` and `fmt.Errorf` are non-nil errors (the message is not modelled); `strings.Builder` is a string cell whose methods are dialect ops, empty as a parameter (a copied non-empty Builder panics in Go); `examples/builder`. The printing and JSON functions stay havoc: their results are rarely branched on |
| the agent on `internal/guardrails/core` (48 specs): one real finding (`detectShellOperator`, already known), one "confirmed" spec violation that was a wrong spec (the report's suggested fix is the current code), and eight spurious postconditions whose *target* had read havoc; every spurious counterexample of the full pass had read havoc too | both engines prefer a refutation whose run read no havoc value and report a havoc-dependent one, so marked, only when nothing better turns up in the budget. The report round then revised the wrong specs and the second pass kept only the real finding. Judging a confirmed spec violation still needs a reader: the agent's own report can argue for a fix that is already there |
| the pass after the first harness fixes (7111 properties): 112 confirmed, 120 spurious, 150 inconclusive, of which 133 were one class: an opaque parameter of a func or map type naming types of other packages (`func(http.RoundTripper) http.RoundTripper`) had no import path in the ABI; the rest were type aliases described inline with their unexported fields, and a parameter named like an imported package | the ABI records every import path a spelling mentions and sees through aliases; the harness renames parameters that shadow an import and tells a compile error from a target that ended the process (`log.Fatal`) |
| **third full pass** (7088 properties, engines preferring havoc-free counterexamples, three hours): **140 confirmed, 126 spurious, 55 inconclusive, 2797 exhaustive certificates**. Confirmed by panic: 56 nil dereferences, 30 index out of range, 18 slice bounds, 18 negative `strings.Repeat` counts, 6 writes to a nil map, a division by zero, an `os.Exit` during the test, and a handful of explicit panics; 50 of the inconclusive were harnesses importing a package by the last element of its path (`anthropic-sdk-go` is package `anthropic`), 4 targets ended the process with `log.Fatal` | the preference raised confirmed findings from 112 to 140 with the same spurious count: the engine keeps looking within its budget for a refutation the real run can reproduce. Imports are now written by package name; the next pass validates |
| issue-guided hunting on tingly-box (see `hunting.md` in [go2lean-tingly-box](https://github.com/fooexec/go2lean-tingly-box)): a hand-written postcondition on `ConvertAnthropicBetaToOpenAIRequest` is refuted and **confirmed by M3**: tool_use ids of 43 to 51 characters reach the assistant `tool_calls[].id` untruncated while the tool-result side is truncated to 40 (open issue #1924 describes a tool-call id mismatch between the two sides); three helper properties are certified. On the way: SDK unions generated one variant at a time, literals mined transitively, empty slices at depth zero |
| **fourth full pass** (7088 properties, go2lean `0375b2e`): **149 confirmed, 141 spurious, 31 inconclusive, 2794 certificates**; 69 of the confirmed carry `nil_opaque_input`; 26 of the inconclusive are tests a `log.Fatal` in an earlier test of the same batch left without a result (fixed after the pass: each such test reruns alone), 4 harness compile errors (two packages named `anthropic`, fixed: aliases) | 80 confirmed findings do not involve a nil opaque input; by kind: 26 index out of range, 14 slice bounds, 13 nil dereferences inside `protocoltest`, 5 writes to nil maps, 2 negative `strings.Repeat`, 3 interface conversions on nil, an `os.Exit` in `Daemonize`, two spec violations |
| **fifth full pass** (7095 properties, go2lean `d2118de`: unions one variant at a time, transitive literal pool, per-test reruns, import aliases): **140 confirmed (60 with `nil_opaque_input`), 152 spurious, 41 inconclusive, 2779 certificates**. Inconclusive: 26 harnesses redeclared a test because a run killed earlier had left its harness file in the package (fixed after the pass: the directory is cleared first), 13 targets ended the test process (`log.Fatal` in `internal/client` helpers), 2 single oddities | by panic kind: 72 nil dereferences, 38 index out of range, 12 slice bounds, 5 writes to nil maps, 3 interface conversions on nil, 2 negative `strings.Repeat`, 2 spec violations. The pass ran three hours; the container restarted minutes after it finished |
| next histogram: `range` over a map whose unused components have the invalid type (86), zero-value constants of struct type written as nil constants (60), function values (30), `[]T == nil` (16), `make(chan)` (20) | all handled (function values and channels are opaque; slices compare equal to nil exactly when nil; the error interface stays precise after a slip that briefly havoc'd `err != nil`). **6588 of 7498 (87.9%): 1988 complete, 4600 partial**; the 910 left are 319 `*testing.T` helpers, `select`, closures, `defer` of modelled functions |
| hand specs beyond issue #1924 found the model too narrow: `usage.FromOpenAIChatCompletion` returns `ai.NewTokenUsageFull(...)` (another package: havoc), the stream converter's `processEvent` appends chunks through `emitChunk` (a call without a result: ignored) to a `[]interface{}` queue (boxing: opaque), and one stuck callee made a whole run moot | **calls across packages**: a call into a sibling package of the same compile is an `xcall` naming the callee's model; the search driver links every model a search reaches into one program (FuncIds and havoc sites renumbered), an unlinked `xcall` is an unmodelled call. **Calls without a result** to modelled functions are followed. **Every call has a fallback block**: a stuck callee gives a havoc result on the pre-call heap instead of a moot run. **Interface values the program builds** are boxed with a tag for the dynamic type (a hash of the type name); an assertion to a concrete type unboxes it, a mismatch gives `(zero, false)` or the panic kind `type_assertion` (M3: "interface conversion"); **Nil-ness of func, map, channel and interface values**: nil literals, zero values and opaque inputs M3 passes as nil are `opaqueNil`; closures, named functions, `make(map)`, `make(chan)` and boxed values are non-nil; only results of unmodelled calls are unknown. `x == nil` is decided on known values, a type assertion on a nil interface panics, and interface-to-interface conversions keep their value. `examples/xpkg` and `examples/iface`; regression: 24 of 24 counterexamples confirmed, 40 certificates |
| the stream/non-stream and round-trip specs were refuted spuriously; probes showed where: the Anthropic -> Responses stream converter keeps its open blocks in maps (opaque, `range` ran zero times) | **probes** (`go2leanProbe(label, value)` in a spec: the model's values in the counterexample, the real run's in the verdict). **Maps the program builds** are modelled: `make(map)` is a heap list of key/value pairs behind `boxed 1`; lookup (comma-ok), update, `delete`, `len`, `range` in insertion order; keys compare by value, strings by bytes. A nil map reads as zero and panics on write (`nil_map_write`, M3: "assignment to entry in nil map"); maps the model knows nothing about keep the old havoc behaviour, chosen per value at run time. A havoc string read by an oracle makes its verdict unknown (havoc strings are always ""). `run -resume`; `agent -spec-file`. `examples/maps`; regression: 26 of 26 confirmed, 45 certificates |
| rerunning the Anthropic -> Responses stream specs after maps: every trial stuck at `uuid.New`, whose error path returns `uuid.Nil` (`var Nil UUID`, a package-level load: an assumed value); the full pass crawled because the exhaustive engine sized a recursive slice type (`[]T` inside `T`) forever before its budget check | **zero globals**: a package-level variable with no initializer and no use but a load anywhere in the program (every package with `-deps`, else the unexported ones of the compiled packages) loads as its zero value. `==` on arrays and structs of integers, bools and pointers compares element by element. The domain size stops at depth 0 for slices, as the enumeration does. `examples/globals`; regression: 27 of 27 confirmed, 47 certificates |
| after zero globals the stream specs still stopped at `ids.New`: ids come from `generate()`, a call through the package-level func variable `generate = randomHex` (written again only by `SetGeneratorForTest`); the request round trips ran into closures (`flushCalls := func() {...}` in Responses -> Chat, `slices.ContainsFunc(parts, func ...)` in Chat -> Responses); a Responses input that set two union variants at once (`OfString` and an empty `OfInputItemList`, no JSON request can) was confirmed as a round-trip loss | **closures and func values**: anonymous functions are modelled (captured values first, then parameters; never targets), `MakeClosure` and a function used as a value build `boxed 0 (tuple [fid, captured...])` (`funcRef`, `xfuncRef` across packages, linked by the driver), a call through a func value is `callv`: the function it names, a nil func panics, an unknown one takes the call's fallback. Generic instances of package `slices` are modelled under `-deps`; generic instances get no properties. **Init-set globals**: a package-level variable the initializer sets to a constant or a function and nothing else writes (writes from `...ForTest`/`...ForTesting` helpers aside) loads as that value. `uuid.New` is a fresh array distinct from every other in the run; `hex.EncodeToString` is modelled. **Unions**: every `Of<Variant>` field counts (not only pointers); the variants not chosen take their zero values. `examples/closures`; regression (`run -deps`): 30 of 30 confirmed, 56 certificates |
| the stream specs still stopped in `ids.New`: `randomHex` is reached only through the `generate` variable, which the dependency walk (static calls only) never followed; `param.Opt[string].Valid` was rejected (`o != Opt[string]{}` on a struct holding a string) | the dependency walk follows functions used as values, closure bodies and the function an init-set variable holds. `==` on arrays and structs holding strings compares strings by their bytes (`eqDeep`). The three Anthropic -> Responses stream specs (text, calls, status) now check 1786 trials each and hold (they were voided at `ids.New`). Regression (`run -deps`): 30 of 30 confirmed, 58 certificates |
| with the stream specs holding, the request round trips stayed spurious: `param.IsOmitted(v any)` asserts `v.(Optional)` (an interface) and calls `o.isZero()` on it, else `reflect.ValueOf(v).IsZero()`; the input generator gave the unexported fields of SDK structs (`param.Opt.status`) unknown values a test can never set; two confirmations were the SDK's deliberate repairs of orphan tool calls (an "aborted" result added, an unmatched output rewritten as a user message), and one a refusal the spec helper did not read as assistant text; generic instances made file names longer than 255 bytes | **interface dispatch**: the types modelled code boxes are collected; `x.(I)` for an interface `I` passes a boxed value whose tag is one of the boxed types implementing `I` (`assertIface`); a method call on an interface value dispatches on the tag to the method of that type (`dispatch`, then `callv`), a nil interface panics, an unknown value takes the fallback; the dependency walk adds the methods of boxed types whose names some call on an interface uses (at most 64 types per interface). `reflect.ValueOf` keeps the interface value and `IsZero` decides zero values. Unexported fields of foreign structs are generated as their zero value (`zero_model` in the ABI). Ids longer than 220 bytes are cut and hashed. The hand specs require matched tool calls and results (repairs are intended) and read refusals as assistant text. All six request round trips and the three Anthropic -> Responses stream specs now hold (887 to 1786 checked trials each). Regression (`run -deps`): 32 of 32 confirmed, 70 certificates |
| the sixth full pass confirmed 121 nil map writes in `internal/db` alone: stores passed as zero structs instead of being made by `NewXxxStore`; generic instances of stdlib and SDK packages were hunted under `-deps`; the agent failed with "matched 35 packages" under `-deps`; the string domain of the exhaustive engine was deduplicated in quadratic time over a pool of thousands of literals | **constructor-built receivers**: for a method of `T` whose package has `NewT`/`newT` returning `T` or `*T` (optionally with an error), the frontend synthesizes `T.M~NewT` (call the constructor, stop on its error, call the method on its result; a stuck call makes the run moot) with the constructor's parameters before the method's; the harness writes `go2leanRecv, err := NewT(...)` and a constructor error is a false precondition; the method's own no_panic property is replaced by the wrapper's. Dependency packages are modelled for their callers only (not targets; the agent picks the named package). Nil map writes on nil inputs are tagged `nil_opaque_input`. The string domain is deduplicated with a hash set. Regression (`run -deps`): 32 of 32 confirmed, 73 certificates |
| **sixth full pass** (7220 properties, go2lean `5c076f5` to `d720742`; details in go2lean-tingly-box `passes/`): **912 confirmed, 228 spurious, 23 inconclusive, 2618 certificates**. 741 of the confirmed come from nil inputs the model now follows (calls through nil funcs and interfaces, writes to nil maps of zero-value stores); without them 171 (fifth pass: 74). The disk filled during the pass (the Go build cache of the PoC builds reached 14 GB) and 71 PoCs did not compile (anonymous SDK JSON metadata structs) | the full-pass launcher clears the Go build cache above 8 GB; the harness leaves empty composite literals out (`b9137a7`); strings built by concatenation, Builder, Join and ReplaceAll are capped at 64 KB (a random search was killed at 9 GB, `4f4c0a1`); the next pass targets methods through constructors (`8be4a06`) |
| **seventh full pass** (7222 properties, go2lean `c9e765e`, constructor-built receivers): **795 confirmed, 299 spurious, 39 inconclusive, 2793 certificates**; nil-input confirmations 633 (sixth: 741), nil map writes 116 (201); 231 of the nil-input findings go through a constructor fed a third-party struct the generator fabricated field by field (`*gorm.DB` with nil internal maps); 48 spurious are constructor errors the model missed | next: generate pointers to third-party structs as opaque non-nil values rather than field by field |
| the seventh pass still confirmed 231 nil-input findings through constructors fed a third-party struct the generator built field by field (`*gorm.DB` with gorm's internal maps nil) | **handles**: a pointer to a struct of another package (outside the standard library) whose package exports a function returning a pointer to it (`gorm.Open`, `NewStore`) is generated as a non-nil pointer to an opaque value (the harness writes `new(T)`); any use of its contents makes the run moot, a callee working on it takes its fallback. `examples/xpkg` `Post` (a `*money.Ledger` from `NewLedger`) certified. Regression: 32 of 32 confirmed, 76 certificates |
| triaging the seventh pass: 31 confirmations were helpers declared in `_test.go` files; `update.sh` on the hand specs showed #1924 out of reach at `-maxlen 6` (ids over 40 characters) and the Anthropic -> Chat stream specs stuck at a `fmt.Sprintf` id | functions declared in `_test.go` files stay in the model but are not targets (`TestFile`). A spec can carry `go2lean:maxlen N`: its property searches inputs up to N long. **`fmt.Sprintf`** with plain `%s %d %v %t %%` on strings, integers and bools the program boxed is formatted (`sprintfOk` decides at run time; any other format or value takes the old havoc string). `examples/strs` `Short`/`Label`. Regression: 33 of 33 confirmed, 78 certificates |

## 9c. Benchmark: bugs found by hand (2026-10-20)

Six tingly-box bugs were found by reading code and checking bug classes of
similar gateways (go2lean-tingly-box findings 6-11). They are a benchmark
for the tool: for each, write the *general* property a reviewer would state
(no trigger input encoded), run go2lean blind, and when it misses, name the
gap and close it. A hit means go2lean found an input that breaks the
property and `go test` confirmed it.

| bug | property (spec) | result (go2lean `bac5535`) | what it took |
|---|---|---|---|
| Anthropic -> Chat request drops temperature, top_p, stop_sequences | `SpecConvertAnthropicBetaToOpenAIRequestKeepsSampling`: a parameter the client sets reaches the Chat request | found at trial 3, confirmed | nothing |
| tool_choice any/none become auto | `...KeepsToolChoice`: the mode keeps its meaning | trial 10, confirmed | nothing |
| Responses stream: arguments only in `.done` are lost | `SpecResponsesToChatConverterProcessEventArgumentsAnySchedule`: for **any legal event schedule** (deltas or `.done` only, chosen by a generated field), the client assembles the response's arguments | trial 13, confirmed | the spec: earlier stream specs replayed one fixed schedule (always deltas), which hid the bug; the schedule has to be an input of the property |
| Chat -> Anthropic stream: a block opens while another is open (found by go2lean) | `SpecOpenAIToAnthropicConverterProcessChunkOneOpenBlock`: for any sequence of deltas, one block is open at a time and deltas go to it | trial 24, confirmed | the harness wrote an empty map as nil |
| Chat -> Anthropic stream: `reasoning_content` after the answer opens a second block (DeepSeek's `null`) | `...OneOpenTextBlock`: the same for deltas without tool calls | trial 80, confirmed | the converter reads `delta.RawJSON()` and decodes it: map and interface inputs, `json.Unmarshal`, SDK raw JSON keyed by compared strings, documented enums; the harness decodes such values from one document (inline union variants); the spec cleared finish reasons and used a converter without a token counter (tiktoken out of reach) |
| Chat -> Anthropic non-stream: thinking is the raw JSON literal | (stream/non-stream agreement) | out of reach | the converter builds its result by `json.Marshal` of a struct and `json.Unmarshal` into an SDK type: encoding and decoding of structs is not modelled |
| non-stream usage counts cache reads twice | (handler-level) | out of reach | the bug sits inline in a gin handler that forwards over HTTP |
| env credentials sent upstream | (SDK client options) | out of reach | the process environment is read inside the SDK's request building |

All five reachable cases are confirmed (go2lean-tingly-box
`scripts/benchmark.sh`). The misses on the way were of three kinds: the
spec (a fixed event schedule; chunks skipped for a random finish reason),
go2lean (inputs it could not generate, calls it did not model, harness
renderings that changed the value), and reach (code the function-level
search does not get to). Regression after these changes (`run -deps
./examples/...`): **36 of 36 confirmed** (Weight, Label, Role new), no
spurious, **82 certificates** (78 before).

After the benchmark, **struct JSON encoding and decoding** (for the
non-streaming case): each package gets a table of JSON descriptions
(`JDesc`: strings with SDK constant defaults, ints, bools, floats,
`[]byte`, `RawMessage`, slices, arrays, pointers, maps, interfaces,
structs with tag options and SDK metadata, SDK unions, `param.Opt`) that
generic `jsonMarshal` / `jsonDecode` ops interpret. Checked against
`encoding/json` by a differential run (12 random structs encoded, 40
documents decoded: all byte-identical). `examples/jsonc`: `Reload` (an
omitempty 0 replaced by the default on reload) and `Quote` (`<` escaped as
`\u003c`) confirmed, `RoundTrip` holds. Random strings now also draw `"`,
`\`, `<`, `&` and tab, JSON texts from the pool are not mangled, and the
keys of decoded struct types join the key pool. The harness keeps empty
slice literals (an empty slice encodes as `[]`, a nil one as `null`).
Regression: 38 of 38 confirmed, 84 certificates.

**Round 2** (go2lean-tingly-box findings 13-20, eight more bugs found by
reading code). Each got a general spec before any search, checked to fail
on the hand-found trigger. Blind with go2lean `3191ef8`: 10 of the 11
reachable cases found and confirmed (stream usage after the finish chunk,
dense tool_call indexes, developer roles, abnormal stop reasons on two
paths, empty tool ids on two paths, three request-coverage cases); the
error-translation finding sits in the HTTP handlers. The one miss, and the
round-1 non-stream thinking case, got stuck in `json.Marshal` of an
`interface{}` holding SDK param unions: the frontend described each
pointer variant (`*BetaTextBlockParam`) as an opaque custom marshaler,
since a pointer type has its element's `MarshalJSON`. With pointers
described by their element, both are found (trials 4 and 160) and
confirmed: **17 of 17 reachable cases**.

The JSON model was then reviewed against Go's and the SDKs' sources, and
the deviations that gave wrong results (not just stuck ones) fixed:
`omitzero` and union variant presence follow the type (an omitted
`param.Opt[float64]` is zero; unknown zero-ness is stuck, not "non-zero");
`json.RawMessage` output is HTML-escaped; raw text (`RawMessage`, the SDKs'
`raw` and `respjson.Field`) is the input's own bytes, taken from a
byte-level scan instead of Lean's re-serialised `Json`; integers take Go's
literal syntax only; duplicate keys are stuck; SDK types decode by apijson's
rules (exact keys, null leaves a member unchanged, fresh pointers and maps,
extra members recorded as invalid, coercions and whole-value union variants
stuck). `testdata/jsondiff` with `scripts/json-diff.sh` checks the model
against Go on random structs and documents (80 of 80 identical).

Changes made for the misses (each with a ground-truth example, `examples/inputs`):

- **Map and interface inputs.** Maps with string, integer or bool keys are
  generated as maps the program built (`.boxed 1 p`), keys mostly from the
  string constants the call tree uses as map keys (`MapKeys`); an empty
  interface holds nil or a string, bool, int, `map[string]interface{}` or
  `[]interface{}` boxed with its type tag (the interface costs no depth;
  the exhaustive engine takes nil and the first eight values of each
  type). ABI kinds `map` and `any`; the harness writes map literals and
  typed interface declarations. Type tags are alias-insensitive (`any`
  and `interface{}` are one type at run time). `Weight` (a map entry 0)
  and `Label` (an interface holding `{"role": "system"}`) are confirmed.
- **`json.Unmarshal`** into a string, bool, `interface{}`,
  `map[string]interface{}` or `[]interface{}` (`jsonUnmarshal`, on
  `Lean.Json`): Go's rules for null, type errors and merging into a
  non-nil map; invalid UTF-8 becomes U+FFFD. When a call tree decodes
  JSON, JSON texts built from its literals join the string pool (random
  bytes are never valid JSON). `Role` (a JSON object with `"role":"tool"`)
  is confirmed; `Text` gets a certificate.
- **SDK JSON metadata.** The unexported `JSON.raw` of OpenAI/Anthropic SDK
  response types (behind `RawJSON()`) is generated as an empty text or an
  object of one or two members whose keys come from the map keys and the
  strings the call tree compares with `==` (`CmpStrs`), values null, a
  bool, 0, "" or a literal. The harness cannot set it, so it builds the
  value by a round trip: marshal the typed literal, merge the members,
  unmarshal into a fresh value. `JSON.ExtraFields` stays nil in the model
  (decoding derives it from the raw JSON; the metadata struct cannot be
  spelled in a test), so code reading both may give spurious results.
- **Documented enums.** The SDKs' enum-like fields are plain strings
  documented `// Any of "stop", "length", ...` (1567 fields in the two
  SDKs). The frontend reads that comment above a string field and gives
  its values to the generator as the field's enum (drawn three times in
  four), as it does for named string types. Before, `FinishReason` was a
  random string and a spec skipping finished chunks saw almost none.
- **Empty maps are not nil.** The harness left out empty composite
  literals as zero values; for a map that turned an empty map into a nil
  one and a write into a panic the model did not have (an inconclusive
  verdict for the first Chat -> Anthropic stream counterexample).

**Rounds 3 and 4** (findings 21-43, all found by reading code) brought
the benchmark to 34 specs. They moved the generator towards the values
real SDK traffic has:
- SDK response values are generated as the documents the harness decodes
  them from (`sdkDoc`: render with `jDoc`, decode with `jDec`), so typed
  fields, raw JSON and metadata agree.
- Union variants are weighted by the ones the call tree reads (`hot`),
  and documented enums likewise.
- Byte slices are drawn from the JSON texts of the pool.
- `apijson.UnmarshalRoot` is lowered to the JSON decoder.
- Logging calls are not followed.
- A counterexample that read havoc is kept only as a fallback while the
  search looks for one that did not.

Then two experiments without hand-found specs:
- **Blind specs.** A Claude subagent that had not seen the findings
  wrote 75 general specs. go2lean and `go test` confirmed 39
  counterexamples: 17 hit 13 known findings, 17 were new bugs (12 root
  causes, findings 44-56), and 5 came from wrong specs. Every spurious
  one named a model gap: base64, `RawMessage` rendering, a JSON round
  trip in a callee, havoc map lookups.
- **The M2 agent** with Claude Opus 5.5 confirmed 25 spec
  counterexamples on the request and non-stream packages: 12 hit known
  findings, 8 were new (findings 57-60), and 4 came from wrong specs.
  MiniMax-M3 had found 2 new bugs and none of the hand-found ones.

Gaps closed on the way:
- **Agent output.** The agent's output budget is 64000 tokens; a spec
  block cut off at the end keeps its complete declarations.
- **apijson's loose decoding.** A member of the wrong JSON type is not
  an error: strings take bools and integer literals stringified, and
  objects and arrays as raw text (invalid). Bools take gjson's `Bool()`.
  A slice member that is not an array leaves the field as it was. Other
  mismatches stay stuck. Inline union variants decode strictly, where a
  coerced member is invalid.
- **SDK documents that agree with the harness.** `jDoc` keeps the later
  of two typed members with one key, as the harness's map does (an inline
  variant's `type` after the union's own). It writes the basic types an
  `any` holds after decoding, and writes a float64 (whose value the model
  does not know) as null, as the harness does. Before, normalisation
  failed about 29000 times in one hunt and kept a value the real run
  never saw; now it fails 82 times.
- **Lookup tables.** A package-level map that the initializer fills with
  constants, and that every load only reads, is rebuilt from those
  constants at each load. Its keys join the key pool. Before, every
  lookup was havoc (`thinking.BudgetMapping` tainted every trial of the
  Chat -> Anthropic request converter). `examples/tables`.
- **Standard library.** `base64` Encode/DecodeString of the four
  standard encodings; `fmt.Sprintf` `%x` of strings and byte slices.
- **Diagnostics.** A dialect can describe where it got stuck: a stuck JSON
  decode names the text, which pointed at each of the decoding gaps above.

Regression with all of these: 40 of 40 confirmed (`Share` new), 85
certificates; json-diff 108 of 108 identical. With these, **31 of 34**
benchmark cases are confirmed (go2lean
`8fe99b3` with the document fixes). The three open cases:
- KeepsTools: the lookup table above, since fixed.
- The non-stream reasoning summary: stuck in `json.Marshal`.
- The Gemini schema filter: its search output (a map result printed as
  an object) could not be read back; fixed.

