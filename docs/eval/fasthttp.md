# The path-normalization property on fasthttp (2026-11-18)

The first run of the catalog's **path-normalization** property
([path-normalization.md](../specs/path-normalization.md)), a new general
family (path traversal). [valyala/fasthttp](https://github.com/valyala/fasthttp)
(23k stars, actively maintained; `normalizePath` was reworked as recently
as PRs #2453, #2526, #2392). On the default branch `master` at `7b1ac76`.

`go2lean agent -guidance <path-normalization> -module fasthttp
github.com/valyala/fasthttp` drives `URI.Parse` from five sequence specs:
`SeqPathNormalized` (no `.`/`..`/`//` in the output), `SeqPathIdempotent`,
`SeqRequestURIRoundTrip`, `SeqPathEncodedDotsNoEscape`, and
`SeqArgsRoundTrip`.

| spec | result |
|---|---|
| SeqPathNormalized | held (2,000 trials): no `.`/`..`/`//` in `URI.Path()` |
| SeqPathEncodedDotsNoEscape | held: a single `URI.Parse` never leaves an escaping `..` |
| SeqRequestURIRoundTrip, SeqArgsRoundTrip | held |
| **SeqPathIdempotent** | **confirmed violated** (replayed with `go test`) |

## The defect: `URI.Path()` is not idempotent

```
raw "/%252e%252e/secret"  -> Path() "/%2e%2e/secret"  -> re-parsed Path() "/secret"
raw "/a/%252e%252e/secret" -> Path() "/a/%2e%2e/secret" -> re-parsed Path() "/secret"
```

`URI.Parse` percent-decodes once and resolves dot segments. A doubly
encoded dot segment `%252e%252e` decodes once to the literal `%2e%2e`,
which is not a dot segment, so it survives into `URI.Path()`. Parsing that
output a second time decodes `%2e%2e` to `..` and resolves it, climbing out
of the directory. So `norm(norm(p)) != norm(p)`.

**Single-encoded input is handled correctly:** `/a/%2e%2e/secret` and
`/a/../secret` both normalize to `/secret` in one pass, and climbs above
the root are clamped. The defect is only the doubly-encoded case and the
non-idempotence it causes.

## Severity: low

Inside fasthttp alone this is not a traversal: the server decodes once, so
the leftover `%2e%2e` is a literal path segment, not `..`. The impact needs
a **second** decoder: a common deployment is a reverse proxy that forwards
the decoded path to an upstream that decodes again, and there the
doubly-encoded dot segment becomes `..` and escapes the root. It is a real
normalization defect (idempotence is the contract the rest of a server
relies on) with a credible multi-hop impact, not a self-contained
high-severity traversal.

The draft report is in [upstream reports](../blog/upstream-reports.md).

## What this says

The property found a genuine normalization bug that single-encoded
traversal tests miss, on a path parser that is actively hardened against
traversal. Idempotence is the right oracle for the double-decoding class:
it needs no reference normalizer, only that parsing a normalized path again
does not change it.
