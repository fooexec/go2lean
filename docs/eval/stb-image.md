# The LLVM line on stb_image (2026-11-18)

The second C library of the advisory method (#271), after
[cJSON](cjson.md). [stb](https://github.com/nothings/stb) (35k stars) is a
set of single-file C libraries; `stb_image.h` (7,800 lines) decodes PNG,
JPEG, BMP, GIF, PSD, TGA, HDR, PIC and PNM from memory, and is built into
a great many programs. Its README says that security bugs are discussed in
public issues. All results are on the default branch, `master` at
`9600522` (2026-11-12), and were checked on `dev` at `3db7872`
(2026-11-17).

The library was compiled as one C file: `#define STB_IMAGE_IMPLEMENTATION`,
`STBI_NO_STDIO` (no file paths) and `STBI_NO_SIMD`. With `STBI_NO_SIMD` the
model runs the portable C decoder, which is also what stb_image uses on
every platform without SSE2 or NEON (WebAssembly without SIMD, RISC-V,
ARM builds without `STBI_NEON`).

## What the model needed first

The API search (`go2lean llvm stb_image.c`) showed three gaps, each fixed
before the specs ran:
- **`inttoptr`.** Every `stbi_load*_from_memory` trial was stuck.
  `stbi__errpuc` returns NULL through an integer, and the JPEG decoder
  aligns its buffers with `((size_t) p + 15) & ~15`. An address is now a
  pointer into the live object that holds it, and 0 is null
  (`examples/llvm/align.c`).
- **Out parameters.** `stbi_load_from_memory(buf, len, int *x, int *y, ...)`
  got its `int *` as byte buffers of 0 or 1 bytes, and ASan reported
  overflows that no caller can cause. A pointer to one scalar with no length
  after it is now one object of its type, never null.
- **The callbacks API** (`stbi_load_from_callbacks`) is given a struct of
  function pointers, which the generator leaves null; its crashes are
  forged inputs, like cJSON's first run.

## Specs from one advisory

The guidance was one fixed report, nothings/stb#1225 (an out-of-bounds read
in the PNM reader, fixed in 2.28), stated as the general property of
[parser-bounds.md](../specs/parser-bounds.md). The property was widened to
"reads only the bytes it was given, writes only what it allocated", for
every format and every requested channel count.

**First round: specs with undefined behaviour of their own.** The agent
wrote eight specs, one per format and one on zlib, each building a small
file from a grammar. Seven reported signed overflow, all in the specs'
own arithmetic (`2 * w` on an unbounded parameter), none in stb_image.
go2lean now tells these apart: when the sanitizer's report starts in the
spec's own body, the verdict is `ub_in_spec`, the spec's error, not a
finding. The agent gets those reports and one more round, and its prompt
asks it to bound each parameter before arithmetic.

**Second round:**

| spec | result |
|---|---|
| `jpeg_memory_safe` | **confirmed: signed integer overflow in the IDCT** (`stbi__idct_block`, `stb_image.h:2563`) |
| `png`, `bmp`, `tga`, `gif`, `psd`, `pnm` | held (2,000 model trials each) |
| `zlib_memory_safe` | exhausted (480 inputs) |

## The defect: signed overflow in the JPEG IDCT

A 200-byte baseline JPEG, which the SSE2 build decodes without error,
overflows `int` in the second, horizontal pass of the scalar IDCT:

```
stb_image.h:2563:7: runtime error: signed integer overflow: -1958860 * 2217 cannot be represented in type 'int'
    #0 stbi__idct_block stb_image.h:2563
    #1 stbi__parse_entropy_coded_data stb_image.h:3060
```

The file is valid in form:
- 8-bit quantisation tables;
- standard Huffman tables;
- a restart interval;
- four by five pixels, three components.

After dequantisation and the first pass, a column value near -2 million
is multiplied by `stbi__f2f(0.5411961f)` (2217). That overflows `int`,
which C makes undefined behaviour.

It is found on `master` and on `dev`. It is missed by stb's own fuzzers,
local and OSS-Fuzz (`tests/local_fuzz/image.c`, `tests/ossfuzz.sh`):
- they build without `STBI_NO_SIMD`, so on x86 they run the SSE2 IDCT,
  whose 16-bit arithmetic wraps by definition;
- the scalar IDCT is never fuzzed under UBSan.

The model runs the portable code, so it reached it.

**Severity: low.** The overflow is undefined behaviour on a crafted file:
- what compilers emit today wraps, and the damage is wrong pixel values;
- it is not a memory error;
- an optimiser may assume it does not happen, so the result is not
  guaranteed.

The draft report is in [upstream reports](../blog/upstream-reports.md).

## libFuzzer on the same specs

The six specs that held in the model search are running under
`go2lean llvm -specs-only -fuzz 120s`; their numbers will be added here.
