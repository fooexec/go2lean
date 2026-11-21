# Changelog

One entry a week, every Sunday ([how](docs/releases/README.md)). The full
notes of each release are in [docs/releases/](docs/releases/).

## v0.1.3 (2026-11-22)

The LLVM line finds defects in real C libraries (specs in C, typed inputs,
memory mode, libFuzzer, multi-file libraries; floats, `ctype`, `strtod`
modelled); the Go line's property catalog gains path normalization.
`go2lean symexec`, a bounded symbolic executor with Z3 that finds panics
fuzzing and the search reach with difficulty ([benchmark](docs/eval/symexec.md)).
73 defects in 30 projects, 44 new. [Notes](docs/releases/v0.1.3.md).

## v0.1.2 (2026-11-12)

The first installable release. [Notes](https://github.com/fooexec/go2lean/releases/tag/v0.1.2).
