Property source: security advisories of other QUIC implementations (quinn-proto, quiche, s2n-quic), each drafted as a general property below. The same defect classes can exist in this package; none of these advisories is about this package. Your job is to check each property against this package's own code, at the level of its components, since the whole connection cannot be built in a spec:

- stream data and limits: the frame sorter (frame_sorter.go: bytes and gaps retained against the flow-control credit, zero-length frames, overlapping and duplicate frames, offsets near 2^62-1), receive_stream.go and send_stream.go (final size, resets, bytes counted against flow control), streams_map*.go and streams_map_incoming.go (a frame that names a stream, such as STREAM, RESET_STREAM, MAX_STREAM_DATA, STOP_SENDING, STREAM_DATA_BLOCKED, must not open streams beyond the advertised limit; the number of streams handed to the application never exceeds the limit);
- queues the peer fills: conn_id_generator.go and conn_id_manager.go (RETIRE_CONNECTION_ID and NEW_CONNECTION_ID floods, retirement of unissued sequence numbers, retire_prior_to, bounded queue, terminating loops), path_manager*.go (PATH_CHALLENGE and PATH_RESPONSE queues), framer.go (control frames queued for sending), the crypto stream and handshake buffers in internal/handshake (CRYPTO frames at any offset and encryption level, bounded buffer), datagram_queue.go (DATAGRAM frames of length 0 and of maximum length against the receive queue limit; send queue byte accounting equals what is queued);
- inputs that must end one connection, never crash: internal/wire transport parameters and frames (every length, zero, and maximum value), Retry and token handling (long tokens, tokens in Initial packets, packet size accounting when peer-supplied values go into outgoing packets), packet-producing calls that loop over queued state (they must return in bounded time).

For each property write the checkable form: the bound stated as a function of the advertised limits, the operations a peer can repeat, and the check after an arbitrary sequence of them. Prefer sequence specs on the component directly (the sorter, the generator, the queue). Do not write specs that need a whole connection.


---
# Memory a receiver retains for unread, out-of-order stream data stays proportional to the flow-control credit it advertised, however the peer fragments that data

## Applies to
Any implementation of a reliable, flow-controlled byte-stream transport that accepts stream data out of order and reassembles it before handing it to the application. Examples include QUIC stacks, SCTP and user-space TCP stacks, and multiplexed transports with per-stream offsets. It covers the receive path from decoded data frames, through the reassembly buffer, to the in-order application read.

## Property
A receiver advertises flow-control credit, per stream and per connection. That credit promises the peer that the receiver will buffer at most that many bytes beyond what the application has consumed. The memory the receiver actually keeps alive for buffered, unconsumed stream data must stay within a constant factor of that credit, plus a fixed overhead per stream and per connection. This must hold whatever the peer chooses for:

- the offsets, sizes and order of the fragments it sends,
- the overlaps between fragments,
- the duplicates it sends,
- the gaps it leaves unfilled.

It must hold even when the application reads strictly in order and the start of the stream never arrives. The peer may legitimately fill the whole window. It must not be able to make the receiver hold more memory than the window implies, for example by sending many small, non-adjacent pieces. If the peer sends data outside its credit, the receiver may close the stream or the connection with an error. Closing is acceptable; growing memory is not.

## Threat model
**The attacker** is the remote peer, or anyone able to complete a handshake. It fully controls stream frames: stream IDs, offsets, lengths, payload, FIN, retransmissions and duplicates. It also controls how frames are packed into packets and datagrams, and when they are sent.

**The attacker cannot** change the receiver's configured limits. These are the initial and maximum stream and connection windows, the maximum number of concurrent streams, and the maximum datagram size. It cannot make the application read faster or out of order.

**The victim** is a server or client that accepts streams and reads them in order (as a byte stream), or does not read them yet. The harm is process memory growing far beyond the configured limits. Repeated across streams or connections, this leads to out-of-memory termination or denial of service.

## Oracle
The oracle is the transport's flow-control contract. For QUIC this is RFC 9000 §4: the receiver must not be required to buffer more than the advertised maximum data for a stream or connection.

Run the receiver with configured windows W_stream and W_conn and a stream limit N. Feed it peer-crafted frames and have the application read in order, or not at all. Measure the bytes actually retained for receive-side stream buffering. This should be retained allocation capacity, including any larger packet or datagram buffers that a small fragment keeps alive, not just the logical payload bytes. A tracking allocator or the implementation's reported retained capacity can provide this.

The property holds if retained memory ≤ k·min(W_conn, N·W_stream) + c·N + C for small constants k, c and C, compared against the credit currently advertised.

Do not compare:
- exact byte counts,
- whether the receiver coalesces or copies data,
- error codes or error text when the receiver rejects a flow-control violation,
- which of stream reset, connection close or silent drop it chooses.

A close or reset that frees the memory counts as passing.

## Inputs
- **Fragment offsets:** zero, small, near the window edge, exactly at the window edge, just beyond the window edge.
- **Fragment lengths:** 0, 1, a few bytes, a large proportion of the window, a full datagram's worth.
- **Gap patterns between fragments:** none (adjacent), one byte, regular stride, random, a missing prefix at offset 0.
- **Overlap patterns:** identical duplicate, partial overlap at the left or right, a fragment containing or contained in an earlier one, re-sending data already consumed.
- **Packing:** one tiny fragment per maximum-size datagram (the payload keeps a large buffer alive), many fragments per datagram, padding frames.
- **Stream dimension:** one stream, many concurrent streams up to the limit, opening new streams as old ones are reset.
- **FIN:** absent, at the end, early (shorter than the data already sent), conflicting final sizes.
- **Credit changes:** the window before and after the application reads (credit updates), frames racing a credit update.
- **Application read behaviour:** never reads, reads in order in small chunks, reads in order in large chunks, reads unordered (if the API supports it).

## Pitfalls
- **Counting logical bytes instead of retained capacity.** Measuring the payload bytes stored misses overhead such as per-fragment allocations, tree or map nodes, and reference-counted views that pin whole datagram buffers. Those are exactly the overhead that causes the harm.
- **Setting the bound too tight.** A requirement of exactly W bytes, or of no per-fragment overhead at all, fails correct implementations. Use a generous constant factor plus per-stream overhead.
- **Ignoring legitimate totals.** N streams each filling W_stream is permitted, up to the connection window. The bound must use the configured limits, not a single stream's window.
- **Using a stale window.** The bound must use the credit currently advertised, which grows as the application reads.
- **Treating rejection as failure.** Data beyond the credit may be rejected by closing the connection. That is correct behaviour, not a failure.
- **Sampling the wrong configuration or moment.** Measuring with unlimited or very large configured windows makes the bound vacuous. Measuring before the receiver processes the frames, or after the connection is torn down, hides the retention.
- **Leaving the application read path out.** The property concerns data waiting for an in-order reader. A harness that reads unordered, or consumes data immediately, never exercises the case where fragments sit behind a missing prefix.

---
# A peer cannot make a receiver hold far more memory for out-of-order stream data than the stream data that flow control lets it send

## Applies to
Any transport-protocol implementation that rebuilds an ordered byte stream from segments that can arrive out of order, overlap or repeat, and that limits the peer with per-stream or per-connection flow control. Examples are QUIC stream and CRYPTO reassembly, userspace TCP, SCTP, and multiplexed streams in HTTP/2 or HTTP/3. This covers ordered-read mode and unordered or chunked-read mode.

## Property
A receiver buffers stream data that has arrived but has not yet been consumed. The memory it keeps for this must stay within a small constant factor of the bytes the peer is allowed to have outstanding under flow control, plus a fixed per-stream and per-connection overhead. This must hold for every way the peer can split that data into frames and packets: the order, the sizes, the gaps, overlaps and duplicates, and how frames are spread across packets and streams. If the receiver cannot keep within the bound, it must refuse the data or reset or close the stream or connection. It must not keep growing its memory. Either way, the memory must not grow with the number of frames or fragments the peer sends. It may grow only with the number of bytes that flow control permits.

## Threat model
- **Attacker:** a remote peer, which may be unauthenticated beyond the handshake. The peer fully controls:
  - which streams it opens, up to the advertised stream limits;
  - the offset, length and FIN flag of every stream frame;
  - how frames are packed into packets;
  - retransmissions, duplicates and overlaps;
  - timing.
- **Limits the attacker cannot change:** the receiver's flow-control windows, its stream-count limits and its configuration.
- **Victim:** a server or client that runs with default or ordinary configuration. The victim's application may read slowly, may read in either ordered or unordered mode, or may not read at all. The victim must not run out of memory, and must not keep memory pinned until the streams or connection close.

## Oracle
- **Reference bound:** use a simple reassembler that stores pending data in one contiguous buffer the size of the flow-control window. Its memory is at most window × streams. This matches the RFC 9000 intent that flow control bounds receive-side buffering.
- **Measurement:** drive the implementation through its public receive API, feeding it frames or packets and making read calls. Measure the heap bytes it retains, using a counting allocator or allocation hooks scoped to the connection, after each step.
- **Pass:** at every step the retained memory is at most K × (the maximum the reference holds at the same flow-control limits) + C, where K is a small constant (for example 4) and C is a fixed overhead.
- **Also a pass:** the receiver rejects the input instead, by closing or resetting the stream or connection with any error code, or by dropping the frames.
- **Not compared:**
  - exact error codes or error text;
  - whether the implementation rejects or compacts;
  - the order of chunks returned in unordered mode;
  - exact allocation counts.
- **Correctness check:** the bytes delivered to the application must still equal the bytes sent at each offset. A fix that throws away valid data without signalling an error has not met the property.

## Inputs
- **Frame offsets:**
  - contiguous with earlier data;
  - ahead of the read position, leaving a gap;
  - behind the read position (already consumed);
  - at the window edge.
- **Frame lengths:** 0, 1, small (a few bytes), medium, and the largest length that fits in a packet.
- **Gap sizes between frames:** none, 1 byte, small, large.
- **Overlaps:**
  - exact duplicate;
  - partial overlap at the start or end;
  - a frame that contains, or is contained by, buffered data.
- **FIN flag:** placement early, late, repeated, or conflicting.
- **Packing:** one frame per packet, many frames per packet, and retransmission of earlier packets.
- **Stream fan-out:** one stream, up to the maximum number of concurrent streams, both unidirectional and bidirectional, and CRYPTO or handshake streams.
- **Application read behaviour:**
  - never reads;
  - reads in ordered mode;
  - reads in unordered mode;
  - partial reads interleaved with arriving frames.
