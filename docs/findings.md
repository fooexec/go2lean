# Findings

Every defect in another project that go2lean found and that was then
confirmed in the real program. In Go, by the replay stage's `go test`, by
Go's fuzzer on a go2lean spec, or by a hand-written test. In C, by the
replayed C program under ASan and UBSan. The evaluation reports in
[`eval/`](eval/) give the details. This page lists the defects once each,
with their status upstream.

Model verdicts that Go does not reproduce (spurious), panics on nil or
forged inputs, documented panics and spec mistakes are not counted here;
the reports classify them, and [validation.md](validation.md) gives the
method. The drafts for the unreported defects are in
[blog/upstream-reports.md](blog/upstream-reports.md), to be filed by
hand.

**64 defects in 27 projects** (as of 2026-11-18):
- **37 new** (1 high, 10 medium, 16 low, 10 minor):
  - 29 drafted, not yet filed;
  - 4 not drafted;
  - 1 filed;
  - 2 dropped as too minor;
  - 1 not reportable (archived);
- **16 already reported upstream** and not fixed;
- **11 re-found from security advisories** or a known issue, all fixed
  upstream since.

By severity: **7 high, 18 medium, 28 low, 11 minor**. The severity is
go2lean's own reading, not the project's (the advisories have their own
scores):
- **high**: an attacker's input breaks a security property (a spoofed
  client, a bypassed guard, an open redirect, a cross-origin read);
- **medium**: ordinary use gives a wrong result, a hang or a leak, or
  untrusted input crashes the program;
- **low**: it takes an unusual configuration or an edge input, or a
  misconfiguration crashes at the first request instead of at start-up;
- **minor**: precision, wording, documentation, or the project's own
  tests.

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

