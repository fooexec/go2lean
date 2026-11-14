# Sixth run on unseen projects (2026-11-14)

Same protocol as the [fifth run](fifth-run.md): `go2lean run ./...` with
the default flags and a one-hour cap, Go 1.26.6, in a clone of a project
go2lean had not been tried on, tool frozen at `0f86002`. The four were
picked as parsers and evaluators with less fuzzing than grpc-go.

| project | kind | time | properties | confirmed (tier 1) | spurious | inconclusive |
|---|---|---|---|---|---|---|
| [araddon/dateparse](https://github.com/araddon/dateparse) `6b43995` | date parsing | 116 s | 8 | 6 (0) | 0 | 0 |
| [Knetic/govaluate](https://github.com/Knetic/govaluate) `7625b7f` | expression evaluator, no `go.mod` | 174 s | 14 | 8 (7) | 1 | 0 |
| [dlclark/regexp2](https://github.com/dlclark/regexp2) `0b4baaf` | regex engine | failed after 2,234 s | | | | |
| [gomarkdown/markdown](https://github.com/gomarkdown/markdown) `13f9312` | Markdown parser and renderer | 412 s | 128 | 43 (6) | 2 | 2 |

## What the findings are

- **govaluate: a real bug, found with no spec.** All seven tier-1
  findings are one panic: `NewEvaluableExpression` on an expression that
  ends in a backslash (`cc\`) reads past the end of its rune slice
  (`lexerStream.readCharacter`, index out of range). It is upstream's open
  issue Knetic/govaluate#162, unfixed. go2lean found it on its own, from
  the `no_panic` template of the constructor, in under three minutes.
- **dateparse: nothing, and a search problem.** The six findings were
  nil functional options (`opts ...ParserOption`), forged and at tier 4:
  every search stopped at calling one and never varied the date string
  (#233, fixed: such a slice is now empty). Rerun, dateparse gives
  `MustParse`'s documented panic and nothing else. Two minutes of Go
  fuzzing over `ParseAny`/`ParseStrict` (4.3M executions) also found no
  panic.
- **gomarkdown: misuse at tier 1, the parser not yet reached.** The tier-1
  findings are nil nodes passed to the tree helpers, `Leaf.SetChildren`'s
  documented panic, `ListItemOpenCR` on a list item whose parent is not a
  list (a tree the parser does not build), and `textutil.SkipDigits` at
  index -1. `Parser.Parse` and `ToHTML`, the entry points, were spurious:
  - the block rules are a table of method expressions (`(*Parser).M`),
    which the model did not know; fixed, `Parse` now gets through `Block`;
  - `ast.Walk` then calls `(ast.Node).GetChildren`, promoted from an
    embedded `ast.Container`, which is not followed yet (#234).
- **regexp2: the run failed.** The `syntax` package's initializer is one
  function of 147,911 SSA values, from 12,000 lines of Unicode tables.
  Lean's compiler stopped on it after 516 s and 10 GB, and since the search
  executable is one build, all four packages failed with it (#232).

## What this run says

- On a library with an open crash bug, a plain `go2lean run` finds it and
  ranks it first. That had not happened on grpc-go, whose one bug sits in
  an unexported helper.
- The other three runs were held back by the tool, not by the code: a
  generator choice (#233), two model gaps on gomarkdown's main path (one
  fixed, #234), and a build that cannot fail one package at a time (#232).
