# Go semantics in go2lean

What part of Go the `gossa` frontend and the `GoSSA` Lean dialect model,
and how faithfully. The layout follows how lean-mlir documents a dialect:
the value domain first, then which operations touch state, how failure is
represented, and the operations by group, each with its status. Section 9
maps lean-mlir's concepts to go2lean's.

Snapshot of go2lean `7f9e10b` (2026-10-20). `docs/design.md` §4.3–4.3b
gives the design, [history.md](history.md) §9b–9c the history of each change.

## Status legend

| status | meaning | effect on results |
|---|---|---|
| **exact** | the model computes what Go computes | counterexamples and certificates are sound for it |
| **havoc** | the result is an unconstrained value of the right type, drawn by the engines (over-approximation) | can produce counterexamples Go does not have; the replay stage (`go test`) rejects them as spurious |
| **assumption** | the model fixes one behaviour among Go's possible ones and records it in the run (`assume`; under-approximation) | can hide a bug; a spec verdict that read an assumed value is not counted as decided, and an exhaustive pass through one is `exhausted_assuming` with the list |
| **stuck** | the model has no value to give (`modelStuck`); the trial is moot | lowers coverage, never produces a wrong verdict |
| **rejected** | the function is not modelled (listed in the model report with the reason) | no properties for it; callers see an unmodelled call (havoc) |

## 1. Value domain

A Go value is one `GoSSA.Val`; the model computes on these, never on Go
memory images.

