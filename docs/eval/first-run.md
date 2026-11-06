# First run on unseen projects (2026-11-06)

go2lean run as a new user runs it, following the README's quick start:
`go2lean doctor`, then `go2lean run ./...` with the default flags, Go
1.26.6, in a clone of a project go2lean had not been tried on. Four
projects, chosen for different kinds of code. The tool was `fb8d042` (#168).

| project | kind | time | result |
|---|---|---|---|
| [dustin/go-humanize](https://github.com/dustin/go-humanize) `f5b54ae` | formatting of numbers and times | 130 s | 12 counterexamples: 4 confirmed with a PoC, 8 spurious |
| [hashicorp/golang-lru](https://github.com/hashicorp/golang-lru) `2bb2909` | generic caches, a mutex | 1 s | no property, and no reason given |
| [spf13/cast](https://github.com/spf13/cast) `d8e892d` | conversions through `any` and `reflect` | 129 s | none |
| [tidwall/gjson](https://github.com/tidwall/gjson) `690362d` | JSON path queries over strings | 152 s | 33 counterexamples: 28 confirmed, 5 spurious |

## What a user met

- **`doctor` said FAIL.** The machine's Go was 1.24.7, and `GOTOOLCHAIN=go1.26.6` selected the Go 1.26 that every run then used. `doctor` checks the local toolchain only, so as not to download one, and reported Go as missing.
- **`run` does not translate dependencies by default.** Without `-deps`, the standard-library models translated from source (`math`'s portable code, `strconv`, `slices`, the encodings) and the shims (the environment, `crypto/rand`, loopback networking) are off. 6 of go-humanize's 8 spurious counterexamples rest on `math.Log`, `math.Pow` and `math.Floor`, the other 2 on `math/big` and `time`. With `-deps` the run took 61 s instead of 130 s and kept the same 4 confirmed findings. Its spurious set changed rather than shrank (8 to 10): the `math` ones went away. New ones came from `strconv.FormatFloat`, which `-deps` translates and whose `genericFtoa` the model gets stuck in. `math/big` and `time` are still unmodelled.
- **The last line was "nothing to verify".** A run over `./...` prints a section per package, and go-humanize's last package had no counterexample. The 4 confirmed findings were above, in the first package's section.
- **Generic packages were skipped silently.** golang-lru's API is generic (`LRU[K, V]`), and generic functions get no properties: the run compiled four packages and ended with "nothing to verify".
- **Most confirmed findings were not where to start.** Of gjson's 28 confirmed panics, 2 are at tier 1 (plain inputs to the package's API: `Result.Path` and `Result.Paths` on a `Result` whose `Index` field is arbitrary). 22 are at tier 2 (unexported functions called with inputs their callers may never pass). The ranking was there, in `go2lean summary`, but not in `run`'s output.

go-humanize's confirmed findings are real panics on unusual inputs:
- `FormatFloat` and `FormatInteger` panic on an invalid format string ("invalid positive sign directive");
- `CustomRelTime` and the unexported `humanateBytes` index with -1.

## Fixed (#169)

- `run` and `compile` default to `-deps`.
- `run` ends with a summary of every package's findings: the counts, then the tier-1 findings with where they panicked and their PoC. The full ranked list goes to `summary.md`.
- The compile stage names the generic functions and types it does not search.
- `doctor` accepts an old local Go when `GOTOOLCHAIN` selects a Go 1.26 that is already downloaded, and otherwise says what `GOTOOLCHAIN` would select.

## Open

- `strconv.FormatFloat` translated from source: the model gets stuck in `internal/strconv.genericFtoa`.
- `math/big` and `time`'s `Sub`, `After` and `Now` arithmetic are unmodelled; go-humanize's `BigComma`, `RelTime` and `Time` rest on them.
- Generic functions: a property needs concrete type arguments. Instantiating a generic API at a simple type (`int`, `string`) would give golang-lru's caches properties.
