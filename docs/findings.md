# Findings

Every defect in another project that go2lean found and that was then
confirmed in real Go: by the replay stage's `go test`, by Go's fuzzer on a
go2lean spec, or by a hand-written test. The evaluation reports in
[`eval/`](eval/) give the details. This page lists the defects once each,
with their status upstream.

Model verdicts that Go does not reproduce (spurious), panics on nil or
forged inputs, documented panics and spec mistakes are not counted here;
the reports classify them, and [validation.md](validation.md) gives the
method. The drafts for the unreported defects are in
[blog/upstream-reports.md](blog/upstream-reports.md), to be filed by
hand.

**49 defects in 23 projects** (as of 2026-11-16):
- **27 new**:
  - 11 drafted, not yet filed;
  - 12 not drafted;
  - 1 filed;
  - 2 dropped as too minor;
  - 1 not reportable (archived);
- **14 already reported upstream** and not fixed;
- **8 re-found from security advisories** or a known issue, all fixed
  upstream since.

"How found" is one of:
- **template**: a property the tool generates from the signature
  (`no_panic`, leaks, races), with no spec;
- **spec from docs**: a spec written by hand from the documentation;
- **agent spec**: a spec written by `go2lean agent`;
- **advisory spec**: an agent spec whose guidance is a project's security
  advisory;
- **reading**: the agent's report, or a person, read the code; the defect
  was then confirmed in Go.

## New

| project | defect | kind | how found | status | report |
|---|---|---|---|---|---|
| panjf2000/ants | `Tune` to a smaller size does not limit concurrency; `Free()` goes negative | contract | spec from docs (fuzzer) | draft | [eleventh run](eval/eleventh-run.md) |
| panjf2000/ants | `PoolWithFunc.Invoke(nil)` returns nil, the task never runs | contract | agent spec | draft | [twelfth run](eval/twelfth-run.md) |
| panjf2000/ants | a size above `MaxInt32` wraps; `Tune(1<<32-2)` gives -2 and `Submit` blocks forever | hang | agent spec | draft | twelfth run |
| hashicorp/go-retryablehttp | `Request.WriteTo` panics on a request with no body | crash | agent spec | draft | [thirteenth run](eval/thirteenth-run.md) |
| avast/retry-go | with a custom `RetryIf`, `Unrecoverable` stops `Attempts(0)` but not `Attempts(n)` | contract | agent spec | draft | thirteenth run |
| avast/retry-go | `MaxJitter(-1)` panics on the first retry | crash | reading | draft | thirteenth run |
| avast/retry-go | `BackOffDelay(0)` underflows to the largest delay | wrong result | agent spec | draft | thirteenth run |
| grpc/grpc-go | `MaxConnectionAge` below 10 ns or negative crashes the server on its first connection | crash | template | draft | [grpc-go](eval/grpc.md) |
| casbin/casbin | `KeyMatch` ignores everything after a `*` in the middle of a pattern, over-granting | security (hardening) | agent spec | draft | [advisory specs](eval/cve-specs.md) |
| go-chi/chi | `Profiler` at the root redirects to `//pprof/`; a query string ends up in the path | wrong result | advisory spec | draft | advisory specs |
| gofiber/fiber (v2) | `BalancerForward(nil)` builds, then its first request panics (divide by zero); fixed in v3 only | crash | advisory spec | draft | advisory specs |
| Masterminds/semver | `<=*` and `<=x` reject every version from 0.1.0 up | wrong result | agent spec | not drafted | [semver](eval/semver.md) |
| Masterminds/semver | `!=*` and `!=X` behave like `!= 0.0.0` | wrong result | agent spec (fuzzer) | not drafted | semver |
| cenkalti/backoff | `ExponentialBackOff` with `Multiplier` 1 shrinks an interval above 2^53 ns | wrong result | agent spec | not drafted (minor) | thirteenth run |
| gobwas/glob | `*{abc,b}*c??` does not match `abcXc` | wrong result | reading | not drafted | [glob](eval/glob.md) |
| gobwas/glob | `[\-]` does not compile | wrong result | agent spec (fuzzer) | not drafted | glob |
| gobwas/glob | a `}` outside a group compiles as a literal | contract | agent spec | not drafted (minor) | glob |
| gobwas/glob | a separator that is not a valid rune: `*` treats it as U+FFFD, `?` does not | wrong result | agent spec (fuzzer) | not drafted (minor) | glob |
| gobwas/glob | the lexer replaces an invalid UTF-8 error with a later one | wrong result | reading | not drafted (minor) | glob |
| gobwas/glob | `Lexer.Offset` after `[` is the end of the class, not as documented | contract | agent spec (fuzzer) | not drafted (minor) | glob |
| eapache/go-resiliency | data race on `total` in the package's own test `TestShutdownSuccess` | race | template (`-tests`) | not drafted | [concurrency](eval/conc.md) |
| xrash/smetrics | `Ukkonen` divides by zero when a cost is 0 | crash | template | not drafted | [prove-blind](eval/prove-blind.md) |
| casbin/casbin | `LRUCache.Put` on an existing key keeps the old value (no effect inside casbin) | wrong result | agent spec | not drafted | advisory specs |
| magiconair/properties | every parse error leaks the lexer goroutine | leak | template | filed: magiconair/properties#101 | [seventh run](eval/seventh-run.md) |
| Masterminds/semver | a coerced version's `String` is over the 256-byte parse limit, so it cannot be read back | contract | spec from docs (fuzzer) | dropped (minor) | [ninth run](eval/ninth-run.md) |
| dustin/go-humanize | `BytesN(MaxUint64, 4)` gives `18.45 EB`, which `ParseBytes` rejects | wrong result | spec from docs | dropped (minor) | ninth run |
| aymerick/raymond, mailgun/raymond | every template syntax error leaks the lexer goroutine | leak | reading, then template | not reportable: archived | [lexer leaks](eval/lexer-leaks.md) |