- **Configuration:** default transport settings, and larger stream or connection windows.
- **Control frames:** RESET_STREAM and STOP_SENDING interleaved with data. Window updates or MAX_STREAM_DATA increases that let the peer keep going.

## Pitfalls
- **Counting the wrong bytes.** Accounted bytes are not live allocation. Measure real heap usage, including per-chunk headers and allocation capacity, not only payload length.
- **Mistaking legitimate growth for a bug.**
  - Memory that grows when the configured window is enlarged is expected. Scale the bound by the configured limits.
  - Data already delivered to the application and held by the application is not reassembly memory. Do not count it.
- **Bounds that are too tight.**
  - Setting K to 1 or C to 0 flags harmless bookkeeping and allocator rounding.
  - Requiring compaction to happen at once fails good implementations. Compaction may be lazy as long as memory stays within the bound at every observation point.
- **Treating rejection as failure.** A connection or stream error is an allowed outcome. Do not require the receiver to accept all of the data.
- **Testing only one read mode.** Exercise both ordered and unordered reads, and the case where the application never reads. Different code paths can skip the limit check.
- **Missing preconditions.**
  - Flow control must actually permit the bytes. Inputs beyond the window are expected to error and show nothing.
  - Retained memory must be checked while the streams are still open. Memory that is freed only at close is still a failure.
- **Wrong measurement.** Wall-clock time or RSS sampled from a shared process gives noisy false results. Use per-instance allocation accounting instead.

---
# A remote peer's stream data can only end in delivered bytes or a protocol error, never a crash of the receiving endpoint

## Applies to
Any implementation of a reliable, multiplexed transport that reassembles stream data from frames or segments sent by a remote peer. Examples are QUIC stacks, HTTP/2 and HTTP/3 stream layers, SCTP and TCP-like reassembly buffers. This includes every read mode the implementation offers the application: ordered reads, unordered or chunked reads, and peeking. It also covers the interval and range-set bookkeeping such code uses to track which offsets it has already received or delivered.

## Property
The peer can send any sequence of stream frames, in any order and with any duplication or overlap. The frames may have any offsets, any payload lengths (including zero) and any end-of-stream flags. The application can read the stream in any supported mode, interleaving its reads with the arrival of frames. For all of these, the receiving endpoint keeps running. Each frame has one of two outcomes:

- It is accepted. Its data is then delivered to the application consistently with what the peer sent.
- It is rejected with a stream-level or connection-level protocol error, as the protocol specification defines.

The receiver must never panic, abort, assert-fail, loop forever or corrupt its own state because of peer-chosen frame contents. In unordered mode, every byte offset the peer covered is delivered at most once, with the byte value the peer sent at that offset. After end of stream, the union of the delivered ranges equals the union of the received ranges.

## Threat model
- **Attacker controls:** the attacker is a remote peer that has completed the handshake, or can otherwise inject authenticated frames on a connection. It fully controls the frames it sends: stream IDs, offsets, payload lengths and contents, end-of-stream flags, and the order, duplication, overlap and timing of frames.
- **Attacker does not control:** the application's choice of read mode. Some behaviours need the application to have opted into a non-default read mode, such as unordered reads. The attacker also cannot control the receiver's flow-control limits or other local configuration.
- **What the victim does:** the victim is a server or client that accepts streams and reads them with the API normally used for that mode. The harm is denial of service: one peer can crash the process or thread that serves all connections.

## Oracle
- **Crash freedom (needs no reference):** run the receiver in a harness that feeds frames and performs reads. Any panic, abort, assertion failure, sanitizer report or hang is a violation. Test with both debug and release builds, so that debug-only assertions are also caught.
- **Data correctness (reference model):** keep a simple independent model of the stream. A byte-array map from offset to value is enough, together with the set of covered offsets and the final size, if known.
  - For each accepted frame, the receiver's delivered data must agree with the model at every offset.
  - In unordered mode, the delivered chunks must be pairwise disjoint in offset. Their union must be a subset of the covered offsets, and must equal them once the stream is finished and fully read.
  - In ordered mode, the delivered data must be the contiguous prefix of the model.
- **Errors:** compare against the protocol specification, for example RFC 9000 §4.5 and §19.8 for QUIC. A frame that the specification says must be rejected must lead to a stream or connection error rather than to data delivery. Examples are data beyond the final size, a final size that changes, and data beyond the flow-control limit. A frame that the specification allows must not cause a crash. Zero-length STREAM frames are allowed.
- **Not compared:**
  - error message text or the exact error code variant, when the specification permits several;
  - how delivered data is split into chunks, and their order in unordered mode;
  - timing;
  - whether redundant data is silently dropped or re-acknowledged.

## Inputs
Build frames from the following classes of pieces:

- **Payload lengths:** 0, 1, small values, and values that reach exactly up to or past the flow-control window or the final size.
- **Offsets:** 0; equal to the end of a previous frame; strictly inside a previous frame; equal to the start of a previous frame; past the end of a previous frame (leaving a gap); in the region the application has already read; near the maximum representable offset (2^62−1).
- **Range relations between frames:** disjoint, adjacent, touching at a single point, nested, partially overlapping on the left or right, identical, and empty ranges placed at any of the positions above.
- **End-of-stream flag:** absent; present on an empty frame; present on a data frame; repeated with the same final size; repeated with a different final size.
- **Duplication and reordering:** retransmitting earlier frames, shuffling arrival order, and repeating the same frame.
- **Application actions:** ordered read, unordered read, reads with small and large maximum lengths, the point at which the read mode is chosen, reads interleaved between frames, stopping or resetting the stream, and dropping the stream.
- **Other stream-level frames:** reset-stream frames, flow-control update frames, and stop-sending frames, interleaved with the data frames.

## Pitfalls
- **Missing the read-mode precondition:** a spec that only exercises the default ordered read path will pass the vulnerable version. The harness must switch the stream into each non-default read mode, at different times relative to frame arrival.
- **Calling an empty frame an error:** treating a zero-length frame, or an empty end-of-stream frame, as a protocol violation is stricter than the specification. A receiver that rejects it would wrongly fail the spec, and a model that rejects it hides the bug.
- **Counting legitimate errors as failures:** stream and connection errors required by the specification must not be counted as violations. Examples are final-size errors and flow-control errors. Only crashes, hangs and wrong data count.
- **Fixed chunking or order in unordered mode:** requiring a particular chunking, or ordering, of delivered data in unordered mode gives false failures.
- **Testing the wrong layer:**
  - Testing only the interval-set helper in isolation can produce reports about inputs the stream layer never passes.
  - Testing only the frame parser never reaches the reassembly logic.
  - Drive the actual receive-and-read path. If you do test the helper directly, assert its documented invariants, such as no empty stored ranges and stored ranges being disjoint and non-adjacent.
- **Missing debug assertions:** a debug-only assertion can mask the bug in release builds, and the reverse can also happen. Build in both modes.
- **Too few frames:** very short frame sequences cannot reach states that need several overlapping frames after an empty one. Allow sequences of at least a handful of frames.

---
# A peer cannot make an endpoint hold more stream state than the endpoint advertised it would accept

## Applies to
Implementations of multiplexed transport protocols in which an endpoint advertises how many streams or channels its peer may open. Examples are QUIC (initial_max_streams_* and MAX_STREAMS), HTTP/2 (SETTINGS_MAX_CONCURRENT_STREAMS), and SSH or other channel-multiplexing protocols with a peer-facing open limit. This covers both client and server roles, and any implementation in the family.

## Property
For each stream category, the number of peer-initiated streams the endpoint instantiates must never exceed the limit it has advertised for that category. Categories are, for example, bidirectional vs unidirectional and client- vs server-initiated. "Instantiates" includes per-stream buffers, state objects, and streams handed to the application. The advertised limit is cumulative for QUIC and concurrent for HTTP/2, and is updated only by the endpoint's own limit-raising messages.

When a peer sends any frame that references or implicitly opens a stream beyond the current limit, the endpoint must not allocate state for that stream or for the gap below it beyond the limit. It must handle the frame as the protocol specifies, which for QUIC means a connection error of type STREAM_LIMIT_ERROR. As a result, the endpoint's resource use attributable to peer streams stays bounded by its advertised limits, whatever the peer sends.

## Threat model
The attacker is the remote peer on an authenticated, established connection. It completes the handshake normally and fully controls the following:
- the frames it sends: frame types, stream IDs, offsets, lengths, FIN and reset flags;
- the order, repetition and timing of those frames;
- the transport parameters it advertises for its own side.

The attacker cannot change the limits the victim advertises. It cannot forge the victim's limit-raising frames, and it cannot break packet protection.

The victim is an endpoint that accepts connections or connects with default or application-configured stream limits. It keeps processing frames and expects memory, CPU and accepted-stream counts to remain bounded by those limits.

## Oracle
The specification is the oracle. For QUIC this is RFC 9000:
- §2.1: stream ID layout. The two low bits give the initiator and the direction. The stream index is ID >> 2.
- §3.2: opening stream N implicitly opens all lower-numbered streams of the same type.
- §4.6: a stream index at or beyond the advertised cumulative limit MUST cause a STREAM_LIMIT_ERROR connection error.
- §19.11: MAX_STREAMS semantics.
- §19.4, §19.5, §19.8, §19.10, §19.13: which frames reference streams.

For HTTP/2 the equivalent is RFC 9113 §5.1.2.

Track the victim's advertised limit per category from its transport parameters and the MAX_STREAMS frames it actually sends. Then check two observations:
1. **Bounded state.** The count of peer-initiated streams the victim creates per category never exceeds that limit. Measure this through the application's accept interface, an internal stream-count or allocation probe, or memory that stays within a bound proportional to the limit.
2. **Correct reaction.** When the peer references an index at or beyond the limit, the connection is closed with the transport error code STREAM_LIMIT_ERROR (0x04), and no stream beyond the limit is ever accepted.

Compare only the error code and the closure itself. Do not compare reason-phrase text, which frame was being processed when the close happened, the exact packet in which CONNECTION_CLOSE was sent, or the order of streams delivered to the application.

