# The proof agent on loops

Two runs of `go2lean prove -llm`, in which an LLM writes the loop
invariants and proofs of functional theorems that go2lean states
([#22](https://github.com/fooexec/go2lean/issues/22)): the first on
single loops, the second on nested loops, a call in a loop, and Go specs
as postconditions.

## Setup

- **Targets** (first run): `examples/invariants`, four functions with one loop each,
  written for this run, none with a hand proof anywhere in the repository:
  `CountPositive` (a count between 0 and the length), `IndexOf` (-1, or an
  index whose element is the one sought), `AllNonNeg` (true only if no
  element is negative) and `Fill` (every element of the slice set). The
  goals are `examples/invariants/goals.json`.
- **What the agent gets**: the library (`WP.lean`, `Tactic.lean`,
  `Simp.lean`), the hand proofs of other functions as worked examples
  (`lean/Proofs/Functional.lean`), the package's model, and the statement.
  It answers with a prelude (invariants, lemmas) and a proof. go2lean
  writes the statement, checks the answer (no `sorry`, axioms, syntax
  extensions or namespace commands), runs Lean, and feeds the errors back.
- **What counts as proved** (in these runs): `#print axioms` lists only
  `propext`, `Classical.choice` and `Quot.sound`, and the theorem has the
  stated type when read again outside the namespace the answer wrote in.
  Since then the verdict is Lean's comparator's, and the statements are
  stronger; every proof here was redone under them
  ([proof-trust](proof-trust.md)).
- **LLM**: the claude CLI's default model (`-llm claude-code`), no tools.

## Single loops (`examples/invariants`)

| function | rounds | time | prelude, proof (lines) |
|---|---|---|---|
| `CountPositive` | 3 | 5 min | 62, 14 |
| `IndexOf` | 3 | 8 min | 90, 20 |
| `AllNonNeg` | 2 | 6 min | 9, 82 |
| `Fill` | 3 | 6 min | 87, 15 |

All four are proved, kernel only, in 2 or 3 rounds (25 minutes in all,
about a minute of each round Lean's). The proofs are in
`examples/invariants/proofs.json` and CI checks them again without the
agent (`scripts/proofs.sh`).

A first run, with the CLI's default effort and no guide to the shape of a
proof in the system prompt, proved neither of the two goals it reached in
four rounds each. Its errors were of one kind: a block on a path left
unwalked before the back edge, and terms in another normal form than
`simp` leaves (`(i + 1).toNat` against `(i.toNat + 1) % 2^64`). The second
run added `--effort high` and a paragraph on how a proof goes (walk every
block of each path from the header; return or back edge); its first-round
errors were of the same kinds, and Lean's errors were enough to fix them.

## Harder loops, Go specs as postconditions (`examples/loops`)

The second run, with the same LLM and settings (`-rounds 8`), on functions
written for it: `ClampAll` (a loop calling `Clamp`), `HasDuplicate`
(nested loops), `BubbleSort` (nested loops writing the slice). Three
goals take a `go2lean:spec` as the postcondition: the spec runs in the
model on the arguments and the result, so the theorem is the property the
search checks, and the agent proves the spec's own loop too. The prompt
also carried the first run's proofs as examples.

| goal | postcondition | rounds | time | prelude, proof (lines) |
|---|---|---|---|---|
| `Clamp_range` | Lean | none (`wp_auto`) | | |
| `SpecClamp` | Go spec | none (`wp_auto`) | | |
| `SpecClampAll` | Go spec, a loop | 2 | 10 min | 204, 14 |
| `HasDuplicate_spec` | Lean | 2 | 7 min | 130, 13 |
| `SpecBubbleSortSorted` | Go spec, a loop | 2 | 27 min | 433, 17 |
| `BubbleSort_perm` | Lean (`List.Perm`) | 2 | 12 min | 154, 8 |

All six are proved, kernel only (`examples/loops/proofs.json`, checked
again in CI). Two of the four failed first rounds were go2lean's: the
check on the answer read the word "prefix" in a comment as a syntax
command, and the agent, which sees only its own goal, reused the names
of another goal's prelude. Both are fixed (comments are not read; the
prompt lists the names the file already declares).

## Loop paths and Lean tools

Two changes, measured by running both packages again from scratch (the
saved proofs of the package being proved are not in the prompt):

- **Loop paths**: go2lean lists, from the control-flow graph, the paths
  from each loop header (to its back edge, another header, or a return),
  so that no block on a path is left unwalked.
- **Lean tools** (`-lean-tools`, the default with `-llm claude-code`): the
  agent gets [lean-lsp-mcp](https://github.com/oOo0oOo/lean-lsp-mcp) on the
  workspace's Lean project (code checking, goal states, diagnostics; its
  network search tools are off) and a complete file to check its answer
  in, the goals before it stated with `sorry`. It checks its prelude and
  proof itself before answering; go2lean's own check is unchanged.

| run | `examples/invariants` (4 goals) | `examples/loops` (4 goals) |
|---|---|---|
| before (above) | 11 rounds, 25 min | 8 rounds, 57 min |
| loop paths | | 7 rounds, 44 min |
| loop paths and Lean tools | 4 rounds (each first), 12 min, 2–8 tool turns a goal | 4 rounds (each first), 31 min, 4–18 tool turns a goal |

With the tools every goal is proved by the first answer: the errors the
earlier runs fed back over rounds (normal forms after `simp`, a missed
block, a type mismatch) are now found and fixed by the agent in its own
turns, in seconds each instead of a round of go2lean's.

## Strings and empty inputs

`examples/strloops` takes strings (`FirstByte`, and `CountByte`, a loop
over the bytes). With the Lean tools both are proved by the first answer
(25 and 27 tool turns: `FirstByte` has no loop, but its string indexing
is past `wp_auto`). Every goal with a slice or string parameter now has a
companion theorem for the nil slice or empty string, which `wp_auto`
proves alone in every case here (the walk is concrete); two
postconditions that speak of the slice's backing array say what holds
for nil instead (`"empty_post": "True"`: no panic).

## Reading

The invariants are what a person would write: `CountPositive`'s says the
index starts at -1 (go/ssa's range loops do) and the count stays between
0 and the index plus one; `Fill`'s, that the elements up to the index are
`v` and the slice keeps its length. The proofs follow the worked examples
closely, so the examples carry most of the method. The second run shows
it reaches past them: nested loops, a call through a contract inside a
loop, and a sort's sortedness and permutation, each in two rounds; the
cost is the agent's time (up to half an hour for a goal), not Lean's.
