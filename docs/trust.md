# Proofs: threat model and trust base

What a `proved` record from `go2lean prove`, or from the WP route of the spec & search stage, asserts. Which parts of
go2lean and Lean that assertion rests on. What an agent writing the proof
can and cannot do to it. Issue
[#22](https://github.com/fooexec/go2lean/issues/22).

## What a `proved` record asserts

A goal names a function `f` (model id `N`) of a package and a postcondition
`P` over the parameters, `ret` and `s'`. `P` is Lean text, or a Go spec run in the model. go2lean writes the statement;
for a function `f(a int, xs []int) int` it is

```lean
theorem f_spec (orc : Oracle) (hv : Havoc Val)
    (a : BitVec 64) (xs : List (BitVec 64)) (xs_len : BitVec 64) (xs_ref : Nat)
    (s : HState Val)
    (h_hv : HavocTyped <model>.prog.havocWidths hv)
    (h_xs_len : xs_len.toInt = xs.length) (h_xs_ref : xs_ref ≠ 0)
    (h_xs : s.heap[xs_ref]? = some (.arr (xs.map (.int 64))))
    (h_assumed : s.assumed = #[]) :
    wpFun <model>.prog orc hv N [.int 64 a, .slice xs_ref 0 xs_len xs_len]
      (fun ret s' => s'.assumed = #[] ∧ P) s
```

It reads: for every memory oracle and every havoc function (every value the
model does not know, of the type its site has: an integer of the site's
width, a bool, an error or a string), every argument, and every heap the
argument lives in, every run of the model of `f` that ends:

- does not panic;
- takes no under-approximation (`s'.assumed = #[]`: the model left out
  none of Go's behaviours on that run, `docs/semantics.md`);
- ends with a result and state satisfying `P`.

A Go spec as `P` is the spec's own run, on the arguments and the result,
returning `true` without an under-approximation.

It does not assert:

- **Termination.** A run that does not end, or runs out of fuel,
  satisfies every `wpFun`: this is partial correctness.
- **Slices past the model's size bound.** A run that would make a slice
  of more than 1024 elements ends out of fuel, so the theorem says
  nothing about it.
- **Uncovered inputs.** A slice is taken with offset 0 and capacity equal
  to its length. A nil slice or empty string is covered by companion
  theorems (`f_spec_empty…`) for up to three such parameters; with more,
  the record's domain names the parameters left out.
- **Anything about Go itself.** The theorem is about the model. How
  faithfully the model follows Go is a separate question
  (`docs/semantics.md`, `docs/validation.md`), which the differential
  checks below test but no proof answers.

The pipeline's WP route states the same theorem with `P` = `True`
(no_panic) or the property's Go spec, and records it with the domain
spelled out and the judge's certificate. The bounded `bv_decide` route
(`Tactic: simp, split, bv_decide`) is a different claim: one fixed oracle,
runs within a fuel, `Trust: compiler` when `Lean.ofReduceBool` appears.
This page is not about that route.

## Trusted (the trust base)

| Part | Why it is trusted | How it is checked |
|---|---|---|
| Lean's kernel, and with `-paranoid` (the `prove` default) the external checkers bundled with Lean v4.35 (`leanchecker-paranoid`, lean4lean, nanoda, con-leche, con-ron) | they decide that a proof term has the stated type | independent implementations must agree |
| `lake comparator` and `leanexport` (Lean v4.35) | they compare the solution's theorem type with the challenge's and export the environment the kernels replay | Lean's own code |
| The model's semantics (`lean/Go2Lean/SSA`, `lean/Go2Lean/Dialect/GoSSA.lean`), including `wpFun` | the statement means what they define | differential tests against Go (`docs/validation.md`); fuzz and replay of proved properties |
| The frontend (go/ssa, `internal/ir`, `internal/ssaout`), including each havoc site's type (`havocWidths`, a hypothesis of the theorem) and the linking of dependencies (`ir.Link`, with `prove -deps`) | the model of `f` is what it prints | the same differential tests |
| The statement generator (`internal/prover/functional.go`: `statement`, `SpecPost`, `Companions`) | it turns a goal into the theorem above | this page; unit tests; mutants of proved functions must fail to prove |
| The judge (`internal/prover/judge.go`) | it builds the trusted project and reads comparator's exit status | the red-team corpus |
| bubblewrap and the kernel's namespaces | they confine Lean text an agent wrote | the corpus's file-write attack |

The judge builds its project only from these inputs:

- the checkout's core sources (`repo.CoreFiles`, hashed into the
  certificate);
- the model file, copied only if its SHA-256 matches the one the compile
  stage recorded (a model over 4 MB is written as part modules, hashed in
  order with it);
- go2lean's own statements, in `Challenge.lean`.

The trusted modules are built before any answer text is in the project.

## Not trusted

- **Everything an agent writes.** The prelude (definitions, lemmas,
  invariants), the proof, and whatever its Lean tools run.
- **Lean's elaboration of that text.** This covers tactics, macros,
  instances, `#eval` and options. The proof library's tactics
  (`Go2Lean.Proof.Tactic`) and lemmas (`Go2Lean.Proof.WP`, checked by the
  kernel) are also untrusted, except the definitions the statement names.
- **Lean's output.** `#print axioms` and error messages are read only to
  give the agent feedback (`CheckGoals`). The verdict does not depend on
  them.
- **Regex vetting of answers** (`vetProof`). It only gives early feedback
  and saves a round.
- **The per-goal check.** In the workspace each goal is checked alone,
  the goals before it stated as axioms (their exact statements) for it to
  use. That file is feedback only; the judge never sees those axioms.

## The adversary

The adversary is the proof agent, assumed malicious.

What it controls:

- the text of its answers;
- its tool calls: lean-lsp-mcp's `lean_run_code`, `lean_goal` and the
  like, which elaborate arbitrary Lean;
- every file those can write.

What it does not control:

- the goals (the user's), the postcondition, the imports, the statement;
- the model and go2lean's sources.

Where its text runs:

- **Agent rounds.** `CheckGoals` runs `lake lean` on the workspace file
  under `bwrap`: the file system read-only, the home directory hidden
  except the toolchain and go2lean's cache (both read-only), a private
  `/tmp`, no network. The library and model it imports are built before,
  outside. The inherited environment is cleared: only PATH, ELAN_HOME,
  a private HOME/TMPDIR and a fixed locale are provided. Host credentials
  from other environment variables are not passed through. Readable files
  outside the hidden home are still exposed; this is not full host-secret
  isolation.
- **Lean tools.** lean-lsp-mcp runs under the same confinement, on a
  disposable copy of the workspace's Lean project. That copy is the only
  writable directory.
- **Verdict.** `lake comparator` builds and exports the solution in its own
  `bwrap` sandbox, where the project is read-only. Nothing the agent wrote
  is ever loaded into go2lean's process.

There is no silent fallback. Without a usable bwrap, go2lean refuses to run
Lean on an agent's text unless `GO2LEAN_LEAN_SANDBOX=off`. Each certificate
then records `"sandbox": "off"`, and the judge runs comparator with
`--inadvisably-no-sandbox`.

## The verdict

go2lean writes two files and runs `lake comparator` (with `--paranoid` by
default for `prove`) in a fresh project:

- **Challenge:** go2lean's statements with `sorry`.
- **Solution:** the full file. It holds the answers' preludes and proofs.
  Goals that are not candidates get `sorry`.

A theorem is accepted only if all three hold:

1. Its type in the solution is the challenge's: the same `Expr`, not the
   same text. A prelude that redefines a name, a notation or an instance
   the statement uses changes the type, and the theorem is rejected.
2. It depends on no axiom outside `propext`, `Quot.sound` and
   `Classical.choice`. That excludes an answer's own axioms, `sorryAx`
   and native_decide's axioms.
3. Lean's kernel, and with `-paranoid` every bundled checker, accepts the
   exported environment when replayed from scratch. That catches
   `debug.skipKernelTC`, and declarations added by a metaprogram without
   the kernel.

Several goals are judged together. If one is rejected, each is judged
again in order, with the accepted ones before it, because a later proof may
use an earlier goal as a contract.

Each accepted theorem gets a certificate, stored in `results.json` in the
judge's directory and in the proof record. It holds:

- the theorem's full name;
- the toolchain;
- the hashes of the core sources, the model, the statement, the challenge
  and the solution;
- the permitted axioms;
- the judge command and the sandbox mode;
- the directory to run `lake comparator` in again.

## Evidence

- **Red-team corpus** (`testdata/redteam`, `scripts/redteam.sh`, in the
  proofs workflow, run by hand).
  Each attack claims something false of `Max2` (that it never returns, or
  returns less than both arguments). Every attack must be rejected, a true
  control goal accepted, and no file outside the sandboxes changed. The
  classes are: an own axiom, sorry under another name, native_decide with
  `implemented_by`, `extern`, an instance hijack, a notation hijack, name
  shadowing, the kernel skipped, an environment hack, `#eval` writing files
  (the checkout, the cache, the model, `/tmp`, a shell), spoofed
  `#print axioms` output, a namespace escape, a Unicode homoglyph, and
  sorry hidden after a string that holds a comment opener. Two more come as
  whole solution files, which may import Lean's metaprogramming (an answer
  cannot): an unchecked `addDecl` and a tactic that assigns an ill-typed
  proof with the kernel off. Results: `docs/eval/proof-trust.md`.
- **Differential checks**. The properties `go2lean prove` proves on the
  examples are restated in Go and run on random, boundary and fuzzed
  inputs (`examples/*/proved_test.go`). A semantic mutant of each proved
  function must fail that test and fail to prove (`scripts/mutants.sh`).
  Both run in the proofs workflow: `docs/eval/proof-trust.md`.

## Outside the boundary

These are not covered by the judge:

- **The meaning of the model.** That covers the go/ssa compiler, the
  frontend and `GoSSA.lean`. A wrong model makes a true theorem about the
  wrong program. Only the differential checks bear on it.
- **The user's postcondition.** A weak or vacuous `P` (`True`) is proved
  honestly, and means only no panic.
- **Partial correctness and the input domain**, above.
- **The Lean toolchain as installed under `~/.elan`.** No sandbox can be
  stronger than the binaries it runs.
- **The bounded `bv_decide` route** and the hand proofs in
  `lean/Proofs/Functional.lean`. The hand proofs are checked by `#print
  axioms` in the proofs workflow, not by the judge. They hold for every oracle and havoc
  function, but most do not state `assumed`.