| severity | project | defect | kind | how found | status | report |
|---|---|---|---|---|---|---|
| high | beego/beego | with `RouterCaseSensitive = false`, a guard filter whose pattern has capitals (`/Admin/*`) never runs: the route is served unguarded | security | advisory guidance spec | draft (private) | [advisory specs](eval/cve-specs.md) |
| medium | panjf2000/ants | `Tune` to a smaller size does not limit concurrency; `Free()` goes negative | contract | spec from docs (fuzzer) | draft | [eleventh run](eval/eleventh-run.md) |
| medium | panjf2000/ants | `PoolWithFunc.Invoke(nil)` returns nil, the task never runs | contract | agent spec | draft | [twelfth run](eval/twelfth-run.md) |
| medium | avast/retry-go | with a custom `RetryIf`, `Unrecoverable` stops `Attempts(0)` but not `Attempts(n)` | contract | agent spec | draft | thirteenth run |
| medium | casbin/casbin | `KeyMatch` ignores everything after a `*` in the middle of a pattern, over-granting | security (hardening) | agent spec | draft | [advisory specs](eval/cve-specs.md) |
| medium | Masterminds/semver | `<=*` and `<=x` reject every version from 0.1.0 up | wrong result | agent spec | draft | [semver](eval/semver.md) |
| medium | gobwas/glob | `*{abc,b}*c??` does not match `abcXc` | wrong result | reading | draft | [glob](eval/glob.md) |
| medium | magiconair/properties | every parse error leaks the lexer goroutine | leak | template | filed: magiconair/properties#101 | [seventh run](eval/seventh-run.md) |
| medium | aymerick/raymond, mailgun/raymond | every template syntax error leaks the lexer goroutine | leak | reading, then template | not reportable: archived | [lexer leaks](eval/lexer-leaks.md) |
| medium | gin-contrib/cors | a regexp origin (`/.../`) is matched unanchored: the package's own example lets `https://google.com.evil.net` read with credentials | security | reading (advisory guidance) | draft (private) | [advisory specs](eval/cve-specs.md) |
| medium | gin-gonic/gin | with `RemoveExtraSlash` and a route `/:p/`, `GET //evil.com` redirects to `//evil.com/` (lookup on the cleaned path, redirect from the raw one) | security | advisory guidance spec | draft (private) | [advisory specs](eval/cve-specs.md) |
| low | panjf2000/ants | a size above `MaxInt32` wraps; `Tune(1<<32-2)` gives -2 and `Submit` blocks forever | hang | agent spec | draft | twelfth run |
| low | hashicorp/go-retryablehttp | `Request.WriteTo` panics on a request with no body | crash | agent spec | draft | [thirteenth run](eval/thirteenth-run.md) |
| low | avast/retry-go | `MaxJitter(-1)` panics on the first retry | crash | reading | draft | thirteenth run |
| low | avast/retry-go | `BackOffDelay(0)` underflows to the largest delay | wrong result | agent spec | draft | thirteenth run |
| low | grpc/grpc-go | `MaxConnectionAge` below 10 ns or negative crashes the server on its first connection | crash | template | draft | [grpc-go](eval/grpc.md) |
| low | go-chi/chi | `Profiler` at the root redirects to `//pprof/`; a query string ends up in the path | wrong result | advisory spec | draft | advisory specs |
| low | gofiber/fiber (v2) | `BalancerForward(nil)` builds, then its first request panics (divide by zero); fixed in v3 only | crash | advisory spec | draft | advisory specs |
| low | gobwas/glob | `[\-]` does not compile | wrong result | agent spec (fuzzer) | draft | glob |
| low | xrash/smetrics | `Ukkonen` divides by zero when a cost is 0 | crash | template | not drafted | [prove-blind](eval/prove-blind.md) |
| low | casbin/casbin | `LRUCache.Put` on an existing key keeps the old value (no effect inside casbin) | wrong result | agent spec | not drafted | advisory specs |
| low | gin-contrib/cors | regexp origins are lowercased: `\S` becomes `\s` | wrong result | reading (advisory guidance) | draft | advisory specs |
| low | rs/cors | `OriginAllowed` returns true without an `Origin` header under `AllowAll`/`"*"`, against its doc (from the #231 fix) | contract | advisory guidance spec | draft | advisory specs |
| low | labstack/echo (v5) | `NewContext` without an `*Echo`, as its doc allows, then `Reset` dereferences nil | crash | advisory guidance spec | draft | [advisory specs](eval/cve-specs.md) |
| low | beego/beego | a route `/a/*.*` panics (index -1) on `GET /a` (a 500) | crash | agent spec (fuzzer) | draft | advisory specs |
| low | beego/beego | `URLFor` writes query keys and values unescaped: `&` or `=` in a value changes the parameters | wrong result | agent spec | draft | advisory specs |
| low | beego/beego | template `Substr` overflows on `start + length` (panic) | crash | agent spec | draft | advisory specs |
| minor | Masterminds/semver | `!=*` and `!=X` behave like `!= 0.0.0` | wrong result | agent spec (fuzzer) | draft | semver |
| minor | cenkalti/backoff | `ExponentialBackOff` with `Multiplier` 1 shrinks an interval above 2^53 ns | wrong result | agent spec | not drafted (minor) | thirteenth run |
| minor | gobwas/glob | a `}` outside a group compiles as a literal | contract | agent spec | draft (minor) | glob |
| minor | gobwas/glob | a separator that is not a valid rune: `*` treats it as U+FFFD, `?` does not | wrong result | agent spec (fuzzer) | draft (minor) | glob |
| minor | gobwas/glob | the lexer replaces an invalid UTF-8 error with a later one | wrong result | reading | draft (minor) | glob |
| minor | gobwas/glob | `Lexer.Offset` after `[` is the end of the class, not as documented | contract | agent spec (fuzzer) | draft (minor) | glob |
| minor | eapache/go-resiliency | data race on `total` in the package's own test `TestShutdownSuccess` | race | template (`-tests`) | not drafted | [concurrency](eval/conc.md) |
| minor | Masterminds/semver | a coerced version's `String` is over the 256-byte parse limit, so it cannot be read back | contract | spec from docs (fuzzer) | dropped (minor) | [ninth run](eval/ninth-run.md) |
| minor | dustin/go-humanize | `BytesN(MaxUint64, 4)` gives `18.45 EB`, which `ParseBytes` rejects | wrong result | spec from docs | dropped (minor) | ninth run |
| minor | gin-contrib/cors | a wildcard's prefix and suffix may overlap: `https://a*a.example.com` grants `https://a.example.com` | wrong result | reading (advisory guidance) | draft (minor) | advisory specs |

## Already reported upstream

Found independently; the project's tracker already has it, not fixed.

| severity | project | defect | kind | how found | upstream | report |
|---|---|---|---|---|---|---|
| high | go-chi/chi | `RealIP` trusts the leftmost `X-Forwarded-For` (deprecated, still shipped) | security | advisory spec | GHSA-3fxj-6jh8-hvhx | advisory specs |
| medium | gin-gonic/gin | `AsciiJSON` writes a character above U+FFFF as `\u` and 5 hex digits | wrong result | spec from docs | issue #4688 | ninth run |
| medium | Masterminds/semver | `^*` and `^x` match only `0.0.0` | wrong result | agent spec | #317 | semver |
| medium | go-co-op/gocron | `WithLimitedRuns(n)` runs more than n times | contract | spec from docs (fuzzer) | PR #984 | eleventh run |
| medium | alitto/pond | subpool: `TrySubmit` succeeds, the task never runs, `StopAndWait` hangs | hang | agent spec | #152 | twelfth run |
| medium | hashicorp/go-retryablehttp | a large `Retry-After` overflows: the client retries with no wait | wrong result | agent spec | PR #297 | thirteenth run |
| medium | Knetic/govaluate | an expression ending in `\` panics in the lexer (repository archived) | crash | template | #162 | [sixth run](eval/sixth-run.md) |
| medium | gin-gonic/gin | `X-Forwarded-Prefix: @` sanitizes to nothing, so a trailing-slash redirect goes to `//evil.com/` | security | advisory guidance spec | PR #4923 | advisory specs |
| low | gin-gonic/gin | `/user/groups/x` is served by `/user/:name/*action`, against its docs | contract | spec from docs | PR #4914 | ninth run |
| low | gin-gonic/gin | `FileAttachment` writes a space as `+` in `filename*` | wrong result | reading (advisory follow-up) | PR #4873 | advisory specs |
| low | go-playground/validator | `cidrv4` rejects an address with host bits that `cidr` accepts | wrong result | reading | #909 | ninth run |
| low | go-playground/validator | `ipv4` accepts an IPv4-mapped IPv6 address | wrong result | reading | #1311 | ninth run |
| low | Masterminds/semver | prerelease numbers above uint64 compared as text | wrong result | spec from docs (fuzzer) | PRs #315, #323, #326 | semver, ninth run |
| low | gobwas/glob | `QuoteMeta` does not escape `,` | wrong result | agent spec (fuzzer) | #78 (closed 2026-10-26, still reproduces at `ff0540d`) | glob |
| minor | dustin/go-humanize | `Ordinal(-1)` is `-1th` | wrong result | spec from docs | PRs #171, #179 | ninth run |
| low | DaveGamble/cJSON | `ReplaceItemInObject(o, item->string, item)` frees the item's key, then copies it: a heap use after free | memory safety | advisory spec (C) | #1081, PR #1083 | [cJSON](eval/cjson.md) |

## Re-found, fixed upstream

Each was found by a spec written before the fix was looked at, on a
version that had the defect. They show the method works; there is
nothing to report.

| severity | project | defect | how found | fixed in | report |
|---|---|---|---|---|---|
| high | go-chi/chi | `RedirectSlashes` puts the Host header in `Location` (GHSA-vrw8-fxc6-2r93) | advisory spec | v5.2.2 | advisory specs |
| high | go-chi/chi | `RedirectSlashes` backslash bypass (GHSA-mqqf-5wvp-8fh8), from the first advisory's spec | advisory spec | v5.2.4 | advisory specs |
| high | labstack/echo | `/%61dmin/f.txt` skips an `/admin` guard and is served, from the `%2F` advisory's spec | advisory spec | v4.16.0 | advisory specs |
| high | gofiber/fiber (v2) | CORS wildcard matched by suffix only (GHSA-g5jq-qmqr-qrwf), from the previous advisory's spec | advisory spec | v2.52.3 | advisory specs |
| high | gofiber/fiber | `BalancerForward` keeps the client's `X-Real-IP` ahead of its own (GHSA-gcfq-8gqf-4876), from a 2023 advisory's spec | advisory spec | v2.52.14, v3.3.0 | advisory specs |
| medium | golang/go | `regexp/syntax`: `(?:[aA]0\|A)` matches `a` (found by glob's own fuzz target during the run) | fuzzer | golang/go#59007, Go 1.26 | glob |
| low | gofiber/fiber (v2) | CORS `New` panics on a list with spaces after a comma | advisory spec | v2.52.3 | advisory specs |
| low | gofiber/fiber (v2) | CORS entry after `", "` mis-parsed | advisory spec | v2.52.16 | advisory specs |
| low | DaveGamble/cJSON | `SetValuestring(item, NULL)` passes NULL to `strlen` (CVE-2024-31755), from CVE-2023-50472's spec | advisory spec (C) | v1.7.18 | [cJSON](eval/cjson.md) |
| low | DaveGamble/cJSON | `InsertItemInArray(a, i, NULL)` dereferences NULL (CVE-2023-50471) | advisory spec (C) | v1.7.17 | cJSON |
| low | DaveGamble/cJSON | `SetValuestring` calls `strcpy` on overlapping strings (#881) | advisory spec (C) | v1.7.19 | cJSON |

## Benchmarks

Bugs already fixed upstream, used as ground truth, are counted in their
own reports and not above: [recent concurrency bugs](eval/conc-recent.md)
and [wrong results](eval/search-vs-fuzz.md), each against Go's fuzzer.
