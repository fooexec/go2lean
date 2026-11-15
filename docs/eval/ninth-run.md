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
for two routers many services stand on.

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

## What this says

These five are mature and widely fuzzed or tested, and the default run
finds no bug in them, which is what one would expect. A spec from the
docs found the one place where gin's documentation and its router
disagree, which a reader had reported three weeks earlier. The two fixes widen
what the search reaches (callbacks, partly broken modules); neither
changes a verdict on the libraries above but jsonparser's.
