# The RFC as a Lean reference: the pilot on packet number decoding (2026-11-19)

Pilot of [#289](https://github.com/fooexec/go2lean/issues/289): write the
reference B in Lean from the RFC, ask whether the model of the code (A)
agrees with it. The function is quic-go's `protocol.DecodePacketNumber`
(`a02d451`), which implements RFC 9000 appendix A.3.

## What was built

- **B, from the RFC's prose.** The RFC says the decoder picks the packet
  number closest to the expected one (largest acknowledged plus one) that has
  the truncated bits. B states that: the result is in `[0, 2^62)`, congruent
  to the truncated number modulo `2^(8 * length)`, and no other such number is
  closer to the expected one (a tie going to the larger). It is not the
  pseudocode, which quic-go's code copies line by line, so agreement with it
  would say nothing.
- **A lemma that B can be checked in two simple pieces**
  (`examples/rfc-pilot/Closest.lean`, accepted by Lean): a number in range,
  congruent to the truncated one, inside the half window around the expected
  one (above it only where the number is under `win`, below it only where it
  is within `win` of `2^62`) is the closest. The first version of the lemma
  had the two exceptions the wrong way round; Lean's counterexample showed
  it, which is the kind of mistake an independent B catches.

## What did not work: A against B

The goals "congruent and in range" and "in the half window"
(`goals-packet-number.json`) are quantifier-free, yet `go2lean prove` does not
prove them:
- **Automatic proof:** `(deterministic) timeout at whnf`, 4,000,000
  heartbeats, in `wp_auto` on the model of the function.
- **With the length fixed** (`length = 2`, so that the shift `1 << (length * 8)`
  is a constant): the same timeout, after 5 minutes. So the cost is not the
  symbolic shift.
- **The proof agent** (`-llm claude`) spent its whole output budget (64,000
  tokens) on the first goal, without an answer; the "closest value" goal as
  one theorem was too large for it.

**Localised afterwards** with small functions of one operation each
(`examples/proofs-limits/`):
- `&^`, `|`, division by 2, a `uint8` multiplication, conditions, `&&` and
  several `if`s with `return` are all proved;
- a shift by a symbolic count was not: `Proof/Simp.lean` had no lemma for
  `shl` and `shr`, so the step stopped at an unreduced match. Added
  (`dp_shl`, `dp_shr`); `ShlVar` is proved now;
- a 64-bit constant of 2^32 or more is what is left: `wp_auto` stops with
  "maximum recursion depth" while stepping (1000 is fine; also with
  `maxRecDepth` at 1,000,000). The decoder compares with `1<<62 - win`, so it
  stops there, and so does the packet number goal.

So the link A to B is open for this function. Its model is loop-free and
small, which is the case `prove` is meant for, so this is a limit of the
prover on 64-bit bit-vector arithmetic (`&^`, `|`, conversions) to be
looked at, not of the approach. Next steps, in order of cost:
1. **Execute B against the model** instead of proving it: a Lean post that is
   decidable on concrete inputs, evaluated on the search engine's trials
   (random and exhaustive), for every length and the boundary values of
   `largest` and `truncated`. The goals file is already in that form; the
   engine has to take a Lean post as a property.
2. **Find what `wp_auto` unfolds** that costs so much on this function (a
   minimal reproduction for the prover), then rerun.
3. The other two pilot functions (the variable-length integer length,
   RFC 9002's RTT update) with the same setup.

## What this says so far

- Writing B in Lean from the prose, with a Lean-checked lemma that reduces
  it to simple pieces, works and caught an error of mine. B is cheap to write
  and check by itself.
- The model side does not yet follow. Whether A and B agree is, for now,
  established only by the Go-side check that is already in the results
  (`quic-go.md`: the agent's spec against the RFC algorithm held 3.7M runs of
  Go's fuzzer, and the model search found no counterexample), not by a proof.
