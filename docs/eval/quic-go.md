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

## Advisories of other QUIC and HTTP/3 implementations (2026-11-20)

quic-go's own eight advisories were already in the specs above. The other
implementations have more: 25 advisories on OSV for `quinn-proto` (13),
`quiche` (5), `s2n-quic` (4) and Netty's HTTP/3 codec (4; none for
aioquic, msquic, neqo or h3). `go2lean advisory` drafted each as a general
property, and the drafts were grouped by the quic-go component they apply
to and given to `go2lean agent -specs-only` as guidance
([connection state](../specs/quic-resource-bounds.md),
[datagram, framer, paths, Retry, transport parameters](../specs/quic-resource-bounds-2.md),
[HTTP/3 decoder](../specs/http3-decoder-bounds.md)). A connection cannot be
built inside a spec, so each property was stated on the component that owns
the state.

| class (advisory examples) | quic-go component | result |
|---|---|---|
| out-of-order stream data memory, zero-length frames (quinn GHSA-4w2j, RUSTSEC-2026-0341/0338) | frame sorter, receive stream, flow control | held, 0.6M runs each |
| frames that name a stream must not open streams past the limit (RUSTSEC-2026-0523, s2n GHSA-475v) | incoming streams map | held; also read by hand: `getSendStream` checks `StreamLimitError` |
| connection ID retirement floods and loops (quiche GHSA-xhg9, GHSA-m3hh; quinn RUSTSEC-2026-0339) | connection ID generator and manager | held |
| CRYPTO frame floods (quiche GHSA-78wx) | crypto stream buffer | held |
| zero-length and flooding DATAGRAM frames, send accounting (RUSTSEC-2026-0342/0343) | datagram queue | held, 1.0M to 1.5M runs |
| unbounded PATH_CHALLENGE and PATH_RESPONSE state (quiche GHSA-w3vp) | framer, path manager | held |
| Netty: frame length, QPACK literal, reserved frame types, default header limit | http3 frame reading and header block | 1 latent defect (below), rest held |

What came out of it:

- **No defect a caller can reach.** quic-go's own history (CVE-2024-22189
  and the others) already closed these classes, and the specs agree.
- `http3.readHeaderBlock` treats a declared length of 2^63 or more as a
  successful empty read (`int64(length)` wraps to -1 and `io.CopyN` with a
  negative count copies nothing). Every caller rejects lengths above
  `maxHeaderBytes` first, so it is latent; a note, not a report.
- `http3.ErrCode.String` prints only the low 16 bits of an unknown code.
  Cosmetic.
- `findSNIAndECH` tests `sniPos == 0` where it means `-1`, so an SNI
  extension with no `host_name` entry is not an error; a client built on
  Go's `crypto/tls` never sends one. Cosmetic.
- The Go fuzzer reported one spec violated (the MTU finder's lost-probe
  array staying sorted) that could not be reproduced by hand on the inputs
  it printed; the MTU finder is an internal heuristic with no RFC rule, so
  it is left open. The second round of the agent was cut short by the
  environment, so its revised specs were fuzzed by hand.

## RFC 9000 rules on a live connection (2026-11-21)

The component specs above cannot see how a whole connection reacts to a
frame. [`examples/quic-conformance`](../../examples/quic-conformance/README.md)
does: an attacking endpoint, built from a copy of quic-go with a method that
queues raw frame bytes, completes a handshake with an unmodified quic-go
and sends the frames of one RFC 9000 sentence; the table records the error
the RFC requires and the harness compares it with the error the receiver
closes with. 45 rules with the attacker as a client and 10 with the
attacker as a server, on master `90eebba`:

- **50 of 55 agree**: frame types and encodings, stream state and limits,
  final sizes, flow control at both levels, connection ID rules, ACK
  validity, client-only frames (NEW_TOKEN, HANDSHAKE_DONE), CRYPTO bounds,
  and the valid cases that must not close the connection.
- **5 differ**:
  1. A NEW_CONNECTION_ID that uses an already held sequence number for a
     different connection ID (or a different stateless reset token) closes
     the connection with **INTERNAL_ERROR** (`conn_id_manager.go`,
     `addConnectionID` returns a plain `fmt.Errorf`). RFC 9000 section
     19.15 allows PROTOCOL_VIOLATION here (a MAY); INTERNAL_ERROR tells the
     peer the receiver failed locally. Reproduced three times. Low.
  2. A NEW_CONNECTION_ID that repeats a connection ID under a different
     sequence number, or one sequence number for two IDs after a Retire
     Prior To, is accepted (MAY, so allowed).
  3. A CRYPTO frame whose end offset passes 2^62-1 closes with
     CRYPTO_BUFFER_EXCEEDED (0x0d); section 19.6 names FRAME_ENCODING_ERROR
     or PROTOCOL_VIOLATION. quic-go checks its 16 KiB buffer first, which
     the same RFC allows for data it cannot buffer; ambiguous.
  4. A DATAGRAM frame when the extension was not negotiated closes with
     FRAME_ENCODING_ERROR; RFC 9221 section 4 requires PROTOCOL_VIOLATION.
     `frame_parser.go` says it is deliberate.

The same attacker can also send random frame sequences (`FUZZ=n`): 2,050
connections over six seeds, half the frames valid and half edge values,
never crashed or hung the server and never produced a code RFC 9000 does
not define; the only INTERNAL_ERROR was the conflicting sequence number
above, found again.

Packet-level rules, on the same revision (all agree): ten raw-datagram
rules (version negotiation for an unsupported version at 1200 bytes with
the connection IDs swapped and without the offered version; unsupported
version below 1200 bytes, over-long connection IDs, a Version Negotiation
packet, a 20-byte short header packet and a cleared Fixed Bit all dropped;
Stateless Resets of at least 21 and under three times the trigger, one byte
shorter than triggers of 43 bytes or fewer, and no loop on a reply to a
reset); reserved header bits set on a 1-RTT and on an Initial packet end in
PROTOCOL_VIOLATION; a packet with the Key Phase bit flipped and sealed with
the current keys is discarded and the connection goes on; and the
anti-amplification limit holds for a client that never answers (the server
sent 0.75 times what it received) and on a new path it cannot validate
(1.0 times).

So the connection layer agrees with RFC 9000 on everything the table can
express with raw frames; the differences are error codes. What the table
does not reach: packet-level rules (reserved bits, empty packets, header
protection, key update), path migration, Retry and version negotiation,
which need the attacker to control whole packets.

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
