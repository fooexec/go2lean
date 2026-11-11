# Third run on unseen projects (2026-11-11)

Same protocol as the [first](first-run.md) and [second](second-run.md)
runs: `go2lean run ./...` with the default flags and a one-hour cap, Go
1.26.6, in a clone of a project go2lean had not been tried on. The tool was
`main` at `e1e6850` (#180). The two fixes below were made during the run,
and uuid was rerun after them.

| project | kind | time | properties | confirmed (listed) | spurious | inconclusive |
|---|---|---|---|---|---|---|
| [google/uuid](https://github.com/google/uuid) `2d3c2a9` | parsing, `crypto/rand`, time | 66 s | 80 | 7 (2) | 23 | 0 |
| [samber/lo](https://github.com/samber/lo) `6f5a63a` | generics, `x/text`, callbacks | 257 s | 99 | 18 (4) | 16 | 1 |
| [mattn/go-runewidth](https://github.com/mattn/go-runewidth) `99da427` | Unicode width tables | 357 s | 55 | 3 (0) | 6 | 0 |
| [blang/semver](https://github.com/blang/semver) `4487282` | version parsing | 152 s | 66 | 5 (4) | 0 | 0 |
| [robfig/cron](https://github.com/robfig/cron) `bc59245` | schedule parsing, goroutines | 37 s | 64 | 15 (7) | 2 | 7 |
| [oklog/ulid](https://github.com/oklog/ulid) `75921a7` | encoding, time, entropy | 91 s | 46 | 10 (2) | 1 | 1 |
| [mitchellh/go-wordwrap](https://github.com/mitchellh/go-wordwrap) `ecf0936` | text | 16 s | 2 | 0 | 0 | 0 |

Every run finished, well inside the cap. "Listed" counts the confirmed
findings the summary shows: a caller can reach them, or a spec fails.

## Fixed during the run

- **uuid did not load.** go2lean writes a file that turns the package's fuzz
  targets into specs, and that file declared `go2leanSink(...any)`. uuid's
  `go.mod` has no `go` line, so its language version is Go 1.16, where `any`
  does not exist, and the whole run failed. The helper now takes
  `...interface{}`.
- **A variable that tests swap was unknown.** uuid's `TestClockSeq` replaces
  `timeNow`, so the model did not know the value of `var timeNow = time.Now`.
  Without `-tests` no property runs a Test function, so such writes no longer
  count.

## What the findings are

- **semver.** `Versions.Less` and `Versions.Swap` index with arbitrary
  indices. `MustParse` and `MustParseRange` raise their documented panics.
- **cron.** `ParseStandard` slices to `[:-1]` (`parser.go:99`), and
  `NewParser` panics on an invalid option set. `Cron.Entries`, `Entry`,
  `Remove` and `Stop` are listed too, but they are not real findings. Each
  blocks on a `Cron` the test built field by field: running is true and the
  channels are nil. A caller gets a `Cron` from `cron.New`, which go2lean
  does not take for its constructor, since it looks for `NewCron` or
  `newCron` (#186).
- **lo.** `Range` with a huge count fails in `makeslice`. `IntN`,
  `RandomString` and `Synchronize` panic on bad arguments, as they check.
- **uuid, ulid.** The `Must` functions raise their documented panics.

## Open

- **`recover`** ([#181](https://github.com/fooexec/go2lean/issues/181)).
  `lo.Try` calls its callback under a `recover`. In the model, `recover()`
  is always nil and a panic ends the run, so a nil callback gives 4
  spurious findings.
- **Interface-typed package variables**
  ([#182](https://github.com/fooexec/go2lean/issues/182)). In uuid,
  `var rander = rand.Reader` starts as an unknown non-nil reader. The model
  never builds an initializer cell of interface type (#55), so
  `io.ReadFull(rander, …)` may fail there and `Must` panic: 23 spurious
  findings.
- **`golang.org/x/text`** ([#183](https://github.com/fooexec/go2lean/issues/183)).
  lo's case conversions go through `cases` and `language`, where the model
  gets stuck (`Tag.Tag`) or guesses (`InheritanceMatcher.Match`): 10
  spurious findings.
- **Tables an init function fills, and very large arrays**
  ([#184](https://github.com/fooexec/go2lean/issues/184)). go-runewidth's
  `joinerBits` is filled by `fillJoinerBits`, called from `init`, and its
  `strictWidthLUT` is 2.2 MB. lo's `unicode.Letter` is a `RangeTable` the
  model does not build. Together: 8 spurious findings.
- **The reason for a spurious finding.** Its diagnosis names the unmodelled
  calls the model chose results for, but not an assumption or a callee the
  model got stuck in. 47 of this run's 48 spurious findings came with no
  stated reason ([#185](https://github.com/fooexec/go2lean/issues/185)).
- **A constructor named `New`**
  ([#186](https://github.com/fooexec/go2lean/issues/186)). A package's `New`
  that returns its main type (`cron.New() *Cron`) is not taken for that
  type's constructor, so the type's methods run on receivers that no caller
  can build.
