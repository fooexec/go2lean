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
| `internal/protocol` (packet numbers against RFC 9000 A.3 and its example, stream IDs, connection IDs, versions) | 29 | held | held, 1.1M to 5.4M runs each; rerun with seeds: 30 held, one spec with no input not run (the export cannot fuzz a function of no argument) |
| `internal/utils` (RTT estimator against RFC 9002 5.3 and A.7, PTO) | 4 | held (the RFC-9002 sequence spec was first spurious; see below) | held, 1.5M to 2.2M runs each |
| `internal/congestion` (Reno window against RFC 9002 7, pacer, hybrid slow start) | 5 | held, but for one spec written to fail: `SeqHybridSlowStartLatchedExitIgnoresWindow`, confirmed (below); first written up as held | held, 1.8M to 2.5M runs each; rerun with seeds: the same, the witness spec violated on the all-zero seed |
| `internal/ackhandler` (PTO backoff, loss and PTO timer selection against RFC 9002 6.1, 6.2 and A.8; the sent-packet handler driven through sends, ACKs, ECN and timeouts against an honest and an adversarial peer) | 3 | held (all three spurious before the `Duration` accessors were modelled; see below) | held, 3.5M to 5.0M runs each |
| `internal/wire` (frame round trips and lengths, ACK ranges, transport parameters, headers) | 40 | **5 violated and confirmed**: the ACK delay and `max_idle_timeout` wrap (two defects, below), and the three `MaxDataLen` specs (a precondition the specs omit, below); first written up as all held, corrected 2026-11-23 | first run, no seeds: 38 held, all five among them (**misses**, #290), 2 violated, both specs' own (below). Rerun with seeds (2026-11-23, 10 s each): the same 5 violated, 38 held |
| root package (frame sorter against a byte array, stream flow control, token store) | 4 | held | held, 1.5M to 1.6M runs each |

The `internal/utils` RTT sequence spec (`SeqRTTMatchesRFC9002`, RFC 9002 §5
and §6.2.1) at first produced a spurious counterexample: the estimator
stores every reading through `(time.Duration).Nanoseconds`, which the model
did not translate, so the smoothed RTT and mean deviation became havoc and
the model's "violation" was a choice Go does not make. A `Duration` is an
`int64` of nanoseconds, so these accessors (`Nanoseconds`, `Microseconds`,
`Milliseconds`, `Seconds`, `Minutes`, `Hours`, `Truncate`, `Round`, `Abs`)
are now translated from source and compute exactly. The sequence spec then
holds with no spurious counterexample and a 19 of 19 mutation score: every
op and branch mutation of `UpdateRTT` is caught, so the RFC-9002 RTT and PTO
arithmetic is pinned down, not merely unviolated. This is the model's time
line (#59) reaching the duration arithmetic the RFC algorithms are written
in, rather than havocking at the package boundary.

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

**The ACK delay wraps** (correction, 2026-11-23). `SeqAckDelayScaling`
checks that an ACK frame's delay decodes as RFC 9000 19.3 says: the field
times 2^ack_delay_exponent microseconds, saturating where it does not fit.
The model found a violation at trial 0 in both runs, and the replay
confirmed it (`real: ret false`). Go's fuzzer ran 2.7M executions and held.
The write-up then took the fuzzer's verdict for the spec and listed it as
held. That was a triage error: a confirmed counterexample is a finding,
whatever another engine says (#317). The fuzzer missed it because its
exported target starts from an empty corpus, and about half of all
uniformly random inputs fail; with eight random seeds it fails at once
(#290).

The defect: `parseAckFrame` computes `time.Duration(delay*1<<exp) *
time.Microsecond`. The shift wraps in `uint64`, and the product in `int64`,
both to non-negative values, so the following `delayTime < 0` check, meant
to saturate an overflow, never fires. A delay field of 2^61 with the
default exponent 3 decodes to 0; a field of 18446744073709552 with exponent
0 decodes to 384 ns instead of about 584 years. This is checked by hand on
master `cb08e8f`. **Severity: low, no security impact.** Both places that use
the delay cap it with `min(ack.DelayTime, MaxAckDelay())`, and a peer can
send any delay in that range directly. A second spec, written independently
from msquic's ACK decoding advisory (`SeqAckFrameDecodesAsRFC`), found it
again at trial 89 and was also held by the fuzzer, over 3.7M executions.

**`max_idle_timeout` wraps too** (2026-11-23). `SeqTransportParametersIdleTimeoutNoOverflow`
checks that a `max_idle_timeout` in milliseconds never turns into a
wrapped duration. The model found it at trial 0 and the replay confirmed it
in the same first round; the unseeded fuzzer held it (466,661 runs), and the
write-up dropped it too. `TransportParameters.Unmarshal` sets
`MaxIdleTimeout = max(MinRemoteIdleTimeout, time.Duration(val) *
time.Millisecond)`: above about 9.2·10^12 ms (292 years) the product
overflows, and the `max` turns the wrapped value into 5 s. RFC 9000 10.1
makes the effective idle timeout the minimum of the two endpoints'
values, so a peer advertising a very long timeout should leave quic-go's
own (30 s by default) in force; quic-go instead closes the connection after
5 s of idleness. **Severity: low, no security impact**: a peer can
advertise 5000 ms directly. Checked by hand on master `cb08e8f`.

**The three `MaxDataLen` specs** (`CryptoFrame`, `StreamFrame`,
`DatagramFrame`) were violated and confirmed in the same round, and
dropped from the write-up as well. They are not defects that can be
reached: `MaxDataLen` assumes the frame's length field takes at most two
bytes, as it does below 16384 bytes, and subtracts one byte for it; with a
larger `maxSize` the frame exceeds it by two or six bytes. Every caller
passes at most a packet's payload (`MaxPacketBufferSize` is 1452), and
`SendDatagram`, which passes the peer's `max_datagram_frame_size`, takes the
minimum with that payload estimate first. The specs lack that precondition.

**The hybrid slow start witness.** `SeqHybridSlowStartLatchedExitIgnoresWindow`
was written to fail: `hybridSlowStart.ShouldExitSlowStart` returns true on
every call after the one that found a delay increase, whatever the
window, while the comment says it exits when "the cwnd is greater than 16
and increasing delay is found". The model confirmed it, and the write-up
listed the package as held. This is the comment's imprecision, not a
defect: the window test guards the detecting call only, as in Chromium's
`HybridSlowStart`, which this code follows.

**The rerun with seeds** (2026-11-23, after #290's seeds and #322): every
spec of `internal/wire`, `internal/protocol`, `quicvarint`,
`internal/utils`, `internal/congestion` and `internal/ackhandler` under
Go's fuzzer for 10 s from the seed corpus. It finds exactly the specs the
model had confirmed and nothing more. For those packages "held under Go's
fuzzer" is evidence again. `http3`'s specs mostly take interface
parameters (`io.Reader`, `quicvarint.Reader`), which the export cannot
build, so their fuzzer results above come from the hand-written harnesses
of those rounds and were not rerun.

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
closes with. 46 rules with the attacker as a client and 10 with the
attacker as a server, on master `90eebba`:

- **51 of 56 agree**: frame types and encodings, stream state and limits,
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

Packet-level rules, on the same revision (all agree; the migration case is open): ten raw-datagram
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
sent 0.75 times what it received). The same limit on a new path is checked
further (migration rules, not yet published).

Transport parameters (the attacker rewrites the encoded parameters it
sends in the handshake): 17 rules for what a server does with a client's
(a repeated parameter, server-only parameters, `max_udp_payload_size`,
`ack_delay_exponent`, `max_ack_delay`, `active_connection_id_limit` and
stream counts out of range, a missing or wrong `initial_source_connection_id`,
a truncated block; and the valid edge cases and an unknown parameter) and 8
for what a client does with a server's (missing or wrong
`original_destination_connection_id` and `initial_source_connection_id`,
`retry_source_connection_id` without a Retry, a short reset token, a
preferred address with a zero-length ID). All 25 agree.

In total 96 rules against `90eebba`: 91 agree, 5 differ, all in the error
code of a MAY or an ambiguous case, listed above.

So the connection layer agrees with RFC 9000 on everything the table can
express with raw frames; the differences are error codes. What the table
does not reach: packet-level rules (reserved bits, empty packets, header
protection, key update), path migration, Retry and version negotiation,
which need the attacker to control whole packets.

## Stream state machines and concurrency (2026-11-21)

The earlier model searches on quic-go did not target the places where the
model might be strongest: state machines and goroutine interleavings. A first
try: a guidance file with RFC 9000 section 3 (the send and receive stream
states, final size, completion once, blocked `Write` and `Read` woken by
every event that should wake them) and `go2lean agent -specs-only` on the
root package. The agent wrote three sequence specs with a small reference
state machine inside each (`SeqSendStream`, `SeqRecvStream`,
`SeqReadUnblocks`). Go's fuzzer, 60 s on the real code: all three held. The
model's counterexamples were all the model's: two spurious and one
inconclusive (the model's schedule did not reproduce).

One cause was a frontend rule, fixed in this change: a pointer to a struct of
another package that has unexported fields and a constructor (`wire.StreamFrame`,
`wire.GetStreamFrame`) was treated as an opaque handle, so `f.Data = ...`
on a frame the model had just taken from the pool was dropped as a store into
a handle. A pointer the model built itself (an allocation, the result of a
modelled function, a pointer loaded from a field of an object the model
owns) is no longer a handle; a handle passed in or returned by an unmodelled
call still is. `SeqSendStream` is no longer spurious. `SeqRecvStream` still
is (a havoc site in the receive path) and `SeqReadUnblocks` stays
inconclusive. The three specs also kill few mutants (2, 6 and 2 of 24), so
they check little; that, not the model, is the next thing to improve before
claiming that the stream state machines are right.

### Stronger send-side spec (2026-11-22)

The surviving mutants of the first three stream specs pointed at what they
did not drive: flow-control windows of a few bytes, a frame budget that cuts
a frame, STREAM_DATA_BLOCKED, two streams on one connection window. A second
agent round, with the survivors listed in the guidance and one spec asked for
(the first attempt, three specs at once, ran out of output tokens in thinking;
`-thinking=false` and one spec worked), wrote `SeqSendStreamFlow`: two send
streams, windows and budgets chosen by the events, a reference model of
delivered offsets, windows, FIN and blocked frames.

- **One deviation, reproduced on master `81d1aff7`**: `SendStream.popStreamFrame`
  returns a FIN-only frame whatever `maxBytes` is. After `Close()` with
  nothing written, `popStreamFrame(0)` returns a 3-byte STREAM frame with the
  FIN, and marks the FIN sent. Its comment says "maxBytes is the maximum
  length this frame (including frame header) will have." The only caller
  (`framer.AppendStreamFrames`) pops only when the room is at least
  `MinStreamFrameSize`, 128 bytes, so no packet overruns today; it is a broken
  contract, not a defect a peer can reach.
- The revised spec, with the budget floor raised past that case, still
  returned false on real Go for an input the fuzzer found. Tracing it: the
  spec's own helper reuses one variable for the budget and for the window
  update, so the second stream's window was set to the last budget. A bug of
  the spec, not of quic-go; the run stopped before the agent's repair round,
  so there is no corrected version yet.

### The send-side spec, repaired (2026-11-22)

Asked again with the spec bug named, the agent wrote `SeqSendStreamFlowControl`
(two send streams on one connection window; windows of a few bytes; budgets
of 24 to 60 bytes; every failure is a `check` with a message, recovered into
`false`). Go's fuzzer, 90 s on the real code: held, 2.8M runs. The model's
counterexample was spurious again, from another case of the handle rule: a
frame returned with a bool by `popNewStreamFrame` is one result of a call
that returns several, and was still treated as an opaque handle. That is
fixed now (a result of a call to a modelled function is built by the model,
whichever result it is), and the model agrees with Go on this spec: no
counterexample, no spurious one.

The spec still kills 1 of 24 mutants of the send path in go2lean's mutation
pass (the survivors are branches of `popNewStreamFrameForPacket`, the flow
controllers and `getDataForWriting`). Two things I tried to explain it:
a hand-made mutant of the real code (`finSent` never set, so the FIN is sent
again) makes the spec false on 54% of 20000 random sequences run natively in
Go, and the original code on none, so the spec is not weak against that kind
of change; an unlimited search budget gives the same 1 of 24, and replacing
the spec's `recover`-based failures by a flag gives 2 of 24, so neither the
time per mutant nor the `recover` is the cause. What is left is the mutants
themselves (a negated branch of the model may be equivalent for this spec's
inputs, or the model mutant may not be what the source-level one is). I then
compared surviving mutants natively. The survivors' blocks map to source
lines of `send_stream.go` (the model's block index is go/ssa's block index).
Mutating the source and running the spec on 20000 random sequences in Go:
`s.nextFrame != nil` flipped (line 416, a "ne to eq" survivor) makes the spec
false on 54% of the sequences, the negated `TryAddBytesSent` check (line 439)
on 54%, `if s.writeLimited` negated (line 387) on 85%, none of them by a
panic. So the mutation pass reports survivors that the spec kills. Three
other survivors are real gaps of the spec: `s.resetErr` and
`reliableOffset` (line 413, no `CancelWrite`, STOP_SENDING or RESET_STREAM_AT
among the events), `s.nextFrameReserved` (line 408, no write through a
limiter) and `!s.finSent` at line 461 (the earlier branch returns first, so
the mutant is probably equivalent); the spec kills none of those natively
either. What makes the model-level mutation miss the killable ones is not
found; it is tracked as an issue (#305).

### The spec extended to the reset and limiter paths (2026-11-22)

The surviving mutants also showed the spec drove neither the reset path
(`resetErr`, `reliableOffset`) nor writes through a limiter
(`nextFrameReserved`). An agent round extended `SeqSendStreamFlowControl`
with `CancelWrite`, a STOP_SENDING frame, RESET_STREAM_AT with a reliable
boundary, `WriteWithLimit`, and the acknowledgement and loss of popped
frames. The outcome, again, was no defect in quic-go: the send stream holds
on the reset path too. The extension instead exposed a bug in the spec's own
completion rule — a stream whose application called `Close` before a
STOP_SENDING arrived completes once (`isNewlyCompleted` takes the
`finishedWriting` branch), and the spec expected zero. The agent restated the
rule as "a reset stream completes if the application flagged the cancellation
or closed the stream", but the container reclaimed the run before the
revision finished, so there is no committed extended spec yet. Two
conclusions stand: the send state machine agrees with the RFC-derived model
on every path tried, and a strong state-machine spec is still hard to get
right in one or two agent rounds — each round so far has debugged the spec,
not the package.

### The receive side (2026-11-22)

A spec for the receive stream (`SeqReceiveStreamRFC`, driving
`ReceiveStream.handleStreamFrame` and `Read` over `frame_sorter.go`): STREAM
frames at arbitrary offsets, overlapping and duplicate, with FIN; Reads of
every size; RESET_STREAM and CancelRead; two streams on one connection
window. Reference model: the contiguous prefix as a byte array, the highest
offset, the final size, a reset flag. Go's fuzzer, 90 s on the real code:
held, 2.8M runs. The model gave one spurious counterexample (model false,
real true) from a havoc site the receive path reaches, not from the spec.
So the receive state machine, like the send side, agrees with the
RFC-derived model on every sequence tried; no defect. The run was first
killed by the container's disk filling during the Lean build (the per-run
workspaces are about 1 GB each); rerun after freeing space, it completed.

### Connection close: the closing state (2026-11-22)

RFC 9000 10.2.1 and 21.1: in the closing state an endpoint retransmits
CONNECTION_CLOSE in response to received packets, at a rate that cannot
amplify. `closedLocalConn.handlePacket` (`closed_conn.go`) does this with a
power-of-two backoff (it sends only when its packet counter has a single
set bit). A hand-written spec `SeqClosedConnRateLimit` drives it with a
sequence of received packets and checks, against an independent counter,
that a send happens exactly on the 1st, 2nd, 4th, 8th, ... packet, that the
count is monotone, never exceeds the packets received, and stays at most
`floor(log2 n) + 1`. It holds: exhaustive over 27931 inputs, and Go's
fuzzer 61 s (1.4M runs). No defect; the closing-state retransmit is
rate-limited as the RFC requires.

This spec is also a minimal reproducer of the mutation under-reporting
issue (#305). The function is eight lines; the spec kills a mutant that
inverts the send condition (`!= 1` to `== 1`) on a one-packet sequence when
run natively in Go, and the original holds. go2lean's model-level mutation
pass, with an unlimited budget and the exhaustive engine certifying the
original, at first reported that mutant (and two others) as survivors: 1 of
4. The cause (#305, fixed): the mutation pass ran each mutant with `check`
and no alternative havoc draws, so a mutant whose refuting run reads a havoc
value (here the `sendPacket` closure call) returned `specUnknown` instead of
a refutation and was counted as a survivor. The random search passes those
draws (#84) and confirms the refutation; the mutation pass now does the
same. With the fix this spec kills 4 of 4.

### Loss detection and PTO, `internal/ackhandler` (2026-11-23)

The package had no spec until the `Duration` accessors were modelled:
`monotime.Time.Add` goes through `Duration.Nanoseconds`, so every timer the
handler computes was havoc. Three sequence specs
([`quic-go/ackhandler_specs_test.go.txt`](quic-go/ackhandler_specs_test.go.txt),
on quic-go `81d1aff7`), each against an independent reference:

- `SeqScaledPTOBackoff`: the PTO period doubles per consecutive PTO (RFC 9002
  6.2.1), capped at 60 s; it is positive, never above the cap, and one more
  PTO never shortens it, including shift overflow at large PTO counts.
- `SeqLossAndPTOTimers`: over reachable handshake states, the loss timer and
  the PTO timer pick the time and space of RFC 9002 A.8
  (`GetLossTimeAndSpace`, `GetPtoTimeAndSpace`); no Application Data PTO
  before the handshake is confirmed; an outstanding ack-eliciting crypto
  packet always arms a PTO.
- `SeqSentPacketHandler1RTT`: a server's handler from its constructor, after
  the handshake, through 1-RTT sends, single-packet ACKs and timeouts.
  `bytes_in_flight` equals the history's in-flight packets and never exceeds
  the bytes sent; the timer is armed exactly while an ack-eliciting packet is
  outstanding; a PTO timeout raises the PTO count by one; an ACK that newly
  acknowledges a packet resets it and leaves no in-flight packet below the
  largest acknowledged older than the time threshold (RFC 9002 6.1.2).

| spec | go2lean before (`7e6378e`) | go2lean after (`07243a7`) | Go fuzzer, 60 s |
|---|---|---|---|
| `SeqScaledPTOBackoff` | spurious (`Nanoseconds`, `Abs` havoc) | holds, mutation 4 of 4 | held, 4.8M runs |
| `SeqLossAndPTOTimers` | spurious (`Nanoseconds` havoc) | holds, mutation 22 of 24 | held, 5.0M runs |
| `SeqSentPacketHandler1RTT` | spurious (`Nanoseconds` havoc) | holds, mutation 8 of 24 | held, 4.1M runs |

No defect. In this first version the handler spec's mutation score was low
because its set-up did not reach most of the mutated code: ECN off, single
packet ACKs, no ack delay, no spurious loss.

**The handler spec tightened.** `SeqSentPacketHandler1RTT` now runs with
ECN on and max_ack_delay 25 ms, and plays both an honest and an adversarial
peer: ACKs of any subset of the recent packets in several ranges with an ack
delay, ECN counts of what arrived (some arrivals CE-marked by the path),
the same with tampered counts, ACKs of arbitrary packet number ranges, and
bursts of up to 12 packets (enough to finish ECN testing). On top of the
first version's checks:

- an ACK of sent packets is not an error, one of a packet number never sent
  is (RFC 9000 13.1);
- the RTT sample is the largest newly acknowledged packet's, less the ack
  delay capped at max_ack_delay when that keeps it at least min_rtt (RFC 9002
  5.3);
- a packet with 3 sent packets after it up to the largest acknowledged is
  declared lost (6.1.1), unless quic-go turned the packet threshold off after
  a spurious loss;
- ECN validation (RFC 9000 13.4.2.1): packets go unmarked or ECT(0); failed
  stays failed; an ACK that does not increase the largest acknowledged does
  not change the state; one that does fails validation when it reports more
  ECT(0) than sent, acknowledges ECN-marked packets without counts, lowers a
  count, or raises ECT(0)+ECN-CE by less than the ECT(0) packets it newly
  acknowledges; an honest path that never CE-marks never fails it;
- the time threshold stays within 9/8 and 16/8 and never tightens, and a
  packet threshold turned off stays off (quic-go's relaxation after spurious
  losses, which RFC 9002 6.1 allows).

It holds in the model and under Go's fuzzer (3.5M runs in 60 s; the other
two specs again 4.7M and 4.8M), and its mutation score goes from 8 to 12 of
24. Of the 12 survivors, 6 can be placed by the function they mutate: 2 are
in path-probe code (`RemovePathProbe`, `detectLostPathProbes`), which the
set-up never reaches; 2 are in the spurious-loss heuristic
(`detectSpuriousLosses`), whose threshold increase the RFC leaves open and
the spec bounds only; 2 are in the ECN tracker, one of them in the mangling
check, a MAY of RFC 9000 13.4.2.1. The other 6 (two in `receivedAck`, two in
`detectAndRemoveAckedPackets`, one each in `getPTOTimeAndSpace` and
`detectLostPackets`) are not classified: the mutation report names blocks of
go2lean's IR, which does not map one to one onto Go's SSA blocks, so they
are not traced to source lines here.

Before the run, the spec was checked natively (50000 random sequences, with
coverage). That caught one more spec mistake: an RTT sample of 0 (an ACK at
the instant of sending) is not taken by `RTTStats`, and the spec now skips
it. The coverage also showed the ECN mangling state was never reached, which
is why the bursts of sends were added.

A first version of the handler spec was stronger than the RFC: it required
every ACK to reset the PTO count. RFC 9002 A.7's `OnAckReceived` returns
before `pto_count = 0` when the ACK newly acknowledges nothing, and quic-go
does the same; the spec was corrected, not the package.

## Calibration on quic-go's own advisories (2026-11-23)

`go2lean calibrate` (#315 P3) checks that a spec written for a fixed bug
is violated before the fix and holds after it. The first case is the
probe-packet panic, GHSA-j972-j939-p2v3, fixed in `b90058ab` (candidate
c001 of `go2lean propose` on quic-go's advisories). The spec,
[calibrate_ackhandler_test.go.txt](quic-go/calibrate_ackhandler_test.go.txt),
drives the sent packet handler through up to 8 operations: sends, path
probes, loss detection timeouts and ACKs naming any of the first 8 packets,
with the clock moved by 0, 100 ms, 500 ms or 1.5 s after each.

| revision | result |
|---|---|
| `b90058ab^` | violated: the model finds a nil dereference at random trial 5215; the replay confirms it (`packet.go:29`) |
| `b90058ab` | held: 50000 random trials, 354109 inputs searched exhaustively in the 600 s budget |

Run's default 2000 trials held before the fix too; plain Go, calling the
spec on random inputs, panics at about the 9000th. This is why calibrate
runs a larger search by default. The results are in
[calibration.json](quic-go/calibration.json). The QPACK header limit fix
(GHSA-g754-hx8w-x2g6) cannot be calibrated this way: it adds a parameter
to `requestFromHeaders`, so one spec file does not build on both
revisions.

The second case is the control frame queue (GHSA-c33x-xqrf-c478, fixed in
`4a99b816`, candidate c003): a peer's frames make the endpoint queue
control frames it cannot send, without a bound before the fix, at most
16384 after it. The spec,
[calibrate_framer_test.go.txt](quic-go/calibrate_framer_test.go.txt),
queues 2^15 + 1 frames and checks the queue holds at most 2^15, twice the
fix's limit, so the bound is not the fix's own constant.

| revision | result |
|---|---|
| `4a99b816^` | violated at random trial 0 |
| `4a99b816` | held: 330 random trials in the 600 s budget, the 6 exhaustive inputs |

The first run did not calibrate. Every run before the fix ended out of
fuel, and calibrate counted that as a hold. Two things were wrong. The
model's slice bound was 16384 elements, and a queue past it ended the run
like fuel; the bound is now 2^20. And a search that checked no input to
the end is now `unknown`, not held.

## Candidates from other implementations' advisories, by the agent (2026-11-23)

`go2lean agent -candidates` (#315) took 12 of the candidates that
`go2lean propose` drafted from other QUIC implementations' advisories and
that target quic-go's root package. The agent wrote 15 specs from them (13 sequences, 2 postconditions),
on master `cb08e8f`, with the slice bound at 2^20. The run took 34
minutes.

| outcome | specs |
|---|---|
| held | 9: crypto buffer bound, datagram queue bound and Peek/Pop, connection ID manager state, retirement bound and error codes, token store order, `Config.Clone` independence, connection ID round trip |
| confirmed, not a defect | 2 (below) |
| spurious | 4: a function value the model does not know (connection ID generator), `crypto/tls` havoc (packing an Initial), a load through a handle (stream frames), and `slices.overlaps` (frame sorter, below) |

The two confirmed results:

- **A CRYPTO frame for a level whose crypto stream was dropped** dereferences
  nil (`crypto_stream_manager.go:30`). It is not reachable. The connection
  drops a level's crypto stream together with its keys
  (`DiscardInitialKeys`, `SetHandshakeConfirmed`), so a later packet at that
  level fails with `ErrKeysDropped` before a frame is parsed. Latent; a note.
- **A NEW_CONNECTION_ID frame that reuses the active connection ID's
  sequence number with another ID** is ignored as a retransmission, not
  rejected. RFC 9000 §19.15 makes the rejection optional ("MAY"), so the
  agent's spec is stronger than the RFC. Not a defect.

One spurious result was a model gap. `slices.Insert` and `slices.Replace`
call `slices.overlaps`, which compares element addresses through
`unsafe`; the model read it as havoc, and the frame sorter's spec failed in
the model only. `slices.overlaps` is now modelled exactly (`sliceOverlap`,
six sem-diff tests), and the spec holds: 2000 trials, 17 of 24 mutants
caught.

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
