# The LLVM line on cJSON (2026-11-17, advisory specs 2026-11-18)

The first run of the LLVM line on a real C library, and of `go2lean agent`
on C (#269, #270). [cJSON](https://github.com/DaveGamble/cJSON)
(13k stars, `6d9f244`, v1.7.19) is a JSON parser and printer in one file,
with a history of CVEs and years of OSS-Fuzz behind it. Each step below
fixed what the previous run showed.

## The library's API, searched for undefined behaviour

`go2lean llvm cJSON.c`, its 79 externally visible functions:

| | first run | typed inputs, API only | + heap, hooks | + string functions | + floats |
|---|---|---|---|---|---|
| exhausted | 6 (of 113) | 35 | 45 | 65 | **74** |
| held (random) | 39 | 39 | 11 | 11 | 3 |
| confirmed | 25 | 0 | 2 | 1 | 2 |
| inconclusive | 41 | 5 | 18 | 1 | 0 |
| spurious | 2 | 0 | 3 | 1 | 0 |

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
- **Floats:** `float` and `double` as IEEE bit patterns, which
  `cJSON_CreateNumber` and the number paths need. This column was measured
  after a correction: a run that read the result of an unmodelled call
  (`strtod`) no longer counts towards exhausting a domain.

Neither confirmed result left is a defect. Both come from inputs no caller
builds with the API:
- `cJSON_CreateStringArray(strings, count)` needs an array of `count`
  pointers, an input shape the generator does not build yet.
- `cJSON_DetachItemViaPointer(parent, item)` was given an `item` that is
  not a child of `parent`. Its replay had first failed to build: two
  pointer parameters declared the same objects, a harness bug now fixed.

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

## Specs from one advisory, across versions (L3)

The method of the Go line's [advisory specs](cve-specs.md), on C (#271).
`go2lean agent -guidance` got one advisory, CVE-2023-50472 (a crash in
`cJSON_SetValuestring`, v1.7.16), stated as the general property of
[c-api-memory.md](../specs/c-api-memory.md). The agent ran on v1.7.16 and
wrote seven specs. The same file then ran unchanged on v1.7.17, v1.7.18 and
HEAD (`6d9f244`, after v1.7.19), with `go2lean llvm -specs-only`.

| spec | v1.7.16 | v1.7.17 | v1.7.18 | HEAD | what it is |
|---|---|---|---|---|---|
| `setvaluestring_null` | confirmed | confirmed | held | held, libFuzzer 47M runs | `SetValuestring(item, NULL)`: `strlen(NULL)`. CVE-2024-31755, fixed in v1.7.18 |
| `insert_null_item` | confirmed | exhausted | exhausted | exhausted | `InsertItemInArray(a, i, NULL)` on a non-empty array. CVE-2023-50471, fixed in v1.7.17 |
| `setvaluestring_alias` | confirmed | confirmed | confirmed | violated | `strcpy` with overlapping strings (cJSON#881), fixed in v1.7.19 to return `NULL`, which this spec does not expect |
| `replace_alias_key` | confirmed | confirmed | confirmed | confirmed | `ReplaceItemInObject(o, item->string, item)` frees the key, then copies it: a use after free. Open: cJSON#1081, PR #1083 |
| `add_cs_alias_key` | confirmed | confirmed | confirmed | confirmed | the same aliasing through `AddItemToObjectCS`, whose key must outlive the item: the spec's mistake |
| `setvaluestring_types` | exhausted | exhausted | exhausted | exhausted | every constructor's item |
| `detach_delete` | held | held | held | held, libFuzzer 35M runs | detach and delete by index, key and NULL |

What this shows:
- **One advisory led to two other CVEs.** The advisory's own crash needs
  an item whose string is NULL, which no spec built. But the spec written
  for it passes NULL as the new string: CVE-2024-31755, found by others a
  year later in v1.7.17. A second spec covered CVE-2023-50471, the
  advisory's sibling.
- **Each spec holds from its fix on.** `insert_null_item` is exhausted from
  v1.7.17 on: no input of its domain crashes. `setvaluestring_null` parses
  documents with numbers, which go through `strtod`. The model does not run
  `strtod`, so it can only say the spec held; libFuzzer, which runs it,
  found nothing in 47 million runs on HEAD.
- **An open defect, re-found.** On HEAD the use after free in
  `ReplaceItemInObject` is still there. It is reported upstream, with a fix
  waiting.
- **Two results are the specs' own.** One spec breaks the documented
  contract of `AddItemToObjectCS`. The other expects a result that
  v1.7.19 changed on purpose. Both are told apart by reading the
  documentation and each version's change, not by the tool.

**A second chain, on the parser.** The guidance was CVE-2019-11834 only
(an out-of-bounds read on a NUL in a string literal, fixed in 1.7.11), as
the property of [parser-bounds.md](../specs/parser-bounds.md). The agent
ran on v1.7.17 and wrote nine specs over `cJSON_Parse`,
`cJSON_ParseWithLength`, `cJSON_ParseWithLengthOpts`, `cJSON_ParseWithOpts`
and `cJSON_Minify`.

| spec | v1.7.17 | v1.7.18 | HEAD |
|---|---|---|---|
| `parse_grammar_truncated`, `_opts` | confirmed: heap over-read in `parse_string` (`cJSON.c:786`), one byte past a buffer of exactly `len` bytes | held | held |
| `parse_nul_in_string` | held | held | held, libFuzzer 21M runs |
| `parse_cstring` | held | held | held, libFuzzer 13M runs |
| `minify_pieces` | exhausted (20,736) | exhausted | exhausted |
| four more (raw bytes, options) | exhausted | exhausted | exhausted |

On HEAD the two truncation specs also held 13 million libFuzzer runs each.

The over-read is CVE-2023-53154, fixed in v1.7.18. As with the first
chain, the spec for an old advisory found a later one. The specs that
found it build a value from a small grammar and cut it at every length;
the raw-byte specs never reached that check.

**Floats were what made the second CVE reachable.** On the first run on
v1.7.16, `insert_null_item` held, with 420 of 2,000 trials stuck. It
builds its array with `cJSON_CreateNumber(double)`, whose float compares
and conversions the model did not have. Floats are now modelled as IEEE bit
patterns, computed with Lean's `Float` and `Float32`
(`examples/llvm/float.c`). Afterwards no trial is stuck, and the spec finds
CVE-2023-50471.

## libFuzzer on the same specs (L2)

`go2lean llvm -fuzz 60s` runs libFuzzer on every function the model search
did not settle:
- it held without exhausting its domain;
- trials were stuck, or it rested on unmodelled calls;
- its violation did not reproduce in the real program.

The target decodes the spec's parameters from the fuzzer's bytes, builds
with ASan and UBSan, and calls the spec. A crash input is run once more and
recorded with its verdict. On cJSON HEAD, the six specs above that the model
could not settle held 13 to 47 million runs each in a minute.
`examples/llvm/fuzz.c` shows the other direction:
- The model guesses `strtol`'s result. Its violation does not reproduce, so
  it is spurious.
- The fuzzer runs the real `strtol` and finds the negative index in under
  20,000 runs.

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

The advisory run adds a third lesson: **a spec written from one advisory
finds that advisory's siblings and successors.** The C line now does what
the Go line did on chi, echo and fiber.
