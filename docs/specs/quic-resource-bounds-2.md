Property source: advisories of other QUIC implementations (quinn-proto, quiche, s2n-quic), drafted below as general properties. None is about this package. A first round already checked the frame sorter, the receive stream, the stream limit, the connection ID generator and manager, the crypto stream buffer and flow control; do not repeat those. This round covers the components that round could not reach:

- datagram_queue.go (newDatagramQueue, Add, HandleDatagramFrame, the receive queue and the send queue): DATAGRAM frames of length 0, of the maximum length and of any length in between received in any number; the receive queue bounded by its configured limit by count and by bytes; the send queue's byte accounting equals what is queued; Add of a datagram larger than allowed returns an error and does not corrupt the queue; no panic on any sequence;
- framer.go (newFramer, QueueControlFrame, QueuedTooManyControlFrames, AddActiveStream, AppendControlFrames and AppendStreamFrames with any budget): control frames queued for sending do not grow past the limit the framer checks, whatever the sequence of QueueControlFrame and Append calls; a frame that does not fit is kept, never lost or duplicated; the budget is never exceeded;
- path_manager.go and path_manager_outgoing.go (newPathManager, GetOrCreatePath, HandlePacket, HandlePathResponseFrame): the number of paths and the queued PATH_CHALLENGE and PATH_RESPONSE state is bounded whatever addresses and frames the peer sends; a PATH_RESPONSE that matches no challenge is ignored; no panic;
- Retry and token handling (handleRetryPacket and its helpers, token generation and validation in internal/handshake/token_generator.go and server.go): a long token, a token of maximum length, an empty token, a token in an Initial from the client; sizes of outgoing packets that carry peer-supplied values (token, connection IDs) never exceed the packet buffer and never panic;
- transport parameters (internal/wire/transport_parameters.go, Marshal and Unmarshal): any bytes the peer sends end in an error or in a value that round-trips; limits (max_ack_delay, ack_delay_exponent, active_connection_id_limit, max_udp_payload_size) are checked; repeated and unknown parameters.

For each, write a sequence spec on the component directly, built from small pieces, with the bound stated as a function of the configured limits. Do not write specs that need a whole connection.


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
