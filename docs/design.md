# go2lean design

> Status: v0.3 (2026-10-17). This document is the overall design, used to
> agree on goals, boundaries, interfaces and milestones before and while
> building. v0.3 records what the first end-to-end slice actually built and
> what it changed in the design (notably the memory oracle, §4.2).
> Items marked **unverified** are judgement calls that a spike must test.

## 0. One sentence

Translate Go programs into executable SSA semantic models in Lean, let an
agent state properties over those models, use Lean's decision tools to find
**counterexamples**, and turn every counterexample into a **reproducible PoC**
with the real Go toolchain. When no counterexample exists, the same model is
used for **proof**.

## 1. Goals and non-goals

### 1.1 Goals

1. **Three modules forming a closed loop.**
   - Compile stage (`programmatic`): deterministic translation. Two frontends feed one Lean
     SSA core:
     - **F-GoSSA**: Go → `golang.org/x/tools/go/ssa` → Lean `GoSSA` dialect (first).
     - **F-LLVM**: Go → TinyGo → LLVM IR → Lean `LLVM` dialect (later; also the
       entry point for other languages).
   - Spec & search stage (`agentic`): an LLM agent states properties over the model and drives
     Lean to produce counterexamples or proofs. Its non-agentic core (property
     templates and engines) is the `constraint` package.
   - Replay stage (`poc`): realises counterexamples as real Go tests, runs them with the
     Go toolchain and judges them.
2. **Trust comes from the replay stage, not from the model.** The model may be imprecise;
   every bug reported to a human carries a PoC that reproduces under `go test`.
3. **One core, several dialects.** The Lean SSA core is frontend-independent.
   A dialect defines only types, instructions and the semantics of a single
   instruction; CFG interpretation, fuel, havoc and panics live in the core.
4. **Cross-checking.** The same Go function compiled by both frontends yields
   two models that can be run on the same inputs. A disagreement is either a
   translation bug or a TinyGo/gc semantic difference; both are worth reporting.
5. **Two-sided output.** Every property ends as a `confirmed` PoC, a
   `proved`/`bounded_proved` Lean theorem, or an explicit `unknown` with a reason.

### 1.2 Non-goals (first phase)

- Weak-memory reorderings: not modelled (goroutines, channels, `sync` and
  data races under happens-before are: see [concurrency.md](concurrency.md)).
- GC, finalizers, `unsafe`, cgo, reflection, the fine semantics of
  `defer`/`recover`.
- Floating point beyond IEEE 754 float64/float32 (complex numbers); floats
  themselves are modelled since #148 (semantics.md §4.9).
- Whole-program verification. The unit is the **function-level** property.

## 2. Design principles

### P1. The model may over-approximate, never under-approximate (for counterexamples)

Anything the translation does not model (unknown external calls, unmodelled
memory operations, runtime functions) becomes **havoc**, an explicit
non-deterministic input. It is never guessed and never aborts the whole function.

- Under over-approximation, **no counterexample ⇒ none in the real program**
  (the proof direction is sound).
- Counterexamples found under over-approximation **may be spurious**; the replay stage
  filters them. The replay stage is the oracle, so the compile stage need not be perfect.
- The one permitted under-approximation is loop fuel; such results are
  labelled `bounded_proved(fuel = N)`, never `proved`.

### P2. Havoc is an explicit parameter

Havoc values are extra **inputs** of the model function, not `sorry` or
axioms. `bv_decide`/random search can choose them, counterexamples carry their
values, and when the replay stage says `spurious` the diagnosis can name the havoc site the
counterexample depended on.

### P3. Every property has two forms: a Lean proposition and an executable Go oracle

Disagreement between the two is the most dangerous failure mode. The
mitigation is **spec-in-Go** (§5.2): the agent writes only the Go form; the compile stage
derives the Lean form mechanically through the same translation pipeline, so
they agree by construction.

### P4. A counterexample is re-validated inside the model before it reaches the replay stage

Whatever Lean output a counterexample is parsed from, it is re-executed in
the model (or reported from the executing run itself) before being written as
a `Counterexample` record.

### P5. Core and dialect are separate

The core knows only typed values, blocks with parameters, terminators, fuel,
havoc and panics. This is lean-mlir's split, restricted to the SSA core; the
rewriting framework and general regions are out of scope.

## 3. System overview

```
                 ┌──────────────────────────────────────────────────┐
                 │ schemas / internal/contract: Target, Property,    │
                 │ Counterexample, Verdict, ProofResult, ModelGap    │
                 └──────────────────────────────────────────────────┘
   Go source
     │
     ├─► F-GoSSA ──► GoSSA dialect terms ─┐
     │                                    ├──► Lean SSA core ──► executable model + abi.json
     └─► F-LLVM  ──► LLVM dialect terms ──┘    (Go2Lean.SSA)          │
                                   ▲                                 ▼
                      crosscheck ◄─┘               ┌──────────────────┐ Counterexample ┌──────────┐
                                                   │ spec & search    │ ─────────────► │ replay   │
                                                   │ constraint core  │ ◄───────────── │ go test  │
                                                   └──────────────────┘    Verdict     └──────────┘
                                                            ▲
                 compile ◄──────── ModelGap ────────────────┘
```

Modules communicate **only through JSON records in a workspace directory**
(§7), so each runs and tests on its own.

## 4. The Lean SSA core (`Go2Lean.SSA`)

### 4.1 Why a core

- Both frontends share CFG interpretation, fuel, havoc, panics and value
  encoding; a frontend emitter only translates instructions.
- Cross-checking is "run two programs through one interpreter".
- Properties are written the same way for both routes.
- Adding Rust/C (via LLVM) or another Go frontend touches no core code.

### 4.2 Structure (as built)