## Inputs
- **Stream ID pieces:**
  - low-bit type selectors: client or server initiated, bidirectional or unidirectional;
  - indices at 0, at limit−1, at limit, at limit+1;
  - indices far beyond the limit, up to 2^60−1;
  - IDs of the victim's own (locally initiated) type, both not yet opened and already opened.
- **Frames that reference a stream:** STREAM (with and without OFF, LEN and FIN), RESET_STREAM, STOP_SENDING, MAX_STREAM_DATA, STREAM_DATA_BLOCKED.
- **Limit-shaping frames and parameters:**
  - the victim's initial_max_streams_bidi and initial_max_streams_uni, including 0;
  - the peer's STREAMS_BLOCKED;
  - waiting for or not waiting for the victim's MAX_STREAMS updates.
- **Lifecycle pieces:** closing streams with FIN or reset to trigger limit increases; reopening patterns; many small frames vs a single frame with a high ID.
- **Payload pieces:** zero-length data, small data, data at large offsets.
- **Equivalent channel-open messages for other protocols:** HTTP/2 HEADERS or PRIORITY on new stream IDs, SSH CHANNEL_OPEN.

## Pitfalls
- QUIC limits are cumulative stream counts, not concurrent ones. A spec that counts only currently-open streams wrongly flags or misses behaviour.
- The limit rises legitimately after the victim sends MAX_STREAMS. Use the limit the victim actually advertised at the time the frame was processed, not the initial parameter.
- Implicit opening of all lower IDs up to the limit is required behaviour, not a bug. Index limit−1 is allowed; index limit is not.
- Frames for not-yet-opened locally initiated streams require STREAM_STATE_ERROR, not STREAM_LIMIT_ERROR. Do not conflate the two.
- Do not flag an implementation that pre-allocates lazily or caps allocations below the limit as long as it is otherwise compliant. Do not demand allocation-free handling of in-limit streams.
- Packets must be validly protected and arrive after the handshake. Inputs rejected for crypto or framing reasons do not test this property.
- Measure state after the frame has been processed and before connection teardown frees everything. Otherwise a vulnerable endpoint that later closes the connection will look correct.

---
# A QUIC endpoint never hands the application more peer-initiated streams than the stream limit it advertised, whichever frame names the stream

## Applies to
Any QUIC transport implementation (RFC 9000), client or server, that accepts peer-initiated streams and enforces stream-count limits through the `initial_max_streams_bidi` / `initial_max_streams_uni` transport parameters and MAX_STREAMS frames. The same property applies to any multiplexed transport in which the receiver grants the peer a cumulative stream-count credit.

## Property
For each stream type, consider the peer-initiated streams of that type: bidirectional or unidirectional. At every point in a connection, the number of those streams that the endpoint treats as open is bounded by the highest cumulative stream limit the endpoint has sent to the peer, either as a transport parameter or in a MAX_STREAMS frame. "Treats as open" covers streams that exist in its state, streams yielded by its accept API, and streams counted toward implicit opening of lower-numbered streams. The bound holds whichever frame type first references a stream: data, reset, flow-control, stop or blocked frames.

A frame that references a peer-initiated stream whose index is at or above that limit must not open the stream or any lower-numbered stream past the limit. The connection must instead be closed with transport error STREAM_LIMIT_ERROR (0x04). As a result, the work and memory the application spends accepting streams is bounded by the limit it chose. It does not grow with a stream ID that the peer picks.

## Threat model
- **Attacker:** a remote QUIC peer that completes a normal handshake. It fully controls the frames it sends in 1-RTT (and 0-RTT, if enabled) packets, including frame types and stream IDs up to 2^62−1. It needs no credentials beyond what the endpoint already accepts for any peer.
- **Outside the attacker's control:** the limits the victim advertises, the victim's configuration, and how the victim's application consumes streams. The attacker cannot forge the victim's MAX_STREAMS frames.
- **Victim:** a client or server that runs a loop calling its accept-stream API, which is the normal pattern. The victim expects the number of streams it receives to be bounded by its configured concurrency or stream limit.
- **Harm:** unbounded stream accepts, which lead to memory exhaustion, CPU spinning that continues after the peer disconnects, or starvation of other connections.

## Oracle
The oracle is RFC 9000 §4.6 together with §19.8–§19.13 (STREAM, RESET_STREAM, STOP_SENDING, MAX_STREAM_DATA and STREAM_DATA_BLOCKED, which "open" streams by reference) and §19.11 (MAX_STREAMS). Use an independent test harness: a scripted QUIC peer, or a frame injector driving the implementation's connection state machine. The harness records the maximum stream-count limit the endpoint has sent for each stream type.

After feeding the frames, compare two things:
1. **Stream count.** Count the streams the endpoint yields through its accept API, or reports as open, for each type. The count must be ≤ the recorded limit. Bound the accept loop, for example by stopping after limit+1 accepts, rather than draining it.
2. **Connection outcome.** If any frame referenced a peer-initiated stream index ≥ the limit, the connection must end in a closed state whose transport error *code* is STREAM_LIMIT_ERROR.

Do not compare:
- the close reason phrase,
- the frame type field in CONNECTION_CLOSE,
- the timing of the close within the packet,
- the order in which streams are yielded.

Frames that refer to stream IDs which are invalid for other reasons are judged by their own RFC error codes (STREAM_STATE_ERROR, FRAME_ENCODING_ERROR). Examples of such frames are those naming a locally-initiated stream not yet opened, or flow-control frames on a peer-initiated receive-only unidirectional stream. For those frames, require only that the stream-count bound holds.

## Inputs
- **Frame types that reference a stream ID:**
  - STREAM (with and without the FIN, OFF and LEN bits)
  - RESET_STREAM
  - STOP_SENDING
  - MAX_STREAM_DATA
  - STREAM_DATA_BLOCKED
- **Stream ID initiator and direction bits:**
  - client-initiated bidirectional
  - server-initiated bidirectional
  - client-initiated unidirectional
  - server-initiated unidirectional
- **Stream index relative to the advertised limit:**
  - 0
  - below the limit
  - limit−1
  - exactly the limit
  - limit+1
  - large values (e.g. 2^20, 2^40)
  - the maximum encodable (2^60−1 index / 2^62−1 ID)
- **Variable-length integer encodings** of the stream ID: 1, 2, 4 and 8 bytes, including non-minimal encodings.
- **Limit configuration:**
  - initial bidi and uni limits, including 0
  - later MAX_STREAMS increases sent by the endpoint
  - streams being finished or reset so that credit is reissued
- **Packet context:**
  - 1-RTT
  - 0-RTT where supported
  - multiple frames in one packet
  - a frame naming a stream after earlier frames opened lower streams
- **Endpoint role:** client or server.

## Pitfalls
- **Treating the limit as concurrent.** The limit is cumulative over stream indices, not a count of concurrently open streams. A spec that checks "currently open ≤ N" misses the bug, or flags legitimate behaviour after streams close and MAX_STREAMS raises the limit.
- **Using only the initial transport parameter.** Use the highest limit actually sent. A spec that ignores later MAX_STREAMS frames produces false positives.
- **Flagging legitimate implicit opening.** Referencing stream index k < limit legitimately opens all lower-numbered streams of that type. This must not be flagged.
- **Matching the wrong part of the close.** Match the error code only, not reason text or the reported frame type. Do not demand STREAM_LIMIT_ERROR where the RFC mandates a different error, such as STREAM_STATE_ERROR for frames on locally-initiated or receive-only streams, or FRAME_ENCODING_ERROR for malformed encodings.
- **Draining an unbounded accept loop.** This hangs or exhausts memory in the test itself. Cap the number of accepts.
- **Testing before the handshake completes.** The frames must be delivered after handshake/keys allow them. Frames dropped earlier for unrelated reasons make the check pass vacuously.
- **Checking only data frames.** Exercise every frame type that can reference a stream. Checking only STREAM frames reproduces the original blind spot.

---
# A peer cannot make an endpoint's per-connection memory grow without bound by sending more frames

## Applies to
Any QUIC implementation, client or server, that receives frames from a remote peer and buffers, queues or reassembles data on that peer's behalf. This covers TLS handshake (CRYPTO) data, stream data, and control frames that cause responses to be queued. The same property fits any transport-protocol endpoint that keeps per-connection reassembly or response buffers.

## Property
For a single connection, the memory the endpoint holds on behalf of the remote peer must stay below a bound that does not depend on how many frames or packets the peer sends, or on how long the peer keeps the connection alive. This memory includes:
- reassembly buffers,
- data handed to the TLS stack and not yet consumed,
- queued responses and frames waiting to be sent.

The bound must hold:
- in every connection phase: before, during and after the handshake;
- at every encryption level;
- for every frame type.

When the peer pushes more data than the endpoint is willing to hold, the endpoint has exactly two acceptable options:
- discard or consume the excess without retaining it;
- close the connection with an error.

It must never keep accumulating.

## Threat model
**What the attacker controls.** The attacker is the remote QUIC peer. It can complete a legitimate handshake and holds valid keys, so its packets authenticate and are accepted. It controls:
- the sequence, type, offsets, lengths and contents of all frames it sends;
- the timing of those frames;
- whether it acknowledges, reads, or responds to anything the endpoint sends.

It can keep the connection alive indefinitely, for example by sending ack-eliciting traffic before the idle timeout.

**What the attacker cannot control.** It cannot forge packets for other connections, cannot change the endpoint's configuration, and cannot change the endpoint's local flow-control or buffer limits.

**What the victim does.** The victim runs an ordinary server or client. It accepts the connection and processes received packets in the normal way. The application may read its streams slowly or not at all.

**The harm.** Process memory grows in proportion to attacker traffic until the host degrades or the process is killed.

## Oracle
The oracle comes from RFC 9000 and RFC 9001, combined with a resource measurement that does not depend on the implementation's own code.

**What the RFCs require.**
- RFC 9000 §7.5 requires buffering at least 4096 bytes of out-of-order CRYPTO data per encryption level. It allows closing with CRYPTO_BUFFER_EXCEEDED beyond the endpoint's limit. CRYPTO data is otherwise not flow-controlled, so the receiver must impose its own cap.
- STREAM data must be bounded by the flow-control limits the endpoint advertised (§4).
- Received data at or below already-delivered offsets must not be retained again.

**How to measure.**
1. Drive one connection with N frames of a given class, then with kN frames (for example k = 4, 16, 64).
2. Measure the bytes held for that connection. Use the implementation's buffer accounting if it exposes it. Otherwise use allocator statistics or heap-in-use for a process hosting only that connection, sampled after the input has been processed.

