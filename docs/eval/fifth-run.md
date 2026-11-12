# Fifth run on unseen projects (2026-11-12)

Same protocol as the [fourth run](fourth-run.md): `go2lean run ./...`
with the default flags and a one-hour cap, Go 1.26.6, in a clone of a
project go2lean had not been tried on. This time the tool was installed as
a user installs it, from the v0.1 line (`main` at `d1080f0`).

| project | kind | time | properties | confirmed (tier 1) | spurious | inconclusive |
|---|---|---|---|---|---|---|
| [hashicorp/go-version](https://github.com/hashicorp/go-version) `5ca63ba` | version parsing, constraints | 41 s | 42 | 8 (6) | 0 | 0 |
| [google/btree](https://github.com/google/btree) `aeba20f` | a generic B-tree | 15 s | 28 | 27 (12) | 0 | 0 |
| [pkg/errors](https://github.com/pkg/errors) `87f8819` | errors with stacks, no `go.mod` | 8 s | 17 | 2 (0) | 0 | 0 |
| [sirupsen/logrus](https://github.com/sirupsen/logrus) `6ef0748` | a logger: mutexes, pools, hooks | 289 s | 190 | 7 (1) | 5 | 0 |
| [gorilla/websocket](https://github.com/gorilla/websocket) `e064f32` | a network protocol | 271 s | 44 | 11 (0) | 0 | 0 |
| [yuin/goldmark](https://github.com/yuin/goldmark) `7a52085` | a Markdown parser | 889 s | 456 | 68 (20) | 0 | 0 |
| [golang/groupcache](https://github.com/golang/groupcache) `2c02b82` | a cache, protobuf messages | 174 s | 102 | 13 (3) | 8 | 0 |

Every run finished, well inside the cap. pkg/errors has no `go.mod`: the
first run stopped with a message saying so and how to create one, and ran
after `go mod init`.

## What the findings are

- **go-version.** `Collection.Less`/`Swap` and `Constraints.Less`/`Swap`
  index with arbitrary indices (the `sort.Interface` contract leaves
  that to the caller). `Must` and `MustConstraints` raise their
  documented panics.
- **btree.** `NewFreeList(n)` with a negative `n` panics in `make`. `New`
  with a degree below 2 raises its documented "bad degree" panic, and
  every method of the tree it would return is listed with it.
- **goldmark.** `text.Segment`, `Lines` and `MultiLineValue` slice their
  source with the offsets they hold, and a segment built with offsets past
  the source panics. `WithTypographicSubstitutions` indexes its table with
  the caller's keys, and `ProcessDelimiters` dereferences a nil delimiter
  list. These are API-reachable but rest on values goldmark's own parser
  never builds.
- **groupcache.** `ByteView.At`, `Slice` and `SliceFrom` index with the
  caller's offsets.
- **logrus.** `Entry.Writer`/`WriterLevel` and `Logger.Writer`/`WriterLevel`
  leak the goroutine reading their pipe when the writer is never closed
  (documented: the caller must close it). After the fixes below, `Panic`,
  `Panicf` and `Panicln` raise their documented panics.

## Spurious verdicts, and what was fixed

| cause | spurious | fix |
|---|---|---|
| logrus: calls into a logging package were skipped (a log line is not the code under test), also inside logrus itself. `SetOutput`'s skipped `Lock` left its deferred `Unlock` unlocking an unlocked mutex ([#214](https://github.com/fooexec/go2lean/issues/214)) | 5 | the skip applies only to calls from outside the logger's package. This let the search into much more of logrus, and showed the two next causes |
| logrus: `logger.entryPool.Get()`, a `sync.Pool` in a struct field, was unmodelled; the model picked a non-nil `*Entry` with havoc fields (#214) | 10 | `Get` calls the pool's `New` field when it is set and returns nil when it is not, as Go does on an empty pool |
| logrus: `Infoln`, `Println`, `Warnln`... format with `fmt.Sprintln`, unmodelled (#214) | 5 | `Sprintln` is `Sprintf` with one `%v` per operand, spaces between them and a newline |
| groupcache: the protobuf messages' `String()` walk the message with `reflect` and reach `reflect.Zero` ([#215](https://github.com/fooexec/go2lean/issues/215)) | 8 | protobuf's text rendering (`proto.CompactTextString`, `MarshalTextString`, `prototext.Format`) is modelled as what it does for any message: some string, no panic. The runtime under it reads messages through `unsafe` as well as `reflect`, so modelling `reflect.Value` alone would not reach it. groupcache after the fix: 13 confirmed (3 at tier 1), **0 spurious** |

logrus after the fixes: 190 properties, 90 confirmed (7 at tier 1: the
four `Writer` leaks and the three `Panic` functions; 63 at tier 4, which
rest on a nil the caller passes or an opaque input left nil), **0
spurious**, 1 inconclusive (`Fatalln` calls `os.Exit`, which ends the
replay). The last 5 spurious verdicts were measured gone on a rerun of
the `...ln` functions (`-run 'ln$'`).

## What this run says

- **Five of seven projects gave no spurious verdict**, on the first try,
  installed as a user installs the tool. After the fixes, none of the
  seven does.
- **Every spurious verdict had a cause already on the audit's lists**: a
  library function not modelled (`sync.Pool` in a field, `fmt.Sprintln`)
  or the `reflect` boundary. None was a new kind of gap, and the summary
  named the unmodelled call each time.
- **The user-model hint is the way out** while the boundary stays open:
  the summary points at `.go2lean/models` for the call the verdict rests
  on.
