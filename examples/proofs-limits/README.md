# Where `go2lean prove`'s automatic proof stops (found by the RFC pilot, #289)

`goals.json` asks, for each small function of `mini.go`, only that it returns
an integer. Run from this directory:

```bash
go2lean prove -workspace out -goals goals.json .
```

Expected today (2026-11-20): 16 of 17 are proved: a shift by a symbolic count
(`ShlVar`), `&^`, `|`, division, `uint8` multiplication, conditions, `&&` and
several `if`s with `return`, and constants of every size (`ConstSmall` to
`Const62Add`, `IfConst`, `Cmp`).

`Full` (the shape of quic-go's `DecodePacketNumber`: 22 instructions, three
returns, a shift, masks and two comparisons with `1<<62`) is open: Lean's kernel
stops with "excessive memory consumption" on its proof term. The term grows
with the number of instructions, because `wp_simp` copies the state update of
every instruction into it; a two-instruction function already has a proof term
of 70 MB when printed. That is what is left of #291.

What was wrong, and what was done (#291):
- `Proof/Simp.lean` had no lemma for shifts (`dp_shl`, `dp_shr`).
- `wp_auto` stopped with "maximum recursion depth" on a goal that is a `match`
  on `BitVec.slt c (K - h)`: `wp_step`'s `apply` `whnf`s it, which counts a
  large `K` down one `succ` at a time. `wp_auto` now runs `wp_step` under
  `wp_bounded` (a small depth, the runtime exception caught), so that `split`
  takes the `match`; goals on concrete values still go through `wp_step`.
- The Lean kernel stopped with "deep recursion" on a proof term that has a
  numeral of 2^32 or more next to a variable (lean4#7386, lean4#8898): it
  unfolds the numeral one `succ` at a time. A constant whose signed value is
  2^16 or more in magnitude is now written behind `bigLit`, an opaque identity
  the kernel cannot see through (`bigLit_eq` gives the value back; `wp_auto`
  uses it at the end). Small negative constants are not wrapped: their bit
  pattern is large, but proofs need their value.
