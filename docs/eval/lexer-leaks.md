# Goroutine lexers that leak on a parse error (2026-11-14)

The [seventh run](seventh-run.md) found that every parse error in
magiconair/properties leaks a goroutine. Its lexer is the design of Go's
old `text/template` lexer: a goroutine runs the state machine and sends
each token on an unbuffered channel, and the parser receives them. When
the parser stops on an error, nothing stops the lexer, which stays blocked
on its next send. Libraries that copied that lexer may have kept the leak,
so this run looked for them.

## Protocol

Template and expression libraries were cloned and searched for a lexer
that runs in a goroutine and sends on a channel. Each one found was
checked by hand (100 failed parses, goroutines counted), and the leaking
ones were then given to `go2lean run ./...` with the default flags, to
see whether the tool finds the leak by itself.

| library | goroutine lexer | leaks on a parse error |
|---|---|---|
| [magiconair/properties](https://github.com/magiconair/properties) `746ad10` | yes | **yes**, `LoadString("=")` |
| [aymerick/raymond](https://github.com/aymerick/raymond) `7c01a03` (archived 2025) | yes | **yes**, `Parse("{{/bar}}")` |
| [mailgun/raymond/v2](https://github.com/mailgun/raymond) `e9dbaad` (the fork projects import) | yes | **yes**, the same inputs |
| [robfig/soy](https://github.com/robfig/soy) | yes | no: the parser drains the lexer |
| [CloudyKit/jet](https://github.com/CloudyKit/jet) v6 | yes | no: the parser drains the lexer |

The other libraries looked at lex without a goroutine: pongo2, amber,
plush, liquid, hoisie/mustache, cbroglie/mustache, both gonja forks, ace,
hil, go-bexpr, go-i18n, bleve; mtail's lexer sends on a channel but runs
its state machine inside `NextToken`.

## raymond

Every syntax error leaves one goroutine behind: `{{/bar}}`,
`{{#each a}}{{/if}}`, `{{> }}`, `{{foo bar=}}`, `{{^}}`, `{{else`. A lexer
error (`{{!-- x`, an unclosed comment) does not leak: the error token is
the lexer's last send. Neither repository's tracker mentions it. An issue
for mailgun/raymond is drafted, not filed.

**What go2lean found, and what it took.** The first `go2lean run` found
the leak only through `lexer.Scan` (tier 1). `Parse` and `Render` were
spurious: the model's lexer and parser parted from Go's on the way to
the error. Five gaps, each fixed:

1. **Regexps passed to a helper.** raymond's lexer matches 23 constant
   patterns through `findRegexp(r *regexp.Regexp)`; the model ran "one of
   the package's patterns" for at most 12, so every match was havoc. The
   bound is now 32.
2. **Patterns built with `regexp.QuoteMeta`.** `rDotID` is
   ``regexp.MustCompile(`^\.` + lookheadChars)`` with `lookheadChars`
   built by `regexp.QuoteMeta("=~}/)|")`: not a constant pattern to the
   model. `QuoteMeta`, `ToLower`, `ToUpper` and `TrimSpace` of a constant
   are now evaluated at compile time.
3. **`fmt.Errorf` and `errors.New`.** The parser panics with
   `fmt.Errorf(...)` and its `errRecover` type-switches the recovered
   value: `case runtime.Error` re-panics, `case error` returns it. The
   model's opaque error took neither branch for sure, and every parse
   error became a panic Go never raises. `errors.New` and `fmt.Errorf`
   without `%w` are now a fresh `*errors.errorString`, as Go makes them,
   and a value of an unmodelled call is not a `runtime.Error`.
4. **The replay's leak check** counted goroutines of the target package
   only; raymond's lexer is in `raymond/lexer`, so a leak Go showed was
   read as none. It now counts any package of the module.
5. **Ranking.** `Render` and `Template.Exec`, with an `interface{}`
   context holding a `[]interface{}`, were ranked as forged inputs; a type
   literal is no package's type.

With these, `go2lean run ./...` on raymond (537 s, 121 properties) gives
the leak at tier 1 through `raymond.Parse`, `parser.Parse` and
`Template.PrintAST`, on the input `{{~\nelse\n~}}`, and through `Render`
and `Template.Exec`. Spurious verdicts go from 10 to 3.

## What this says

- A plain run finds this class of bug: the replay checks every call for a
  goroutine left blocked, so a `no_panic` template catches a leak with no
  spec. Two libraries in five with the design leak, both unreported.
- On raymond the tool needed five fixes before it could say so through the
  public API; each was a gap a template engine exercises and the earlier
  runs had not.
