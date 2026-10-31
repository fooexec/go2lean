# Goose's semantics tests, as a differential corpus

The Go files here (all but `zz_*` and `go2lean_*`) are the semantics tests of
[Goose](https://github.com/goose-lang/goose), `testdata/examples/semantics`
at commit 3be88bb (2026-04-07), under Goose's MIT licence (`LICENSE`).
`prims.go` and `wal.go`, which need Goose's disk primitives, are left out.

Each `func testX() bool` returns true in Go (`zz_goose_test.go`). go2lean's
model must agree: `zz_seq_test.go` makes each one a sequence spec, and a
counterexample, a stuck run or a havoc read is a disagreement between the
model and Go (#58).

`scripts/sem-diff.sh <workspace>` runs the suite in Go and in the model.
`expected.txt` lists the known disagreements, each with the issue that
tracks it; CI fails when the outcomes change, so a fix removes its lines
(`scripts/sem-diff.sh <workspace> -update`).

`go2lean_*.go` are go2lean's own tests in the same form, one file per gap
closed (`go2lean_atomic.go`: #67; `go2lean_stdlib.go`: #69, #71; `go2lean_init.go`: #55; `go2lean_defer.go`: #62; `go2lean_errors.go`: #69; `go2lean_io.go`: #69, #47; `go2lean_chan.go`: #58, channels, `select` and `sync` after Go's runtime tests; `go2lean_pool.go`: #105). After adding a test, regenerate the
`zz_*` files with `python3 scripts/sem-gen.py`.
