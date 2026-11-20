# Where `go2lean prove`'s automatic proof stops (found by the RFC pilot, #289)

`goals.json` asks, for each small function of `mini.go`, only that it returns
an integer. Run from this directory:

```bash
go2lean prove -workspace out -goals goals.json .
```

Expected today (2026-11-20): all 17 are proved: a shift by a symbolic count
(`ShlVar`), `&^`, `|`, division, `uint8` multiplication, conditions, `&&` and
several `if`s with `return`, and constants of every size (`ConstSmall` to
`Const62Add`, `IfConst`, `Cmp`).

All 17 are proved and judged. `Full` (the shape of quic-go's
`DecodePacketNumber`: 22 instructions, three returns, a shift, masks and two
comparisons with `1<<62`) is judged with a note: nanoda needs more than 13.9 GB
for its 6,608 declarations and the host's memory limit kills it (exit 137),
while Lean's two kernels, lean4lean, con-leche and con-ron accept. The judge
allows that one case, when goals are judged one at a time, and the certificate
and the `prove` output say so (`unrun_checkers`, `docs/trust.md`).

What was wrong, and what was done (#291):
- `Proof/Simp.lean` had no lemma for shifts (`dp_shl`, `dp_shr`).
- `wp_auto` stopped with "maximum recursion depth" on a goal that is a `match`
  on `BitVec.slt c (K - h)`: `wp_step`'s `apply` `whnf`s it, which counts a
  large `K` down one `succ` at a time. `wp_auto` now runs `wp_step` under
  `wp_bounded` (a small depth, the runtime exception caught), so that `split`
  takes the `match`; goals on concrete values still go through `wp_step`.
- `a & ^m` is two instructions in the model (`bnot`, `and`), and Lean's kernel
  ran out of memory on the proof of a function that has them, while the same
  value as the one instruction `a &^ m` is accepted (found by bisecting `Full`;
  straight-line code of 24 instructions, branches and large constants are not
  the cause). The frontend now writes `a & ^b` as one `andNot` when the `^b` has
  no other use (`TestAndNotFold`). Why the pair is too much for the kernel is
  not known: a stack of the kernel shows it in `is_def_eq`
  (`lazy_delta_reduction`), not which constant.
- The Lean kernel stopped with "deep recursion" on a proof term that has a
  numeral of 2^32 or more next to a variable (lean4#7386, lean4#8898): it
  unfolds the numeral one `succ` at a time. A constant whose signed value is
  2^16 or more in magnitude is now written behind `bigLit`, an opaque identity
  the kernel cannot see through (`bigLit_eq` gives the value back; `wp_auto`
  uses it at the end). Small negative constants are not wrapped: their bit
  pattern is large, but proofs need their value.
