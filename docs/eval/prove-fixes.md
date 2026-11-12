# Evaluation 4: `go2lean prove` on panic fixes

Can `go2lean prove` tell a Go function that panics from the same function
after the panic was fixed? This page is the protocol. It is committed
before `prove` runs on any pair; the results section is added afterwards,
and the protocol is not edited after the run starts.

## Raw material

29 commits from the history of 20 Go modules that fix a runtime panic
(`prove-fixes/pairs.json`). For each one:

- **pre:** the parent commit, where the function panics;
- **post:** the fix commit, where it does not on that input.

**How they were found.** A subagent mined the history of 79 Go modules of
small string, number and encoding libraries:

- commit messages that name a panic, an index or bounds error, a divide
  by zero, an overflow;
- changed functions whose signatures `prove` states (parameters integers,
  bools, strings or integer slices; at most one result);
- plus a probe that called small changed functions on many inputs at the
  parent and the fix.

**Each pair is confirmed on real Go.** A reproducer
(`prove-fixes/tests/<id>_test.go.txt`) panics on pre and passes on post. Both
were run again on the trees this evaluation uses: 29 of 29 panic on pre,
29 of 29 pass on post.

About 40 candidates were rejected, each with its reason
(`prove-fixes/rejected.md`). Nobody running `prove` chose the pairs.

| panic | pairs |
|---|---|
| index out of range | 11 |
| slice bounds out of range (4 through integer overflow) | 11 |
| integer divide by zero | 3 |
| explicit (`strings.Repeat` with a negative count, `regexp.MustCompile`, `panic(err)`) | 3 |
| `make` with a negative length | 1 |

**Build setup, as the reproducers need it.**
- Eight old trees have no `go.mod`; one is written with the module path.
- go-diff's and lz4's own test files are removed (they import dead
  paths).
- minify's `../parse` replace points at a parse tree of the same date.
- s2 builds with `-tags=noasm`.

All 58 trees build.

## Tool

**The build.** go2lean is frozen at the commit that adds this page (Go
1.26, Lean v4.35.0), built once into a separate binary. It has the fixes
from evaluation 3: typed havoc, `-deps` linking, the size bound as fuel,
one goal per check.

**The run, per pair and side.**

```sh
go2lean prove -module <tree> -func <F> -post True -deps \
  -llm claude-code -lean-tools -rounds 4 -budget 10m <package>
```

- The goal is `no_panic`: postcondition `True`, so the theorem says that
  every run that ends returns without panic and without an
  under-approximation, for every input and every value of each type the
  model does not know.
- Companion theorems cover a nil slice or empty string. A companion is
  attempted only once the main goal is proved.
- Loop-free functions are tried with `wp_auto` alone first; the agent
  gets what stays open.
- The judge is `lake comparator --paranoid`.
- Three runs at a time: a pair's side under an eight-hour limit, a
  package of the rerun (measure 5) under sixteen hours.

The agent sees the function's code and the model, as in any run. It is
not told whether a side is pre or post, or that the pair is about a
panic.

## Measures

**1. Soundness: no pre side is proved.** Every pre function panics on an
input the theorem covers: offset 0, capacity equal to length, the
recorded input. A judged proof of a pre main goal would be a soundness
failure. It is reported first, with the proof. A companion of a pre side
may be proved when the panicking input is not nil or empty; such a
companion is checked against the reproducer's input and reported.

**2. The post side's proved rate.** For each post goal:
- **proved**, with the judge's certificate;
- or **open**, with one reason:
  - (a) the function or a callee is not modelled completely; the
    blockers `prove` lists are given;
  - (b) the statement is not supported (a parameter or result kind);
  - (c) false in the model: a run of the model panics or takes an
    under-approximation, and whether Go takes it too (a reproducer);
  - (d) no proof within the rounds;
  - (e) a tool error.

**3. Fuzzing.** Each proved post property runs as a Go fuzz target for
60 seconds on the real code. Inputs stay in the theorem's domain:
capacity equal to length; nil or empty only where that companion is
proved. A panic would be a soundness failure.

**4. Cost.** Rounds, agent time and tool turns per goal.

**5. Rerun of evaluation 3.** The 13 functions of
[evaluation 3](prove-blind.md)'s arm A (fuzzysearch, levenshtein,
smetrics, `Match` included), with this build and the settings above.
Reported separately from the pairs and from evaluation 3's frozen run.

Not done: a comparison with Gobra or another Go verifier.

## Expectations, written before the run

- **No pre side proved.** Anything else is a bug in go2lean.
- **Post sides.**
  - Loop-free, byte-level functions (`isBadTrailer`, `revSquash`,
    `unwrap`, `Before`) should prove.
  - Functions over `[]rune`, `regexp`, `sync.Pool`, `fmt` or
    `strconv` will mostly stay open through (a).
  - Functions with loops depend on the agent's invariants (d).
  - A post-side proved rate near a third would be a good result.
- **Some fixes may be partial.** The post side may still panic on another
  input. `prove` would then rightly leave it open with (c), and the
  reproducer for that input is the check.

## Results

### Run log

**The first start was stopped after 7 of 61 runs**, at the frozen build
`4e23908`. Each failure was a tool error, not a verdict:

- **Prompt too large.** The agent's prompt held the whole linked model,
  0.4 to 1.7 MB, and `claude -p` failed on one.
- **Recursion depth.** The lookup lemma `prog_fN := rfl` hit Lean's
  recursion limit on a linked program of 500 functions. So did the walk
  of the proof.
- **A dependency too large to build.** `rivo/uniseg`'s break tables are
  one block of 22,000 instructions. The model of go-pretty's `text`
  failed to build.
- **One check stopped the whole run.** A Lean check that was killed or
  timed out aborted `prove` (exit 137 on natsort).

None of these runs proved a goal; nothing from them is counted. Fixed in
[#34](https://github.com/fooexec/go2lean/pull/34):
- the prompt carries only the function and what it reaches;
- the recursion limits are raised;
- a check that times out or runs out of memory (capped at 6 GB) leaves
  that goal unchecked;
- the agent hears of it, and the run goes on.

The run restarts from scratch at that merge, the build frozen there,
with **two runs at a time instead of three**, for memory. Everything else
is as above.

(The results follow when the run ends.)
