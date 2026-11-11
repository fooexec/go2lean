# Fourth run on unseen projects (2026-11-11)

Same protocol as the [earlier runs](third-run.md): `go2lean run ./...` with
the default flags and a one-hour cap, Go 1.26.6, in a clone of a project
go2lean had not been tried on. The tool was `main` at `775650e` (#191,
`recover`).

| project | kind | time | properties | confirmed (listed) | spurious | inconclusive |
|---|---|---|---|---|---|---|
| [go-chi/chi](https://github.com/go-chi/chi) `c64ed7d` | HTTP router, middleware | 86 s | 212 | 64 (8) | 0 | 6 |
| [kelseyhightower/envconfig](https://github.com/kelseyhightower/envconfig) `7834011` | `reflect`, environment | 50 s | 21 | 4 (1) | 0 | 0 |
| [vmihailenco/msgpack](https://github.com/vmihailenco/msgpack) `19c91df` | encoding over `reflect` | 179 s | 313 | 203 (0) | 1 | 3 |
| [sourcegraph/conc](https://github.com/sourcegraph/conc) `5f936ab` | goroutine pools, panics caught | 95 s | 61 | 11 (4) | 9 | 1 |
| [hashicorp/go-multierror](https://github.com/hashicorp/go-multierror) `1fda6a3` | errors, a goroutine group | 7 s | 24 | 7 (2) | 0 | 0 |
| [pelletier/go-toml](https://github.com/pelletier/go-toml) `686c980` | a parser, an encoder, a CLI | 701 s | 533 | 138 (29) | 7 | 2 |
| [hashicorp/golang-lru](https://github.com/hashicorp/golang-lru) `2bb2909` | generic caches | 148 s | 41 | 5 (0) | 6 | 5 |

Every run finished, well inside the cap. "Listed" counts the confirmed
findings the summary shows: a caller can reach them, or a spec fails. One
of go-toml's prover checks ran out of memory while another job shared the
machine; it changes no verdict.

## What the findings are

- **chi.**
  - `Throttle`, `ThrottleBacklog`, `ThrottleWithOpts`, `Compress`,
    `NewCompressor`, `ClientIPFromXFF` and
    `ClientIPFromXFFTrustedProxies` raise their documented panics on bad
    arguments.
  - `Context.URLParam` indexes `Values` with an index into `Keys`. The
    test built a `Context` whose two slices differ in length. The fields
    are exported, but chi always fills them together, so this is a weak
    finding ([#195](https://github.com/fooexec/go2lean/issues/195)).
- **go-toml.**
  - `LocalTime.String` and `LocalDateTime.String` slice past their buffer
    for a large `Precision`.
  - `unstable`'s `Iterator.Next`, `Node.Child` and `Parser.Raw` index
    with arbitrary offsets.
  - `SeenTracker` and `KeyTracker` raise "unreachable" panics on
    hand-built AST nodes.
- **envconfig.** `MustProcess` raises its documented panic.
- **go-multierror.** `Error.Less` and `Error.Swap` index with arbitrary
  indices.
- **conc.** `WithMaxGoroutines(n)` with `n < 1` raises its documented
  panic.

## Fixed during the run

| fix | effect |
|---|---|
| **Templates of the API only** ([#172](https://github.com/fooexec/go2lean/issues/172)) | `run` searches the `no_panic` and `no_hang` templates of exported functions and methods of exported types. `-unexported` adds the others, and `-run` searches what it names. On these seven projects the properties fell from 1,205 to 389. Every spurious finding of go-toml and every inconclusive one of chi went: all were in unexported functions. No real finding a caller can reach was lost. The one tier-1 finding dropped, go-toml's `scanDigitsWithUnderscores`, rests on a nil its caller never passes ([#194](https://github.com/fooexec/go2lean/issues/194)). |
| **`sync/atomic.Pointer[T]`** ([#192](https://github.com/fooexec/go2lean/issues/192)) | conc's `panics.Catcher` keeps a caught panic there. The model assumed `Load` returned a value, and `Repanic` panicked: 9 spurious findings. It is now modelled. A struct of another package that the test cannot fill in (conc's `WaitGroup` inside a `Pool`, a `regexp.Regexp`) is its zero value in the model, as the test passes it. |
| **Two instances of one generic method** ([#196](https://github.com/fooexec/go2lean/issues/196)) | `LRU[int, int].Keys` and `LRU[int, struct{}].Keys` were both `LRU.Keys`, and a target ran on the other instance's methods: golang-lru's 6 spurious findings. Methods of generic types now carry their type arguments and are modelled for their callers, as the docs said, not searched. |
| **A panic in a spawned goroutine** ([#197](https://github.com/fooexec/go2lean/issues/197)) | `Group.Go(nil)` panics in the goroutine it starts. The PoC reported before that goroutine ran, so one rerun called the finding spurious. A schedule that spawns a goroutine now gets the concurrent harness. |
| **The replay's channel gate** ([#198](https://github.com/fooexec/go2lean/issues/198)) | conc's `for callbackCh := range s.queue` shadows the type `callbackCh`, and the rewritten file did not build. The gate now spells the channel's underlying type. |

## After the fixes

The same projects, rerun with the fixes (`b1d179a`):

| project | time | properties | confirmed (listed) | spurious | inconclusive |
|---|---|---|---|---|---|
| chi | 59 s | 109 | 31 (8) | 0 | 0 |
| envconfig | 49 s | 9 | 1 (1) | 0 | 0 |
| msgpack | 117 s | 111 | 67 (0) | 1 | 0 |
| conc | 65 s | 40 | 9 (4) | 0 | 1 |
| go-multierror | 7 s | 17 | 3 (2) | 0 | 0 |
| go-toml | 167 s | 96 | 32 (28) | 0 | 0 |
| golang-lru | 133 s | 7 | 1 (0) | 0 | 0 |

The listed findings are the same, except go-toml's ranking error above. The
confirmed count no longer drowns them: on go-toml, 28 of 32 are listed,
where 29 of 138 were.

## Open

- **`sync.Map`** (fixed, [#193](https://github.com/fooexec/go2lean/issues/193)).
  msgpack's `Marshal` reads its encoder cache through `Load`, which the
  model left free: 1 spurious finding. `sync.Map` is now a map, empty at
  first. The finding stays spurious for another reason: the encoder is
  chosen by `reflect.Type`'s `Implements` and `Kind`, which were
  unmodelled ([#200](https://github.com/fooexec/go2lean/issues/200)), as
  in go-toml's `encPropsForType`. With `reflect.Type` modelled (fixed),
  neither is spurious: go-toml's `encodeRoot` holds within its budget, and
  msgpack's `Marshal` finds no counterexample (its runs that reach a
  `reflect.Value` the model did not make get stuck, which is moot).
- **conc's `Stream.Go`** ([#198](https://github.com/fooexec/go2lean/issues/198)).
  The model reports a data race after getting stuck in `callbacker`, and
  the replay cannot follow the schedule into `conc/pool`: 1 inconclusive.
- **Ranking** ([#195](https://github.com/fooexec/go2lean/issues/195)). A
  struct whose fields break the type's invariant is ranked tier 1. (A nil
  traced to a func value the caller calls first, go-toml's
  `scanDigitsWithUnderscores`, is now tier 3: fixed,
  [#194](https://github.com/fooexec/go2lean/issues/194).)
- **Methods of generic types are not searched.** golang-lru is almost all
  of them (`LRU[K, V]`, `Cache[K, V]`): 7 properties.