| Go type | `Val` | status |
|---|---|---|
| `int`, `int8`…`int64`, `uint`…`uint64`, `uintptr`, `byte`, `rune` | `.int w (BitVec w)` | exact (bit-precise, every width) |
| `bool` | `.bool` | exact |
| `string` | `.str ref off len`: a window on an immutable byte array in the heap | exact |
| `error` | `.err nonNil` | nil-ness exact; the message is not modelled; two non-nil errors compare equal (havoc-like) |
| pointer | `.ptr ref path` (heap object + field/element path; `ref 0` is nil) | exact |
| struct | `.structv fields` | exact |
| array `[N]T` | `.arr elems` (a value; `N > 64` is opaque in the ABI) | exact |
| slice | `.slice ref off len cap` over a heap `.arr` | exact |
| map | `.boxed 1 p`, `p` → heap `.arr` of `(key, value)` in insertion order; nil is `.opaqueNil` | exact for maps the program built or the generator made; see §5 |
| interface (incl. `any`) | `.boxed tag v` (`tag` = hash of the dynamic type's name), nil is `.opaqueNil` | exact for values the program boxed or the generator made; `.unit` (unknown) for others |
| func | `.boxed 0 (tuple [fid, captured…])`, nil `.opaqueNil` | exact for functions and closures of the program |
| tuple (multiple results) | `.tuple vs` | exact |
| `float32/64`, `complex64/128` | opaque (`.unit`) | havoc: arithmetic, comparisons and conversions |
| `chan T` | `.ptr r []` to a heap object `.structv [cap, .arr buffer, .bool closed, zero]`; nil is `.ptr 0 []` or `.opaqueNil` | exact (§6) |
| `unsafe.Pointer` | opaque | havoc |
| `unsafe.String(unsafe.SliceData(b), len(b))`, `unsafe.Slice(unsafe.StringData(s), len(s))` (zero-copy views, prometheus' `yoloString`) | `string(b)`, `[]byte(s)` | exact: package `unsafe` forbids writing the bytes while the view is in use (#87) |

Integers are bitvectors of the Go width, not mathematical integers: the
`int` of a 64-bit target is 64 bits, overflow wraps.

## 2. Effects

As lean-mlir splits ops into pure and impure (`EffectKind`), go2lean's
dialect has `denotePure : Op → List Val → Res Val` for ops that only read
their operands and `denote : Oracle → Heap → Op → List Val → Res (Val ×
Heap)` for those that read or write the heap (allocation, loads, stores,
strings, maps, slices). The heap is threaded by the interpreter (memory
SSA, lean-mlir's "pure form" of memory ops); objects are never freed
(Go's GC makes that sound).

## 3. Failure

Go has no undefined behaviour in the fragment modelled; where lean-mlir
yields UB or poison, Go panics. A run ends in one of:

| outcome | Go | kinds |
|---|---|---|
| `ok v` | returned | — |
| `panic k` | runtime panic or `panic(...)` | `index_out_of_range`, `slice_bounds`, `nil_dereference`, `divide_by_zero`, `negative_shift`, `make_slice_len`, `type_assertion`, `nil_map_write`, `explicit` (a `panic` call), `closed_channel` (send on or close of a closed channel, close of nil), `sync_fatal` (unlock of an unlocked mutex: a fatal error in Go), `deadlock`, `goroutine_leak`, `data_race` (§6), `other` |
| `panic modelStuck` | — | the model cannot continue (stuck); not a Go panic, never reported as one |
| `outOfFuel` | — | the step budget ran out (loops, recursion), a slice would exceed the model's size bound (`sliceBound`, 16384 elements), or a run would start more than 128 goroutines (`goroutineBound`); moot |

`recover` is not modelled: a panic ends the run.

**Loops that never end (#86).** The engines rerun up to 20 of a
property's out-of-fuel runs in a copy of the sequential interpreter
(`Go2Lean/Hang.lean`) that watches the state at each back edge: the
block, the environment, the heap and the havoc counters. A run that
comes back to a state it was in loops forever on that input, since the
run from there is deterministic. Brent's cycle detection keeps one saved
state per call, so a back edge costs one comparison. The hunt summary
counts these runs and names the loop (`@Words#1`). A run that had taken
an under-approximation by then is marked `(under assumptions)`, since
the loop may be the model's.

Every function with a loop of its own also gets a `no_hang` property. A
run that comes back to a state violates it: the input and the loop
(`panic_at`) become a counterexample. The replay runs the call once
under a 2s watchdog. The finding is confirmed if the call does not
return, and spurious if it does (`returned_in_real_run`). A domain the
exhaustive engine covers with no run out of fuel is a certificate that
every input in it ends.

Some hangs are not caught:
- **Changing state.** A loop that changes a counter it never reads
  never repeats its state.
- **Long cycles.** A cycle longer than about half the fuel closes too
  late.

A concurrent run draws a schedule choice at every step. Its state is
therefore compared without the schedule's counter, and without the race
detector's clocks, which only grow: the goroutines' frames, the heap and
the other havoc counters. A state that comes back means the choices made
in between can be made again forever. It is reported as a livelock
(`@Relay#3 (livelock: 2 goroutines cycle)`) only if three things hold:
- **the schedule is fair:** every goroutine that could move during the
  cycle moved in it;
- **the call has not returned;**
- **the cycle spans scheduling points:** a goroutine spinning inside one
  step (no channel, lock or other scheduling point) ends out of fuel in
  that step and is not checked.

A spin-wait for another goroutine (`for !ready {}` under a lock) under a
schedule that never runs that goroutine is unfair and is not reported.

The hunt summary also says how many out-of-fuel runs ran out inside one
goroutine step, and where the most common such step started. A step is
a goroutine's code between two scheduling points. Example:
`100 of them inside one goroutine step (most from
@SeqConcOOOCompactionHead#8)`. Such a run either spins without
synchronising or does more sequential work than the fuel allows, such as
an expensive setup.

`examples/hang` holds two such bugs, a scan that stops advancing on a
tab and a chain of links with a cycle. Both are found and confirmed in
Go. It also holds a long loop that does end, which gives no
counterexample. Two concurrent cases are there too. `Relay` is two
goroutines passing a value back and forth while the caller waits, and is
reported as a livelock. `Handoff` is a spin-wait that only loops under an
unfair schedule; checked under a schedule that always runs the first
goroutine that can move, it is not reported.

## 4. Operations by group

### 4.1 Integers and booleans

| Go | model | status |
|---|---|---|
| `+ - * & \| ^ &^` | `add sub mul and or xor andNot` | exact, wrapping |
| `/ %` | `sdiv udiv srem urem` | exact; zero divisor panics; `MinInt / -1` wraps |
| `<< >>` | `shl shr` (signed/unsigned count) | exact; count ≥ width gives 0 or the sign fill; negative signed count panics |
| `== != < <= > >=` | `eq ne slt sle … ult ule …` | exact (signedness from the type) |
| unary `- ^ !` | `neg bnot not` | exact |
| integer conversions | `conv w signExtend` | exact |
| constants | `const` (mined into the engines' value pools) | exact |

### 4.2 Strings

| Go | model | status |
|---|---|---|
| literal, `len(s)`, `s[i]`, `s[lo:hi]`, `s + t`, `s == t`, `s != t`, `s < t` (and `<=`, `>`, `>=`: byte-wise) | `strConst strLen strIndex strSlice strConcat eq ne ult ule ugt uge` | exact (bounds panics as Go) |
| `for i, r := range s` | `strNext` (UTF-8 decoding, invalid bytes → U+FFFD) | exact |
| `for i := range n` (integer) | an ordinary loop | exact |
| `[]byte(s)`, `string(b)`, `[]rune(s)`, `string(rs)`, `string(r)` | `strToBytes bytesToStr strToRunes runesToStr runeToStr` | exact |
| `s[lo:hi:max]` | — | rejected (Go forbids it too) |

### 4.3 Pointers, structs, arrays, slices

| Go | model | status |
|---|---|---|
| `new(T)`, `&T{…}`, escaping locals | `alloc zero` | exact |
| `*p`, `*p = v`, `&x.f`, `&xs[i]`, `x.f`, `a[i]` | `loadPath storePath fieldAddr indexAddr field arrIndex` | exact; nil dereference panics |
| `xs[i]`, `xs[lo:hi]`, `xs[lo:hi:max]` | `indexCheck load reslice reslice3` | exact, bounds panics |
| `make([]T, n, c)` | `makeSlice` | exact; a negative length, or one above the capacity, panics; above 16384 elements: out of fuel |
| `append` | `appendSlice` | exact while capacity allows; growth doubles the capacity (Go's growth is implementation-defined); growth above 16384 elements: out of fuel |
| `copy` | `copySlice` | exact |
| `min`, `max` on integers | a comparison and `ite` per argument | exact (on strings and floats: havoc) |
| `len`, `cap` of slices, arrays, pointers to arrays | `len cap` | exact |
| `==` on arrays and structs | `eq` / `eqDeep` (strings compared by bytes) | exact for comparable fields; floats inside are havoc |
| struct/array literals | `mkStruct`, `.arr` | exact |

### 4.4 Maps

| Go | model | status |
|---|---|---|
| `make(map…)`, `m[k] = v`, `m[k]`, `v, ok := m[k]`, `delete`, `len` | `makeMap mapUpdate mapLookup mapDelete mapLen` | exact on known maps; nil map write panics, nil read gives zero |
| `for k, v := range m` | `mapRange mapNext` | assumption: insertion order (Go randomises; order-dependent bugs can be missed) |
| a map the model knows nothing about (result of an unmodelled call) | — | lookups havoc, updates ignored (assumption), `range` runs zero times (assumption), `len` havoc below 16 (assumption) |
| keys | string, integer, bool, and comparable values | exact (`keyEq`) |

Whether a map (or an interface or func value) is known is decided at run
time, per value. The model report lists the unknown case as an issue only
where it can happen: not for values known by construction (made or boxed
in the function, inputs, which the search generates concretely, and what
the function reads out of those without writing anything unknown into
memory).

### 4.5 Interfaces and type switches

| Go | model | status |
|---|---|---|
| storing a value in an interface | `box tag` | exact |
| `x.(T)` for a concrete `T`, comma-ok and panicking | `unbox` | exact on boxed values; havoc on unknown ones |
| `x.(I)` for an interface `I` | `assertIface` (the boxed types implementing `I`) | exact on boxed values |
| method call on an interface value | `dispatch` over the implementing boxed types (at most 64; for `*T`, a method declared on `T` through a helper that loads the receiver, #61), then `callv` | exact; a nil interface panics; an unknown value takes the call's fallback (havoc) |
| `==`, `!=` on interface values | `ifaceEqKnown`, then `ifaceEq` on the dynamic types' tags and values (a type alias has the tag of the type it stands for) | exact when both are values the model built, or nil; havoc when either is not (#60) |
| `reflect.ValueOf(x)`, `.IsZero()` | `reflectOf reflectIsZero` | exact on boxed values; other `reflect` calls havoc |

### 4.6 Functions and calls

| Go | model | status |
|---|---|---|
| static call in the package | `call` | exact |
| call into another package compiled together (`-deps`) | `xcall` (the driver links the models) | exact; a dependency's functions are translated up to 4 calls deep from the package under test, except its small helpers (a few blocks: `xxhash`'s `round`, `rol1`), which come with their caller at any depth (#71); deeper ones on request of the refinement loop (#85) |
| closures, function values, method values | `funcRef`/`xfuncRef` + `callv` (captured values first) | exact; nil func call panics; an unknown func takes the fallback |
| generic functions | modelled per instance (`Must[int]`, concrete types); the generic function itself is not in the model; instances of package `slices` under `-deps` | partial |
| call into a logging package (logrus, log, slog, zap) | results opaque, calls without results ignored | exact for the code under test |
| `apijson.UnmarshalRoot` (the SDKs' `UnmarshalJSON`, unions' `As*` methods) | `jsonDecode` with the target type's description | as JSON decoding above |
| call to an unmodelled function with a result | `havocInto`: scalars havoc; structs havoc field by field (an array field of up to 16 scalars element by element; another field the model cannot vary is its zero value, recorded as an assumption, #68); pointers to a fresh struct of havoc fields; slices of 0–3 zero elements (assumption); interfaces/maps/funcs unknown | havoc / assumption |
| an unmodelled call with a documented contract (#85) | the havoc result kept within it: `Read`/`Write` counts 0..len(p) (`io.Reader`, `io.Writer`); a `hash.Hash`'s `Write`/`WriteString` takes all of p and returns nil; `Len()`/`Cap()` 0..16384; `runtime.Callers` 0..len(pc); `rand.Perm(n)` the identity permutation (assumption) | havoc within the contract |
| call to an unmodelled function without a result | ignored | assumption (its effects are lost) |
| a modelled callee that gets stuck | the call's fallback binds a havoc result | havoc |
| recursion, loops | fuel-bounded | exact up to fuel (`outOfFuel` beyond) |
| `defer f()` of an unmodelled function | ignored | assumption |
| `defer f(args)` of a modelled function | the arguments are evaluated at the `defer` statement; at each return the calls whose `defer` statement ran are made, last first (go/ssa's `RunDefers`; a panic in one ends the run with it). In a function with a `defer` in a loop, every defer pushes its call onto a defer stack of the frame, which each return pops (#62; checked by `testdata/goosesem/go2lean_defer.go`) | exact for returns; on the way out of a panic no deferred call is made (the run ends with the panic, as a panic does here anyway) |
| `panic(v)` | `panic explicit` | exact |
| `recover()` | havoc | unreachable in practice (a panic ends the run) |

### 4.7 Package-level variables

| Go | model | status |
|---|---|---|
| a variable nothing writes after init, initialised to a constant or a function | its value | exact (writes from `…ForTest` helpers ignored) |
| an array its initializer sets to a literal of constants, arrays and struct literals of constants (`var table = [256]encoding{...}`, a stringer's `_X_index`, utf8's `acceptRanges`), or fills element by element with constants, and nothing else writes; read whole, by element or by field of an element | the constant array; `table[i]` indexes it (out of range panics, as in Go), `table[i].f` reads the field | exact |
| a slice its initializer sets to a slice literal of such constants, or to an `append` of such slices (`var inTermsBreakers = append(inTextBreakers, '}', ',')`), and no code writes an element of | a fresh slice holding the constants at each load | exact while nothing writes it; a callee that writes into the slice it was passed is not seen by the next load: assumption |
| a variable initialised to a constant expression over constants and such variables (`var mb = kb * 1024`) | its value, folded at compile time | exact (an expression that would wrap or divide by zero is not folded: havoc) |
| a zero-valued variable no function writes | the zero value | exact |
| any other load | havoc (a confirmed panic whose run read one carries the verdict note `reads_unknown_state`, naming it: the program may initialise it first) | havoc |
| stores to globals | ignored | assumption |
| a package variable its initializer sets to a call with constant arguments whose result is a plain value (numbers, strings, bools, and arrays and structs of them: `var V0_11_0_0 = newKafkaVersion(0, 11, 0, 0)`), or an alias of one (`var MinVersion = V0_8_2_0`), and nothing else writes | the call again at each load (a plain value has no identity to keep) | exact as the call is (#55) |
| `init` functions | not run as code; a store an `init` function makes on every path through it (its block dominates every return), to a variable no initializer sets and nothing else writes, counts as that variable's initializer (a constant, a function, a constant `regexp.MustCompile`, a table of constants or functions) | exact for those stores; any other effect of an `init` function: assumption |

### 4.8 Conversions

| Go | model | status |
|---|---|---|
| integer ↔ integer | `conv` | exact |
| named ↔ underlying type, `json.RawMessage` ↔ `[]byte` | no-op | exact |
| string ↔ bytes / runes | §4.2 | exact |
| integer ↔ float, float ↔ float | havoc | havoc |
| others | — | rejected |

## 5. Standard library and SDK models

Calls not listed here are unmodelled (§4.6).

| function | status |
|---|---|
| `strings.HasPrefix HasSuffix Contains Index IndexByte LastIndex Count TrimPrefix TrimSuffix Trim TrimLeft TrimRight ReplaceAll Split Fields Join Repeat` | exact |
| `strings.TrimSpace ToLower ToUpper EqualFold` | exact for ASCII; Unicode spaces and case folding are not modelled |
| `strings.Builder` (`WriteString Write WriteByte WriteRune String Len Reset`) | exact (a string cell); `Grow` (also `bytes.Buffer`'s) panics for a negative argument and is otherwise a no-op |
| `strconv.Atoi` | exact for decimal `int64` (sign, range error) |
| `strconv.Itoa` | exact |
| `sync.Pool.Get` on a package variable whose initializer sets `New` (`var ppFree = sync.Pool{New: func() any { return new(pp) }}`); `Put` | `Get` calls `New`, which Go may do at any `Get` (an empty pool); `Put` is a no-op. An object `Put` back is never returned: assumption (#105). Another pool's `Get`: unmodelled |
| `sync/atomic` `Bool Int32 Int64 Uint32 Uint64`: `Load Store Add Swap CompareAndSwap And Or` (#67); the functions `Load Store Add Swap CompareAndSwap` on `*int32 *int64 *uint32 *uint64` | exact: plain reads and writes (a goroutine's instructions between two scheduling points run atomically, §6); `Uintptr`, `Pointer[T]`, `Value`: havoc |
| `strconv.ParseInt ParseUint` | exact: bases 0 (prefixes `0b 0o 0x 0`, underscores) and 2–36, bit sizes 0–64, syntax, range (the bound, as soon as the value overflows), base and bit size errors; checked against Go by `PKG=testdata/convdiff scripts/json-diff.sh` |
| `strconv.ParseBool FormatInt FormatUint` | exact (an illegal base panics, as in Go) |
| `math/rand` and `math/rand/v2` `Intn Int31n Int63n IntN Int32N Int64N UintN Uint32N Uint64N` (functions and `*Rand` methods) | any value in `[0, n)` (havoc); Go's panic for `n <= 0` |
| `regexp.MustCompile` of a constant pattern (a constant expression, or `fmt.Sprintf` of a constant format and constant string, integer or bool arguments, folded at compile time); `MatchString FindString FindStringIndex FindStringSubmatch FindAllString FindAllStringSubmatch FindAllStringIndex` on it (the FindAll methods: Go's `allMatches` loop over the same search, started at each position; an empty match where the previous one ended is skipped), `regexp.MatchString` with a constant pattern | exact: the frontend compiles the pattern with `regexp/syntax` (Perl syntax, simplified, as `regexp.Compile` does) and the model runs Go's backtracking algorithm on that program (leftmost-first, each (instruction, position) tried once, so linear; empty-width assertions, case folding, UTF-8); `MustCompile` of a valid pattern does not panic. A regexp passed around (a helper's parameter) is one of the package's constant patterns, picked at run time (at most 12; one compiled from a pattern built at run time: havoc). Strings each pattern matches (its shortest match, and one taking every optional part once) join the search's string pool. Checked against Go by `PKG=testdata/rediff scripts/json-diff.sh`. Other methods (`ReplaceAllString`, `Split`, the `[]byte` ones): unmodelled |
| `fmt.Sprintf` with `%s %d %v %t %%` on strings, integers and bools, no flags or widths; `fmt.Fprintf` into a `*strings.Builder` or a `*bytes.Buffer` (the same text, written with the writer's `WriteString`) | exact (an integer of an unsigned type prints unsigned: the frontend passes the type tags of the unsigned types the program boxes); any other format or argument: havoc string; `Fprintf` to another writer: unmodelled. `fmt` itself is not translated (it formats through `reflect`) |
| `fmt.Errorf`, `errors.New` | a non-nil error (message not modelled) |
| sentinel errors: a package variable initialised (or set by an `init` function on every path) to `errors.New` of a constant or `fmt.Errorf` of a constant format and constant arguments without `%w` (`io.EOF`, `context.Canceled`, `ErrNotFound`), assumed not reassigned | a value of its own (`*errors.errorString` boxed with the variable's identity); `err == S`, `err != S` and `errors.Is(err, S)` exact when `err` is nil or a sentinel (for `==` also a value of an error type), havoc when it is an error the standard library made or the model knows nothing about (it may be, or wrap, `S`) |
| `errors.Is(err, target)`, `errors.As(err, &v)`, `errors.Unwrap(err)` | exact on the errors the model built (#69): the walk along `Unwrap() error` as Go's, `==` when the target's type is comparable, `Is` methods, `As`'s match on the dynamic type (or interface) and its store; checked by `testdata/goosesem/go2lean_errors.go`. An error the standard library made or the model knows nothing about met on the way: havoc (`As`: a havoc value of the target's type when it holds). An `As` method or an `Unwrap() []error` (`errors.Join`) on the way: havoc for `As`, havoc for `Is` at `Unwrap() []error` |
| `encoding/json.Unmarshal` into `*string`, `*bool`, `*interface{}`, `*map[string]interface{}`, `*[]interface{}` | exact (Go's rules for null, type errors, merging into a non-nil map; numbers are opaque floats; invalid UTF-8 → U+FFFD) |
| JSON numbers decoded into `float64` or an interface | the bytes `json.Marshal` writes for them when the model knows them: a number of at most 15 significant digits with 1e-6 <= abs < 1e21 (Go then writes it in plain decimal, without trailing zeros), and 0 when the document has no `-0`; any other float is unknown, and encoding it gets stuck. Checked against Go by `PKG=testdata/numdiff scripts/json-diff.sh` |
| `encoding/json.Valid` | exact (the JSON parser the decoding model uses; checked against Go by `PKG=testdata/jsonvalid scripts/json-diff.sh`, 60 same, 0 different over a corpus of edge cases and raw bytes) |
| `encoding/json.Marshal` | exact for strings (Go's HTML-safe escaping, invalid UTF-8 → `\ufffd`), bools, integers, `[]byte` (base64), `json.RawMessage` (compacted and HTML-escaped, as Go does to a Marshaler's output), slices, arrays, pointers, maps with string or integer keys (sorted), interfaces holding a type the package boxes, structs (json tags, `omitempty`, `omitzero` by the type's zero value, `-`, embedded and `,inline` fields); SDK param objects (`default:` constants, `param.Opt`: an omitted Opt is zero, also when its value is a float the model does not know), SDK param unions (one present variant by `param.IsOmitted`; none → null, several → error); floats, `omitzero` on a value of unknown zero-ness, SDK metadata that is set (`SetExtraFields`, overrides, typed `ExtraFields`), `,string` fields, non-empty interfaces, `MarshalText` / `json.Number` and other custom marshalers: stuck. Unexported embedded structs promote their exported fields; `\b` and `\f` are escaped as such (Go 1.22) |
| `encoding/json.Unmarshal` into structs and the types above | exact for the same types (exact key then case-insensitive match, unknown keys ignored, type errors reported while decoding goes on, maps merged, nil pointers allocated); `json.RawMessage` and all raw text are the input's own bytes (a byte-level scan gives each value's source); integers take Go's literal syntax only (`1e2`, `1.0` are errors); duplicate keys: stuck. SDK types decode by the SDKs' apijson rules: exact keys only, null leaves a member unchanged (an Opt becomes null), pointers and maps decoded afresh, metadata with raw text, per-member `respjson.Field` (null or valid) and `ExtraFields` (invalid unless null, as apijson has no decoder for them); a member that does not decode (apijson would coerce or mark it invalid), an SDK struct from a non-object, and union variants decoded from the whole value, members no field reads in an SDK type with metadata or typed `ExtraFields`: stuck |
| `encoding/hex.EncodeToString` | exact |
| `reflect.ValueOf`, `(reflect.Value).IsZero` | §4.5 |
| `(reflect.Value).Kind`, `IsNil` | exact for the types modelled code boxes and the ones the search and the JSON model put in an `any` (the frontend lists each tag's kind); `IsNil` panics for kinds that cannot be nil, as Go does; any other type: stuck. Checked by `PKG=testdata/miscdiff` |
| `unicode/utf16.Encode` | exact (surrogate pairs; invalid runes and surrogates become U+FFFD); checked by `PKG=testdata/miscdiff` |
| `strings.Cut`, `strings.LastIndexByte` | exact |
| `strings.NewReplacer` with literal pairs, `Replace` | exact (first matching old string, left to right) |
| `strconv.ParseFloat(s, 64)` | exact for decimal syntax away from the float64 limit (value as JSON encodes it, syntax and range errors); infinities, NaN and hex floats havoc; checked by `scripts/json-diff.sh` |
| `json.NewEncoder(&b)` on a `strings.Builder`, `SetEscapeHTML` with a constant before every `Encode`, `Encode` | exact (`Marshal`, without HTML escaping when the encoder says so, and a newline); checked by `scripts/json-diff.sh` |
| `strings`, `bytes`, `strconv`, `unicode/utf8`, `unicode/utf16`, `encoding/binary`, `container/list`, `bufio`, `io` | translated from their Go source under `-deps` (as `net/url`; `container/list` and `bufio` checked by `testdata/goosesem/go2lean_stdlib.go`, #69, #71; `io.ReadFull`, `ReadAtLeast` and `ReadAll` over the program's readers and `strings.Reader` by `go2lean_io.go`, #69, #47; for `encoding/binary`, the functions a package calls, such as `Uvarint`, `Varint` and `PutUvarint`, checked by `testdata/stdaudit`), except the functions with a model of their own in the rows above, which keep it; the assembly primitives of `internal/bytealg` (`IndexByte`, `IndexByteString`, `Index`, `IndexString`, `Count`, `CountString`) are leaf operations of the model (the rest of `internal/bytealg`, `internal/stringslite`, `internal/strconv`, `math/bits` and `unicode` are translated too; `bytealg.MaxLen` is 63, an AVX2 machine's value, which picks an algorithm, not a result), `unsafe` conversions inside them are havoc, `defer`/`recover` (`bytes.growSlice`) an assumption. The hand-written models of `strings` and `strconv` functions and the translation of their source are both checked against Go by `PKG=testdata/stdaudit DEPS=1 scripts/json-diff.sh` (the second with `GO2LEAN_NO_STDLIB_OPS=strings,strconv`; with `unicode/utf8` decoding, 444 same, 0 different); the hand-written ones stay, because the translation alone leaves `strings.Builder` (`unsafe`) and `strconv.Atoi`'s callers with havoc (the regression gate: 3 spurious, 4 certificates weakened) |
| pointer sentinels: a package variable its initializer sets to `&T{...}` of constants, array fields included (sjson's `errNoChange`; grpc's `bdpPing`, `&ping{data: [8]byte{...}}`, #71) | boxed as an error it compares by identity like `errors.New` sentinels; its fields read as the constants |
| `net/url` | translated from its Go source under `-deps` (the standard-library packages listed in `modelledStdlib`), like a dependency: `url.Parse`, `Hostname`, `Port`, `Query` and the rest are the library's own code. Not modelled inside it: IPv6 literals (`netip.ParseAddr`) and the `urlstrictcolons` GODEBUG setting (a host with two colons), both havoc. Checked by `DEPS=1 PKG=testdata/urldiff` (90 same, 0 different) |
| `github.com/google/uuid.New` | a fresh value distinct from every other in the run (real UUIDs collide with negligible probability) |
| `time.Now` and other clocks, `os.Getenv`, I/O, network | havoc |

On native builds whose `bytealg.CompareString` calls the bodyless
`internal/bytealg.abigen_runtime_cmpstring` assembly bridge, that bridge is
lowered to the existing unsigned byte-string comparisons and conditional
selection of -1, 0 or 1. It does not decode Unicode. This narrow lowering does
not cover arbitrary assembly or generic `cmp.Compare`. Run
`DEPS=1 PKG=testdata/stringcmpdiff scripts/json-diff.sh <workspace> 6`
for generated-pair and fixed byte-edge matrix comparisons against real Go.

## 6. Concurrency

A function that can reach a synchronisation operation (directly, or through
a call, a closure or a deferred call) runs in a small-step machine
(`Go2Lean.GoConc`, design in [concurrency.md](concurrency.md)); the others
keep the big-step interpreter. A goroutine runs from one scheduling point
(a `go` statement or a synchronisation operation) to the next in one step;
which goroutine moves next is the next value of a havoc site (`schedule`),
so the engines vary schedules as they vary inputs and a counterexample
records its schedule.

| Go | model |
|---|---|
| `go f(x)`, `go func(){...}()`, `go` of a method | a new goroutine; arguments evaluated by the spawning goroutine |
| `make(chan T, n)` | a channel object (capacity, buffer, closed flag, zero value of `T`) |
| `ch <- v`, `<-ch`, `v, ok := <-ch`, `for v := range ch` | exact: unbuffered is a rendezvous, buffered blocks when full / empty, a receive on a closed channel gets the zero value and `ok == false` once the buffer is drained, a nil channel blocks forever, a send on a closed channel panics |
| `close(ch)`, `defer close(ch)` | exact: a closed or nil channel panics; blocked receivers wake, blocked senders panic |
| `len(ch)`, `cap(ch)` | exact |
| `select` (send and receive cases, with or without `default`) | any enabled case, chosen by the schedule; blocks when none is enabled and there is no `default` |
| `sync.Mutex` `Lock Unlock TryLock` | a lock word in place of the field; `Unlock` of an unlocked mutex: `sync_fatal` |
| `sync.RWMutex` `Lock Unlock RLock RUnlock` | readers count and writer flag; writer preference: a blocked `Lock` stops new `RLock`s, as Go does (read–write–read deadlock) |
| `sync.WaitGroup` `Add Done Wait` | a counter; negative: `explicit` (Go's `sync: negative WaitGroup counter` panic); `Wait` blocks until zero |
| `sync.Once` `Do` | not started / running / done; a `Do` while another runs blocks; `f` runs once |
| `time.Sleep`, `runtime.Gosched` | a scheduling point; time is not modelled |
| deferred `Unlock`, `RUnlock`, `Done`, `close`, and calls through a func value (`defer cancel()`) | run at the function's returns, as other deferred calls |
| `go x.M()` on an interface value, `go close(ch)` | the method of the dynamic type (as a call through the interface); a goroutine closing `ch` |
| `context.Background`, `TODO` | a context that never ends (`Done()` is a nil channel) |
| `context.WithCancel`, `WithTimeout`, `WithDeadline` | a child context: its own done channel, registered with the parent (cancelling the parent cancels it, a cancelled parent makes it cancelled); the cancel func closes the done channel; for a timeout or deadline a goroutine cancels it at any scheduling point, as the deadline may pass at any time |
| `ctx.Done()`, `ctx.Err()` | the done channel; nil, or a non-nil error once cancelled (which of `Canceled` and `DeadlineExceeded` is not modelled); a context the model did not make (an input) never ends, as `context.Background()` the harness passes |
| `time.After`, `time.NewTimer` | a channel of one buffered value and a goroutine that sends on it at any scheduling point, without blocking when the buffer is full; `Timer.Reset` sends once more, `Timer.Stop` returns any bool and stops nothing (its late send cannot block anyone) |
| `time.NewTicker` | a daemon goroutine sending forever: a run ends when only daemons can move and two steps each wake nobody; `Stop` changes nothing |
| `time.AfterFunc(d, f)` | a goroutine running `f` |
| `sync.NewCond`, `Cond.Wait`, `Signal`, `Broadcast` | a ticket queue in the cond's `notify` field: `Wait` takes a ticket, unlocks `L`, blocks until `Signal` (the oldest ticket) or `Broadcast` (all) releases it, then locks `L` again; a `Signal` with no waiter is lost, as in Go |
| `Lock`, `Unlock` through a `sync.Locker` | the writer side of a `*RWMutex`, else a `*Mutex` (by the boxed value's type) |
| time itself (`time.Now`, durations), `context.WithValue`, `ctx.Deadline`, `ctx.Value` | havoc; deadlines are not compared |

The end of a run, when no goroutine can move: all goroutines done, the main
goroutine's result; the main goroutine blocked, `deadlock` (Go's "all
goroutines are asleep", also when Go would not detect it because other
goroutines of the process are running); the main goroutine returned and
another one blocked forever, `goroutine_leak`, unless it waits on a channel
the main goroutine returns (the caller may still use it). The report names
where each blocked goroutine waits. Fuel counts every goroutine's steps; an
unfair schedule that spins runs out of fuel and is moot.

What a certificate means for a concurrent function: the exhaustive engine
enumerates inputs and, for each, every schedule (C3): the choices at each
synchronisation point (which ready goroutine moves, which ready `select`
case fires). A goroutine runs atomically between two such points, and the
race detector below checks the accesses in between, so `exhausted` means
no deadlock, leak, misuse panic or data race under any interleaving of
the inputs tried. Past 2000 schedules for an input (10,000 for a
property) or when a schedule runs out of fuel, the schedules were
sampled and the certificate is `exhausted_havoc`.

Data races (C2): the machine keeps a vector clock per goroutine and per
synchronisation object. Every synchronisation operation joins the clocks
of the goroutines taking part with the object's, both ways; a spawn
orders the parent's past with the child; `sync/atomic` operations acquire
and release their location, and are accesses too: an atomic access never
races with another atomic one, but does with an unordered plain one (an
atomic add against a plain copy of the struct). A `WaitGroup` carries Go's
own race annotations: an `Add` that raises the counter from zero reads a
location of the group, a `Wait` that finds it above zero writes it, so an
`Add` concurrent with a `Wait` races (`Add` called inside the goroutine it
counts). This over-approximates Go's happens-before (a
buffered send is ordered with every receive on its channel, two readers of
a `RWMutex` with each other), so a race the model reports is one Go's
memory model has, and some are missed. Each step's heap accesses (loads
and stores through pointers, map reads and writes, `append`/`copy` on a
backing array; a map or slice is one location, a field or element through
a pointer its own) are checked against the location's last write and the
reads since (FastTrack); two accesses, one a write, neither ordered before
the other, end the run with `data_race`. Within a step the machine is
sequentially consistent: no weak-memory reorderings. In a package that
starts goroutines, the package variables its functions write are shared
cells, so their races are seen. A cell starts at the variable's zero
value, at the one constant its initializer stores, or, for an interface
its initializer builds (`var logger Logger = &std{}`, or a call of a
function that returns one), at an opaque
non-nil value whose method calls are havoc (#55). A variable the
package's functions use that the package initializer sets once to a value
it builds (a new object, map, slice or channel, made in place or by a call
of a function of the package: `var reg = &registry{m: map[string]int{}}`)
is a cell too: the compile stage keeps the slice of the package
initializer that builds it (the store, what it depends on, and what writes
into the object built), and the search runs that slice once, before the
trials, so every trial starts with the object the initializer built and the
goroutines share it. Havoc the slice reads is the same in every trial; it,
or a slice that fails to run, makes an exhaustive certificate one with an
assumption. A variable with any other initializer, or set in a `func
init()`, is not a cell: its stores are ignored and its loads are havoc.

The replay stage for `data_race` builds the test with `-race` and runs the
call 20 times: Go's race detector reports a race whenever both accesses
happen, whatever the timing. A report whose accesses are both in the
program (not in the replay controller or the harness) confirms it. When
none comes, the model's schedule is replayed (below) under `-race`, with
the controller invisible to the detector (`//go:norace`, its lock's
events off), so it neither orders the program's accesses nor reports its
own.

The replay stage for `deadlock` and `goroutine_leak` runs the call up to 200 times with a
2-second watchdog per run, and for a leak scans the goroutine dump for a
goroutine of the package blocked on a channel or a lock that was not there
before the first run. When none of the runs shows it, the replay stage
replays the model's schedule: the package is rewritten (an overlay, the
package itself untouched) so that every synchronisation operation waits
for its turn in the schedule, the model's synthetic goroutines (timers,
deadlines) are played by the replay controller, and once the schedule is
done the goroutines still held are released in the order they arrived:
Go's own runtime then decides whether they block. A run that leaves the
schedule (an operation the model does not have, a goroutine that does not
arrive) proves nothing: `inconclusive`, with the step it left at.

## 7. Inputs (what the engines generate)

The search engines build each parameter from its Go type; the replay stage harness
writes the same value as a Go literal.

| Go type | generated |
|---|---|
| integers | boundary values, small values, literals mined from the call tree (± 1), per-trial profiles |
| bool, error | both values / nil and non-nil |
| string | the literal pool and its neighbours, digits, random bytes; named string types and fields documented `// Any of "a", "b".` (Stainless SDKs) draw from their constants; length up to `-maxlen` or `go2lean:maxlen N` |
| slices, arrays | up to `maxlen` elements, halved per nesting level but at least one; a byte slice (`[]byte`, `json.RawMessage`) is a pool string a third of the time |
| structs | field by field; an SDK union (`Of<Variant>` fields) gets one variant, three times in four one the call tree reads; the live variant's pointer costs no depth |
| pointers | non-nil by default (nil with `-nil-inputs`); recursive types through a bounded type table |
| maps | nil (with `-nil-inputs`) or up to `maxlen` entries, keys mostly from the strings the call tree uses as map keys |
| `interface{}` | nil, string, bool, int, `map[string]interface{}`, `[]interface{}`, and (at the top level) up to six concrete types the function's call tree asserts empty interfaces to (`x.(int64)`, a type switch's cases), so that every case can be taken; the PoC writes them typed (`uint8(0)`) |
| interfaces with methods, funcs, channels, floats | their zero value (nil, 0) |
| two parameters of one kind (integers of one width and sign, strings) | one trial in four, one takes the other's value: equal arguments are an edge case independent draws almost never reach for strings (two appends of one label set, #87) |
| `context.Context` | `context.Background()` |
| unexported fields of another package's struct | their zero value (a test cannot set them) |
| SDK response types (with JSON metadata) | generated field by field plus a raw JSON object of one or two members keyed by strings the call tree compares with `==`, then written as the JSON document the harness builds for the value and decoded by the JSON model: typed fields, `RawJSON()`, per-member `respjson.Field` and `ExtraFields` agree as decoding makes them; the harness decodes the same document |
| documented enums (`// Any of ...`) | one of the values three times in four; the values the call tree compares make half of the choices |
| pointers to another package's struct with an unexported field, whose package exports a function returning one (a handle) | opaque non-nil (the PoC passes a zero one: a panic through it is inconclusive); a field read through it is havoc, a store into it ignored; a struct of exported fields is generated field by field |
| receiver of a method whose type has a constructor (`NewT`, `newT`; for an unexported `t` also `NewT`/`newT` with its first letter capitalised) | built by it (`T.M~NewT`); when no wrapper can be built (a variadic constructor, parameters the model cannot generate), the method of an unexported type is no target: only its constructor makes one |

## 8. Not modelled (rejected functions)

string orderings (`<` on strings);
`unsafe` conversions; parameters of type `*testing.T` (test helpers; with
`-tests` the test functions themselves, `TestXxx(t *testing.T)`, are
targets, `t` the harness's own);
generic functions as targets (their instances are not targets either:
a harness cannot name them).

Not rejected but worth knowing: `range` over an integer (Go 1.22) is an
ordinary loop in go/ssa and exact; `range` over a function iterator (Go
1.23) is lowered by go/ssa to closures and modelled as such (not yet
covered by an example).

## 9. Correspondence with lean-mlir

| lean-mlir | go2lean | note |
|---|---|---|
| `Ctxt`/SSA core with regions, generic over dialects | `Go2Lean.SSA` core: blocks with parameters, terminators `jump br ret call xcall callv`, generic over `Dialect` | no regions: Go's control flow is flattened by go/ssa |
| dialect = op set + `denote` | `GoSSA` dialect: `Op`, `denotePure`, `denote` | an `LLVM` dialect is designed (§4.4 of the design) but not built |
| types as bitvectors of width `w` | `Val.int w (BitVec w)` | Go's fixed widths, no arbitrary-width integers |
| `EffectKind` pure / impure | `denotePure` / `denote` (heap) | the split is by function, not yet a per-op tag |
| memory: block id → block, pointer `(block, offset)` | heap: `ref` → object, pointer `(ref, path)` | Go has no pointer arithmetic, so a typed path replaces a byte offset |
| pure memory ops (memory SSA) | the heap threaded through `denote` | the explicit-`mem` form is the representation to switch to for proofs |
| UB / `PoisonOr` | Go panics (`Res.panic k`) and `modelStuck` | Go defines the behaviour the C/LLVM world leaves undefined |
| refinement proofs between programs | properties of one program: `no_panic` and spec-in-Go postconditions; counterexamples checked by `go test`, small-domain certificates, `bv_decide` proofs (§5.3) | go2lean finds bugs rather than verifying rewrites |
| `simp`/`bv_decide` over unfolded programs | random and exhaustive engines; proofs where the domain is small or the model is loop-free | |
