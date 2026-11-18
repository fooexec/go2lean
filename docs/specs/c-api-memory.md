# A C library's API is memory safe on what the API produces

## Applies to

Language layer: C and C++, and `unsafe` Rust behind a safe API. Checked
through the LLVM binding ([llvm.md](llvm.md)).

## Property

No function of the library's public API crashes, or reads or writes memory
it does not own, on arguments a caller can make with the API itself:
- any object the API creates (its constructors, its parser, the parts of
  a parsed result), of every kind, with the fields the API leaves null or
  empty;
- null where the function documents null or visibly handles it (it
  returns null, false, or does nothing);
- any C string, including one that aliases or overlaps a string the
  object already holds.

A function that takes ownership or frees memory must not then read what it
freed, even when the caller passes a pointer into it.

## Threat model

The attacker controls the data the program feeds the library: the bytes a
parser reads, and the strings the program copies from a request into the
library's objects. The attacker does not control the program's calls. So
a crash on null is a defect only where the program can plausibly pass
null, such as an earlier call's failed result; an aliased argument only
where the program can plausibly pass it, such as an object's own key or
value.

## Oracle

The replay under AddressSanitizer and UndefinedBehaviorSanitizer: a spec
builds its objects through the API, calls the function, and returns 1.
Any sanitizer report is the violation. Where the documentation states a
result (`NULL` on failure, the new string), the spec also checks it.

## Inputs

- A table of at most 16 documents the parser accepts, each valid by
  construction, and an index into a parsed result's children.
- Each constructor of the API, chosen by a small integer.
- Null, and C strings of up to 16 bytes.
- The object's own strings, as the argument (aliasing), or offset into
  them (overlap).

## Pitfalls

- **A contract the API states:** cJSON's `AddItemToObjectCS` takes a key
  that "will definitely survive the cJSON object". A spec that passes the
  item's own key, which the call frees, is the spec's mistake. The same
  aliasing through `ReplaceItemInObject`, which copies its key, is the
  library's defect.
- **A contract that changed:** cJSON 1.7.19 makes `SetValuestring` return
  `NULL` on overlapping strings. A spec that expects the new string fails
  there by design. Check the expected result against each version's
  documentation.
- **Unmodelled code in the spec:** before floats were modelled, a spec that
  called `cJSON_CreateNumber` was stuck in it and missed CVE-2023-50471.

## Source

- cJSON CVE-2023-50472 (`cJSON_SetValuestring`, v1.7.16), the only
  guidance given.
- The specs written from it also re-found CVE-2024-31755 and
  CVE-2023-50471, and the known, unfixed use after free of cJSON#1081. See
  [the cJSON report](../eval/cjson.md).