**Verdict.**
- The property **holds** if, once N exceeds a small constant, held memory stops growing with N. Growth up to roughly the advertised flow-control windows plus a fixed per-level crypto cap is acceptable.
- The property **also holds** if the connection is closed and its memory released.
- The property **fails** if held memory grows roughly linearly in N while the connection stays open.

**What is not compared.**
- The specific error code or reason text.
- Whether the endpoint closes the connection or silently drops the excess.
- The exact numeric bound.
- The order of frames in any responses.

## Inputs
Classes of pieces to combine:

**Connection phase**
- Before the handshake completes.
- After the handshake completes.
- After HANDSHAKE_DONE.
- After a key update.
- During connection migration.

**Encryption level / packet type**
- Initial.
- Handshake.
- 0-RTT.
- 1-RTT.

**CRYPTO frames**
- Offsets: contiguous with what has been received; leaving a gap; overlapping earlier data; duplicating already-consumed ranges; very large offsets near 2^62.
- Lengths: zero; small; near the packet size.
- Contents:
  - well-formed TLS handshake or post-handshake messages, such as NewSessionTicket or KeyUpdate-like records;
  - partial message headers that announce large lengths;
  - unparseable bytes.

**STREAM frames**
- Directions: bidirectional and unidirectional.
- Offsets: within flow-control limits, at the limit, or leaving gaps.
- With or without FIN.
- Received while the application never reads the stream.

**Control frames that cause the endpoint to queue state or responses**
- PATH_CHALLENGE, which queues PATH_RESPONSE.
- NEW_CONNECTION_ID / RETIRE_CONNECTION_ID.
- NEW_TOKEN.
- DATAGRAM.
- MAX_STREAMS / STREAMS_BLOCKED.
- PING and other ack-eliciting traffic that causes ACKs to be generated.

**Peer behaviour**
- Never acknowledging the endpoint's packets.
- Never consuming data the endpoint sends.
- Sending at a sustained rate for long durations, renewing the idle timer each time.

**Repetition**
- The same frame repeated N times.
- Distinct offsets on each repetition.

## Pitfalls
**Measuring the wrong thing**
- Whole-process RSS is noisy, and allocators rarely return freed memory to the OS. Measure heap-in-use or the implementation's buffer counters. Compare growth across several values of N rather than reading one absolute number.
- Memory released when the connection closes or is dropped is a pass. Sample while the connection is still open, and treat a close as compliant.

**Demanding behaviour the RFCs do not require**
- Small fixed buffering is required by the RFC. Do not fail an endpoint for retaining 4096 bytes or so per encryption level, or for retaining data up to its advertised flow-control window.
- Do not require a particular error code such as CRYPTO_BUFFER_EXCEEDED or PROTOCOL_VIOLATION. Do not require that the endpoint close the connection at all; silently dropping the excess is equally valid.

**Missing preconditions**
- Frames must actually be accepted by the endpoint. That means:
  - valid keys for the encryption level;
  - correct packet numbers;
  - anti-amplification limits respected;
  - the frame type permitted in that packet type.

  Inputs rejected at packet decryption test nothing.
- Some frames are legal only after the handshake. Without completing the handshake you never reach the post-handshake paths.

**Missing the slow and indirect cases**
- Do not rely on the idle timeout or a short test duration to cap growth. The attacker can extend the connection, so the test must show the bound is independent of N, not merely that the test ended.
- Memory held inside the TLS library on the endpoint's behalf counts. A spec that only inspects the QUIC layer's reassembly buffer can miss growth that has been pushed one layer down.

---
# A peer cannot make a transport connection hold unbounded memory by sending frames that require a reply while slowing the replies down

## Applies to
Implementations of connection-oriented transport protocols, especially QUIC endpoints (client or server), where some frames received from the peer require the endpoint to send something back. The endpoint has to keep state until that reply goes out, and the peer can influence how fast the endpoint is allowed to send.

## Property
For any sequence of packets a peer sends, the memory a connection keeps for replies it still owes must stay bounded. Such replies include path-validation responses, acknowledgements, connection-ID retirements and stream resets. The bound must be either a constant or a limit the endpoint itself chose and advertised. It must not grow with the number of reply-eliciting frames received.

This must hold even when the reply rate is throttled below the arrival rate. Congestion control, the anti-amplification limit, flow control, a small path MTU, or withheld or lossy acknowledgements can all cause that throttling.

When pressure is excessive, the endpoint may coalesce replies, drop superseded ones (for example, answer only the most recent challenge), or close the connection with an error. It must not queue every pending reply indefinitely.

## Threat model
The attacker is an unauthenticated remote peer. It can complete a handshake, or reach any state in which the relevant frames are accepted. It fully controls:
- the content and rate of the packets it sends;
- which of the victim's packets it acknowledges;
- the transport parameters it advertises;
- the source addresses it sends from.

The attacker cannot read or alter the victim's memory, and cannot forge packets for other connections. The victim runs a normal event loop: it processes incoming datagrams and repeatedly asks the connection for packets to send. The harm is that memory use grows without limit and the process or host is exhausted.

## Oracle
The RFC 9000 contract is the reference:
- Path validation (§8.2): one response per challenge at most, and responding only to the latest challenge is allowed.
- Frames (§13.3): PATH_RESPONSE is not retransmitted.
- Security considerations (§21): endpoints may limit peer-induced state or close the connection.

The check works as follows:
1. Drive one connection with N reply-eliciting frames under sustained send throttling.
2. Measure the per-connection retained state. Use the length of pending-reply structures, or allocation attributable to the connection.
3. Repeat with roughly 2N, 4N and 8N frames.

The property holds if the measurement plateaus at a bound independent of N. It also holds if the connection is closed.

What is not compared:
- exact memory figures or queue sizes;
- which replies were dropped or coalesced;
- the order of replies;
- error codes or close-reason text;
- whether every challenge received a response.

## Inputs
- **Reply-eliciting frames:** path challenges with distinct or repeated 8-byte data, pings, stop-sending, new connection IDs with retire-prior-to values, ACK-eliciting packets of any kind, and new-token or handshake-done equivalents.
- **Multiplicity:** one frame per packet, many frames packed into one packet, coalesced packets, and packets arriving in bursts.
- **Path changes:** packets from new source addresses or ports, which trigger path validation and the anti-amplification limit on an unvalidated path, and migration to new connection IDs.
- **Throttling levers:**
  - withholding acknowledgements entirely;
  - acknowledging with gaps so packets are declared lost;
  - a small advertised max UDP payload size;
  - small connection or stream flow-control limits;
  - large ack-delay values.
- **Victim send-loop cadence:** how often send is called relative to receive. This includes calling it often, which shows whether throttling or the loop itself limits output.
- **Keep-alive traffic:** traffic that prevents an idle timeout from ending the test early.

## Pitfalls
- **No real throttling.** If replies drain as fast as challenges arrive, every implementation passes. The test must confirm the send budget is actually exhausted, and must keep calling the send path.
- **Missing preconditions.** Frames sent before the handshake completes, or in packet types where they are illegal, are rejected and test nothing.
- **Over-strict expectations.** Requiring a response to every challenge, or a specific error, contradicts the RFC. Dropping, coalescing and closing the connection are all valid outcomes.
- **Counting allowed buffers.** Buffers bounded by limits the endpoint advertised (receive windows, active connection ID limit) are legitimate. Do not count them as unbounded growth.
- **Noisy measurements.** Absolute process memory is noisy. Compare growth across increasing N, not a single threshold.
- **Premature termination.** An idle or handshake timeout that closes the connection would cause a false pass if the frame count never actually reached the larger N.

---
# A remote peer cannot make an endpoint keep an ever-growing amount of per-connection state by sending frames faster than the endpoint can send the replies they require

## Applies to
Any implementation of a connection-oriented network protocol where frames from the peer cause the endpoint to remember something. That includes cases where the endpoint must later send a response, acknowledgement or confirmation frame, and the endpoint's own sending is limited by congestion control, flow control, pacing or the peer's acknowledgements. Examples are QUIC stacks, TCP/TLS-like transports in user space, HTTP/2 and HTTP/3 servers and clients, and multiplexed RPC transports.

## Property
For a single connection, the memory the endpoint keeps because of peer input stays below a bound. That memory includes pending outbound control frames, retired or pending identifiers, buffered out-of-order data, outstanding challenges and similar records. The bound depends only on limits the endpoint chose or advertised: transport parameters, flow-control windows and configured maxima. It does not depend on how many frames the peer has sent, how long the connection has lasted, or how slowly the endpoint can drain its replies. When the peer pushes past the bound, the endpoint must do one of two things: close the connection with a protocol error, or drop or merge state in a way the protocol allows. It must not keep queueing.

## Threat model
- **The attacker** is an unauthenticated remote peer that can complete a handshake. It fully controls the content, count and timing of the frames it sends inside an established connection. It also controls the endpoint's ability to send, by:
  - withholding or delaying acknowledgements;
  - inducing apparent loss;
  - keeping the endpoint's congestion or flow-control window small.
- **What the attacker cannot control:**
  - It cannot break cryptographic protection.
  - It cannot forge packets for other connections.
  - It cannot exceed the limits the endpoint advertised without that being a protocol violation the endpoint can detect.
- **The victim** is an endpoint (server or client) that accepts the connection and keeps processing the peer's packets. The harm is excessive or unbounded memory or CPU use for that connection, and through many such connections, denial of service of the process.

## Oracle
The oracle is the protocol specification together with an observable resource measurement. It does not use the package's own logic.

The reference is the spec's resource-limit guidance:
- For QUIC, RFC 9000 §5.1.2 and §19.16. An endpoint should limit the identifiers it has retired but not yet had acknowledged as retired, should allow at least about twice the active-identifier limit, and should otherwise close with CONNECTION_ID_LIMIT_ERROR.
- RFC 9000 §21 on memory exhaustion.
- Analogous clauses in other protocols.

The test procedure:
1. Drive one connection with a growing number N of state-creating frames, while holding the endpoint's send capacity low.
2. Measure the endpoint's per-connection retained state as a function of N. Use the length of internal queues if they are exposed. Otherwise use heap usage attributable to the connection.

The property holds if either of the following is true:
- Retained state levels off below a value that does not grow with N (a constant related to the advertised limits).
- The connection is closed, or the frames are rejected.