```lean
class Dialect (Op : Type) (Val Oracle : outParam Type) where
  denote : Oracle → Op → List Val → Res Val   -- one instruction; may panic, may read the oracle
  toBool : Val → Option Bool                    -- branch conditions

inductive Instr (Op) | op (dst : Var) (o : Op) (args : List Var) | havoc (dst : Var) (site : Nat)
inductive Term
  | ret (v) | jump (b) (args) | br (cond) (t) (targs) (f) (fargs) | panic (k : PanicKind)
  | call (f : FuncId) (args) (dst) (next) (nargs)

structure Block (Op) where params : List Var; instrs : Array (Instr Op); term : Term
structure Func (Op)  where name; params : List Var; nvals : Nat; entry : BlockId; blocks : Array (Block Op)
structure Prog (Op)  where funcs : Array (Func Op); havocWidths : Array Nat

def run (p : Prog Op) (or : Oracle) (h : Havoc Val) (fid) (args) (fuel : Nat) : Res Val × HState Val
```

- **Phi → block parameters.** Both `go/ssa` `Phi` and LLVM `phi` become block
  parameters; the core has no phi.
- **`denote` is branch-free and call-free.** Single instructions are pure
  functions (they may panic and may read the oracle). All control flow is in
  `Term`. This is what lets `simp` unfold `run` for a fixed fuel into a
  straight-line bitvector expression for `bv_decide`, the same route lean-mlir
  takes with `simp_peephole`.
- **`Res α := ok α | panic PanicKind | outOfFuel`.** `outOfFuel` is a model
  artefact; `panic` is observable behaviour. `PanicKind.modelStuck` marks an
  ill-typed or malformed model (a frontend bug), never a Go panic.
- **Oracle (new in v0.3).** Read-only ambient state fixed for a whole run.
  For GoSSA it is memory: `mem : ref → index → BitVec 64`. Two reads of the
  same location agree, across functions — necessary because a spec-in-Go
  function reads the same slice as the function it specifies. Site-keyed
  `havoc` remains for values with no location (unmodelled calls).
- **Fuel** is consumed per block entry and per call; every recursion is
  structural on fuel, so the interpreter is total and `#eval`-able.

Deep embedding plus a denotation, rather than emitting shallow Lean
functions directly: one more layer of Lean work in exchange for both
frontends sharing everything and translation being testable apart from semantics.

### 4.3 `GoSSA` dialect

The current coverage, construct by construct with its status (exact,
havoc, assumption, stuck, rejected), is kept in [semantics.md](semantics.md).

Stage A (built): integers of every width, bools, read-only slices of integers.
Stage B (built, §4.3b): heap-backed slices of any modelled element type,
structs, pointers, strings, `append`/`copy`.

| go/ssa | GoSSA op / term | Status |
|---|---|---|
| `BinOp` arithmetic / bitwise / comparisons / shifts | `add sub mul sdiv udiv srem urem and or xor andNot shl shr eq ne slt … uge`; shift ≥ width gives 0 or sign fill, negative signed count panics | built |
| `UnOp` `-` `^` `!` | `neg bnot not` | built |
| `Convert` / `ChangeType` between integer types | `conv w signExtend` | built |
| `If` / `Jump` / `Return` / `Panic` | `Term` | built |
| `Phi` | block parameters | built |
| `Call` to a modelled function in the same package | `Term.call` (the block is split at the call) | built |
| `Call` to a builtin `len`/`cap` on a slice | `len` / `cap` | built |
| `Call` to anything else | havoc result, function marked `partial` | built |
| `IndexAddr` then `UnOp *` | `load w` (bounds check + oracle read) | built |
| `Slice` `xs[lo:hi]` | `reslice` with Go bounds panics | built |
| `Store` to a slice element | ignored, function marked `partial` | built (read-only model) |
| `Alloc` / `FieldAddr` / maps / interfaces / closures / multiple results | function rejected | later |
| `Go` / `Send` / `Select` / `Defer` | function rejected | out of scope |

**Go semantics, not LLVM:** `x / 0` and `x % 0` panic; `MinInt / -1` wraps;
negative indices are out of range (indices are compared unsigned after
sign-extension).

**Memory model stages:**
- **A (built, read-only):** a slice is `(ref, off, len, cap)`; element reads go
  to the oracle. Bounds checks only need `len`, so index-out-of-range
  properties need no memory contents. Writes mark the function `partial`.
- **B:** a heap `Ref → Obj` threaded through functions; `Store`, `*`,
  `IndexAddr`, `MakeSlice` become heap operations and aliasing is exact.
- **C:** interfaces, closures, map iteration (as havoc).

### 4.3b The heap model

