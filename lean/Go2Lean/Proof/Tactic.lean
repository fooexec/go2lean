import Go2Lean.Proof.WP
import Lean.Elab.Tactic

/-! Tactics for proofs over `Go2Lean.Proof.WP`: the block walking that is
the same in every proof (issue #1). A proof names the function's lookup
equation (`prog.funcs[f]? = some fN`, by `rfl`) and the definitions to
unfold (the function, `fN`); the walk stays in that function, since calls go
through the callees' contracts.

- `wp_simp [fN]`: unfold the instructions and terminator of the block in
  the goal.
- `wp_step hf [fN]` (`as n`): one block, with the fuel a variable: out of
  fuel, or the block unfolded with one fuel less (named `n`).
- `wp_auto hf [fN] using c₁, …`: walk every path of a loop-free function,
  splitting on branches and applying a contract `cᵢ` (a `wpFun` theorem) at
  each call, then close what is left with `simp` and `grind`. A contract's
  postcondition of the form `∃ x, v = e x ∧ P` (or, as go2lean prove states
  it, `s.assumed = #[] ∧ ∃ x, v = e x ∧ P`) is destructured so that the
  walk goes on with `e x` for the result; a contract's hypotheses that the
  state at the call has taken no under-approximation, and that havoc
  values are typed (`h_hv`), are closed from the caller's. -/

namespace Go2Lean.GoSSA.WP

open Lean Parser.Tactic
open Go2Lean.SSA Go2Lean.GoSSA


/-- `wp_bounded tac`: run `tac` with a small recursion depth, and treat running out of it
(a runtime exception, which `first` and `try` do not catch) as an ordinary failure,
with the state restored. -/
syntax "wp_bounded " tactic : tactic
elab_rules : tactic
  | `(tactic| wp_bounded $t) => do
    let s ← Lean.Elab.Tactic.saveState
    tryCatchRuntimeEx
      (withTheReader Lean.Core.Context (fun c => { c with maxRecDepth := 3000 })
        (Lean.Elab.Tactic.evalTactic t))
      (fun _ => do s.restore; throwError "wp_bounded: out of recursion depth")

syntax "wp_simp" (" [" term,* "]")? : tactic
macro_rules
  | `(tactic| wp_simp $[[$fs,*]]?) => do
    let fs := (fs.map (·.getElems)).getD #[]
    let fs ← fs.mapM fun f => `(simpLemma| $f:term)
    `(tactic| simp [wpBlockN, wpTermN, execInstrs, enter, bindParams, $fs,*,
      Dialect.denote, Dialect.denoteH, Go2Lean.GoSSA.denoteH_spec,
      Dialect.toBool, Dialect.assumption, Go2Lean.GoSSA.assumption, denote,
      Array.getElem!_eq_getD, Array.getD_eq_getD_getElem?, Array.getElem?_setIfInBounds,
      Array.size_setIfInBounds, Array.getElem?_replicate, Array.size_replicate])

syntax "wp_step " term:max (" [" term,* "]")? (" as " ident)? : tactic
macro_rules
  | `(tactic| wp_step $hf $[[$fs,*]]? $[as $n]?) => do
    let fs := (fs.map (·.getElems)).getD #[]
    let n ← match n with
      | some n => pure n
      | none => `(n)
    `(tactic| (apply wpPtN_step $hf rfl; intro $n:ident hn; subst hn; wp_simp [$fs,*]))

syntax "wp_auto " term:max (" [" term,* "]")? (" using " term,+)? : tactic
macro_rules
  | `(tactic| wp_auto $hf $[[$fs,*]]? $[using $cs,*]?) => do
    let fs := (fs.map (·.getElems)).getD #[]
    let cs := (cs.map (·.getElems)).getD #[]
    let contract ← cs.foldlM (fun acc c => `(tactic| first | apply $c | ($acc:tactic)))
      (← `(tactic| fail "wp_auto: no contract for this call"))
    `(tactic| (
      (try (apply wpPt_of_N; intro _))
      -- `wp_step`'s `apply` first `whnf`s the goal: that evaluates a `match` on
      -- concrete values (what makes a goal with literal inputs go on), but on
      -- `BitVec.slt c (K - h)` with a large literal `K` it recurses on `K` one
      -- `succ` at a time (#291). A bounded depth lets the first go through and
      -- makes the second fail at once, so that `split` takes the `match`.
      repeat' (first
        | (wp_bounded (wp_step $hf [$fs,*]))
        | split
        | (apply wpFun_mono
           rotate_left
           ($contract:tactic)
           all_goals first
             | assumption
             | (intro _ _ hq
                first
                  | (rcases hq with ⟨_, _, hv, _⟩; subst hv)
                  | (rcases hq with ⟨_, hv, _⟩; subst hv)
                  | skip)
             | (simp [*]; done)))
      all_goals (try simp only [BitVec.sle, BitVec.slt, BitVec.ule, BitVec.ult, decide_eq_true_eq,
        decide_eq_false_iff_not, Option.some.injEq, Val.int.injEq, bigLit_eq] at *)
      all_goals (try grind)))

end Go2Lean.GoSSA.WP
