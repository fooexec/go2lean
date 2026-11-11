# Second run on unseen projects (2026-11-07)

The same protocol as the [first run](first-run.md): `go2lean run ./...` with
the default flags (now `-deps`, a one-hour cap per project), Go 1.26.6, in a
clone of a project go2lean had not been tried on. Seven projects, chosen for
different kinds of code. The tool was the branch after #169; the fixes below
were made during the run, and the projects they concern were rerun after
them.

| project | kind | time | properties | confirmed (tier 1) | spurious | inconclusive |
|---|---|---|---|---|---|---|
| [spf13/pflag](https://github.com/spf13/pflag) `f8a9906` | flag parsing, global registries | 501 s | 712 | 189 (150) | 95 | 0 |
| [BurntSushi/toml](https://github.com/BurntSushi/toml) `d733fc5` | a parser and an encoder over `reflect` | 362 s | 320 | 111 (6) | 5 | 7 |
| [gorilla/mux](https://github.com/gorilla/mux) `db9d1d0` | HTTP routing over `regexp` | 96 s | 133 | 2 (0) | 16 | 6 |
| [cenkalti/backoff](https://github.com/cenkalti/backoff) `0232ff2` | timers, a ticker goroutine | 17 s | 35 | 0 | 0 | 3 |
| [shopspring/decimal](https://github.com/shopspring/decimal) `a5eee10` | arbitrary precision over `math/big` | 640 s | 156 | 21 (11) | 48 | 11 (577 s, 0 spurious, 0 inconclusive after the reruns below) |
| [kballard/go-shellquote](https://github.com/kballard/go-shellquote) `95032a8` | string splitting, no `go.mod` | 7 s | 8 | 0 | 0 | 0 |
| [rs/xid](https://github.com/rs/xid) `34d3905` | ID encoding | 28 s | 32 | 4 (1) | 0 | 0 |

"Tier 1" counts the confirmed findings a caller can reach. The other
confirmed findings depend on inputs the callers may never pass, or on
package state.

## What a user met, and what was fixed

- **toml and decimal hit the one-hour cap.** Four exhaustive searches on
  functions taking an `interface{}` (decimal's `Scan` and `DecodeSpanner`,
  each also on `NullDecimal`) ran for about half an hour each, though their
  budget is 10 s. The cause was the engine building its whole input domain
  before the first budget check. Two causes were fixed:
  - Each dynamic type in an interface was enumerated whole (for a
    `[]string`, every list of up to 3 pool strings) before 8 values were
    taken. It now uses lists of at most one element and the pool's first 8
    strings.
  - The enumeration of a field list rebuilt its tail, string pool included,
    once per value of the head. It is now built once.

  The four searches now cover 14,625 to 29,250 inputs in seconds, and both
  projects finish.
- **decimal's replay stalled.** Some counterexamples make the real code
  loop or allocate without bound: arithmetic on a huge exponent. Each PoC
  runs in its own process, and each held its process until the sandbox's
  5 CPU minutes ran out. PoCs now run under Go's test timeout, 30 s for a
  sequential one (10 s since the `math/big` rerun below) and 3 min for a
  concurrent one. A run past it is
  `inconclusive` (`real_run_timeout`): 6 in decimal.
- **A run out of memory was confirmed.** Go's `fatal error: out of memory`
  in the sandbox counted as the panic the model found, an index out of
  range. It is now `inconclusive` (`real_run_out_of_memory`).
- **pflag's findings confirmed one another.** The PoCs of a package shared
  one process, so a flag registered by one PoC made the next panic with
  "flag redefined". Each PoC now runs in a process of its own: 250
  confirmed findings became 189.
- **The summary named properties, not functions.** It now groups findings
  by panic site, names the function, lists runtime errors before the
  package's explicit panics, and labels the latter "check whether its
  documentation says it panics there". pflag's 150 tier-1 findings show as
  5 places.
- **No `go.mod`.** In go-shellquote, `run` failed with Go's "directory
  prefix . does not contain main module". It now says that the directory
  has no `go.mod` and suggests `go mod init`.
- **`strconv.FormatFloat` got stuck in the model.** It is now translated
  from source and modelled, with tests in `testdata/goosesem`.

## What the findings are

- **decimal.** The package's documented panics (division by zero, an
  unsupported cash rounding interval, a Decimal from NaN or ±Inf), and
  exponent overflow in `Mul` and `Shift` ("exponent 4294967292 overflows an
  int32"). Also an index out of range in the unexported `decodeDecimal128`
  and in `pow10`/`RoundHalfUp` on large exponents.
- **toml.**
  - `ParseError.ErrorWithPosition` and `ErrorWithUsage` index past the input
    on a `ParseError` whose position is arbitrary.
  - The internal `tag.Remove` asserts an `interface{}` to `string`.
  - `tag.Add` and `parser.value` raise explicit panics on types they do not
    know.
- **pflag.** Explicit panics on a shorthand longer than one character, an
  unknown shorthand in `Parse`, and an unknown error message type. An index
  out of range in `ValueRequiredError.Error` on an empty flag name.
- **xid.** `ID.Encode` into a buffer shorter than 20 bytes, and the
  unexported `sorter` indexing with arbitrary indices.

## Open

- **backoff's ticker.** The model reports a deadlock, while the real run
  dereferences nil: `panic_kind_mismatch` on `Ticker.run` and `Ticker.send`
  (a `*time.Ticker` left nil by the harness).
- **Unexported functions dominate.** 105 of toml's 111 confirmed findings
  are in unexported functions or depend on package state. The ranking puts
  them below tier 1, but they still make up most of the summary's count.

## Rerun of decimal with `math/big` translated (#171)

All 48 of decimal's spurious findings rested on results of `math/big` calls
that the model chose freely (`NewInt`, `Sign`, `Abs`, `String`). `math/big`
is now translated from its portable Go under `-deps`, and a `*big.Int` input
is its zero value in the model, as in the test.

| | before | after |
|---|---|---|
| time | 640 s | 690 s |
| confirmed (listed in the summary) | 21 (10) | 22 (10) |
| spurious | 48 | 22 |
| inconclusive | 11 | 26 |

- **Spurious.** None of the 22 rests on an unmodelled `math/big` call any
  more. 12 come from `pow10Table` (#176).
- **Inconclusive.** All 26 are real runs past the PoC timeout. With exact
  arithmetic, the model now reaches more calls that are very slow in Go
  (`Exp` with a huge exponent, `Atan`), and it finds the explicit panics
  behind them, which Go does not reach in 10 s.
- **Time.** The sequential PoC timeout went down from 30 s to 10 s: a
  replay that panics does so at once, and at 30 s each, the 26 timeouts
  made the run take 1,729 s.

## And with package initializers run (#176)

The model now runs every package's variable initializers, not only those of
programs that start goroutines. That includes a function literal called in
place (`var pow10Table = func() []*big.Int {...}()`). Three more fixes were
needed:
- A test file's `func init()` whose loop was not in the slice failed the
  slice's trial translation, and every variable of the package dropped.
- Initializers get their own fuel, 200,000 steps.
- A search is killed past three times its budget. With exact tables, one
  trial of `expFixed` ran for 45 minutes on numbers of thousands of words,
  and the engines check the budget only between trials.

| | `math/big` alone | with the initializers |
|---|---|---|
| time | 690 s | 577 s |
| confirmed (listed in the summary) | 22 (10) | 22 (12) |
| spurious | 22 | 0 |
| inconclusive | 26 | 0 |

decimal's run now gives no spurious or inconclusive verdict. The index out
of range in `pow10` on a negative exponent (`pow10Table[n]` with `n < 0`,
reached from `RoundHalfUp` and `powNegIntApprox`) is now confirmed from the
table the initializer built. Before, it rested on an assumed table.

## Rerun of mux with run-time regexps (#173)

mux builds each route's pattern at run time. It quotes the template's
literal parts with `regexp.QuoteMeta`, adds named groups `(?P<v0>...)`,
compiles the pattern through `var RegexpCompileFunc = regexp.Compile`, and
checks `NumSubexp`. All four steps were havoc. Five changes:
- The model compiles a run-time pattern for `regexp.Compile`, as it did
  for `MustCompile`.
- Its parser takes named groups.
- `QuoteMeta` is exact.
- `NumSubexp` is counted from the parse.
- A variable that only benchmarks reassign keeps its initial value.
  `BenchmarkNewRouterRegexpFunc` swaps `RegexpCompileFunc`, and go2lean
  runs no benchmark.

| | before | after |
|---|---|---|
| time | 96 s | 99 s |
| confirmed (listed in the summary) | 2 (0) | 10 (1) |
| spurious | 16 | 0 |
| inconclusive | 6 | 4 |

The finding a caller can reach is an index out of range at `regexp.go:122`,
in `newRouteRegexp`, reached from `Route.addRegexpMatcher`. It takes
`strings.SplitN(template, "=", 2)[1]` of a query template that has no `=`.

