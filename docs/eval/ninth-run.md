# Ninth run: widely used libraries (2026-11-15)

Same protocol as the [eighth run](eighth-run.md), tool frozen at
`205b7c2` (the file-system model). The earlier runs' bugs were in
libraries few projects depend on, so this run took five that many do.

| project | kind | time | properties | confirmed (tier 1) | spurious | inconclusive |
|---|---|---|---|---|---|---|
| [go-ini/ini](https://github.com/go-ini/ini) `b9a4289` | INI files | 174 s | 128 | 29 (0) | 0 | 0 |
| [buger/jsonparser](https://github.com/buger/jsonparser) `36a686d` | JSON without reflection | 106 s | 75 | 10 (0) | 0 | 0 |
| [julienschmidt/httprouter](https://github.com/julienschmidt/httprouter) `4840180` | HTTP routing | 66 s | 20 | 11 (0) | 0 | 0 |
| [microcosm-cc/bluemonday](https://github.com/microcosm-cc/bluemonday) `aba082a` | HTML sanitizer | 255 s | 220 | 2 (0) | 2 | 0 |
| [mailru/easyjson](https://github.com/mailru/easyjson) `89250db` | JSON code generator and runtime | failed at load | | | | |

## What the findings are

No bug: nothing at tier 1 in any of the five. Below it:
- **go-ini:** `Key` methods on a `Key` whose `*Section` the test left nil
  (tier 4), and two interface inputs holding the package's own sources
  built field by field (tier 2).
- **jsonparser:** every finding was a nil callback (`ArrayEach(data, nil)`)
  at tier 2 or 3. See below: the searches never got past it.
- **httprouter:** nil handlers and a nil tree root, at tier 2 and 3.
- **bluemonday:** a nil reader (tier 3) and a writer built field by field
  (tier 2); its other 216 properties hold. Its 2 spurious verdicts rest on
  `unicode/utf8.EncodeRune` called from `golang.org/x/net/html`'s
  `unescapeEntity`, which the dependency walk left untranslated.

## What the tool got wrong

| problem | cost | status |
|---|---|---|
| A function parameter was always nil: jsonparser's `ArrayEach`, `EachKey` and friends stopped at calling the callback, and the parser was never searched | every jsonparser search | fixed: a func with no results is a no-op function, written by the PoC as a literal; rerun, `ArrayEach` and `EachKey` hold on 2,000 random documents each |
| One package of `./...` that does not type-check (easyjson's `tests/`, which needs generated code) failed the whole run | the easyjson run | fixed: such a package is named and left out; rerun, easyjson runs in 309 s (257 properties, 51 confirmed, none at tier 1: receivers built field by field) |
| A callback that returns a value (`ArrayEachErr`'s) is still nil | 5 jsonparser findings at tier 2 and 3 | open |
| `utf8.EncodeRune` from `x/net/html` not translated | 2 spurious (bluemonday) | open |

## Specs from the documentation

With templates finding nothing, specs were written from the docs alone
(the [go2lean-spec skill](../../.claude/skills/go2lean-spec/SKILL.md)),
for the routers many services stand on.

- **httprouter's `CleanPath`.** Its doc calls it "the URL version of
  path.Clean": the spec compares it with `path.Clean("/"+p)`, trailing
  slash aside (the doc is silent on it), and checks the result is rooted
  and canonical. Exhausted over the bounded domain in the model, and Go's
  fuzzer held on both specs for 60 s each (5.5M executions). No defect.
- **gin's routing tree** ([gin-gonic/gin](https://github.com/gin-gonic/gin)
  `0f09c3a`). A sequence spec encodes the four rules of docs/doc.md,
  "Parameters in path", over the order the routes are added and the
  request. go2lean found a violation at trial 1, confirmed in Go: with
  `/user/:name`, `/user/:name/*action` and `/user/groups`, a request for
  `/user/groups/x` is served by `/user/:name/*action` (`name=groups`),
  where the doc says "Routes starting with /user/groups are never
  interpreted as /user/:name/... routes". The sentence predates gin's
  static-over-param fallback; gin's open PR #4914 (2026-10-26) removes it
  for the same reason. Found independently, but not new.
- **gin's `Context.ClientIP`.** Its doc states when the remote address is
  returned (an untrusted remote, headers that are not valid, no trusted
  proxies) and, read with it, that the result is otherwise an address from
  the headers. A sequence spec over remote addresses, `X-Forwarded-For` and
  `X-Real-IP` values (malformed ones included) and trusted-proxy lists.
  The model's counterexample was spurious (it does not follow `net`'s
  address parsing); Go's fuzzer held for 60 s (675k executions, the whole
  table). No defect.
- **gin's `AsciiJSON`.** The doc: "Generates ASCII-only JSON with escaped
  non-ASCII characters". The spec renders a string drawn from a table of
  characters and checks the output is ASCII and decodes back to the same
  string. go2lean found a violation at trial 2, confirmed in Go: a
  character above U+FFFF is written as `\u` and five hex digits, so `😀`
  becomes `"\u1f600"`, which every JSON parser reads as `"ὠ0"`; `𝄞`
  becomes `"ᴑe"`. The escape must be a UTF-16 surrogate pair
  (`\ud83d\ude00`). It is gin's open issue #4688 (2026-06-03), with seven
  open fix PRs, none merged. Found independently, five months later.
- **echo's router priority** ([labstack/echo](https://github.com/labstack/echo)
  v5 `f275a6b`). A sequence spec over the routes added (static, `:param`,
  `*`) and a request checks the guide's order (static, then param, then
  any) against a reference matcher. The model's counterexample was
  spurious, but Go's fuzzer found a difference in seconds: with only
  `/users/:id`, `/users/42/files` is served with `id=42/files`. This is
  deliberate (the router's comment: a param that ends a route "should act
  similarly to any node"), and v4.16.5 does the same. The reference
  matcher assumed that a param stops at a slash; that was the spec's
  mistake. No defect.
- **go-playground/validator** (`19397f2`). Most tags call the standard
  library's parsers; probes of the edges, against what the doc says,
  found three: `cidrv4` rejects an address with host bits
  (`192.168.1.5/24`) that `cidr` and `cidrv6` accept (open issue #909),
  `ipv4` takes an IPv4-mapped IPv6 address (open issue #1311), and
  `hostname_port` takes the port `+80` (as Go's `net` does). Nothing new.
- **Masterminds/semver** ([Masterminds/semver](https://github.com/Masterminds/semver)
  `dd2b995`, the version library of Helm and many others). Five sequence
  specs: `StrictNewVersion` takes semver.org's grammar and `Compare` is its
  precedence (both against `golang.org/x/mod/semver`); `Compare` is a total
  order; a parsed version's `String` parses back; and `Constraints.Check`
  of AND and OR lists means what the README's tables say, for release
  versions. In the model, the specs held (the constraint spec exhausted
  87k inputs once `fmt.Sprint` was modelled; before, its havoc gave one
  spurious verdict). Go's fuzzer found four violations, each in seconds:
  - `~0.0.0` accepts `1.0.0`, where the README's `~1.2.3` rule gives
    `< 0.1.0`: a special case in the code ("equivalent to >= 0.0.0"),
    asked about in its open issue #254;
  - a prerelease number above uint64 is compared as text, so
    `9.9.9-10.-0` sorts below `9.9.9-10.99999999999999999999`: open PRs
    #315 and #326;
  - a version number above uint64 is rejected, and so is a string over
    256 bytes: implementation limits, so the spec was narrowed to skip them;
  - a version that `NewVersion` coerces (`1-` and 252 bytes) has a
    `String` of 258 bytes, over the 256-byte limit that parsing checks, so
    it cannot be read back from its JSON, text or SQL form; `SetPrerelease`
    and `New` build such versions too. Not reported yet (the limit came in
    April 2026); a draft issue waits for review.
- **dustin/go-humanize** (`f5b54ae`). Nine specs from the docs: `Comma`
  groups by three and `ParseComma` reads it back, `BigComma` is `Comma`,
  `Commaf` round-trips, `CommafWithDigits` gives exactly the decimals
  asked for, `Ordinal` follows the English rule, `BytesN` parses back
  within its last digit, and `Bytes` and `SI` never show `1000` of a unit.
  The model found two violations, both confirmed in Go:
  - `Ordinal(-1)` is `-1th` (open PRs #171 and #179);
  - `BytesN(MaxUint64, 4)` is `18.45 EB`, rounded up past MaxUint64, so
    `ParseBytes` rejects it as too large; `IBytes(MaxUint64)` gives
    `16 EiB` the same way. Go's fuzzer held on that spec for 40 s
    (4.4M executions), since it rarely draws the top of the range. Not
    reported (the open #182 and #183 are about exact halves); a draft waits
    for review.
  The fuzzer's one other violation was the spec's mistake: `Commaf` prints
  a float's shortest decimal form, not the integer it holds. The model
  gave two spurious verdicts, from `strconv.ParseFloat` (havoc on a
  300-digit number) and the portable `math.Log`.

## What this says

These are mature and widely fuzzed or tested, and the default run
finds no bug in them, which is what one would expect. A spec from the
docs found two defects in gin within minutes each: a router behaviour its
documentation denies and `AsciiJSON` corrupting every emoji. Both had been
reported, three weeks and five months earlier, and neither is fixed.
Specs on echo, go-playground/validator (by hand, against the standard
library's parsers), Masterminds/semver and go-humanize found behaviours
that the docs do not describe; all but two were already reported or by
design, and the two left (semver's over-long versions, humanize's sizes
near 2^64) are minor. These libraries are now combed by many automated
fix PRs (17 opened on humanize and 23 on semver in 2026, still open), which leaves
little for a newcomer. The fixes widen
what the search reaches (callbacks, partly broken modules, `fmt.Sprint`); none
changes a verdict on the libraries above but jsonparser's.