The property fails if retained state keeps growing roughly linearly with N while the connection remains open.

What is not compared:
- the exact error code or reason text on close;
- when the close happens, as long as it happens before state exceeds a generous multiple of the spec's suggested minimum;
- the order or framing of the endpoint's outbound frames;
- absolute byte counts.

## Inputs
**Frames that make the endpoint create state or owe a response:**
- New-identifier announcements, with varying sequence numbers and varying "retire everything before X" values.
- Path challenges.
- Stream data and crypto data, sent out of order, with gaps, or repeatedly.
- Stream open, stream reset and stop-sending frames.
- Requests that elicit acknowledgement or confirmation frames.
- Tokens and ping-like probes.
- Data-blocked and limit-update frames.

**Sequence and value pieces:**
- Monotonically increasing sequence numbers.
- Large jumps in sequence numbers.
- Retire thresholds equal to, just below, and far above the current sequence.
- Values at the advertised limit, one above it, and far above it.

**Conditions that slow the endpoint's sending:**
- Never acknowledging the endpoint's packets.
- Acknowledging selectively or late.
- Acknowledgement gaps that the endpoint reads as loss.
- Small flow-control credit.
- Long periods with no acknowledgement, kept just under the idle timeout.

**Timing:**
- Bursts of many frames in one packet.
- Many small packets.
- Sustained rates over a long connection lifetime.

**Endpoint configuration:**
- Advertised active-identifier limit.
- Flow-control windows.
- Initial congestion window.
- Idle timeout.

## Pitfalls
- **No back-pressure in the harness.** If the harness acknowledges promptly or leaves the endpoint free to send, the reply queue drains and the vulnerable version looks fine. The test must actually starve the endpoint's sending.
- **Peer violations masking the effect.** If the peer exceeds advertised limits in a way that already has a mandatory close, the test fails or passes for an unrelated reason. Stay within the advertised limits and vary only volume and rate.
- **Idle timeouts.** The connection may close on an idle timeout or a handshake timeout before growth shows up. Keep it alive, and make sure the frames are sent after the handshake completes.
- **Bounds that are too strict.** State proportional to the endpoint's own advertised limits is legitimate. Do not demand a tiny fixed byte count, and do not flag bounded growth up to about twice the advertised identifier limit.
- **Rejecting legitimate fixes.** A close with any protocol error, or protocol-allowed merging or dropping of redundant state, must count as passing. Do not require a specific error code.
- **Noisy measurements.** Allocator caching, shared buffers and measuring after the connection has closed give noisy readings. Look for a trend across N, not a single sample.
- **Coverage gaps.** Testing only one frame type misses the same harm reached through other state-creating frames, so enumerate all of them.

---
# A QUIC endpoint's memory for state queued in response to peer frames stays bounded, no matter how many frames an authenticated peer sends or how long it withholds acknowledgements

## Applies to
Implementations of connection-oriented transport protocols with peer-driven control frames, such as QUIC (RFC 9000) stacks and similar encrypted transports. These implementations queue outgoing control frames or bookkeeping entries in reaction to frames received from the peer. The queued work drains only when the endpoint is allowed to send, which depends on congestion control, flow control and the peer's acknowledgements.

## Property
For a single connection, the amount of state the endpoint keeps in response to peer frames has an upper bound. This covers pending outgoing control frames, unacknowledged retirement records, path-validation responses and similar items. The bound is a constant fixed by the endpoint's own configuration and the negotiated transport parameters, for example a small multiple of `active_connection_id_limit`. It does not grow with the number of frames the peer has sent. A peer that has completed the handshake may send any sequence of well-formed or malformed frames, including repeated, stale or out-of-window ones, and may refuse to acknowledge anything. The endpoint must then do one of two things:
- keep its pending state within the bound, by deduplicating, ignoring or coalescing frames, or
- close the connection with a transport error.

An endpoint fails the property if its retained per-connection state keeps growing for as long as the peer keeps sending.

## Threat model
- **Attacker:** a remote peer that has completed the handshake. It controls every frame it sends and every field in them, including sequence numbers, `retire_prior_to` values, duplicates, ordering and timing. It also controls which packets it acknowledges, and it can acknowledge none.
- **Not controlled by the attacker:** the endpoint's configuration, its locally chosen transport parameters, its congestion-control state (beyond starving it of ACKs), and its memory limits.
- **Victim:** a client or server using the library. It accepts or makes one connection and leaves it open while the peer stays active. The harm is unbounded memory growth in the victim process, which ends in denial of service for that process and all its other connections.

## Oracle
Use RFC 9000 as the specification. Section 5.1.2 says an endpoint SHOULD limit the number of locally retired connection IDs whose RETIRE_CONNECTION_ID frames are not yet acknowledged. It says the endpoint should allow at least twice `active_connection_id_limit`, and that it may treat exceeding this limit as CONNECTION_ID_LIMIT_ERROR. More generally, peer-induced queued state must not be unbounded.

Run the test as follows:
1. Drive one connection with a scripted peer that never acknowledges anything.
2. Send N frames, for increasing N (for example 10², 10³, 10⁴, 10⁵).
3. After each batch, record two things:
   - the size of the endpoint's pending-frame and retirement queues, or the process's per-connection heap usage if the queues cannot be observed;
   - whether the connection has been closed.

The property holds if either of these is true:
- the measured size stays below a fixed bound that does not depend on N, or
- the connection is closed with a transport error before the bound is exceeded.

Fix the bound from the transport parameters before the run. A reasonable default is 2–4 × `active_connection_id_limit` entries plus a constant.

What to compare and what to ignore:
- Compare growth shape and the closed/open outcome.
- Do not compare the exact error code. CONNECTION_ID_LIMIT_ERROR, PROTOCOL_VIOLATION and FRAME_ENCODING_ERROR are all acceptable.
- Do not compare the exact frames emitted, their order, or the reason-phrase text.

## Inputs
- **Frame types that cause the receiver to queue work:**
  - NEW_CONNECTION_ID;
  - RETIRE_CONNECTION_ID;
  - PATH_CHALLENGE (queues PATH_RESPONSE);
  - NEW_TOKEN;
  - MAX_STREAMS, MAX_DATA and MAX_STREAM_DATA;
  - STREAMS_BLOCKED and DATA_BLOCKED;
  - RESET_STREAM and STOP_SENDING (queue stream-control replies);
  - post-handshake CRYPTO;
  - PING and other ack-eliciting frames.
- **NEW_CONNECTION_ID sequence numbers:**
  - 0, the current lowest active value, the highest seen, and highest+1;
  - values far ahead of the window;
  - values below the current retirement window (already retired);
  - the same sequence number repeated.
- **`retire_prior_to` values:**
  - 0 and unchanged;
  - incremented by 1;
  - equal to the sequence number;
  - far ahead of any issued sequence;
  - decreasing relative to an earlier frame.
- **Connection ID and stateless reset token contents:**
  - fresh;
  - identical to a previously issued one;
  - same ID with a different sequence number;
  - same sequence number with a different ID.
- **Lengths:** connection ID length at minimum, maximum and out-of-range values.
- **Peer acknowledgement behaviour:**
  - acknowledge everything;
  - acknowledge nothing;
  - acknowledge selectively;
  - acknowledge with a large ack delay.
- **Network conditions:** no loss, total loss of the endpoint's packets, small congestion window, and time advanced or not between batches.
- **Repetition count and batching:** one frame per packet versus many frames per packet.

## Pitfalls
- **Missing preconditions.** The handshake must be complete, and the peer must withhold ACKs or otherwise keep the congestion window closed. If the endpoint can drain its queue, a vulnerable implementation looks bounded.
- **Idle timeout masking the bug.** Idle timeouts or keep-alive closes can end the connection before growth shows up. Keep the peer active and count only closes caused by the frames under test as passes.
- **Bound set too tight.** Legitimate state up to roughly twice `active_connection_id_limit` must not be flagged. Measure growth as a function of N, not an absolute byte count, and ignore transient per-packet allocations and allocator slack.
- **Treating strictness as a failure.** An implementation that closes the connection earlier than strictly required, or rejects duplicates outright, still satisfies the property. A spec that demands the connection stay open is wrong.
- **Matching exact error codes or emitted frames.** This produces false failures between correct implementations.
- **Testing only one code path.** Exercise stale, duplicate, in-window and far-ahead sequence numbers. The unbounded path is often the "already handled" branch, not the main one.

---
# A QUIC endpoint's packet-producing call always returns in bounded time, whatever frames an authenticated peer has sent

## Applies to
Implementations of QUIC, or of similar multiplexed and multipath transport protocols, that keep state driven by the peer. That state includes connection IDs, paths, retirement queues, ACK state and stream state. The property concerns the operations that build outgoing packets from that state, such as "send", "poll for next datagram" or "on timeout", and the operations that ingest received datagrams.

## Property
After any sequence of well-formed or malformed datagrams from a remote peer, every call into the endpoint must finish in bounded time and bounded memory. This includes ingesting a datagram, producing the next outgoing datagram, and handling a timer. A call finishes by doing one of three things:
- returning a datagram,
- reporting that there is nothing to send,
- closing the connection with a protocol error.

If the application has supplied no new data and the peer sends nothing further, repeatedly calling the send operation must reach "nothing to send" after a bounded number of datagrams. That bound depends on queued data, congestion and amplification limits, and pending control frames. It must not depend on the peer's choices about connection ID sequence numbers or paths.

Every datagram the endpoint emits must also obey the protocol's own rules for the state it was built from. For example, a packet must never carry a frame that retires the connection ID that the same packet is addressed with.

The harm being ruled out is an endpoint, or the whole process or event loop that serves many connections, that stops responding because of state a remote peer arranged.

## Threat model
- **Attacker:** a remote, unauthenticated network peer. It can complete a handshake with the victim, because a QUIC server accepts handshakes from anyone, or because the victim is a client that connects to the attacker.
- **What the attacker controls:**
  - every frame it sends after the handshake: their types, their order, and their field values (sequence numbers, retire-prior-to values, limits, path probes);
  - the transport parameters it advertises;
  - the source addresses and ports it sends from, so it can create or migrate paths;
  - the timing and loss of its own packets.
- **What the attacker cannot control:**
  - the victim's code and configuration;
  - the victim's cryptographic keys;
  - the connection IDs the victim issues;
  - packets the victim exchanges with other peers.