The smoke test (history §9b) fixed the order: pointers and structs first, strings
second. As built: `Dialect.denote` takes and returns the heap (`Array Val`,
slot 0 reserved for nil), so the interpreter core only threads it; the
dialect gains `ptr (ref, path)`, `structv`, `arr` values and the ops
`alloc`, `loadPath`, `storePath`, `fieldAddr`, `indexAddr`, `field`,
`mkStruct`, `makeSlice`, `appendSlice`, `copySlice`. The generators materialise every slice argument and
every pointee on the heap before the run, so stores are exact and aliasing
is real; the oracle remains only for slices that come out of unmodelled
calls. A spec runs on the post-run heap, so it observes mutations through
its arguments (`examples/alias`: a deposit that overshoots its limit, a fill
that misses the last element, nil dereferences — all confirmed by `go test`).
Counterexamples carry struct and pointer arguments under `objects`; the
harness materialises `&T{F: v, ...}` and `func() *int { v := ...; return &v }()`.
Slice elements are any modelled type (`examples/recs`: `[]Item`, `[]string`,
built with `append`); `append` writes in place while the capacity allows and
otherwise copies into a fresh backing array of twice the capacity (Go's
exact growth is implementation-defined and visible only through `cap`).
Interfaces, maps, funcs, channels, complex numbers and `unsafe.Pointer` are
opaque values (history §9b; floats are IEEE bits since #148); `range` over strings is modelled, over maps assumed empty;
deferred calls the model does not look into are ignored, deferred calls
to modelled functions run at the returns, in a loop too (#62); goroutines
and `select`: [concurrency.md](concurrency.md).

The original design notes (the parts not built yet) are in
[history.md](history.md) §4.3b.

### 4.4 `LLVM` dialect: C and TinyGo (built, 2026-10-21)

The LLVM line takes C (or any LLVM IR) through the same SSA core and the
same engine ideas, with **VeIR's semantics as a dependency** instead of
semantics written here.

**Semantics: VeIR, required by lake.** [VeIR](https://github.com/opencompl/veir)
(opencompl, Apache-2.0 WITH LLVM-exception) defines LLVM integers as a bit
vector or poison (`Veir.Data.LLVM.Int`) with every flag (`nsw`, `nuw`,
`exact`) and UB condition, and a byte-addressed memory (`Veir.MemoryState`:
objects, poison bytes, an access outside its object is UB). Its whole
dependency closure (ExArray, lean-ctrees, Coinductive) uses Lean's core
library only, and it builds with our v4.35.0 although it pins
v4.35.0-rc4 (checked: the whole library, 278 jobs, 0 errors, about 14 min
cold; what the dialect imports, 31 jobs, 79 s, 67 MB). We use
its data layer as pure functions inside our `denote`, not its CTree
interpreter: the engines, havoc and certificates stay the core's. lean-mlir,
the earlier plan, was dropped: it pins a Lean nightly and Mathlib.

**Package layout.** `lean-llvm/` is a lake package of its own
(`go2lean-llvm`) that requires the core (`../lean`, by path) and VeIR
(git, pinned revision), so the Go line keeps no dependency beyond Lean's
core. VeIR builds once in `lean-llvm/.lake` and every workspace requires
`lean-llvm` by path, so workspaces share that build.

**Dialect** (`Go2LeanLLVM/Dialect.lean`). Values are `int w (LLVM.Int w)`,
`ptr (LLVM.Ptr)`, and the memory state, which lives in heap slot 1. Ops:
`const poison null`, `add sub mul` (with `nsw nuw` and `cUB`), `udiv sdiv
urem srem` (with `exact`), `shl lshr ashr and or xor`, `icmp`, `icmpPtr`,
`select`, `trunc zext sext`, `alloca load loadPtr store`, `gep` (constant
offset plus signed index × stride), `cond`, `unreachable`, `unsupported`. For TinyGo's IR also
`agg` and `fn` values and the ops `zero extract insert goAlloc global
resolveFn memcpy memset minmax abs bitCount fshl opaque`.
UB stops the run as a core panic: through null `nilDereference`, outside
the object `indexOutOfRange`, division by zero `divideByZero`, any other
kind the new `undefinedBehavior` (a poison branch condition, `unreachable`,
`INT_MIN / -1`). **C's signed overflow**: LLVM only makes `add nsw` poison;
for C sources the frontend sets `cUB`, and a poison result from operands that
were not poison is UB where it happens, as C (and UBSan) say.

**Frontend** (`internal/frontend/llvm`, built only with `-tags llvm18` or
another go-llvm version tag, because it links libLLVM through cgo):
`clang -O0 -Xclang -disable-O0-optnone`, `opt -passes=mem2reg`, then
[go-llvm](https://github.com/tinygo-org/go-llvm) (tinygo, the LLVM C API).
llir/llvm, the earlier plan, cannot parse opaque pointers, which every
current clang emits. Phis become block parameters, a call to a function of
the module is the core's call terminator (a fallback block havocs the result
if the callee gets stuck), a call to a declaration is havoc, constant
`getelementptr` indices fold into the offset, `switch` becomes a chain of
equality tests. The `nsw`/`nuw`/`exact` flags are read from the
instruction's text (go-llvm has no accessors). What is not lowered becomes
`unsupported` (the model is stuck there, never a false UB) and is listed
per function.

**Inputs.** A pointer parameter points to a fresh object of 0 to 8
elements whose size is the stride of the first `getelementptr` on it (else
the width of the first load or store through it); an integer parameter right
after a pointer is that buffer's element count. The property, the C
analogue of `no_panic`, is therefore *no UB for a caller that passes a buffer
with its real length*; a caller passing a wrong length is not reported.

**Engines** (`Go2LeanLLVM/Search.lean`): seeded random trials, then exhaustive
enumeration (integers from {0, 1, -1, 2, INT_MAX, INT_MIN}, buffers of up to
3 elements from {0, 1, -1}); an exhaustive run without UB, stuck or
out-of-fuel trials is a certificate for that domain.

**Replay stage** (`internal/llvmhunt`): the counterexample becomes a C program that
`#include`s the source (its `main` renamed) and calls the function with
buffers from `malloc` of exactly their size, built with
`-fsanitize=address,undefined -fno-sanitize-recover=all`. A sanitizer report
confirms; a clean exit is spurious. Needs `llvm-18-dev` and
`libclang-rt-18-dev` (or the version's equivalents).

**First run** (`go2lean llvm examples/llvm/oob.c`): `sum`'s off-by-one read
and `avg`'s signed overflow are found and confirmed (ASan heap-buffer-overflow;
UBSan "signed integer overflow: 2147483647 + 7"), and `max_of` gets the
exhaustive certificate over 40 inputs.

**TinyGo line** (`go2lean tinygo [-compare] <pkg>`, `-tags llvm22`,
`internal/llvmhunt/tinygo.go`). A Go package goes through TinyGo 0.43
(`tinygo build -opt=0`, LLVM 22) to LLVM IR: a temporary `main` package
keeps every exported function alive, and the frontend models them with the
whole program they reach. The standard library and the runtime are not
hand-written models here but their own Go source, compiled by TinyGo:
`strings.Builder`, `append` (`runtime.sliceAppend`), maps
(`runtime.hashmap*`), interfaces and `errors.New` run as TinyGo implements
them, under VeIR's semantics. This is the cross-check of the hand-written
GoSSA models (docs/limitations.md §5): the two lines share no model code.

What the frontend handles for TinyGo's IR, beyond C's:
- Go's ABI: slices and strings as `{ptr, len, cap}` aggregates
  (`insertvalue`/`extractvalue`, loads and stores of aggregates field by
  field), the trailing `context` parameter, function values `{ctx, fn}`
  called through a table that maps function objects to the program's
  functions;
- globals with initializers (bytes and relocations, through nested constant
  `getelementptr`s; a global modelled only in part records an `assume`),
  function objects, constant structs (interface constants), and the
  package's `init`, run before each trial;
- TinyGo's runtime panics (`runtime.lookupPanic` → index out of range,
  `slicePanic`, `nilPanic`, `divideByZeroPanic`, `_panic`, ...) as the
  core's panic kinds; `runtime.alloc` as a zeroed allocation (above 1 MiB:
  stuck); `inttoptr` constants (GC layout tags) as opaque values;
- intrinsics `memcpy memmove memset smin smax umin umax abs ctlz cttz
  ctpop fshl`;
- the callees, found breadth-first from the targets through instruction
  operands and global initializers (a compile-time map keeps its
  key-equality function there), up to 400 functions; a callee the model
  gets stuck in makes the result havoc for an integer (and records an
  assumption), stuck otherwise.

Inputs come from the Go signature: integers; slices and strings as buffers
of their element size (zeroed when the element holds pointers); half of the
string inputs are the package's own string literals; pointers point to one
object of the pointee's size; maps, channels, funcs and interfaces are nil.
The search prefers a counterexample that read no havoc and took no
assumption, as the Go line's does. A counterexample is replayed as a
`go test` of the real package (in the sandbox), which confirms it or calls
it spurious.

**Comparison** (`go2lean tinygo -compare`: both lines on the same package,
the go/ssa line with `-deps`; verdicts after replay; 2026-10-21, all 22
example packages, 94 exported functions):

| outcome | functions |
|---|---|
| both lines agree | **73** of 94: 29 the same bug, confirmed by `go test` on both lines (index and slice bounds, division by zero, nil dereference, type assertion, explicit panics, across 18 packages); 44 no bug on either (4 of them with a TinyGo counterexample that the replay rejected, each through havoc) |
| they differ | **6**: the TinyGo line passes nil for interfaces and maps, so it misses inputs `Label`, `Weight` and portable `Per`; it gets stuck in `sync.Once.Do` (`regexp`, `math/rand`) and reports a havoc-dependent, spurious nil dereference for portable `Minor` and `Pick`; for xpkg `Post` it builds a `*money.Ledger` field by field with a nil map and confirms a nil map write, an input the Go line does not generate for a handle (its own result there is `exhausted_assuming`: `Ledger.Add` got stuck) |
| not comparable | **15**: the TinyGo line is stuck in 14 (`strconv.Atoi`, `encoding/json.Marshal`, `runtime.stringToBytes`, `strings.TrimSpace`, `regexp.MustCompile`, `crypto/rand.Read`, a `inttoptr` of a variable), and one counterexample cannot be replayed (an unexported struct argument) |

No difference came from the two models computing different results on the
same input. The comparison did find model bugs, all on the TinyGo side and
fixed before the final run: `inttoptr` GC tags, `ctlz` (a havoc that made
`append` allocate gigabytes), constant aggregates, map parameters from
random bytes, a key-equality function held in a global initializer (the
map lookups were havoc, giving spurious panics), and an initializer with a
nested constant `getelementptr` left half zero, which made `ErrEmpty` nil
and hid a real division by zero (`portable.Head`). It also showed the Go
line missing two bugs when run without `-deps` (xpkg `Share`, portable
`Port`), which the comparison now always passes.

Not yet: floats, `ptrtoint`, `freeze`, `malloc`/`free` and other C library
calls (havoc), specs and properties beyond "no undefined behaviour" in the
LLVM line, inputs for interfaces and maps in the TinyGo line, `sync.Once`
and reflection-heavy code (`encoding/json`) on the TinyGo line, and CI for
the LLVM line (it needs LLVM's development files).

### 4.5 Frontend emitters (Go side)

```
internal/frontend/gossa/   go/packages + go/ssa → ir.Prog → internal/ssaout   (built)
internal/frontend/llvm/    clang + opt + go-llvm → Lean (own printer)        (M0 built)
internal/ir/               the Go line's program representation
internal/ssaout/           Lean printer, abi.json, model report, targets (Go line)
```

The LLVM frontend prints its Lean directly: `ir.Prog` and `ssaout` are
shaped by GoSSA (its ops, heap and JSON tables).

### 4.6 ABI bridge

The replay stage needs to turn model values back into Go values. The GoSSA ABI is nearly
the identity; the LLVM ABI follows TinyGo's expansion rules (slice →
`ptr,len,cap`, string → `ptr,len`, structs flattened, multiple results → an
LLVM struct). Both produce the same record:

```json
{
  "go_func": "github.com/andreasfoo/go2lean/examples/overflow.Sum",
  "go_sig": "func(xs []int) int", "frontend": "gossa",
  "params": [{"go": "xs", "go_type": "[]int", "kind": "slice", "elem": "int",
              "model": ["xs_ref", "xs_off", "xs_len", "xs_cap"]}],
  "result": {"go_type": "int"},
  "havoc": []
}
```

Counterexamples carry slice contents under `memory` (per parameter), read
from the oracle, so the replay stage materialises exactly the slice the model saw.

### 4.7 Compile stage outputs

- `<workspace>/lean/Go2Lean/Generated/<frontend>/<pkg>.lean`, in the
  workspace's own lake project (see §8)
- `<workspace>/models/<frontend>/<Func>.json` (ABI), `report.json` (per function:
  complete / partial / rejected, issues, spec-for, Lean FuncId)
- `<workspace>/targets/<id>.json`
- (later) `crosscheck.json`

## 5. Spec & search stage: properties and counterexamples

### 5.1 Inputs

Go source and type information, the model report, ABI records,
cross-check results, and optionally a natural-language focus from the user.

### 5.2 Property kinds and spec-in-Go

| kind | meaning | Lean form | Go oracle |
|---|---|---|---|
| `no_panic` | no panic under a precondition | `run f args ≠ .panic _` | mechanical: `defer recover()` |
| `postcondition` | the result satisfies a predicate | derived from the spec function | the agent's `Spec(args…, ret) bool` |
| `sequence` | a `go2lean:sequence` spec returns true for every input sequence | `run seq args = .ok (.bool true)` | the spec itself, called with the sequence |
| `no_hang` | every run of a function with a loop ends (#86) | violated by a run that comes back to a state (`Hang.lasso`) | mechanical: the call under a 2s watchdog |
| `equivalence` | `f ≡ g` | `run f = run g` | run both, compare |
| `crosscheck` | GoSSA model ≡ LLVM model | core-level comparison | `go test` vs `tinygo test` |
| `custom` | the agent writes Lean directly | agent-written | agent-written (mandatory) |

**Spec-in-Go** (built): the agent — or a human — writes a Go function with a
`// go2lean:spec Target` doc comment, e.g. `func SpecSum(xs []int, ret int)
bool`. The compile stage compiles it into the model with the same pipeline, and the property
is `run Sum args = .ok r → run SpecSum (args ++ [r]) = .ok true`. The Lean
form and the Go oracle agree by construction; nobody has to write Lean.
`custom` is the only place the agent writes Lean, and its counterexamples are
flagged separately.

**Sequence specs** (built, 2026-10-22): properties of a sequence of calls
(a stream converter that eventually emits the whole message, an invariant
that holds after every event) are written as a Go function with a
`// go2lean:sequence Target` doc comment that takes the events (and any
other inputs), drives `Target` over them, checks the invariant after each
step ("always") and the end condition when the input ends ("eventually",
for a finite stream: at its end), and returns a bool. The function is its
own target; the property `sequence` says it returns true without
panicking. Random search draws sequences up to `-maxlen` (or the spec's
`go2lean:maxlen`); the exhaustive engine enumerates every sequence up to the
exhaustive length over a narrow alphabet (the program's own integer
constants with their neighbours, 0 and 1; up to 2,000,000 sequences), which
makes it bounded model checking of the spec; the replay stage replays the failing
sequence. `examples/stream`: a converter that drops an open block's text
on a second `Start` is caught (random search, trial 25, confirmed), and the
invariant `SeqBounded` holds on all 538,084 sequences of up to 3 events.
This is not temporal logic: no infinite traces, no fairness, and no
concurrency (one goroutine, `sync` ignored); docs/limitations.md §7.

### 5.3 Engine ladder

1. **Random search** (built): `Go2Lean.Search.run`, a seeded generator with
   per-trial *value profiles* (boundary-only, small, mixed) so that correlated
   inputs are likely; memory and havoc are re-drawn every trial from the trial
   seed. The value pool includes every integer and string literal of the
   target and its spec, with off-by-one and numeric-suffix neighbours, so
   comparisons against magic constants are reachable. Inputs are materialised
   on the heap (slices, strings, structs, pointees, bounded-depth recursive
   structures from the ABI type table). Top-level pointers and receivers are
   non-nil unless `-nil-inputs` is given. A run on which the model got stuck
   (ill-typed, or a call into a rejected function's stub) is *moot*: it never
   counts as a refutation. Reports one JSON line. Seconds.
2. **Exhaustive small domain** (built): `Go2Lean.Search.exhaust` enumerates
   boundary values, mined literals and magnitudes ≤ 8 for every integer
   parameter and slice element, slices up to length 3, with a domain-size
   cap. No counterexample yields a `ProofResult` of status `exhausted`, a
   weak but honest bounded certificate; havoc sites are not enumerated and
   the record says so.
3. **Proof over all inputs** (built, 2026-10-22, `internal/prover`, `-prove`,
   on by default): when the exhaustive engine passed on a `no_panic`
   property of a function whose parameters are all integers or bools, the spec & search stage
   writes a theorem over every value of every parameter: the model returns
   without panicking, reads no havoc and takes no assumption, within fuel
   64. The proof is generic: `simp` with the interpreter, `rfl` lemmas that
   look the program's functions up (`prog.funcs[i]? = some fi`), the
   dialect's integer lemmas (`Go2Lean.Proof.Simp`: one per op, so that
   `denotePure`'s match over every op is never unfolded) and the core's
   array lemmas for the environment; then `split`, `simp_all`, splitting
   the run states' conjunctions and substituting them, `simp_all` again,
   and `bv_decide` for what is left. The result is recorded as `proved`
   with its trust base from `#print axioms`: `kernel` when only `propext`,
   `Classical.choice` and `Quot.sound` appear, `compiler` when
   `Lean.ofReduceBool` does (bv_decide's certificate check). On the example
   packages 7 of the 13 eligible properties that held are proved, all with
   trust `kernel`, in 15–25 s each (`checked.Double`, `checked.Half`,
   `divzero.Ratio` including `MinInt / -1`, `oob.Clamp`, ...); the other 6
   allocate, dispatch through an interface or call another package. Since
   2026-10-24 a second route follows when this one does not apply or
   fails: the function's contract in `Go2Lean.Proof.WP` (no panic, or the
   spec returns true), walked by `wp_auto`, for a loop-free function that
   makes no call, reads no havoc and takes integers, bools or integer
   slices; it covers every run that ends (partial correctness, any fuel)
   and needs only the kernel. It proves spec properties, which the case
   split could not (`oob.Clamp`'s `SpecClamp` and `SpecClampIdentity`,
   `loops.Clamp`), and `globals.DefaultGen`'s `no_panic`. Not attempted:
   functions with loops (an invariant: `go2lean prove -llm`) or calls (a
   contract), strings and pointers, and the LLVM line.
4. **`bv_decide`** is the last step of rung 3; on its own it is not used.
5. **SMT export** (Z3/cvc5) when complete bug-finding on loops is needed:
   external, asynchronous, better `mul`/`div` heuristics; no Lean certificate,
   which bug-finding does not need because the replay stage is the certificate.
6. **Proof**: `simp`/`omega`/`decide`, then agent-written Lean.

Counterexamples are re-validated per P4. The random engine reports the values
it actually executed, so revalidation is implicit.

### 5.4 Agent loop (built, first version)

`internal/agent` drives one package:

```
1. compile the package (compile stage) to learn which functions are modelled
2. ask the LLM for go2lean_specs_test.go: spec-in-Go functions for the modelled
   functions, inside the supported subset (system prompt states the rules)
3. write the file into the package, compile again; specs the frontend rejects
   go back to the LLM with the frontend's issues (up to -repairs rounds)
4. propose (templates: no_panic + one postcondition per spec), hunt, verify (search and replay stages)
5. report round: verdicts, counterexamples and proof results go back to the LLM,
   which writes out/agent/report.md and may return a revised spec file; a
   revision triggers one more pass through 3–4
```

Design choices:
- Specs live in a `_test.go` file of the target package, so production code is
  untouched and the frontend sees them by loading the package's internal test
  variant (`go/packages` with `Tests: true`).
- The LLM is behind a one-method Go interface (`Complete(system, user)`).
  `Claude` uses the Anthropic SDK (Claude Opus 5.5, adaptive thinking);
  `Mock` replays scripted responses from a directory, used by tests and for
  offline demonstration.
- Every prompt and response is written to `<workspace>/agent/`, so a run is
  inspectable and replayable without the model.
- A postcondition run where the target panicked before the oracle could be
  evaluated is *moot*: it neither refutes nor supports the property, and the
  exhaustive engine issues no certificate when any input was moot. The
  `no_panic` property carries that finding instead.

**Spec mutation check** (since 2026-10-23, after the semver blind run,
`docs/eval/semver.md`, where four agent specs had checked nothing). A spec
that held is only as good as the bugs it would catch. For every
postcondition and sequence spec without a counterexample, the mutation
engine (`Search.mutate`, engine `mutate` of `go2lean-search`) builds
mutants of the code under test: the target (for a sequence spec, the
function it drives) and the functions of its package it calls, each with
one branch negated (its targets swapped) or one operator replaced (`==`
and `!=`, `<` and `>=`, `<=` and `>`, `+` and `-`). The mutants are data
transformations of the linked program, so one build serves them all and
neither the interpreter nor the proofs change. For each mutant (at most
24, spread over the sites) a random search looks for a distinguishing
input: the spec fails on the mutant and not on the original. The result
goes to `mutations/<property>.json` and the summary ("k of n mutants fail
the spec"); a spec no mutant fails is flagged as checking nothing a
one-line change breaks, and the agent's report round is told so (and now
gets the spec file, so it can restate it). On the agent's semver specs
the flagged ones are exactly the four found vacuous by hand; on the
examples, the deliberately weak `SpecReverse` and four other weak specs
score 0, the rest 1 to 6 kills. The regression gate records each score.
This follows the "distinguishing mutant" rule of Theorem's OSS Verify
fuzz-target guidelines (a property no change can make fail checks
nothing). Not mutated yet: constants, stores, calls.

**Specs as Go fuzz targets** (`go2lean fuzz`, `internal/fuzzexport`;
since 2026-10-23). The semver blind run showed Go's own fuzzer finding,
in seconds, violations of the agent's specs the model could not reach:
the value was in the specs, and both searches should run on them. `go2lean
fuzz <pkg>` writes every postcondition and sequence spec of the package
as a `FuzzGo2Lean_<Spec>` target: parameters of the types Go's fuzzer
takes are fuzz arguments, slices, arrays, structs and pointers to them
are decoded from one extra `[]byte` (little-endian integers, lengths up to
8, strings up to 32 bytes); a postcondition calls the target (function or
method) and skips inputs on which it panics, as the model's postcondition
does. The targets are built with coverage instrumentation through an
overlay (`go test -c -fuzz`, nothing written into the package) and each
is fuzzed in the sandbox for `-fuzztime`; a failing input is kept in the
workspace in Go's corpus format with the generated file and the command
that replays it. `go2lean agent` runs it after the pipeline (`-fuzztime`,
30 s per spec by default) and gives the results to the report round. On
the semver agent specs all 21 export; 6 are violated within seconds, the
four the hand-written wrappers of the frozen run found and two on
receivers built field by field.

**Fuzz targets as specs** (`internal/fuzzimport`; `go2lean run`,
`-fuzz-specs`, on by default; since 2026-10-23). The other direction: a
package's own `FuzzXxx` targets are properties already written. The
function each hands to `f.Fuzz` becomes a sequence spec
`Go2LeanFuzz_FuzzXxx` over the same parameters, at the source level so
the model needs nothing new: the body is kept inside a closure, and its
`t` calls become plain code (`t.Fatal*`, `t.FailNow`: failed, return;
`t.Error*`, `t.Fail`: failed; `t.Skip*`: passed, return; `t.Log*`,
`t.Helper`: their arguments evaluated). A target that uses `t` otherwise
(hands it to a helper, `t.Run`, inside a nested function) or reads a
variable of `FuzzXxx` is left out with the reason. The generated file
reaches the model and the replay stage build through an overlay
(`gossa.Options.Overlay`, `pipeline.Env.Overlay`, `poc.VerifyAll`):
nothing is written into the package. `examples/fuzzed` holds a round trip
whose planted bug the model finds and `go test` confirms, a target that
holds, and one left out.

Not yet: ranking properties by expected yield, `ModelGap` records from
spurious verdicts, generalising a confirmed bug to neighbouring functions, and
proof attempts.

The replay stage executes the target with generated inputs inside the target package:
a function that writes files writes them. Run on a scratch clone.

## 6. Replay stage: counterexample → real verification (built)

### 6.1 Harness generation

Into the target package, the replay stage writes `zz_go2lean_poc_<id>_test.go`:

```go
func TestGo2LeanPoC_cx_sum_specsum_s42(t *testing.T) {
    xs := append(make([]int, 0, 5), 1, 9223372036854775806, 9223372036854775807, 0)
    var ret int
    res := map[string]any{"panicked": false}
    func() {
        defer func() { if r := recover(); r != nil { res["panicked"] = true; res["panic"] = fmt.Sprint(r) } }()
        ret = Sum(xs)
        res["ret"] = fmt.Sprint(ret)
    }()
    if !res["panicked"].(bool) { res["spec"] = SpecSum(xs, ret) }
    b, _ := json.Marshal(res)
    fmt.Println("GO2LEAN_RESULT " + string(b))
}
```

The test never fails by itself; it prints a result line and the `poc`
package decides. That keeps "the harness broke" separate from "the property
is violated".

### 6.2 Running and judging

All counterexamples of a package go into one test file, added to the
package through `go test -c -overlay` (nothing is written into the
package; the package's tests compile once), and the test binary runs in
the sandbox (`internal/sandbox`: a copy of the package directory under
bubblewrap and `prlimit`: read-only file system, no network, resource
limits; see docs/limitations.md §6). Results are mapped
back by test name, and a batch that does not compile falls back to one
run per test. Each PoC directory still holds a standalone file, run with
plain `go test`. (`tinygo test` for `crosscheck` properties,
later.)

| status | condition |
|---|---|
| `confirmed` | the real run violates the Go oracle in the way the kind requires (a panic of the expected category for `no_panic`; the spec returning false for `postcondition`; the call not returning within 2s for `no_hang`) |
| `spurious` | the real run satisfies the oracle |
| `inconclusive` | compile failure, timeout, missing result line, a panic where the oracle could not be evaluated, or a panic category mismatch |

`spurious` carries a diagnosis (model output vs real output, havoc sites used,
whether the property was `custom`); this is the only feedback channel into the compile and spec & search stages.
`confirmed` produces `<workspace>/poc/<id>/` with the test file, a README with
the reproduction command, and the verdict, and carries a `rank` (tier 1 to 4)
by how plausible its input is for real callers (docs/limitations.md §3).

## 7. Shared contract

JSON records; Go types in `internal/contract`. Integers are decimal strings,
rendered per the Go type in the ABI (signed/unsigned). The workspace layout:

```
<workspace>/targets/<id>.json
<workspace>/properties/<id>.json
<workspace>/counterexamples/<id>.json
<workspace>/verdicts/<counterexample id>.json
<workspace>/proofs/<property id>.json
<workspace>/models/<frontend>/<pkg>/{report.json, <Func>.json}
<workspace>/poc/<counterexample id>/
```

```jsonc
// Target
{"id":"t-sum","module":"github.com/andreasfoo/go2lean","package":".../examples/overflow",
 "func":"Sum","go_sig":"func(xs []int) int","models":{"gossa":"models/gossa/Sum.json"}}

// Property
{"id":"p_sum_specsum","target":"t-sum","kind":"postcondition",
 "nl":"Sum satisfies the spec-in-Go oracle SpecSum",
 "spec":{"go_func":"SpecSum","lean_decl":"Go2Lean.Generated.gossa.overflow.f0"},
 "frontend":"gossa","source":"template"}

// Counterexample
{"id":"cx_sum_specsum_s42","property":"p_sum_specsum","frontend":"gossa","engine":"random","fuel":1000,
 "inputs":{"xs_ref":"0","xs_off":"0","xs_len":"4","xs_cap":"5"},
 "memory":{"xs":["1","9223372036854775806","9223372036854775807","0"]},
 "model_output":{"res":"ok","value":"-2"},"revalidated_in_lean":true}

// Verdict
{"counterexample":"cx_sum_specsum_s42","status":"confirmed",
 "evidence":{"cmd":"go test -count=1 -v -run '^TestGo2LeanPoC_cx_sum_specsum_s42$' ...","exit":0,"stdout_tail":"..."},
 "observed":{"ret":"-2"},"poc_dir":"out/poc/cx_sum_specsum_s42"}

// ProofResult
{"property":"p_clamp_specclamp","status":"bounded_proved","fuel":16,"tactic":"bv_decide",
 "lean_file":"lean/Go2Lean/Proofs/p_clamp_specclamp.lean","time_ms":3200}

// ModelGap
{"from_verdict":"cx_0050","frontend":"gossa","kind":"unmodeled-call",
 "site":"f.go:42 call strings.Index","suggestion":"model as length-bounded havoc"}
```

## 8. Repository layout and CLI

```
go2lean/
  cmd/go2lean/              CLI
  internal/contract/        §7 types, workspace IO, validation
  internal/repo/            locating the checkout and the lake project
  internal/ir/              frontend-independent program representation
  internal/frontend/gossa/  F-GoSSA
  internal/frontend/llvm/   F-LLVM (later)
  internal/ssaout/          Lean printer, ABI, model report, targets
  internal/constraint/      spec & search stage core: property templates, engine runners
  internal/agent/           agent loop, LLM interface (later)
  internal/poc/             replay stage
  lean/                     lake project `go2lean`, the core (Lean 4 core only; no Mathlib, no plausible)
    Go2Lean/SSA/Core.lean        the core
    Go2Lean/Dialect/GoSSA.lean   the GoSSA dialect
    Go2Lean/Search.lean          the engines
    Go2Lean/Driver.lean          the search driver (JSON in, JSON out)
    Proofs/                      proof prototypes (checked from a workspace)
  <workspace>/lean/         lake project `go2lean-models`, written by the compile stage
    lakefile.toml                requires the core by path
    Go2Lean/Generated/           compile-stage output (do not edit) and its Registry
    Main.lean                    the `go2lean-search` executable
    Search/                      per-property search files (scratch)
  examples/                 ground-truth packages
  docs/
```

Each workspace has its own lake project for its models, so runs in different
workspaces never share build state: the generated modules, the registry and
the `go2lean-search` executable are per workspace, and the core is built once
per version under a file lock (`.lake/go2lean-build.lock`, as is each
workspace's build): runs build and require a snapshot of `lean/`'s core
sources in `~/.cache/go2lean/core-<hash of their contents>`
(`$GO2LEAN_CACHE` overrides the directory; the four most recently used are
kept), so editing the checkout while a run is going makes a new snapshot
instead of rebuilding the core under the running searches. Before this split every run wrote its models into the
checkout's `lean/` and rebuilt one shared executable, so two runs at the same
time (an agent pass and a regression run, say) rebuilt each other's driver
mid-hunt and failed. The core library has no roots, only globs: a lake
library's default root claims every module under its prefix, and `Go2Lean`
would claim the workspaces' `Go2Lean.Generated.*`.

```
go2lean compile [-workspace out] <package>        compile stage: Go → Lean model, ABI, targets
go2lean props   [-workspace out]                  spec & search stage: template properties from the model report
go2lean hunt    [-workspace out] [-property id]   spec & search stage: random search for counterexamples
go2lean poc     [-workspace out] -counterexample id   replay stage: realise and verify
go2lean run     [-workspace out] <package>        all of the above
```

The Lean side depends on Lean 4 core only (`BitVec`, `bv_decide`, `Lean.Json`);
the random engine is our own so it can print JSON and avoid a dependency on
`plausible`'s output format. No Mathlib: build time and size are not worth it.

## 9. Ground truth and metrics

Each package under `examples/` contains buggy and correct functions, specs,
and expected outcomes.

| package | bug | expected |
|---|---|---|
| `overflow` | `Sum` wraps; `SpecSum` says non-negative inputs give a non-negative sum | `p_sum_specsum` confirmed; `p_sum_nopanic` none |
| `oob` | `Last` has an off-by-one (`i <= len`) | `p_last_nopanic` confirmed (index out of range); `Clamp` none |
| `divzero` | `Avg` divides by a caller-controlled count | `p_avg_nopanic` confirmed (divide by zero); `Ratio` none (`MinInt/-1` wraps, no panic) |
| `magic` | `Decode` indexes an empty payload only when `tag == 0x5EC4E7` | `p_decode_nopanic` confirmed; reachable only because literals are mined into the value pool |

Planned: `shift` (count ≥ 64 / negative count), `lenfield` (untrusted length
driving `make`/slicing), `alias` (writes through aliased slices; GoSSA stage B only).

Metrics: counterexample recall, spurious rate, unknown rate, proof rate,
crosscheck agreement, end-to-end time. All of `examples/` runs on every
change to the compile stage's modelling rules.

## 9b. Runs on real code

The runs on tingly-box (the smoke test, the seven full passes and what each
changed in the model) and the benchmark of bugs found by hand are the
development record: [history.md](history.md) §9b and §9c. The current
numbers are in the README; what they do not show is in
[limitations.md](limitations.md).

## 10. Milestones

- **Done — end-to-end PoC slice.** Contract and workspace; Lean SSA core;
  GoSSA stage A; random engine with mined literals and value profiles;
  exhaustive small-domain engine; go/ssa frontend (package test variants);
  property templates (`no_panic`, spec-in-Go `postcondition`); the replay stage harness,
  runner and verdicts; agent loop (propose → repair → run → report) behind a
  mockable LLM interface; four ground-truth packages confirmed end to end
  (~2–5 s per package).
- **M1b — `bv_decide` engine.** simp lemma set unfolding `run` for fixed fuel,
  counterexample extraction and re-validation, `ProofResult` for bounded proofs.
- **M2a — agent.** Property proposal over the model report, the engine
  ladder, verdict feedback, `ModelGap` records.
- **M1c — F-LLVM stage A + crosscheck.** TinyGo spike (resolve every
  "unverified"), llir parsing, LLVM dialect, cross-check command.
- **M1d — GoSSA heap (stage B).** **M2b — `custom` properties and the proof agent.**
- **Later:** LLVM byte memory, a second LLVM language, interfaces/closures, concurrency.

## 11. Risks

| risk | effect | mitigation |
|---|---|---|
| `simp`-unfolded `run` blows up for `bv_decide` | loop properties limited to random search | small fuel; loop invariants go to the proof agent; keep `denote` branch-free so unfolding stays linear |
| go/ssa semantic details (shifts, conversions, string indexing) | model ≠ Go | ground truth + replay stage + cross-check |
| TinyGo IR structure destroyed by optimisation | LLVM route unreadable | low `-opt`, compare pipeline stages; it is the secondary route |
| spec functions outside the subset | property cannot be compiled | the model report flags the spec's status; the agent rewrites it |
| counterexample parsing tied to Lean output formats | breaks on upgrade | our own JSON-printing driver + P4 |
| `custom` properties with inconsistent forms | false positives/negatives | flagged, tried last, every `confirmed` still goes through the replay stage |
| rejected callee | callers hit `modelStuck` | the call's fallback block havocs the result and the caller goes on; the model report lists the rejection reason |

## 12. Decisions

Decided:
- Dual frontends over one Lean SSA core; `go/ssa` first, LLVM second as
  cross-check and multi-language entry.
- Deep embedding with a denotation, not per-frontend shallow emitters.
- Stage A slices are read-only; the heap model is a later milestone.
- A read-only memory oracle, shared by all functions in a run, instead of
  site-keyed havoc for loads (v0.3).
- No Mathlib, no `plausible`; Lean core plus our own engine.
- LLVM semantics are VeIR's, required by lake at a pinned revision in a
  separate package (`lean-llvm/`); the Go line stays core-only (2026-10-21).
- All documentation and code comments in English.

## 13. References

- lean-mlir (SSA theory and LLVM dialect semantics in Lean; the source of the core/dialect split): https://github.com/opencompl/lean-mlir
- `golang.org/x/tools/go/ssa`: https://pkg.go.dev/golang.org/x/tools/go/ssa
- TinyGo calling convention (slices, strings, interfaces expanded to scalars): https://tinygo.org/docs/concepts/compiler-internals/calling-convention/
- TinyGo data type layout: https://tinygo.org/docs/concepts/compiler-internals/datatypes/
- TinyGo compiler pipeline: https://tinygo.org/docs/concepts/compiler-internals/pipeline/
- Lean 4.12 `bv_decide` (built-in CaDiCaL, counterexample assignments): https://lean-lang.org/blog/2024-10-3-lean-4120
- VeIR (LLVM integer and memory semantics in Lean, core library only): https://github.com/opencompl/veir
- go-llvm (Go bindings to the LLVM C API, tinygo): https://github.com/tinygo-org/go-llvm
- llir/llvm (pure-Go LLVM IR parser; no opaque pointers, not used): https://github.com/llir/llvm
