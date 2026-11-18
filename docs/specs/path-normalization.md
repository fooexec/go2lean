# A URL path is normalized once and for all

## Applies to

General (layer 1): any router, proxy or file server that percent-decodes a
request path and resolves `.`/`..` segments. Written without naming a
language.

## Property

Let `norm(p)` be the path a server exposes to the rest of its code after
parsing a raw request target `p` (percent-decoded, with `.` and `..`
segments resolved). Then:
- `norm` is **idempotent**: `norm(norm(p)) == norm(p)`. Parsing the
  normalized path again returns the same bytes.
- The result has no `.` or `..` segment, no empty segment (`//`), and does
  not climb above the root.
- Percent-escapes are resolved so that an **encoded** dot segment cannot
  survive normalization and reappear as `..` to a later consumer. In
  particular a doubly-encoded `%252e%252e` must not normalize to a path
  that a second decode turns into `..`.

Idempotence is the property that catches double-decoding: a server that
decodes once leaves `%2e` (the single-decode of `%252e`) in the path; a
proxy or upstream that decodes that path again turns it into `..` and
escapes the root.

## Threat model

The attacker controls every byte of the raw request target, including
percent-encoding and repeated encoding (`%25` is the encoding of `%`). The
attacker does not control the server's routing table or the trusted root.
Deployments where one hop forwards the decoded path to another that decodes
again (a reverse proxy in front of an app server) are in scope: that is the
normal shape of the bug.

## Oracle

A reference: normalize the path, then normalize the result again and
compare; and scan the normalized path for a `.`/`..`/empty segment. A spec
builds the raw path from a small grammar (slashes, dots, letters, `%`, hex
digits, and encoded sequences `%2e`, `%2f`, `%2e%2e`, `%25`) and checks
both the structural guarantees and idempotence.

## Inputs

The grammar pieces: `/`, `.`, a letter, `%`, a hex digit, `%2e` (`.`),
`%2f` (`/`), `%2e%2e` (`..`), `%25` (`%`), `..`, `/..`, `/.`. Short paths,
a handful of segments; the bug is at the boundary where one decode leaves a
`%` that a second decode re-reads.

## Pitfalls

- **Single-decode servers are safe on single-encoded input:** a server
  that decodes `%2e`→`.` and resolves `..` handles `/%2e%2e/` correctly, so
  a spec that only checks single-encoded dots misses the bug. Check
  idempotence and doubly-encoded sequences.
- **The impact needs a second decoder:** inside one server the leftover
  `%2e%2e` is a literal filename, not a traversal. State the threat model
  (a proxy chain) so the finding is not over- or under-stated.

## Source

- Found by go2lean on valyala/fasthttp (`URI.Path()` is not idempotent;
  `/%252e%252e/secret` normalizes to `/%2e%2e/secret`, which re-parses to
  `/secret`). See [fasthttp](../eval/fasthttp.md). No prior advisory; the
  property is the general path-traversal class.
