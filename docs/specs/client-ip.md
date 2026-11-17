# The client's identity does not come from what it sends

## Applies to

General: any code that decides who the client is (an IP extractor, a
"from localhost" check, a rate limiter's key, a proxy that tells an
upstream who the client is).

## Property

Who the client is must not be decided by values the client controls,
beyond what the configured trust allows. The client's address is the
direct peer's, unless the peer is a configured trusted proxy; then the
forwarding headers may stand for it, as the documentation defines. Any
decision or value derived from the client's identity follows the same
rule: whether it is local, what is logged or rate-limited, and what a
proxy forwards upstream.

## Threat model

Write this section into every spec. A spec that leaves part of it out
misses the attack.
- The attacker can connect directly. The request's peer address is then
  the attacker's own, and it is not a trusted proxy. The attacker
  controls every header:
  - any number of `X-Forwarded-For`, `X-Real-IP` and `Forwarded` values;
  - with any content: addresses that look trusted (`127.0.0.1`, `10.x`,
    `::1`, IPv4-mapped IPv6), ports, zones, spaces, empty elements and
    garbage.
- Or the attacker is behind a trusted proxy. The proxy appends the
  attacker's real address, as the last hop or as `X-Real-IP`, and the
  attacker still controls every value it sent before that.
- When the package forwards a request, the upstream reads the header the
  way upstreams do: the first value of a repeated header.
- The attacker does not control the trust configuration.

## Oracle

- **Direct connection from an untrusted peer:** an extractor that reads
  headers must not return any address the attacker wrote in one. It
  returns the peer's address, or what its documentation says it returns
  in that case.
- **Behind a trusted proxy:** it must not return an address to the left
  of the hop that the trusted proxy appended, unless that hop is itself
  trusted as the documentation describes.
- **When the package forwards a request:** the value an upstream would
  read is the real client's.

## Inputs

- Peer addresses: trusted and not; loopback, private, public, IPv6 and
  IPv4-mapped.
- Headers: built from small pieces, single and repeated.
- Trust options: the ones the package offers.
- For a forwarding helper: a local upstream that records what it
  receives.

## Pitfalls

- On go-chi/chi `ClientIPFromXFF`, the agent followed the function's doc,
  which describes a deployment behind proxies, and never varied the peer
  address. It missed the live defect in two runs. Always vary the peer.
- An extractor documented as trusting any header (echo's
  `LegacyIPExtractor`) fails the property by design. Check its documented
  contract instead.

## Source

- gofiber/fiber GHSA-3q5p-3558-364f (`IsFromLocal` trusted
  `X-Forwarded-For`) and GHSA-gcfq-8gqf-4876 (a proxy kept the client's
  `X-Real-IP` ahead of its own).
- go-chi/chi GHSA-3fxj-6jh8-hvhx and GHSA-j6qf-3m9c-vwj3 (an XFF parser
  that never checked that the peer was a trusted proxy).
