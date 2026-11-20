# The RFC as a Lean reference (pilot, #289)

quic-go's `protocol.DecodePacketNumber` against RFC 9000 appendix A.3, with
the reference written in Lean from the RFC's prose, not from its pseudocode.

- `Closest.lean`: B is sound. In pure integers: a number in `[0, 2^62)`,
  congruent to the truncated one modulo `win`, inside the half window around
  the expected one (except where the range forbids it), is the closest such
  number, a tie going to the larger. Checked by Lean (`lake env lean`).
- `goals-packet-number.json`: the two model-side goals for `go2lean prove`
  (range and congruence; the half window). Together with `Closest.lean` they
  give the closest-value property. They are **not proved**: the automatic
  prover times out on this function, see `docs/eval/rfc-pilot.md`.

- `PacketNumberAgainstModel.lean`: B executed against the model. `closestRep`
  is B in Lean (the closest representative, by enumerating the candidates
  around the expected number); the program runs the model of quic-go's
  `DecodePacketNumber` on every 1-byte input with `largest` in [-1, 600] and on
  boundary and random inputs of 1 to 4 bytes, and compares. Needs a workspace
  with the model built:

```bash
cd quic-go                       # a checkout of quic-go (a02d451)
go2lean compile -workspace ws ./internal/protocol
cd ws/lean && lake build Go2LeanModels
lake lean <path to>/PacketNumberAgainstModel.lean
```

  Result: 154,112 exhaustive and 12,532 boundary and random inputs, no
  disagreement. With B's tie rule reversed (a negative control) the same
  program reports 44 disagreements, all on exact ties.
