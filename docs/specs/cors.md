# CORS grants only configured origins

## Applies to

General: any CORS middleware or origin check (exact origins, wildcards,
regular expressions, origin functions).

## Property

A CORS middleware grants cross-origin access only to origins that its
configuration allows, as the package's documentation defines "allows".
Granting access means sending `Access-Control-Allow-Origin` naming the
request's origin, or `*`, and, with credentials,
`Access-Control-Allow-Credentials: true`. Credentials are never combined
with `*`. A helper that checks an origin outside the middleware (for a
WebSocket `CheckOrigin`) follows its own documented contract.

## Threat model

- The attacker controls the web page that makes the request, so it chooses
  the `Origin` among those a browser can send:
  - a serialized origin `scheme://host[:port]`, with a lowercase host of
    DNS labels or an IP literal;
  - any registrable domain the attacker can buy, including one that shares
    a suffix or a prefix with a configured one (`evil-example.com`,
    `example.com.evil.net`, `notexample.com`);
  - any scheme, any port, and the literal `null`.
- The attacker does not control the configuration.
- Whatever the package documents about wildcards (how many characters or
  labels `*` stands for) and about patterns is the contract.

## Oracle

Write a reference for "allowed" from the documented contract, compared
origin by origin: scheme, host and port for an exact entry; the documented
reach of a wildcard; for a regular-expression entry, a match of the whole
origin, unless the documentation says otherwise. Then check each response's
headers against it, on a simple and on a preflight request.

## Inputs

- Configurations: lists of exact origins, wildcard entries and pattern
  entries, with and without credentials, as the package's options allow.
  Include the patterns from the package's own tests and examples, which
  users copy.
- Origins: built from schemes, labels, dots, hyphens, ports, base domains
  that share a suffix or a prefix with a configured one, and `null`. Allow
  enough pieces that a configured domain followed by an attacker's domain
  can be built.

## Pitfalls

- A cap on the number of pieces can make the attack unreachable: on
  gin-contrib/cors the unanchored-pattern origin needed nine pieces, and
  the spec allowed eight. The agent's reading of the source found it
  instead.
- Credentials with `*` are refused by browsers. Unless the package
  promises to reject that configuration, report it as hardening, not as
  a breach of the property.

## Source

gofiber/fiber GHSA-fmg4-x8pw-hjhg (a wildcard with credentials) and
GHSA-g5jq-qmqr-qrwf (a wildcard matched by suffix only, introduced by the
first fix).
