# An authorization matcher grants no more than its pattern

## Applies to

General: any function that decides whether a request (a path, a method,
an IP address, a subject) matches a policy rule.

## Property

A matcher reports a match exactly for the requests that its rule's
documented meaning covers. Two failures matter:
- **Over-matching (a bypass):** a request the rule does not cover is
  reported as matching, so an allow rule grants more than it says, or a
  deny rule fails to match a request it covers.
- **Under-matching:** a request the rule covers is not matched, which
  gives a denial of service or a deny rule that does not apply.

Where two matchers are documented as related (one is the other plus a
feature), the relation holds.

## Threat model

- The attacker chooses the request:
  - path segments with `..`, `.`, `//`, `%2F` and other escapes, trailing
    slashes, empty segments, case and query strings;
  - IP addresses in every textual form the language's parser accepts:
    IPv4-mapped IPv6, zones, leading zeros, and the edges of a CIDR
    (`/0`, `/32`, `/128`).
- The policy's author writes the rules, and may put a wildcard or a named
  parameter at any position, as the documentation allows.

## Oracle

Write a reference from the documentation: a segment-by-segment matcher
for paths, and the language's own address type for IPs. Do not restate
the implementation.

## Inputs

- Rules with wildcards and named parameters at every position, including
  parameters that would have to span a `/`.
- Requests built from the pieces above.

## Pitfalls

- A documented limitation (a matcher that supports `*` only at the end)
  makes an over-match of a mid-pattern `*` a hardening report, not a
  breach. Say which it is.

## Source

casbin `KeyMatch` (no advisory; found by go2lean: a `*` in the middle of a
pattern ignores the rest).