## Already reported upstream

Found independently; the project's tracker already has it, not fixed.

| project | defect | kind | how found | upstream | report |
|---|---|---|---|---|---|
| gin-gonic/gin | `/user/groups/x` is served by `/user/:name/*action`, against its docs | contract | spec from docs | PR #4914 | ninth run |
| gin-gonic/gin | `AsciiJSON` writes a character above U+FFFF as `\u` and 5 hex digits | wrong result | spec from docs | issue #4688 | ninth run |
| gin-gonic/gin | `FileAttachment` writes a space as `+` in `filename*` | wrong result | reading (advisory follow-up) | PR #4873 | advisory specs |
| go-playground/validator | `cidrv4` rejects an address with host bits that `cidr` accepts | wrong result | reading | #909 | ninth run |
| go-playground/validator | `ipv4` accepts an IPv4-mapped IPv6 address | wrong result | reading | #1311 | ninth run |
| Masterminds/semver | `^*` and `^x` match only `0.0.0` | wrong result | agent spec | #317 | semver |
| Masterminds/semver | prerelease numbers above uint64 compared as text | wrong result | spec from docs (fuzzer) | PRs #315, #323, #326 | semver, ninth run |
| dustin/go-humanize | `Ordinal(-1)` is `-1th` | wrong result | spec from docs | PRs #171, #179 | ninth run |
| go-co-op/gocron | `WithLimitedRuns(n)` runs more than n times | contract | spec from docs (fuzzer) | PR #984 | eleventh run |
| alitto/pond | subpool: `TrySubmit` succeeds, the task never runs, `StopAndWait` hangs | hang | agent spec | #152 | twelfth run |
| hashicorp/go-retryablehttp | a large `Retry-After` overflows: the client retries with no wait | wrong result | agent spec | PR #297 | thirteenth run |
| gobwas/glob | `QuoteMeta` does not escape `,` | wrong result | agent spec (fuzzer) | #78 | glob |
| Knetic/govaluate | an expression ending in `\` panics in the lexer (repository archived) | crash | template | #162 | [sixth run](eval/sixth-run.md) |
| go-chi/chi | `RealIP` trusts the leftmost `X-Forwarded-For` (deprecated, still shipped) | security | advisory spec | GHSA-3fxj-6jh8-hvhx | advisory specs |

## Re-found, fixed upstream

Each was found by a spec written before the fix was looked at, on a
version that had the defect. They show the method works; there is
nothing to report.

| project | defect | how found | fixed in | report |
|---|---|---|---|---|
| go-chi/chi | `RedirectSlashes` puts the Host header in `Location` (GHSA-vrw8-fxc6-2r93) | advisory spec | v5.2.2 | advisory specs |
| go-chi/chi | `RedirectSlashes` backslash bypass (GHSA-mqqf-5wvp-8fh8), from the first advisory's spec | advisory spec | v5.2.4 | advisory specs |
| labstack/echo | `/%61dmin/f.txt` skips an `/admin` guard and is served, from the `%2F` advisory's spec | advisory spec | v4.16.0 | advisory specs |
| gofiber/fiber (v2) | CORS wildcard matched by suffix only (GHSA-g5jq-qmqr-qrwf), from the previous advisory's spec | advisory spec | v2.52.3 | advisory specs |
| gofiber/fiber (v2) | CORS `New` panics on a list with spaces after a comma | advisory spec | v2.52.3 | advisory specs |
| gofiber/fiber (v2) | CORS entry after `", "` mis-parsed | advisory spec | v2.52.16 | advisory specs |
| gofiber/fiber | `BalancerForward` keeps the client's `X-Real-IP` ahead of its own (GHSA-gcfq-8gqf-4876), from a 2023 advisory's spec | advisory spec | v2.52.14, v3.3.0 | advisory specs |
| golang/go | `regexp/syntax`: `(?:[aA]0\|A)` matches `a` (found by glob's own fuzz target during the run) | fuzzer | golang/go#59007, Go 1.26 | glob |

## Benchmarks

Bugs already fixed upstream, used as ground truth, are counted in their
own reports and not above: [recent concurrency bugs](eval/conc-recent.md)
and [wrong results](eval/search-vs-fuzz.md), each against Go's fuzzer.