- **What the victim does:** it runs its normal event loop. It feeds received datagrams in, calls the send operation until told there is nothing more to send, and handles timers. Success means the loop keeps making progress for this connection and for all other connections.

## Oracle
1. **Termination.** Run each operation under a wall-clock or instruction budget. The budget must be orders of magnitude larger than a normal call needs, for example one second per call.
2. **Bounded drain.** After the scripted input ends and with no new application data, call send repeatedly. It must return "nothing to send" or a closed or draining state within a generous bound, for example a few thousand datagrams. Run this both before and after firing any pending timers.
3. **Specification conformance (RFC 9000).** Decrypt the emitted packets with the test's own keys or through the implementation's test hooks. Check that no packet contains a RETIRE_CONNECTION_ID frame whose sequence number equals the sequence number of the connection ID in that packet's Destination Connection ID (RFC 9000 §19.16). Also check that retired sequence numbers were actually issued by the peer.

Do not compare the following:
- exact packet contents, sizes or coalescing;
- the order in which control frames are scheduled;
- which path a frame is sent on;
- whether the endpoint chooses to close the connection with an error rather than continue (closing is acceptable);
- error codes or reason text.

## Inputs
Build inputs only after a completed handshake against the endpoint. Combine pieces from these classes:
- **NEW_CONNECTION_ID frames:**
  - sequence numbers: 0, 1, small, gaps, duplicates, decreasing, near the maximum;
  - retire_prior_to values: 0, equal to the sequence number, above all issued numbers, increasing across frames;
  - connection ID lengths: 0, 1, 8, 20;
  - repeated or conflicting reset tokens;
  - more frames than the advertised active_connection_id_limit.
- **RETIRE_CONNECTION_ID frames:**
  - for IDs the endpoint issued;
  - for unissued IDs;
  - for the ID currently in use.
- **Transport parameter values:**
  - active_connection_id_limit: 2, large values;
  - disable_active_migration;
  - a zero-length connection ID.
- **Path events:**
  - datagrams from a new source address or port;
  - switching back to an old address;
  - NAT rebinding;
  - several paths active at once;
  - PATH_CHALLENGE and PATH_RESPONSE frames, both matching and non-matching;
  - application-initiated probing or migration on the victim side, where the API exposes it.
- **Other peer-driven state that feeds packet scheduling:**
  - ACK frames: huge ranges, many ranges, acknowledging unsent packet numbers;
  - stream and flow-control frames: MAX_STREAMS, MAX_DATA, STREAM_DATA_BLOCKED, RESET_STREAM, STOP_SENDING;
  - CRYPTO frames after the handshake;
  - PING and padding.
- **Timing:**
  - dropped or reordered peer packets;
  - firing idle, loss-detection and path-validation timers between inputs.

## Pitfalls
- **Missing the precondition.** Most connection-ID and migration state can only be reached after the handshake completes and, for multiple paths, after path validation. A harness that never gets past the handshake will pass trivially.
- **Treating legitimate output as a hang.** Large application data, pending retransmissions, and anti-amplification or congestion limits can produce many datagrams or cause temporary blocking. Set the bounds generously. Do not count datagrams the application caused by queuing data.
- **Treating rejection as failure.** Closing the connection with PROTOCOL_VIOLATION or CONNECTION_ID_LIMIT_ERROR is valid behaviour. A spec that requires the endpoint to accept every frame sequence is stricter than the RFC.
- **Demanding prompt retirement.** Do not require that RETIRE_CONNECTION_ID is sent promptly, on a particular path, or at all before close. The RFC allows the frame to be deferred until a suitable connection ID or path exists.
- **Checking only one call.** A loop may appear only across repeated send calls, or only after a timer fires. Drain fully, and drain again after timers.
- **Wrong oracle for the self-retirement check.** Compare sequence numbers against the connection ID actually used in that packet's header. Do not compare against the "current" connection ID of some other path.

---
# Peer-sent unreliable messages cannot grow the receiver's buffered memory past a bound set by its configured buffer limit, whatever the message sizes

## Applies to
Any network protocol implementation (QUIC stacks, message transports, tunnels, RPC layers) that:
- queues messages from a remote peer, or messages from the local application, in a buffer limited by a configured size, and
- handles traffic that is not covered by flow control: unreliable datagrams, control frames, keep-alives, notifications.

It applies in both directions: messages received from the peer and waiting for the application, and messages the application queued for sending.

## Property
For any sequence of messages a peer sends after the connection is established, the memory held for not-yet-consumed messages stays below a fixed bound. That bound depends only on:
- the configured buffer size, and
- a constant per-message overhead.

It must not grow with the number of messages received. The bound must hold whatever the size of each message, including the smallest sizes the protocol allows. When the limit would be exceeded, the implementation must do one of these:
- drop older or newer messages,
- refuse to accept the message, or
- close the connection.

It must not keep queuing.

The send direction has the same rule. If the application keeps enqueuing outgoing messages faster than they drain, it must at some point see backpressure: a "blocked" error, rejection or dropping. It must not be allowed to queue an unbounded number of messages, whatever their sizes.

## Threat model
- **Attacker:** a remote peer that has completed the handshake. It fully controls:
  - the frames it sends,
  - each frame's payload length and encoding,
  - how many frames go in each packet,
  - the packet rate.
- **Attacker cannot control:**
  - the victim's configuration (buffer size, whether the feature is enabled),
  - how often the victim's application reads queued messages,
  - the victim's handling of any other connection.
- **Victim:** runs the endpoint with default or chosen settings and either reads incoming messages more slowly than they arrive, or does not read them at all.
- **Harm:** heap memory grows without limit until the process is killed. This is a denial of service.
- **Send direction:** the "attacker" is buggy or hostile input fed into the local application's enqueue calls. The harm is the same: memory grows without limit.

## Oracle
The oracle is the documented contract: a configured receive or send buffer size bounds buffered data. RFC 9221 also allows receivers to drop DATAGRAM frames. Do not use the implementation's own byte counter, because that counter is what fails.

Measure the bound from outside the implementation:
1. Drive the connection with N messages while the application never reads, for increasing N (for example 10³, 10⁴, 10⁵, 10⁶).
2. After each run, observe one of the following:
   - the number of messages the application can then dequeue,
   - the allocator's live heap bytes attributable to the connection.
3. The property holds if the observed value stops increasing once N passes a threshold. That threshold must be roughly (limit / smallest per-message cost) + a constant.
4. Run the send-direction check the same way: enqueue N messages without letting the connection transmit, and count the successful enqueues before the first backpressure signal. That count must be bounded independently of N.

Do not compare any of the following:
- which messages were evicted or the eviction order,
- the exact bound value or the per-message overhead constant,
- whether excess messages are dropped silently, rejected, or the connection is closed with an error,
- error text or error codes.

## Inputs
- **Payload lengths:** zero, one byte, small, exactly the configured limit, just above the limit, and the maximum frame size.
- **Frame encodings:** a DATAGRAM frame with an explicit length field and one with an implicit length (extends to end of packet). Also other frame types that flow control does not cover and that cause the receiver to queue or record state: PING, PADDING, PATH_CHALLENGE, NEW_CONNECTION_ID, NEW_TOKEN, ACK with many ranges, and stream frames with zero length.
- **Packing:** one frame per packet, many frames per packet, frames mixed with other frame types, and sustained packet rate.
- **Receiver configuration:** default buffer size, very small buffer size, large buffer size, and feature disabled (where messages must be rejected instead of queued).
- **Application behaviour:** never reads, reads slowly, reads in bursts.
- **Send direction:** enqueue calls with the same payload-length classes, with the connection blocked from transmitting (no congestion window, or the peer not acknowledging).

## Pitfalls
- **Trusting the implementation's bookkeeping.** Checking that the internal "bytes buffered" counter stays below the limit passes on the vulnerable version, because zero-byte payloads add nothing to it. Count queued items or real heap memory instead.
- **Asking for an exact byte bound.** A correct fix adds per-message overhead, so allow a constant factor and an additive slack. Only growth that keeps rising with N should count as failure.
- **No sustained load.** Short runs or an application that reads quickly drain the queue, so nothing builds up. The test needs a slow or absent reader and a large N.
- **Missing preconditions.**
  - Frames sent before the handshake completes, or with the feature disabled, are rejected for other reasons and do not test this property.
  - Peer-side limits such as maximum datagram frame size or packet size must be respected. Otherwise frames are dropped as protocol violations.
- **Mistaking noise for failure.** Allocator noise and per-connection fixed allocations can look like growth. Compare growth across several N values instead of using one absolute threshold.
- **Being stricter than the specification.** Closing the connection, or dropping new messages instead of old ones, is a valid response and must not be flagged as a failure.

---
# Sending unreliable messages into a bounded outgoing buffer never crashes the sender, and the buffer's byte accounting always equals what is actually queued

## Applies to
Network transport and protocol libraries that buffer outgoing unreliable messages (QUIC DATAGRAM frames, WebRTC/SCTP unreliable messages, UDP-style message queues in async runtimes) behind a memory or byte limit. When the limit is reached, these libraries evict queued messages, reject new ones, or make the caller wait. The property covers every send path the library exposes: drop-oldest, drop-newest, return-an-error, and blocking or async wait.

## Property
For any sequence of application sends, transmissions, acknowledgements, losses, congestion or flow-control stalls and idle periods, the sender keeps four guarantees:
- It never panics, aborts or hits an internal assertion while sending or tearing down the connection.
- The buffered-byte figure it uses for its limit always equals the sum of the sizes of the messages actually still queued. It never goes below zero, never wraps, and never exceeds the configured limit beyond what the documented policy allows.
- Eviction removes exactly the messages the documented policy says it removes, and no others. A full buffer never causes the whole queue to be silently drained.
- After any send that is accepted or evicts something, the connection stays usable. Later sends, transmissions and dropping the connection behave as documented, with no poisoned locks and no double panics.

## Threat model
The remote peer controls:
- whether and when it acknowledges packets, so it can withhold ACKs to keep the sender's congestion window closed;
- keep-alive traffic such as PING frames that stops the connection from timing out;
- its advertised maximum datagram or frame size;
- the timing of loss and reordering;
- the rate at which it reads.

The local application can trigger the same states without any malicious peer. For example, it can send a burst larger than the buffer limit and then go idle.

