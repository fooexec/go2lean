# Symbolic execution (2026-11-21)

`go2lean symexec` ([usage](../usage.md#symbolic-execution)) is a bounded
symbolic executor for Go functions over `go/ssa`, with Z3 as the solver. It
exists because the engines of the search stage run concrete inputs: a random
search with mined constants, an exhaustive search over small domains, and
Go's fuzzer on the exported specs. None of them solves an equation, so an
input that must satisfy `x*K == C`, or two linear equations at once, is found
only by luck. The Lean proof route does solve them (`bv_decide`, 0.5 s for
`x*2654435761 == 0xdeadbeef`), but extracting a bitvector formula from the
Lean model of a function took over eight minutes for ten lines, and the
residual goals of `wp_auto` are not in a form `bv_decide` takes; the design
note's last rung (SMT export, no certificate, because the replay stage is the
certificate) is what this is.

## The benchmark

`testdata/symexecbench` has twelve functions that panic only on a hard input:
B01 a multiplicative hash that must match, B02 an 8-bit checksum, B03 a length
field equal to the rest of the packet, B04 an overflowing size, B05 four bytes
spelling a magic number, B06 a rotate-and-xor mix equal to a constant, B07 two
linear equations on two `uint16`, B08 six bytes summing to 1000, B09 a
five-byte prefix, B10 three fields xoring to a constant with an order
condition, B11 a divisor that is a product of three bytes minus a constant,
B12 an off-by-one on a computed offset. I wrote them to be hard for a fuzzer,
so the table says what the engines do on this kind of function, not how often
the kind occurs in real code.

| engine | found | time |
|---|---|---|
| Go's fuzzer, 25 s per function | 6 of 12 (B02, B03, B08, B09, B11, B12) | 5 min |
| go2lean `run` (random with mined constants, exhaustive, mutation), 20 s per property | 2 of 12 (B04, B10) | 4 min |
| the two together | 8 of 12 | |
| `symexec` | 12 of 12, each reproduced on the real code | about 1 s per function |

The four that only `symexec` found are B01, B05, B06 and B07. B08 and B12 need
`-maxlen 24` (the default slice bound is 12, and the failing inputs need 6 and
16 elements); `testdata` test `TestBench` runs the twelve.

## On real code

- quic-go `quicvarint`, `internal/protocol`, `internal/wire`: the executor
  reaches the explicit panics for out-of-range values that the functions
  document (`Append` for values above 2^62) and negative lengths passed as
  `int` (`ParseShortHeader` with `connIDLen = -18`): reproduced, and not
  defects, since no caller passes them. Candidates through calls the executor
  does not model (the random position in `GetGreasedVersions`, the library
  code under `GenerateConnectionID`) did not reproduce.
- snappy `Decode`, `Encode`: 83 paths of `Decode`, no reproduced panic.

So far it has found no defect in real code that the other engines missed.
What it adds is a class of inputs, and the benchmark says the class is not
small for the engines already here.

## What it does not do

- Parameters are integers, booleans and slices of integers. A struct, a
  pointer, a map, a string, an interface or a float makes the function
  unsupported; fields, `append` and library calls are havoc values.
- Loops are unrolled to a bound and paths are capped (2000 paths, 30 s per
  function), so it never says that a function cannot panic.
- Concurrency, time and I/O are not modelled.
- Explicit `panic` calls that guard documented preconditions are reported
  like any other panic; the ranking of the other stages does not apply yet.
