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