The attacker cannot:
- change the sender's code or configuration;
- change the configured buffer limit;
- inject anything outside valid protocol frames.

The victim is an application that sends unreliable messages over a long-lived connection using the library's normal, documented send APIs. Its harm is a crashed or aborted process, a connection left unusable, or silent loss of messages that were not supposed to be evicted.

## Oracle
Run a simple reference model alongside the library. The model is a plain FIFO list of message sizes with the same configured byte limit and the same documented full-buffer policy (drop oldest until the new message fits, reject, or wait). Feed both the same operations.

After every step, compare:
- **Crash behaviour:** the library must not panic or abort, in debug or release builds, including when the connection is dropped. Run debug builds so arithmetic overflow traps.
- **Queued bytes:** where the library exposes the queued byte count, or it can be observed through a test hook, it must equal the model's sum.
- **Delivered messages:** the multiset and order of messages that reach the wire or the peer must be a subsequence the model permits. Messages the model still holds must not vanish, except through documented eviction.
- **Acceptance:** whether each send is accepted, evicts something or is rejected must match the model's decision.

Do not compare:
- error message text or the exact error variant name;
- packet boundaries or how messages are coalesced into packets;
- transmission timing;
- which tie-break the library uses among equally sized evictions, if its docs leave that open.

## Inputs
- **Message sizes:** zero bytes, one byte, typical sizes, exactly the peer's maximum datagram size, one more than that maximum, exactly the remaining buffer space, and exactly the whole limit.
- **Buffer limit values:** the minimum, the default, a limit smaller than one message, and a limit equal to a small multiple of the message size.
- **Full-buffer policy and send path:** drop-oldest or evict mode, reject or error mode, and blocking or async wait-for-space mode.
- **Local operations:** send one message, send a burst, poll or transmit, idle with no polling, close the connection, and drop or free the connection object.
- **Peer behaviours:**
  - acknowledge everything;
  - withhold all ACKs while sending PINGs or other keep-alive frames;
  - partial or delayed ACKs;
  - declare packets lost;
  - shrink or advertise its maximum datagram size;
  - stop reading.
- **Network and congestion state:** congestion window open, congestion window closed, pacing limited, path MTU changes.
- **Build modes:** debug, with overflow checks on, and release, where arithmetic wraps.

## Pitfalls
- **Single-send checks:** checking only one send into a full buffer misses the bug. The accounting drift needs sustained eviction over many operations, so specs must compare the model after long sequences, not just once.
- **Release-only runs:** testing only release builds lets wrapping arithmetic hide the drift until a much later, unrelated-looking check. Run both build modes and check the byte count at every step, not just at the end.
- **Policy mismatch:** a reference model with a different eviction policy (drop newest versus drop oldest), or one that is stricter than the documented policy, reports false failures. Copy the policy from the library's documentation for the specific send path being tested.
- **Counting accepted bytes:** counting bytes the application *offered* instead of bytes still queued gives a wrong expected value once messages are transmitted or evicted. The model must remove messages on both transmission and eviction.
- **Oversized messages:** messages larger than the peer's maximum, or larger than the limit, are legitimately rejected. Expecting them to be queued, or to evict others, is a spec error.
- **Mistaking delivery loss for bugs:** unreliable messages may be lost on the network. Measure loss at the sender's queue, not at the receiver, or allow network loss explicitly in the model.
- **Narrow peer scenario:** the stall states that trigger eviction need the peer to keep the connection alive while withholding ACKs. A harness whose peer always ACKs promptly never fills the buffer.
- **Missing teardown:** many crash paths only show up when the connection is dropped after an earlier failure. Always include teardown in the sequence.

---
# A QUIC endpoint survives any transport parameters a peer sends during the handshake: malformed ones end that one connection with an error, and never crash the process or other connections

## Applies to
QUIC implementations, both server and client roles, and any library that decodes the QUIC transport parameters extension or similar length-prefixed, variable-length-integer-encoded handshake data received from an unauthenticated network peer. This includes libraries embedded in long-running services, where a panic, abort or crashed worker takes down unrelated connections.

## Property
An unauthenticated peer can send arbitrary transport parameter bytes in its handshake. The endpoint must handle every such input in one of two ways:
- It accepts the input as valid.
- It rejects the input with a connection error (for example a CONNECTION_CLOSE, a failed handshake or a dropped connection attempt) that is confined to that one connection.

Whatever the bytes are, processing them must never panic, abort, crash or wedge the endpoint process or its worker threads or tasks. After such an input, the endpoint must still complete a handshake with a well-formed peer and keep serving connections that already existed.

## Threat model
**Attacker controls:** the full contents of the handshake messages it sends, including:
- the transport parameters extension inside its TLS ClientHello or ServerHello/EncryptedExtensions;
- every parameter ID, length and value byte;
- how the CRYPTO data is split across packets.

The attacker is only network-reachable. It needs no credentials, certificate or prior connection, and a single Initial packet (or a few) is enough.

**Attacker cannot control:**
- the endpoint's configuration;
- the endpoint's keys;
- other clients' traffic.

The attacker can, however, compute Initial packet protection keys, because they are derived from the public Destination Connection ID.

**Victim:** the operator of a QUIC server, or a client connecting to an attacker-chosen server. The victim runs the library normally and expects one bad peer to affect at most its own connection.

## Oracle
- **Specification.** RFC 9000 §7.4 and §18 say how malformed transport parameters are treated:
  - Malformed, out-of-range, duplicated or role-forbidden parameters are a connection error (TRANSPORT_PARAMETER_ERROR or another connection-level close).
  - Unknown or reserved parameter IDs must be ignored.
- **Behavioural check, independent of the package's code:**
  1. Feed the input.
  2. Observe that the process is still alive.
  3. Observe that no panic or abort was logged or raised. This includes panics caught by an async runtime or a thread pool.
  4. Observe that an existing established connection still works.
  5. Observe that a fresh, well-formed client can complete a handshake afterwards.
- **What is compared:**
  - survival of the process, worker or task;
  - continued service;
  - that the bad connection did not end up fully established when the RFC requires an error.
- **What is not compared:**
  - the exact error code chosen (TRANSPORT_PARAMETER_ERROR vs PROTOCOL_VIOLATION vs FRAME_ENCODING_ERROR);
  - error or reason text;
  - whether the close is sent at all or the connection is silently dropped;
  - timing;
  - the order of log messages.

A reference QUIC stack (for example a different implementation) may be used to sanity-check whether an input is legitimately acceptable. The property itself is only "no crash, connection-scoped failure".

## Inputs
- **Parameter IDs:**
  - each defined ID (connection-ID-related, flow-control limits, stream limits, ack_delay_exponent, max_ack_delay, idle timeout, max_udp_payload_size, active_connection_id_limit, disable_active_migration, preferred_address, stateless_reset_token, original/initial/retry source connection IDs);
  - unknown IDs;
  - reserved/GREASE IDs (31·N+27);
  - IDs encoded at the maximum varint width;
  - duplicated IDs.
- **Length fields:**
  - 0;
  - shorter than the value's own encoding;
  - longer than the remaining extension bytes;
  - exactly at the remaining bytes;
  - very large values (up to 2^62−1).
- **Varint encodings:**
  - 1-, 2-, 4- and 8-byte prefixes;
  - non-minimal encodings;
  - truncated encodings, where the prefix announces more bytes than are present (both inside a parameter value and at the end of the extension).
- **Value boundaries:**
  - 0, 1, 2^62−1;
  - max_udp_payload_size below 1200;
  - ack_delay_exponent above 20;
  - max_ack_delay at or above 2^14;
  - active_connection_id_limit below 2;
  - flag parameters with a non-empty value.
- **Structured values (preferred_address):**
  - IPv4 and IPv6 address/port bytes cut short;
  - connection ID length byte of 0, 20, or above 20;
  - connection ID shorter than its length byte;
  - missing or short 16-byte reset token;
  - trailing bytes after the structure.
- **Fixed-size values:**
  - stateless_reset_token or connection IDs with wrong lengths (0, 1, 15, 17, 21+).
- **Role mismatches:**
  - server-only parameters (original_destination_connection_id, retry_source_connection_id, stateless_reset_token, preferred_address) sent by a client;
  - required parameters omitted.
- **Container level:**
  - empty extension;
  - extension length inconsistent with the TLS message;
  - the extension sent twice;
  - the extension split across several CRYPTO frames or packets.
- **Other peer-supplied pre-authentication data that reaches the same harm:**
  - packet header fields (connection ID lengths, token lengths, packet-number lengths);
  - frame encodings inside Initial and Handshake packets (CRYPTO offsets, ACK ranges);
  - TLS handshake message fields.

## Pitfalls
- **The packet must reach the parser.** An Initial that is not correctly protected with the Initial keys, or is not padded to at least 1200 bytes, is dropped earlier. That makes the test pass vacuously. Confirm that the parse path was exercised.
- **Panics hidden by the runtime.** Async runtimes and thread pools may catch a panic and only kill one task. Detect panics through hooks or logs, and through a follow-up handshake. Do not rely on process exit alone.
- **Wrong reason for a failure.** Do not demand a specific error code or a CONNECTION_CLOSE frame. Silent drop and different-but-valid codes are compliant. Do not demand that unknown or GREASE parameters be rejected; the RFC requires that they be ignored.
- **Debug-only failures.** Fuzzing the decoder directly in a debug build can report integer-overflow panics that do not occur in release builds, or that are unreachable end to end. State the build profile, and prefer end-to-end confirmation.
- **Stricter than the specification.** Treating acceptance of a legal-but-unusual value as a failure is wrong. Examples are non-minimal varints, or limits of 0 where the RFC permits them.
- **Short timeouts.** These can mistake a slow handshake rejection for a hang. Distinguish "process alive, connection failed" from "process wedged".

---
# A QUIC endpoint never panics, aborts or makes unbounded allocations when it puts peer-supplied values into its outgoing packets; it keeps sending valid datagrams or closes the connection with an error

## Applies to
QUIC protocol implementations, client side especially, that are written as a state machine: received datagrams are fed in and outgoing datagrams are polled out. This covers any code that stores values chosen by the peer and later copies them into the headers of its own size-limited packets. Such values include address-validation tokens, connection IDs and version lists.

## Property
After receiving any sequence of datagrams from the network, every later call that processes input, handles timers or produces output returns normally. The process does not panic, fail an assertion, abort, or attempt an allocation far larger than a datagram.

