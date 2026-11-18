# The LLVM line on cJSON (2026-11-17)

The first run of the LLVM line on a real C library, and of `go2lean agent`
on C (#269, #270). [cJSON](https://github.com/DaveGamble/cJSON)
(13k stars, `6d9f244`, v1.7.19) is a JSON parser and printer in one file,
with a history of CVEs and years of OSS-Fuzz behind it. Each step below
fixed what the previous run showed.

## The library's API, searched for undefined behaviour

`go2lean llvm cJSON.c`, its 79 externally visible functions:

| | first run | typed inputs, API only | + heap, hooks | + string functions |
|---|---|---|---|---|
| exhausted | 6 (of 113) | 35 | 45 | **65** |
| held (random) | 39 | 39 | 11 | 11 |
| confirmed | 25 | 0 | 2 | 1 |
| inconclusive | 41 | 5 | 18 | 1 |
| spurious | 2 | 0 | 3 | 1 |

What each step changed:
- **The first run's 25 confirmed findings were forged inputs.** With LLVM's
  opaque pointers a parameter is only `ptr`, so `cJSON_Delete(cJSON *item)`
  got six integers as its `item`, and ASan reported overflows no caller can
  cause.
- **Typed inputs** now come from the debug information. A pointer to a
  struct is built member by member, a `char *` is a C string unless a
  length follows it, and a `T **` is a pointer slot.
- **API only:** the ten findings left after that were all in static
  functions called with arguments no caller passes. Only the API is
  searched by default now; `-internal` adds the rest.
- **The heap:** `malloc`, `calloc`, `realloc` and `free`, also behind
  function pointers, since cJSON allocates through its hooks table. A use
  after free and a double free are undefined behaviour.
- **The string functions:** `strlen`, `strcmp` and their kin, byte by byte.

The one confirmed result left, `cJSON_CreateStringArray(strings, count)`,
is an input shape the generator does not build yet: an array of `count`
pointers.

## Specs written by the agent

`go2lean agent cJSON.c` with guidance for three properties:
- a round trip (parse, print, parse again: an equal tree), restricted to
  JSON without numbers, which go through `sprintf`;
- memory safety of the parser on any bytes;
- the getters agreeing with the tree.

The agent wrote six specs, and clang accepted the file the first time.

**A planted bug tests the specs.** In a copy of cJSON, the printer writes
`\'` for a quote, which is invalid JSON.
- **The first spec file did not catch it.** The spec itself was right: run
  natively on the right input it returns 0. But it indexed 22 input pieces
  with `% 22`, and the search draws mostly small integers (0 to 16) and the
  program's constants, so the pieces with escaped quotes were almost never
  drawn. The agent's prompt now asks for piece tables of at most 16 entries
  and for inputs valid by construction.
- **The second spec file still did not catch it.** The model got stuck in
  `parse_string` on `ptrtoint` (a pointer difference), guessed the parser's
  result, and reported violations that the replay found spurious.
  `ptrtoint` is now modelled, and a finding prints what it rests on.
- **Then the bug was found** by two specs, each confirmed by the replayed C
  program: `spec_roundtrip_string` by the random engine, and
  `spec_roundtrip_nested` by the exhaustive one.

On the real cJSON the same six specs hold, with no false positive. Two are
**exhausted**: the round trip holds on every input of their grammars, 729
strings built from escape pieces and 248,832 small nested arrays and
objects.

## What this says

The LLVM line now runs a real C library through its API without forged
inputs. Specs written in C by the agent check a property, here a round
trip, that crash-oriented fuzzing does not state. And the method found a
planted printer bug once the model followed the parser's pointer
arithmetic. No real defect was found in cJSON, which is as expected for a
library this fuzzed.

Two lessons for the next targets:
- **The search's integer distribution shapes the specs:** small tables, and
  inputs valid by construction.
- **A violation resting on an assumption must say so:** the model's guess
  at a stuck callee looked like a finding.

libFuzzer on the same specs (L2, #270) would cover what the model's search
draws rarely, as Go's fuzzer does on the Go line.
