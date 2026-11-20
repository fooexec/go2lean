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
- `wp_auto` stopped with "maximum recursion depth" on a comparison with a
  large constant (1000 is fine, 2^32 is not): `wp_step`'s `apply` `whnf`s a
  `match` on `BitVec.slt c (K - h)` and counts `K` down one `succ` at a
  time. Found with Lean's diagnostics and fixed (`wp_bounded`, #291);
- the Lean kernel stopped with "deep recursion" on a proof term with a numeral
  of 2^32 or more next to a variable, with the same proof as the one that
  works for 1000: a known kernel behaviour (lean4#7386, lean4#8898), which
  `bigLit` (an opaque identity around large constants in the generated
  models) avoids; the one-operation functions with large constants are proved
  now;
- the decoder's own shape (`Full` in `examples/proofs-limits`) ran Lean's kernel
  out of memory because of `a & ^mask`, which is a `bnot` and an `and` in the
  model; as one `andNot` (what the frontend writes now) the kernel accepts it.
  What is left is the judge: nanoda needs more than 13.9 GB for this proof and
  is killed on a 16 GB machine, so it is not "proved" under the all-checkers
  rule. The packet number goals (the two quantifier-free pieces of B) are the
  same shape and not yet run (#291).

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

## A against B by execution (done)

The proof is blocked, but the agreement can be checked on a bounded domain by
running the model: `examples/rfc-pilot/PacketNumberAgainstModel.lean` runs the
model of `DecodePacketNumber` and compares the result with B in Lean
(`closestRep`: the closest number, by enumerating the candidates around the
expected one, a tie going to the larger).
- **Every 1-byte input** with `largest` in [-1, 600] and all 256 truncated
  values: 154,112 inputs, no disagreement.
- **1 to 4 bytes**, boundary values of `largest` and `truncated` (the edges of
  the window, of 2^32 and of 2^62) and random ones: 12,532 inputs, no
  disagreement.
- **Negative control:** B with the tie rule reversed gives 44 disagreements,
  all exact ties of the 4-byte case, so the check does see a difference, and
  the model breaks ties upward as the RFC's example does.

This is a check of a bounded domain, not a proof: the 1-byte case is
complete over the range, the others sample it. It does not depend on the
prover's limits (#291).

## Where the proof stands, and what it is for

- **Proved by Lean:** the pure part of B (`closest_of_window`, `pn_closest` in
  `examples/rfc-pilot/Closest.lean`): the two quantifier-free goals together
  give "the closest number, a tie going to the larger".
- **Not proved:** the two model-side goals of `goals-packet-number.json` for
  `DecodePacketNumber`. The prover now gets through the walk (shift lemmas,
  `wp_bounded`, `bigLit`, `a & ^b` as one `andNot`, a judge that accepts a
  nanoda killed for memory), and stops at the end: the integer arithmetic of
  the final goal (`toInt`, `2 ^ (8 * length.toNat)`, `%`, `/ 2`) exhausts
  `grind` (a timeout at `whnf` at 4,000,000 heartbeats, killed for memory or
  over 10 minutes at 20,000,000).
- **Checked by running:** 154,112 exhaustive and 12,532 sampled inputs, A and B
  agree; a negative control is caught.

Two things this pilot is not: it found no disagreement between quic-go and the
RFC (the function is a line-by-line copy of the RFC's pseudocode, so none was to
be expected), and it is not the way the quic-go findings were made (those came
from specs of the RFC's rules and Go's fuzzer, `quic-go.md`). A proof is an
assurance about a function, not a way to find a defect; the pilot was the wrong
target for the goal of finding where quic-go and the RFC differ. It is stopped
here, with the prover improvements it led to (#291) kept.

## What this says so far

- B written in Lean from the RFC's prose, with a lemma checked by Lean, is cheap
  and caught an error of mine (a window exception the wrong way round).
- A and B agree on every input tried, and the check can fail.
- The proof for all inputs is open on the last arithmetic step, and its price
  (days of prover work) was out of proportion to what it adds to the goal.
