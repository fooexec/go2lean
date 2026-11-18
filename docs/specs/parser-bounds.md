# A parser reads only the bytes it was given

## Applies to

Language layer: C and C++, and `unsafe` Rust. Checked through the LLVM
binding ([llvm.md](llvm.md)). Its general form, that a parser accepts
only what its grammar allows, belongs to every language.

## Property

Every parse entry point reads no byte before the start of its input, and
none at or past its end:
- for a buffer and a length, not the byte at `buf[len]`, even when the
  buffer has no terminator;
- for a C string, nothing past its terminator.

This holds for every input, valid or not:
- strings cut off at the end;
- escapes (`\u` sequences, surrogate pairs) cut off at the end;
- a NUL inside a string literal;
- unterminated comments;
- a number at the end of the buffer.

## Threat model

The attacker controls every byte of the input and its length. Nothing
else: the program calls the parser as documented.

## Oracle

AddressSanitizer on the replay. The spec copies the input into a buffer of
exactly `len` bytes (`malloc(len)`, no terminator), parses it, frees the
result, and returns 1. Any read outside that buffer is the violation.

## Inputs

- A small grammar: a value kind, then its parts (a string's characters and
  escapes, an object's key, a nested array).
- The input cut at every length up to its end: a truncated input is where
  the bounds checks are.
- Raw bytes, for inputs no grammar produces.

## Pitfalls

- **A NUL-terminated copy hides the bug:** a spec that passes `strlen(s)`
  of a C string gives the parser a terminator to stop on. Copy into a
  buffer of exactly `len` bytes.
- **Random bytes rarely reach the deep cases:** most of them are rejected
  at the first byte. The cut-off grammar reached `parse_string`'s end check,
  and the raw-byte specs never did.

## Source

- cJSON CVE-2019-11834 (out-of-bounds access with `\x00` in a string
  literal, before 1.7.11), the only guidance given.
- The specs written from it re-found CVE-2023-53154 (`parse_string`'s
  over-read in `cJSON_ParseWithLength`, fixed in 1.7.18). See
  [the cJSON report](../eval/cjson.md).
