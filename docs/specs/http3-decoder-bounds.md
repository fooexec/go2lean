Property source: advisories of other HTTP/3 implementations (Netty's HTTP/3 codec), drafted as general properties below. None is about this package; check whether the same classes exist here (http3 frame reading, HEADERS payload handling, reserved and unknown frame types, SETTINGS limits, the default header-size limit of Server and Transport, QPACK decoding of long literals and the number of streams waiting on the decoder). Write specs on the frame parser, the header-reading paths and the qpack decoder calls, with a peer-controlled length as the input and a bound on memory (allocation) as the oracle.


---
# A peer cannot make an HTTP/3 (or other length-prefixed frame) decoder hold memory beyond a fixed bound by declaring large frame lengths

## Applies to
Any streaming decoder for a length-prefixed, framed wire protocol that accepts data from an untrusted peer. Examples are HTTP/3 or QUIC application-frame codecs, HTTP/2 frame decoders, and other type-length-value protocol parsers that collect input until a frame is complete. The property matters most where the protocol says some frames must be ignored (unknown, reserved, GREASE or extension types), because ignoring a frame should not require keeping its payload.

## Property
The memory the decoder keeps for one stream must stay below a bound. That bound is set by the decoder's configuration or by a fixed constant. It must not depend on the frame length the peer declares on the wire, and it must not grow with the number of payload bytes the peer has sent toward an unfinished frame.

When a frame's payload is not needed, the decoder must do one of two things:
- discard the payload as it arrives, or
- reject the frame and fail the stream or connection.

It must never accumulate that payload while waiting for the declared length to arrive.

When a frame's payload is needed, a declared length above the configured limit must be rejected before the decoder buffers up to that length. Legitimate frames whose length is within the limits must still decode normally. Frame types the protocol says to ignore must still be ignored silently and must not cause an error. Across many streams, total retained memory must grow at most as (number of streams) × (per-stream bound).

## Threat model
The attacker is a remote peer, client or server, that has completed the transport handshake. The attacker fully controls:
- the frame type and declared length on every stream it opens or writes to;
- how many payload bytes it actually sends, and how they are split into chunks;
- how many streams it opens, within the stream limits the victim advertised.

The attacker cannot exceed the transport's flow-control windows or stream-count limits, and cannot change the victim's configuration. The victim runs the decoder with default or typical settings and simply receives and processes peer data. The harm is that memory retained per stream grows until the process runs out of memory or slows down: a denial of service.

## Oracle
The reference is the protocol specification. For HTTP/3, RFC 9114 §7.2.8 and §9 say frames of unknown or reserved type MUST be ignored and their payload has no meaning, so a conforming receiver never needs to keep it. The specification also allows a receiver to treat an oversized frame as a connection error (H3_FRAME_ERROR or H3_EXCESSIVE_LOAD).

To check the property, drive the decoder directly through an embedded or in-memory channel, so that transport flow control is not the thing limiting the input. Feed it a frame header followed by N payload bytes, with N less than the declared length. Then measure what the decoder retains: the size of its cumulation buffer, or the bytes still held as unread or unreleased in its input buffers.

The property holds if, for every N, either:
- retained bytes stay below a fixed bound (for example, the frame header size plus a small constant, or the configured maximum frame size), or
- the decoder signals an error or closes the stream or connection.

It fails if retained bytes grow linearly with N.

What is not compared:
- the exact error type, error code or message;
- whether the decoder closes or keeps discarding;
- the timing of the close;
- whether any partial output was emitted.

A second check is required: frames to be ignored with a small length, and well-formed frames within the limits, must decode with the same observable result as the reference (ignored, or delivered).

## Inputs
- **Frame type values (variable-length integers):**
  - reserved or GREASE types (0x1f·N + 0x21);
  - unassigned or unknown types;
  - extension types;
  - types reserved from HTTP/2 that must be rejected;
  - known types: DATA, HEADERS, SETTINGS, PUSH_PROMISE, GOAWAY, MAX_PUSH_ID, CANCEL_PUSH;
  - non-minimal encodings of each.
- **Declared length values:**
  - 0;
  - small;
  - just under and just over configured limits;
  - 2^14, 2^30, 2^31−1, 2^32;
  - up to 2^62−1;
  - in 1-, 2-, 4- and 8-byte varint encodings.
- **Payload delivery:**
  - none;
  - a few bytes;
  - large single chunks;
  - many small chunks;
  - the header itself split across chunks.
- **Stream context:**
  - bidirectional request stream;
  - control stream;
  - push stream;
  - unidirectional stream of unknown type;
  - before and after SETTINGS have been exchanged.
- **Multiplicity:** one stream versus many concurrent streams, each with an unfinished frame.
- **Configuration:** default limits versus explicitly set maximum header-list or frame sizes.

## Pitfalls
- **Testing through a real transport.** QUIC flow control caps the bytes sent per stream, so a fixed decoder and a vulnerable one may look the same in a small test. Feed the decoder directly, or use a bound well below the flow window.
- **Measuring JVM heap or RSS.** These are noisy and depend on the allocator. Measure the decoder's own retained buffer instead.
- **Requiring one specific reaction.** Requiring a particular error code, or requiring the connection to close, wrongly fails decoders that discard incrementally. Requiring incremental discard wrongly fails decoders that reject oversized frames.
- **Being stricter than the specification.** Flagging any error on reserved or GREASE frames, or rejecting all unknown types, contradicts the specification. Small ignorable frames must be accepted.
- **Counting streamed DATA.** DATA payload that is passed downstream as it arrives is not buffering by the decoder. Count only bytes the decoder itself holds.
- **Comparing exact retained sizes or buffer capacities.** Check for a fixed bound versus linear growth, not exact numbers.
- **Omitting the per-stream precondition.** Without it, many streams that each stay legitimately under the bound can look like a violation. Normalise retained memory by the number of streams.

---
# A decoder never allocates memory far beyond the bytes it has actually received, whatever lengths the peer declares

## Applies to
Decoders of binary, length-prefixed network formats that read untrusted peer data. Examples: header-compression decoders (QPACK, HPACK), HTTP/2 and HTTP/3 frame parsers, TLV and varint-length protocol codecs, and serialization readers. Any of these may size a buffer from a length field in the input.

## Property
Memory allocated while decoding one message must be bounded by a small constant multiple of the bytes actually present in that message, plus fixed, configured limits. A declared length (of a string literal, field, frame or table entry) must not cause an allocation larger than the data available to fill it. When a declared length exceeds the remaining input, or exceeds a configured maximum, the decoder must reject the input with an error, or wait for more data that is counted against a flow-control or size limit. It must not first reserve memory of the declared size. A peer that sends a few bytes must not be able to make the server allocate megabytes or gigabytes, and must not be able to slow it down, stall it or crash it by doing so.

## Threat model
The attacker is a remote peer, either client or server, that controls every byte of the frames and streams it sends. That includes all length prefixes, integer encodings, flags and instruction types. The attacker cannot change the victim's configured limits (maximum header list size, maximum frame size, flow-control windows, dynamic-table capacity it advertised) and cannot bypass the transport layer. The victim is a server or client that runs the decoder on whatever arrives on an open connection, and it may do so for many connections or streams at once.

## Oracle
Run the decoder on a single crafted input and record the peak bytes allocated during decoding. Use a heap allocation tracker, an allocation-hook agent, or a run under a small fixed heap limit. The property holds when peak allocation ≤ k × (input bytes) + C. Choose k to allow legitimate expansion: for example, Huffman decoding yields at most about 8/5 output bytes per input byte. C covers fixed per-decoder state.

A second check comes from the format specification (e.g. RFC 9204 / RFC 7541 / RFC 9000 varints): an input whose declared length exceeds the bytes present is malformed or incomplete. The decoder must reject it, or not complete it, without an out-of-memory condition.

Compare only the allocation bound and whether decoding succeeded or failed. Do not compare the exception type, error text, error code, or whether the connection closes immediately or later.

## Inputs
- Prefix-integer encodings with different prefix widths (3, 4, 5, 6, 7 bits): short values, values at the prefix boundary, and multi-byte continuation sequences that encode very large values (up to 2^31–2^62). Include over-long and redundant continuation bytes.
- Variable-length integers (QUIC varint 1/2/4/8-byte forms) used as frame lengths and setting values.
- Flag bits that sit next to lengths: Huffman/non-Huffman (H), never-indexed (N), static/dynamic (T).
- Field-line instruction kinds: indexed, indexed post-base, literal with name reference, literal with post-base name reference, and literal with literal name (both name length and value length).
- Encoder-stream instructions that carry lengths: insert with literal name, insert with name reference, duplicate, set dynamic table capacity.
- Field-section prefix values: required insert count and delta base, including their sign bit.
- Frame headers: frame type (HEADERS, DATA, SETTINGS, unknown or reserved types) and declared frame length versus actual payload length.
- Payload bodies: empty, truncated right after a length field, short real data, and Huffman-coded bytes.
- Stream boundaries: data split across several writes, and a stream ended (FIN) mid-field.

## Pitfalls
- **Counting declared sizes that were never filled.** Decoders often allocate a buffer and fill it incrementally. Measure actual peak allocation, not declared sizes that the decoder correctly rejects before allocating.
- **Penalizing legitimate buffering.** Buffering of incomplete data the peer has really sent (within flow-control windows) is legitimate and counts as input bytes. Bound allocation by bytes received, not by bytes in the current read.
- **Treating configured large maxima as violations.** If an application configures large limits, allocation up to bytes actually received is fine. Allocation up to a configured limit without matching input is not.
- **Making the bound too tight.** Huffman expansion, header-list object overhead, and per-field objects must fit inside k and C. Set them generously, then use inputs claiming far larger lengths (≥ 100 MB) so the signal is unambiguous.
- **Mistaking an earlier rejection for a pass.** An input that is rejected for an unrelated reason (bad required insert count, an unknown dynamic index, or a frame-length cap) before the length is ever read does not exercise the property. Build valid prefixes so that decoding reaches the length-carrying field.
- **Ignoring GC and heap noise.** Run each input in isolation, or use per-thread allocation counters rather than total heap usage.
- **Asserting on error type.** Do not require a specific exception class or connection-close behaviour; only the allocation bound and the reject/accept outcome matter.

---
# An HTTP/3 header decoder never keeps more streams waiting on missing dynamic-table state than it advertised, and releases a stream's waiting state once that stream unblocks or ends

## Applies to
Any decoder for header compression where a header block can reference dynamic-table entries the decoder has not received yet, so the decoder must hold the stream until the entries arrive. This covers QPACK in HTTP/3 servers and clients, and similar schemes that use out-of-order table updates. It applies to any library or proxy that implements the decoder side and advertises a limit on blocked streams.

## Property
For a single connection, at any moment, the number of streams the decoder holds pending because their header block needs table insertions it has not yet received is at most the blocked-streams limit the decoder advertised to its peer. If the peer did not send the setting, the limit is the protocol default, which is 0, meaning no stream may block. When a peer causes the limit to be exceeded, the decoder must refuse rather than keep queuing. Per RFC 9204 this refusal is a connection error of type QPACK_DECOMPRESSION_FAILED.

The limit counts streams that are blocked right now, not streams that were ever blocked. When the missing insertions arrive and the block is decoded, or when the stream is reset or cancelled, the decoder must drop all per-stream waiting state: its slot, its callbacks, and any buffered data. Memory held for blocked streams therefore stays bounded by the limit and does not grow with the number of streams over the connection's lifetime.

## Threat model
The attacker is a remote HTTP/3 peer. It controls:
- its own SETTINGS frame;
- everything it sends on the encoder stream: capacity changes and insertions, or the absence of insertions;
- the header block prefixes it sends: Required Insert Count and Base;
- the field-line representations it uses;
- how many request streams it opens, when it opens them, and when it ends, resets or abandons them.

The attacker does not control:
- the victim's advertised settings, such as max table capacity and blocked-streams limit. These may be left at the default.
- the victim's QUIC transport limits.

The victim is a server, or a client, that accepts the connection and decodes headers with a non-zero dynamic table capacity. The harm is that pending-stream state grows without bound, ending in memory exhaustion and a crash. A single connection is enough to cause it.

## Oracle
The oracle is RFC 9204, §2.1.2 (Blocked Streams) and §4.5.1 (Required Insert Count), together with §5 (the default SETTINGS_QPACK_BLOCKED_STREAMS = 0).

Drive the decoder through a scripted sequence of encoder-stream and request-stream bytes. Then observe:

1. **Pending count against the limit.** Observe the maximum number of streams that are simultaneously pending: no headers have been delivered to the application, and no error has been raised for them. This must never exceed the advertised limit L.
   - When stream number L+1 would block, the connection must be terminated with an error.
   - Delivering that stream's headers later, after the insertions arrive, is a failure.
   - Leaving the stream pending forever is also a failure.
2. **Release after unblocking or termination.** After the insertions arrive and the pending streams are decoded, or after they are reset or cancelled, the decoder must again accept up to L newly blocked streams before it raises an error. Where introspection is possible, check that the retained per-stream objects (listeners, buffers) return to baseline. Use counts of objects, not raw heap size.
3. **Correct decoding when the limit is respected.** Headers decoded after unblocking must match what the RFC decoding of the block yields.

What is compared:
- whether an error occurs;
- the error type, accepting a connection error. A stricter implementation that closes the connection for the first over-limit stream is correct.
- bounded pending counts;
- decoded header lists, compared as ordered name/value pairs.

What is not compared:
- error message text;
- the exact moment within the event loop when the error surfaces;
- allocation sizes;
- the order in which several unblocked streams are delivered.

## Inputs
- **Advertised decoder settings:**
  - SETTINGS_QPACK_MAX_TABLE_CAPACITY: 0, small, large.
  - SETTINGS_QPACK_BLOCKED_STREAMS: absent, 0, 1, a small N, a large value, the maximum varint.
- **Encoder-stream instructions:**
  - Set Dynamic Table Capacity;
  - Insert With Name Reference (static or dynamic);
  - Insert With Literal Name;
  - Duplicate;
  - the omission or delay of any of these.
- **Header block prefixes:**
  - Encoded Required Insert Count values: 0, equal to inserts received, one greater, much greater, and values near the wrap-around modulus.
  - Delta Base, with sign bit 0 or 1.
- **Field-line representations:**
  - indexed static;
  - indexed dynamic;
  - indexed post-base;
  - literal with dynamic or post-base name reference;
  - literal with literal name.
- **Stream lifecycle events:**
  - open a bidirectional request stream;
  - send HEADERS with or without FIN;
  - RESET_STREAM or STOP_SENDING;
  - abandon the stream;
  - decoder-stream Stream Cancellation and Insert Count Increment, as seen from the encoder side.
- **Ordering and volume:**
  - the interleaving of encoder-stream data with request-stream data;
  - the number of request streams opened, from 1 up to many times the limit;
  - repeated cycles of block, then unblock, then block again.

## Pitfalls
- **Wrong default.** Assuming the default blocked-streams limit is non-zero when the setting is absent. The protocol default is 0, so an absent setting must forbid any blocking.
- **Counting the wrong streams.** Counting streams whose blocks are immediately decodable, because their Required Insert Count is at most the inserts received, as blocked. These must decode without error.
- **Confusing error sources.** Treating every error as proof of the limit. An invalid Required Insert Count, one that cannot be decoded or exceeds what the table capacity allows, is a different error. A test must send prefixes that are valid but not yet satisfiable.
- **Only testing the first burst.** A decoder that never frees slots passes a single burst of L streams. It fails only on block, unblock, block-again cycles, or on reset-then-reopen cycles. Include those cycles.
- **Relying on transport limits.** Relying on the QUIC concurrent-stream credit as the bound. The property is about the QPACK limit, and the transport limit may be large or may be replenished as streams finish.
- **Measuring raw heap.** Raw heap size is noisy. Measure pending-stream behaviour or retained per-stream object counts instead.
- **Requiring an exact error code or timing.** Rejecting implementations that report the error at a slightly different point, or that close the connection earlier than strictly necessary, is wrong. The essential requirement is that no stream beyond the limit stays pending or is later decoded.
- **Non-deterministic ordering.** The order of encoder-stream and request-stream data must be deterministic in the test harness. Otherwise blocks resolve by accident and the limit is never exercised.

---
# A default-configured HTTP endpoint refuses a peer's header block once its decoded size passes a finite limit, instead of buffering it without bound

## Applies to
Any HTTP protocol implementation (HTTP/1.x, HTTP/2, HTTP/3, or similar framed protocols with header or metadata blocks) that decodes header sections received from a remote peer. This covers both server and client roles, and applies when the implementation is built through its default constructors or factories, with no limit set explicitly by the application.

## Property
When the application has not set a limit, the endpoint enforces a finite, peer-independent bound on the memory spent decoding and holding the header section of one message (request headers, response headers, or trailers). It also bounds the total across the streams of one connection. A header section whose decoded size goes past that bound is rejected (a stream reset, a connection error or an error response such as 431) without being fully accumulated. The memory the endpoint holds for a header section must not grow in proportion to the amount of header data the peer chooses to send. The bound may be large, but it must be finite and in force by default. The secure default the implementation uses for its other HTTP versions shows that such a bound is expected. A peer's own settings, or the protocol's "unlimited" default in the RFC, must never remove the local bound.

## Threat model
- **Attacker:** a remote peer, either a malicious client connecting to a server or a malicious server answering a client. The attacker fully controls the bytes on the connection: frame types, header names and values, header count, compression encoding (HPACK/QPACK literals, dynamic-table references), how the header section is split across frames, the number of concurrent streams, and the attacker's own SETTINGS values.
- **What the attacker cannot control:** the victim's configuration. The victim uses defaults and sets no header-size limit.
- **Victim:** an application that accepts a connection, or opens one, and processes ordinary messages.
- **Harm:** heap or memory exhaustion leading to an out-of-memory crash or severe degradation. This is a denial of service for all users of the process.

## Oracle
The oracle is a documented contract that needs no reference implementation. RFC 9110 §15.5.20, RFC 9113 §6.5.2 and RFC 9114 §4.2.2 / §7.2.4.1 let an endpoint advertise and enforce a maximum field-section size. Header size is computed as the sum of name length plus value length plus 32 octets per field. The decoded (uncompressed) size counts, not the wire size.

Test procedure:
1. Build the endpoint with default settings.
2. Make sure no explicit limit is set.
3. Have a test peer send a single header section with a decoded size well above any reasonable default, for example 16 MiB, and then far larger.

The property holds if both of the following are true:
- The endpoint signals rejection (stream or connection error, error status, or the connection closing) before or shortly after a fixed size threshold.
- The memory retained for that stream, measured as heap growth or as bytes buffered by the decoder, stays below a constant that does not scale with the size sent.

The property fails if the full header section is delivered to the application, or if memory grows linearly with the input until exhaustion.

Also check that the limit the endpoint advertises (for example its max-field-section-size setting) is finite by default. If an implementation advertises a limit, it must enforce it.

Not compared:
- the exact numeric limit (8192 or another value)
- the error code or error text
- whether rejection is at stream level or connection level
- the ordering of frames in the rejection

## Inputs
- **Header size shape:** many small fields; a few fields with very large values; very long names; repeated identical fields (for example, many cookie fields).
- **Message part:** request headers, response headers, trailers, and informational (1xx) responses.
- **Framing:** a header section split across many frames (HEADERS plus CONTINUATION, or several HTTP/3 HEADERS frames); a single huge frame; frames sent slowly across many reads.
- **Compression amplification:**
  - HPACK or QPACK literals with and without Huffman coding;
  - dynamic-table insertions followed by many indexed references, so the decoded size is much larger than the wire size;
  - QPACK encoder-stream instructions;
  - large table capacities.
- **Peer settings:** the attacker's own advertised max-field-section-size or max-header-list-size, whether absent, zero, huge or the maximum varint; settings that are absent entirely.
- **Concurrency:** one stream; many concurrent streams, each just under the per-stream limit; streams opened and then left incomplete.
- **Endpoint role:** server decoding requests; client decoding responses.
- **Protocol version:** HTTP/1.1, HTTP/2, HTTP/3, and h2c upgrade paths.

## Pitfalls
- **Requiring an exact limit value or error code.** Any finite bound with any kind of rejection satisfies the property.
- **Measuring wire bytes instead of decoded size.** A compression-amplified header section passes a wire-size check while exhausting memory.
- **Using the peer's advertised setting as the oracle.** The peer's limit governs what the endpoint sends, not what it accepts. The local default is what matters.
- **Testing with an explicitly configured limit.** That hides the insecure default. Equally, flagging an application that deliberately configured "unlimited" is a false positive, because the property concerns defaults.
- **Flagging rejection of legitimately small header sections.** The limit must still admit normal traffic of a few KiB.
- **Treating "the RFC default is unlimited" as compliance.** The RFC allows the endpoint to advertise a limit, and the harm is what the endpoint does when no limit is set.
- **Testing only one frame.** That misses accumulation across continuation frames and across concurrent streams.
- **Declaring failure on transient allocation alone.** The check concerns memory that scales with the attacker's input, not a bounded buffer that is reused.
