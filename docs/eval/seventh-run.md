# Seventh run on unseen projects (2026-11-14)

Same protocol as the [sixth run](sixth-run.md): `go2lean run ./...` with
the default flags and a one-hour cap, Go 1.26.6, tool frozen at
`14bd878`. Five more parsers, chosen for having no fuzz targets of their
own.

| project | kind | time | properties | confirmed (tier 1) | spurious | inconclusive |
|---|---|---|---|---|---|---|
| [mattn/go-shellwords](https://github.com/mattn/go-shellwords) `4e5c2e5` | shell word splitting | 75 s | 7 | 0 (0) | 0 | 0 |
| [google/shlex](https://github.com/google/shlex) `e7afc7f` | shell lexing | 8 s | 7 | 2 (0) | 0 | 0 |
| [russross/blackfriday](https://github.com/russross/blackfriday) `e96880f` | Markdown (v1) | 203 s | 79 | 10 (3) | 1 | 4 |
| [magiconair/properties](https://github.com/magiconair/properties) `746ad10` | `.properties` files | 74 s | 77 | 14 (10) | 5 | 1 |
| [jmespath/go-jmespath](https://github.com/jmespath/go-jmespath) `b0104c8` | JSON query language | 82 s | 12 | 2 (1) | 0 | 1 |

## What the findings are

- **properties: a goroutine leak on every parse error, found with no
  spec.** `LoadString("=")` (and every loader that parses) returns its
  error and leaves the lexer's goroutine blocked forever on an unbuffered
  channel send: the parser stops reading on the error and nothing stops
  the lexer. 100 failed parses leave 100 goroutines. The `no_panic`
  template caught it because a replay checks for leaked goroutines.
  Upstream fixed a different leak in 2014 (magiconair/properties#2, a
  second lexer never drained); the error path is not in its tracker. An
  issue is drafted, not filed. The other nine tier-1 findings are
  documented: the `Must` functions, `PanicHandler`, and an encoding value
  outside the declared constants.
- **blackfriday: misuse, now ranked as such.** `Markdown` and
  `MarkdownOptions` panicked in `Html.DocumentFooter` on a renderer the
  test built field by field, with a table-of-contents marker past the
  output. A renderer from `HtmlRenderer` does not. The ranking checked
  forged fields of a receiver but not of an interface argument; it does
  now (tier 2). Left at tier 1: `Latex.BlockCode` indexes
  `strings.Fields(info)[0]` and panics on a blank info string; the parser
  trims the info, so only a caller of the exported method reaches it.
- **go-jmespath:** `MustCompile`'s documented panic.
- **shlex, go-shellwords:** nothing at tier 1 or 2; go-shellwords is
  exhausted on all seven properties.

## What the tool got wrong

| problem | cost | status |
|---|---|---|
| A test variant's own type spelled with the package's qualifier (`jmespath.ASTNode{}`, from an assertion in the call tree): the PoC imported its own package, an import cycle | 1 inconclusive (go-jmespath) | fixed: packages compared by path |
| An interface argument holding the package's struct with forged unexported fields ranked "plain inputs" | 2 blackfriday findings at tier 1 | fixed: tier 2 |
| `os.ReadFile` is not modelled: the model reads a file Go does not find | 5 spurious (properties) | known (#70) |
| A regexp from a non-constant pattern | 1 spurious (blackfriday) | known |

Rerun with the fixes, go-jmespath has no inconclusive verdict and
blackfriday one tier-1 finding (`Latex.BlockCode`).

## What this run says

- With the sixth run's govaluate, this is the second library where the
  default run, with no spec, finds a real defect a maintainer would fix;
  this one is not in the project's tracker.
- The noise left is the outside world (files) and unmodelled regexps,
  both known; the run's own mistakes (a PoC that did not build, two
  rankings) are fixed.
