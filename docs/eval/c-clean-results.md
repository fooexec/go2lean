# C libraries with no finding (2026-11-18 to 11-19)

Three C parsers searched with the LLVM line's memory mode and libFuzzer,
and nothing found. They are written down so that nobody repeats the work
and so that the choice of the next target rests on what they show.

All were run in `-memory` mode (signed arithmetic wraps; AddressSanitizer
and the array-bounds checks; a finding names its memory-error class), with
specs written by `go2lean agent` from a stated property, on the default
branch at the commit named below.

| library | commit | what the agent was given | model | libFuzzer |
|---|---|---|---|---|
| [json-parser](https://github.com/json-parser/json-parser) (1,474 lines) | `9d50ae5` | its own three past out-of-bounds reads (#68, #74, #78: a literal or a `\u` escape cut off at the end of the buffer) | stuck in `sprintf`, `pow`, `ctype` at first; not stuck after `ctype` was modelled, but its number path still is | 4 specs run with fuzzing: 32M to 51M runs each in 90 s, then 63M to 98M each in 180 s |
| [parson](https://github.com/kgabis/parson) (3,700 lines) | `ec53fb6` | the same property for a NUL-terminated string, and the getters | 0 of 2,000 trials stuck, after `ctype`, `strtod` and `errno` were modelled; passes through the number path | 5 specs, 7M to 27M runs each |
| [Little-CMS (lcms2)](https://github.com/mm2/Little-CMS) (38,000 lines, 27 files) | `914ab50` | an ICC profile as untrusted input, from the project's own issue history (tag-table offsets and sizes, multi-localized strings, lookup-table stages, text parsers), plus two consistency properties | 1,262 functions, 0 of 200 trials stuck | 3 specs, 1.6M to 2.3M runs each in 40 s; 5.2M to 5.6M in 120 s on the raw-tag spec |

The lcms2 specs:
- **a profile read from an untrusted buffer** and everything the API does
  with it: `cmsReadTag` and `cmsReadRawTag` on every tag, the string
  getters;
- **raw and cooked access agree on bounds** (the direction of lcms2 #573);
- **a profile written out and read back** has the same tag count.

Each builds a profile from a grammar: the 128-byte header, a tag
directory, and a table of 16 tag payloads, among them a `curv` that claims
65,536 entries and has two, a `desc` whose length is `0xFFFFFFF0`, an
`mluc` record whose offset is past the end and one whose length is
`0xFFFFFFFF`. The buffer is allocated at exactly its final size.

## What this says

- **The tool works on these libraries.** The lcms2 model has 1,262
  functions and runs without getting stuck. The fuzzer runs tens of
  thousands of inputs a second. The same method found real defects in
  cJSON and stb_image, so a clean result here is not a gap in the tool.
- **The libraries are clean for this property.** lcms2's tracker shows a
  steady stream of memory defects through 2026; the ones read for this
  note are closed, and head is the state after them. lcms2 is also in
  OSS-Fuzz (as `lcms`).
- **Memory safety on an untrusted input is the property crash-fuzzing has
  already worked hardest.** The one defect here that a crash-guided fuzzer
  cannot state is a contract: `URI.Path()` is not idempotent in fasthttp
  (a normalized path normalized again changes). The cJSON CVEs were crashes
  a fuzzer can reach; the method's part there was that one advisory's spec
  led to its siblings.

## The next target

Pick a library for a property a fuzzer cannot state, not for a long
history of memory bugs:
- a **round trip**: `decode(encode(x)) == x`, or parse, serialize, parse;
- an **idempotence**: normalizing twice is normalizing once;
- an **agreement** of two functions the documentation relates (a cooked and
  a raw accessor; a size function and the writer it sizes).

Libraries where one of these is stated in the documentation and the code is
hand-written are the likely yield: encoders and decoders (base64, UTF-8
and UTF-16 converters, percent-encoding, compression containers), and
parsers with a serializer.

A note on how targets were picked: whether a project is in OSS-Fuzz was
checked against the project list (google/oss-fuzz, `projects/`), not
guessed. Names do not always match (lcms2 is `lcms` there), so a first
match that finds nothing is not evidence of absence.
