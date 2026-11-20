# Where `go2lean prove`'s automatic proof stops (found by the RFC pilot, #289)

`goals.json` asks, for each small function of `mini.go`, only that it returns
an integer. Run from this directory:

```bash
go2lean prove -workspace out -goals goals.json .
```

Expected today (2026-11-20): 10 of 17 are proved: a shift by a symbolic count
(`ShlVar`), `&^`, `|`, division, `uint8` multiplication, conditions, `&&` and
several `if`s with `return`, and a small constant (`ConstSmall`, 1000).

The 7 that carry a 64-bit constant of 2^32 or more (`Const32`, `Const40`,
`Const50`, `IfConst`, `Const62Add`, and so `Cmp` and `Full`, which have
`1<<62`) are open: Lean's kernel stops with "deep recursion detected" (`Full`:
"excessive memory consumption") while checking the proof term. It depends on
the size of the constant alone: `c + 1000` is proved, `c + 1<<32` is not, with
the same proof. Plain Lean shows the kind of step that is too much for the
kernel: `(c + K#64).toNat = (c.toNat + K % 2^64) % 2^64 := rfl` takes
`Nat.add` of a variable and `K`, which the kernel evaluates one `succ` at a
time unless both arguments are literals. Which step of the proof term does
that is not known yet (#291).

Fixed on the way (#291): `Proof/Simp.lean` had no lemma for shifts, and
`wp_auto` stopped with "maximum recursion depth" on a goal that is a `match`
on `BitVec.slt c (K - h)`: `wp_step`'s `apply` `whnf`s it, which counts down a
large `K` one `succ` at a time. `wp_auto` now runs `wp_step` under
`wp_bounded` (a small depth, the runtime exception caught), so that `split`
takes the `match`; goals on concrete values still go through `wp_step`'s
evaluation.
