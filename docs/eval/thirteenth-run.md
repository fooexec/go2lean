# Thirteenth run: the agent on retry libraries (2026-11-16)

Same protocol as the [twelfth run](twelfth-run.md): `go2lean agent` with
Claude Opus 5.5 writes the specs, and the tool searches the model, replays
and fuzzes them (`-budget 5`, `-fuzztime 45s`), then the agent reports and
revises its specs once. The targets are three maintained retry libraries
that many services stand on. Every finding was reproduced again by hand,
outside the agent. Tool at `59d30a9`.

| project | stars | time | finding | upstream |
|---|---|---|---|---|
| [hashicorp/go-retryablehttp](https://github.com/hashicorp/go-retryablehttp) `fd00458` | 2.4k | 12 min | a `Retry-After` past `MaxInt64/1e9` seconds overflows: `DefaultBackoff` returns -2 s and the client retries with no wait (5 requests in 1 ms) | open PR #297 |
| | | | `Request.WriteTo` panics on a request with no body (`NewRequest("GET", url, nil)`) | **unreported** |
| [cenkalti/backoff](https://github.com/cenkalti/backoff) `ce2ae81` | 4.1k | 12 min | `ExponentialBackOff` with `Multiplier` 1 shrinks an interval above 2^53 ns by float rounding | minor, not drafted |
| [avast/retry-go](https://github.com/avast/retry-go) `360fd45` | 3.0k | 14 min | with a custom `RetryIf`, `Unrecoverable` stops `Attempts(0)` but not `Attempts(n)` (3 calls instead of 1) | **unreported** |
| | | | `MaxJitter(-1)` panics on the first retry (`RandomDelay` guards only 0) | **unreported** |
| | | | `BackOffDelay(0)` underflows to the largest delay (the `n--` from #130) | **unreported**, minor |

Drafts for the unreported ones (go-retryablehttp's `WriteTo`; retry-go's
three in one issue) wait for review.

## How they were found

- **go-retryablehttp.**
  - Overflow: two `go2lean:spec`s on `parseRetryAfterHeader` ("the wait is
    never negative", "it is the header's seconds") were confirmed in Go at
    once. The agent traced the cause to `time.Second * time.Duration(sleep)`
    with only `sleep < 0` checked.
  - `WriteTo`: the agent's report flagged a nil-body path it had read in the
    source. Its revised spec (`SeqWriteToNilBody`) was confirmed in the
    second round.
- **retry-go.** Both sequence specs failed in the model and under the
  fuzzer within two executions:
  - `SeqDoUnrecoverableStopsWithCustomRetryIf` drives `Do` with an
    `Unrecoverable` error and a custom `RetryIf`;
  - `SeqBackOffDelayMonotone` checks that the delay never shrinks as `n`
    grows.

  `MaxJitter(-1)` was in the report, read from the source, and confirmed by
  hand.
- **backoff.** The model confirmed `SeqExponentialDeterministicBounds` (no
  jitter, `Multiplier` >= 1: an interval never shrinks) with an initial
  interval of `0x3FF0000000000001` ns (146 years). Go's fuzzer did not
  reach such values in 1.9M executions.

## What the tool got wrong

- **Blank parameters shared one input.** `RandomDelay(_ uint, _ error,
  config)` has two parameters named `_`. In the ABI they had one key, so
  the PoC set the `uint` to the error's `nil` and did not build. Each blank
  parameter is now named by its position; fixed here, with a test.
- **Most of the 44 spurious verdicts** (over the two rounds of each library)
  come from a few unmodelled callees: `math/rand` and `math/rand/v2`, which
  the backoffs' jitter draws from; `net/http.NewRequestWithContext`; some
  `fmt.Sprintf` formats; `context.Cause` (#256). Since then, the random
  draws are modelled in the ranges Go documents, and `context.Cause` as
  `ctx.Err()`. Rerun on the agent's final specs, the three libraries give
  3 spurious verdicts where they gave 8, with the same confirmed findings.
  The 3 left are `NewRequestWithContext` and `fmt.Sprintf`'s `%s` of an
  error and a `time.Duration`.

## What this says

The agent, with a strong model, keeps turning up real defects in
influential libraries:
- three runs on retry libraries, 12 to 14 minutes each;
- seven defects confirmed in Go, four of them unreported.

The specs that found them are the ones a reviewer would ask for: the
documented meaning of an option (`Unrecoverable`, `MaxJitter`), a header's
range, a nil body. They are written from the docs and checked on every
input the model reaches. The cost is noise: the agent's reports classified
every spurious verdict correctly, but a human reading the raw summary would
wade through them.
