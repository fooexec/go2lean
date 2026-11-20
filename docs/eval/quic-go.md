# quic-go (2026-11-19)

[quic-go](https://github.com/quic-go/quic-go) is the QUIC and HTTP/3
library behind much of Go's HTTP/3 (Caddy, Cloudflare's tooling, many
proxies). It is on OSS-Fuzz and its parsers are well tested, so the
properties here are the ones a crash-guided fuzzer does not state: a
round trip, an agreement with the RFC's own algorithm, a rule an RFC
places on what is accepted. Default branch, `a02d451` (2026-11-19). Its
[AI policy](https://github.com/quic-go/quic-go/blob/master/AI-POLICY.md)
asks for disclosure and for a report in the reporter's own words.

## Where the specs came from

- **The documentation and the RFCs:** one guidance file per area, each with
  the RFC section as the oracle (quicvarint, wire frames, http3 headers,
  the received-packet history; later RFC 9002's RTT and Reno, packet
  numbers, transport parameters, stream reassembly).
- **The project's history:** the 8 published advisories on OSV
  (`api.osv.dev`), drafted into properties with `go2lean advisory`; and
  the fix history. In the last three years http3 gained a stream of
  "reject X" commits (field names and values, pseudo-headers, `:authority`,
  `:scheme`, `:path`, `:status`, Content-Length, trailers), each a check that
  was missing when it was added. The guidance gave the classes and asked
  for the complete RFC rule set, so that a check still missing is found
  whatever its class. OSS-Fuzz's own tracker cannot be read from here, and
  OSV has no OSS-Fuzz entries for quic-go.

## Results

| area | specs | model search | Go's fuzzer |
|---|---|---|---|
| `quicvarint` | 7 (round trips, length, parse agreement) | held | held, 2.3M to 3.0M runs each |
| `http3`, round 1 (RFC 9114 rules, request round trip) | 8 | 2 counterexamples, both spurious (the model chose `Header.Get` and `CanonicalMIMEHeaderKey`) | held, 1.1M to 1.8M runs each |
| `http3`, round 2 (history and the QPACK advisory) | 7 | 3 counterexamples, all spurious | **3 specs violated**, 3 held (size limits, trailers, plain CONNECT with a port) |
| `http3`, round 3 (RFC 9114 frames, SETTINGS, GOAWAY, PRIORITY_UPDATE, capsules; complete rule set) | 11, then 11 revised | 3 counterexamples, all spurious (a `closeConn` closure the model does not know) | **1 spec violated** (reserved SETTINGS identifiers), the rest held, 1.0M to 2.1M runs each |
| `qpack` v0.6.0 (RFC 9204/7541: integers, static table, Huffman, field sections, encoder round trip) | 6 | 1 counterexample, spurious (stuck in `Decoder.at`) | 2 violated, both specs stronger than the RFC (non-minimal and over-long integer encodings), 4 held |
| `internal/protocol` (packet numbers against RFC 9000 A.3 and its example, stream IDs, connection IDs, versions) | 29 | held | held, 1.1M to 5.4M runs each |
| `internal/utils` (RTT estimator against RFC 9002 5.3 and A.7, PTO) | 4 | held | held, 1.5M to 2.2M runs each |
| `internal/congestion` (Reno window against RFC 9002 7, pacer, hybrid slow start) | 4 | held | held, 1.8M to 2.5M runs each |
| `internal/wire` (frame round trips and lengths, ACK ranges, transport parameters, headers) | 40 | held | 38 held; 2 violated, both specs' own (below) |
| root package (frame sorter against a byte array, stream flow control, token store) | 4 | held | held, 1.5M to 1.6M runs each |

The three violations of round 2 are all inputs quic-go accepts and the RFC
rules out (reproduced by hand on `a02d451`, with the same field sent to
`net/http` for comparison):

1. **An empty `content-length`** (request or response) is accepted and
   dropped: `ContentLength` is -1 and no `Content-Length` header is set.
   `net/http` answers `invalid empty Content-Length`; RFC 9110 section 8.6
   defines the field as `1*DIGIT`. The code skips the parse when the
   string is empty (`http3/headers.go`).
2. **A plain CONNECT whose `:authority` has no port** (`example.com`) is
   accepted, with `Host` set to it. RFC 9114 section 4.4 and RFC 9110
   section 9.3.6 make it `host:port`.
3. **A field value with leading or trailing whitespace**
   (`content-type: " text/html "`) reaches `http.Request.Header` as it is;
   `net/http` gives `text/html`. RFC 9110 section 5.5 says a recipient
   excludes it before evaluating the value. RFC 9114 does not repeat the
   rule, so this one is a reading of the RFC.

A fourth, from the frame rule set (round 3), reproduced by hand on `90eebba`
(master at 2026-11-20): **a SETTINGS frame with a reserved HTTP/2 identifier**
(0x00, 0x02, 0x03, 0x04, 0x05) parses without error; the identifier lands in
`settingsFrame.Other` (`parseSettingsFrame`, the `default:` branch). RFC 9114
section 7.2.4.1 makes receipt of these a connection error of type
H3_SETTINGS_ERROR. Low severity: an unusual peer, no crash. The rest of the
frame rules held (reserved frame types, unknown types skipped, GOAWAY length,
truncated headers, duplicate and boolean settings, PRIORITY_UPDATE, capsules).
In `qpack`, the agent's report notes that a `DecodeFunc` call after an error
can return `io.EOF` (the error is not sticky); the doc comment says any
non-EOF error is fatal, so this is a note, not a defect.

The two `internal/wire` violations are not defects. One is a transport
parameters default check that the agent revised after the fuzzer had run on
the first version (in real Go the defaults are the RFC's). The other is
`MaybeSplitOffFrame` panicking on more than 1452 bytes of data, the size of
the pool buffer it splits into; callers pass at most one packet of data, so
the API cannot reach it, and the agent's revised spec states that
precondition.

**Severity: low**, all four. Each needs a peer that sends an unusual
field, none is a memory or crash problem, and the effect is that an
invalid message is processed as a valid one. They matter where another hop
reads the same message differently (an HTTP/3 front end that forwards the
field to a server, a CONNECT proxy that dials `r.Host`). The drafts are in
[upstream reports](../blog/upstream-reports.md).

## What this says

- The model search found nothing real here: its two rounds gave five
  counterexamples, all spurious because the model chose results of
  `net/http` calls it does not model. Go's fuzzer, on the same specs,
  found all three. The specs came from the project's own history and the
  RFC; the model's part was to exhaust small domains and to say which specs
  held.
- The method of the previous runs holds here too. A spec stated as "the
  complete rule set of the RFC, independent of the package" finds the check
  the project has not added yet, where a spec for one past commit would
  only find that commit.
