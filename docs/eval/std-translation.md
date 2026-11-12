# Translating more of the standard library (2026-11-12)

Under `-deps`, the compile stage translates a dependency's Go source and
runs it in the model. For the standard library it does so only for a list
of packages (`modelledStdlib`, 22 of them: `strings`, `strconv`, `bytes`,
`math/big`, ...). A call into any other package is unmodelled: the model
picks its result, and a wrong pick can give a spurious verdict.

If the model covered the language completely, every library written in Go
would be a natural extension, with no list. This experiment measures how
far that holds today. `GO2LEAN_STD_ALL=1` widens the list in two ways:

- **every package:** every package except the boundary and the packages the
  model has its own semantics for (`fmt`, `errors`, `regexp`,
  `encoding/json`, `sync`, `time`, `context`, `testing`, `log`, `net`, ...).
  The boundary is the runtime, `unsafe`, `reflect`, `os` and `syscall`.
- **no boundary import:** of those, only the packages that import no
  boundary package directly (`reflect`, `os`, `unsafe`, `syscall`, `net`,
  the runtime). `text/template` and `encoding/gob` stay out;
  `text/tabwriter`, `container/*`, `hash/*` and `compress/*` come in. This
  is what `GO2LEAN_STD_ALL=1` does now.

Both are measured on the [standard-library audit](std-audit.md) and on the
seven projects of the [fourth run](fourth-run.md). Every mode used the
same build, `main` after #208.

## The audit

16,277 to 16,417 functions (test helpers left out). The total varies,
because translating more packages also brings in the generic instances
they use.

| family | the list (default) | every package | no boundary import |
|---|---|---|---|
| **translated completely** | **71.8%** | **75.9%** | **74.4%** |
| language: Go the model does not cover yet | 3.1% | 2.8% | 3.0% |
| boundary: runtime, `unsafe`, assembly, the OS | 7.9% | 8.0% | 7.9% |
| a dependency `-deps` did not reach | 9.4% | 4.8% | 6.7% |
| a value of unknown origin (compiled out of context) | 7.7% | 8.4% | 7.9% |
| packages that fail to compile | 0 | 0 | 0 |
| compile time, all 165 packages | 356 s | 453 s | 344 s |

Translating every package halves the dependencies not reached, and lifts
the complete share by 4.1 points: more than every fix so far together
(#205 to #208: 0.4 points).

## The seven projects

Each project was run with `go2lean run ./...` and the default flags, once
per mode. "Tier 1" counts the confirmed findings a caller can reach.

| project | the list: time | confirmed (tier 1) | spurious | inconclusive | every package: time | confirmed (tier 1) | spurious | inconclusive | no boundary import: time | confirmed (tier 1) | spurious | inconclusive |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| golang-lru | 130 s | 1 (0) | 0 | 0 | 142 s | 1 (0) | 0 | 0 | 130 s | 1 (0) | 0 | 0 |
| conc | 55 s | 9 (4) | 0 | 1 | 204 s | 9 (4) | 0 | 1 | 51 s | 9 (4) | 0 | 1 |
| chi | 52 s | 31 (8) | 0 | 0 | 70 s | 30 (8) | 0 | 0 | 60 s | 30 (8) | 0 | 0 |
| envconfig | 49 s | 1 (1) | 0 | 0 | 99 s | 1 (1) | **2** | 0 | 41 s | 1 (1) | **1** | 0 |
| go-multierror | 7 s | 3 (2) | 0 | 0 | 7 s | 3 (2) | 0 | 0 | 5 s | 3 (2) | 0 | 0 |
| msgpack | 174 s | 68 (0) | 0 | 0 | 164 s | 68 (0) | 0 | 0 | 164 s | 68 (0) | 0 | 0 |
| go-toml | 151 s | 32 (28) | 0 | 0 | 389 s | 32 (28) | 0 | 0 | 154 s | 32 (28) | 0 | 0 |
| **total** | **618 s** | **145 (43)** | **0** | **1** | **1,075 s** | **144 (43)** | **2** | **1** | **605 s** | **144 (43)** | **1** | **1** |

The tier-1 findings are the same in every mode.

## What changed, and why

- **The time, under every package.** conc went from 55 s to 204 s, and
  go-toml from 151 s to 389 s. More of their dependencies' code runs in the
  model, and every trial runs it. Leaving out the packages that import a
  boundary package brings the total back to the default's.
- **envconfig, 2 spurious verdicts under every package.**
  - `Usage` and `Usagef` reach `text/template`.
  - `text/template` calls `reflect.Type.NumOut`, which is unmodelled.
  - The model picks a result that leads to an explicit panic inside the
    template code. Go returns an error instead.
- **envconfig, 1 spurious verdict with no boundary import.**
  - `text/template` stays unmodelled now, but `text/tabwriter` is
    translated.
  - Its `Write` and `Flush` recover their own `osError` panics with
    `defer b.handlePanic(&err, op)`, a shape the model's `recover`
    (#181) did not cover (#209). Fixed: with #209, envconfig gives no
    spurious verdict under `GO2LEAN_STD_ALL=1`.
- **chi, 1 finding lost in both.**
  - `Compressor.SetEncoder~NewCompressor` is confirmed by default.
  - Translated, `NewCompressor` runs `compress/flate`'s writer
    constructors, which allocate and fill large tables.
  - Every one of 2,000 random trials and 14,060 exhaustive inputs ran out
    of fuel before reaching the panic (#210).

## Conclusion

Translating more of the standard library is the direct way to need no
hand-written models, and the audit shows its size. But each package it
adds also reaches what the model does not cover yet:
- the boundary (`reflect`);
- a language gap (`recover`, #209);
- code too costly to run in every trial (#210).

So `GO2LEAN_STD_ALL` stays off by default, and its gaps join the
[language-completeness plan](https://github.com/fooexec/go2lean/issues/207).
It becomes the default when the seven projects give the same findings in
the same time.
