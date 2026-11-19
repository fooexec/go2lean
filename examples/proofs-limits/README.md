# Where `go2lean prove`'s automatic proof stops (found by the RFC pilot, #289)

`goals.json` asks, for each small function of `mini.go`, only that it returns
an integer. Run from this directory:

```bash
go2lean prove -workspace out -goals goals.json .
```

Expected today (2026-11-19): everything is proved except the functions with a
64-bit constant of 2^32 or more (`Const32`, `Const40`, `Const50`, `IfConst`,
`Const62Add`, and so `Cmp` and `Full`, which carry `1<<62`): `wp_auto`
stops with "maximum recursion depth has been reached" while stepping the
function (also with `maxRecDepth` raised to 1,000,000), and for `Const62Add`
the kernel reports a deep recursion. `ConstSmall` (1000) is proved. A shift
by a symbolic count (`ShlVar`) failed until `Proof/Simp.lean` got its shift
lemmas (`dp_shl`, `dp_shr`).
