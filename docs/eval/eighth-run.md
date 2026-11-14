# Eighth run on unseen projects (2026-11-14)

Same protocol as the [seventh run](seventh-run.md), tool frozen at
`0cf09c6` (the seventh run's fixes). Five more libraries without fuzz
targets.

| project | kind | time | properties | confirmed (tier 1) | spurious | inconclusive |
|---|---|---|---|---|---|---|
| [andybalholm/cascadia](https://github.com/andybalholm/cascadia) `a346454` | CSS selectors | 68 s | 25 | 0 (0) | 0 | 10 |
| [aymerick/douceur](https://github.com/aymerick/douceur) `f9e2974` | CSS parser, no `go.mod` | 103 s | 34 | 2 (2) | 0 | 0 |
| [huandu/xstrings](https://github.com/huandu/xstrings) `28aabeb` | string functions | 302 s | 56 | 7 (7) | 0 | 5 |
| [fatih/camelcase](https://github.com/fatih/camelcase) `556ff74` | word splitting | 29 s | 2 | 0 (0) | 0 | 0 |
| [docker/go-units](https://github.com/docker/go-units) `2bd057e` | size and duration parsing | 33 s | 10 | 1 (1) | 0 | 0 |

## What the findings are

No bug a maintainer would fix urgently:
- **douceur:** `DeclarationsByProperty.Less`/`Swap` with out-of-range
  indices, the `sort.Interface` contract.
- **xstrings:** documented panics (`ExpandTabs` with a non-positive size,
  `Insert` and `Slice` out of range), `Center`/`LeftJustify`/
  `RightJustify` with a length near `MaxInt` (`strings.Builder.Grow`
  overflows), and one edge: `Translator.TranslateRune(-1)` indexes its
  ASCII table with the negative rune (`r <= unicode.MaxASCII` lets it
  through). A rune of -1 does not come from decoding a string.
- **go-units:** `CustomSize` with an empty unit list.
- **cascadia:** after the fixes below, `MustCompile`'s documented panic.

## What the tool got wrong

| problem | cost | status |
|---|---|---|
| The replay sandbox copied the package and its `testdata` only; cascadia's tests read `test_resources/` in an `init`, which `log.Fatal`ed | 10 inconclusive (every cascadia replay) | fixed: data directories (no Go file, no `go.mod`, under 64 MB) are copied too |
| An alias of another package's type (`type stringBuilder = strings.Builder`) made that type's methods targets of the package, spelled without a qualifier | 5 inconclusive (xstrings, PoCs that did not compile) | fixed: a package's methods are its own functions |

Rerun with the fixes: cascadia 9 confirmed (1 at tier 1), 1 spurious, 0
inconclusive; xstrings 0 inconclusive.

## What this run says

Five more libraries and no new bug: on small, well-tested string
libraries the templates find contract edges, not defects. The two tool
problems were the replay's environment, not the model.
