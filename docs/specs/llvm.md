# Languages compiled to LLVM (planned)

*Not built yet. This is the plan for checking the same catalog on C, C++,
Rust and other languages go2lean reads as LLVM IR (`go2lean llvm`).*

## Binding

A spec is written in the target's own language, next to the code it
checks, and compiled with it into the same IR. go2lean sees one more
function that returns a bool, whatever language it came from.

| language | spec function | marker |
|---|---|---|
| C, C++ | `bool spec_f(args..., ret)` | `__attribute__((annotate("go2lean:spec f")))` |
| Rust | `fn spec_f(args..., ret) -> bool` | `#[go2lean::spec(f)]`: a procedural macro that emits `#[no_mangle]` and the same annotation |
| any LLVM language | a function returning `i1` | an entry in `llvm.global.annotations` naming the target |

The marker always ends as an entry in `llvm.global.annotations`, which is
the one thing go2lean reads.

The two checking tools mirror the Go binding:
- **Fuzzer:** libFuzzer, which every LLVM language can link. It is to this
  line what `go test -fuzz` is to Go.
- **Replay:** a C harness that calls the target and the spec through the
  C ABI, at first. Each language's own test runner (`cargo test`, CTest)
  comes later.

A general property from the catalog is then checked as on Go: the agent
reads the property file and writes the spec in the target's language.

## Properties of their own

| property | applies to | oracle |
|---|---|---|
| no out-of-bounds read or write | C, C++, `unsafe` Rust | AddressSanitizer on the replay |
| no use after free, no double free | C, C++, `unsafe` Rust | AddressSanitizer |
| no undefined behaviour (signed overflow, misaligned access, invalid shift) | C, C++ | UndefinedBehaviorSanitizer |
| no panic across an FFI boundary, no unwinding out of `extern "C"` | Rust | the replay's abort |
| `Send` and `Sync` hold for the types that claim them | `unsafe` Rust | ThreadSanitizer on a two-thread replay |
| no data race | C, C++, Rust | ThreadSanitizer |

The Go properties in [go.md](go.md) (goroutines, channels) do not apply
here. Their nearest kin, threads and locks, are covered by
ThreadSanitizer.

## First prototypes

A Rust project and a C project, each with a published advisory in one of
the catalog's general families. The method is the one validated on Go:
- give the advisory as guidance;
- run on the vulnerable version, the fixed versions and the latest;
- then move the property to similar projects.