Each datagram the endpoint emits must meet the transport's size rules:
- No datagram exceeds the current maximum datagram size.
- A datagram carrying an Initial packet is at least 1200 bytes.

Sometimes a peer-supplied value makes it impossible to build a conforming packet. In that case the endpoint must end the connection as a normal connection error. Building an oversized or malformed packet is not acceptable, and neither is failing in arithmetic or allocation. This must hold in builds with debug assertions and in optimized builds.

## Threat model
**Attacker capabilities.**
- The attacker is the server the victim connects to, or any party that can see the victim's first Initial datagram and send a reply before the real server does.
- Initial packet protection keys come from the client's Destination Connection ID. The Retry integrity tag uses a published fixed key. The attacker can therefore build any well-formed Initial, Retry or Version Negotiation packet that the client will accept.
- The attacker chooses the contents and lengths of tokens, connection IDs, and other fields that the protocol allows the peer to set.
- A malicious server can also issue address-validation tokens in a NEW_TOKEN frame. The client stores these and uses them on later connections.

**Attacker limits.** The attacker cannot change the client's configuration, its maximum datagram size, or the client's local state, except through packets the client accepts.

**Victim behaviour.** The victim application opens outgoing connections and drives the state machine in the usual way: it feeds received datagrams in and polls for output and timers.

## Oracle
Two sources set the expected behaviour:
- **Specification:** RFC 9000 and RFC 9001. Initial datagrams are at least 1200 bytes, and no datagram exceeds the maximum UDP payload in use.
- **The library's documented contract:** peer misbehaviour surfaces as a connection error or a lost connection, never as a crash.

For each run, check the following:
1. No panic and no abort occurs in a debug-assertion build or in a release build.
2. No single allocation exceeds a fixed bound, such as 1 MiB. Measure this with a counting or limiting allocator. Total memory stays bounded across many polls.
3. Every emitted datagram has a length within the limits above. Every emitted datagram is parseable as QUIC packets by an independent parser, such as a second QUIC implementation's header parser.
4. The connection ends in one of two states: still progressing, or closed or failed with a reported error.

The following are not compared:
- which error code is used (PROTOCOL_VIOLATION or another);
- error text;
- whether a CONNECTION_CLOSE frame is actually sent;
- the ordering or timing of events;
- whether the handshake eventually succeeds.

## Inputs
Pieces a search can combine. The attacker builds Retry and Initial packets from fields of the client's first datagram.

**Packet types the peer can send to the client:**
- Retry
- Initial
- Version Negotiation
- Handshake
- coalesced datagrams that mix these

**Fields the peer controls:**
- address-validation token bytes, with lengths from 0 up to the largest the packet allows: 0, 1, 100, 1000, 1100, 1150, 1200, 1500, 4000 and 65000 bytes;
- the Source Connection ID in Retry and Initial packets, with lengths 0, 1, 8, 20 and over 20 bytes;
- an echo of the client's original Destination Connection ID, correct or incorrect;
- a Retry integrity tag, valid or invalid.

**Frames from the server that carry stored values:**
- NEW_TOKEN, with a token of varying length that the client caches for later connections to the same server;
- NEW_CONNECTION_ID, with connection IDs of varying length.

**Client configuration knobs:**
- maximum datagram or MTU size: 1200, 1350 and 1500 bytes;
- initial Destination Connection ID length;
- whether a cached token is present;
- the version offered.

**Driver actions:**
- deliver a datagram;
- poll for output repeatedly;
- advance time to fire timers, including loss detection, idle timeout and probe timeout (PTO);
- close the connection from the application.

## Pitfalls
- **Wrong build mode.** Testing only one build mode misses part of the problem. A debug assertion fires only with assertions on. The huge allocation or abort shows only in optimized builds.
- **Inputs that are never accepted.** A Retry needs a correct integrity tag, a matching original connection ID, and must arrive before any other server packet. Otherwise it is silently dropped. Generating unauthenticated garbage tests nothing.
- **Never asking for output.** Feeding the input but never polling for output, or never firing timers, misses the failure. The failure shows up when the next packet is built, not when the input is received.
- **Requiring a particular outcome.** Requiring the connection to succeed, or requiring a particular error code, is stricter than the specification. The RFC sets no maximum token length. A client may legitimately reject any token that leaves no room for frames, and may legitimately accept a modest one.
- **Wrong allocation bound.** Setting the allocation bound too low flags normal buffer pools and ordinary batch-send buffers. Base the bound on datagram size multiplied by the batch count, not on zero.
- **Exact byte comparisons.** Comparing exact datagram bytes or padding layouts against another implementation fails for legitimate reasons. Check only lengths and parseability.
- **Checking only one input.** Checking only Retry tokens misses the other routes to the same harm: cached NEW_TOKEN values, long connection IDs, and room needed for CONNECTION_CLOSE after a large header.
- **Ignoring the client side.** Servers do not process Retry packets, so a harness that drives only a server endpoint never reaches the Retry path.

---
# A QUIC endpoint's memory use stays bounded by the bytes a peer sends and the limits it advertised, however the peer addresses those bytes

## Applies to
Any implementation of a connection-oriented transport or protocol that reassembles offset-addressed byte streams from untrusted peers. This covers QUIC stacks (CRYPTO and STREAM frame reassembly), TLS-over-QUIC handshake buffers, TCP/SCTP reassembly, and HTTP/2- or HTTP/3-style framing layers. It matters most where the input is processed before the peer is authenticated.

## Property
The memory an endpoint allocates for data received from one peer stays within a fixed bound. That bound is a small constant multiple of the payload bytes the peer actually delivered, plus a per-connection constant. Where the protocol defines limits, the bound must also respect them:
- flow-control credit the endpoint advertised;
- the out-of-order crypto buffering limit the endpoint chose (at least 4096 bytes per RFC 9000 §7.5).

The bound must hold no matter where in the stream's offset space the peer places that data. When a peer's data would exceed the bound, the endpoint must do one of two things:
- discard the data, or
- close the connection with an error (for example CRYPTO_BUFFER_EXCEEDED, FLOW_CONTROL_ERROR or PROTOCOL_VIOLATION).

It must never allocate to accommodate the data. This must hold before any handshake completes.

## Threat model
- **Attacker:** an unauthenticated remote peer that can send arbitrary datagrams to the endpoint. It controls:
  - every field of every frame it sends: frame types, offsets, lengths, payloads, encryption levels it has keys for (Initial keys are derivable from public values);
  - packet count and pacing;
  - how many connections it opens, using distinct connection IDs and source addresses.
- **What the attacker cannot control:** it cannot complete a handshake it lacks credentials for. It cannot exceed the datagram size limits. It cannot change the endpoint's configured limits.
- **Victim:** a server (or client) that accepts and processes incoming packets. It is harmed when memory grows far faster than inbound traffic, which leads to allocator pressure, out-of-memory termination, or denial of service to legitimate peers.

## Oracle
Instrument the process with a counting allocator, or measure resident or peak heap. Feed the endpoint a sequence of packets on one connection. Record peak live heap attributable to that connection: subtract a baseline taken after an idle connection was set up. Check this inequality:

peak_extra ≤ C × (total payload bytes received on the connection) + K

- C is a small slack factor (for example 4), allowing for container growth and per-chunk metadata.
- K is the configured per-connection allowance: advertised flow-control windows plus the crypto reassembly limit, rounded up generously.

The property also holds if the endpoint closes the connection or drops the offending frames and memory returns to baseline.

This check is grounded in RFC 9000:
- §4 and §7.5: an endpoint need not buffer beyond its limits and may close with CRYPTO_BUFFER_EXCEEDED;
- §19.8 and §19.6: offsets and flow control.

Do not compare:
- the exact error code or reason phrase;
- whether the endpoint closes or silently drops;
- timing of the close;
- ordering of emitted frames;
- the exact peak value beyond the bound.

## Inputs
- **Frame types:**
  - CRYPTO;
  - STREAM (with/without OFF, LEN, FIN bits);
  - RESET_STREAM;
  - NEW_TOKEN;
  - ACK with many ranges;
  - PADDING and PING as filler;
  - DATAGRAM where supported.
- **Encryption levels:** Initial, Handshake, 0-RTT, 1-RTT. These cover the levels for which the attacker has keys and those for which it does not.
- **Offset values:**
  - 0;
  - small;
  - near 4096;
  - near the advertised flow-control window;
  - just past it;
  - large values up to the varint maximum 2^62−1.
- **Length values:**
  - 0 and 1;
  - the maximum that fits in the remaining packet space;
  - lengths that make offset+length overflow or cross limits.
- **Varint encodings:** 1, 2, 4 and 8 bytes, including non-minimal encodings of small values.
- **Range layout:**
  - contiguous;
  - gaps of varying size;
  - overlapping;
  - duplicated;
  - descending order;
  - many disjoint tiny fragments.
- **Packet-level choices:**
  - multiple frames per packet;
  - packet size from minimum (1200) to maximum;
  - coalesced packets in one datagram.
- **Repetition:** the same connection repeatedly, or many connections with distinct connection IDs and addresses.

## Pitfalls
- **Rejecting legitimate buffering.** A spec that flags any allocation above the bytes received is wrong. Buffering up to advertised flow-control credit, or up to the crypto reassembly limit, is allowed. K must include those configured limits.
- **Assuming a compliant response.** Requiring a specific error code, or requiring a close rather than a silent drop, rejects compliant implementations.
- **Measuring the wrong quantity.**
  - Measure peak live allocation, not final heap, since memory freed after close masks spikes.
  - Measure live allocation, not allocator capacity alone, since amortized growth can double.
- **Mixing per-connection and global limits.** A bounded per-connection allocation multiplied by many connections is a separate property (connection-count limits). Do not fail this property for it unless the per-connection bound is exceeded.
- **Using offsets the protocol rejects first.** Offsets beyond the protocol's maximum (2^62−1) or beyond flow control may be rejected early. That is a correct outcome, not a missed test.
- **Counting the wrong allocations.**
  - Fixed preallocations made at connection setup that do not depend on input belong in the baseline.
  - Global caches belong in the baseline as well.
- **Discarding valid inputs.** Some inputs are dropped before parsing, such as packets that fail decryption or undersized Initial packets. Making them unparseable means the property is never exercised; inputs must be valid enough to reach frame processing.
