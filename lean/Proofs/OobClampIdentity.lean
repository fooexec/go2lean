import Std.Tactic.BVDecide
import Go2Lean.Search
import Go2Lean.Generated.gossa.github_com_fooexec_go2lean_examples_oob

/-! Prototype of the proof engine: a theorem over all 64-bit inputs, not a
sample. For every `v lo hi`, the model of `oob.Clamp` returns a value on
which `SpecClampIdentity` holds. The model is the deep embedding the
engines run (`run` and `runFunc` from `Go2Lean.SSA`), unfolded by `simp`
with one `rfl` lemma per op used, then split into cases. The model lives in
a workspace's lake project, so check it from there:

    go run ./cmd/go2lean compile -workspace out ./examples/oob
    cd out/lean && lake lean ../../lean/Proofs/OobClampIdentity.lean

(`lake lean`, not `lake env lean`: the core and the workspace both hold
modules under `Go2Lean`, which only lake's per-package resolution tells
apart.) It is not part of the `Go2Lean` library build: the FuncIds below are
those of the generated model and change when M1 regenerates it. -/

open Go2Lean Go2Lean.SSA Go2Lean.GoSSA
open Go2Lean.Generated.gossa.github_com_fooexec_go2lean_examples_oob

set_option maxHeartbeats 0
set_option maxRecDepth 100000

/-- Clamp and its spec read no memory and no havoc. -/
def or0 : Oracle := { mem := fun _ _ => 0#64 }
def h0 : Havoc Val := fun _ _ => .unit

-- The functions the proof needs, looked up in the model by `rfl` instead of
-- unfolding the whole program.
theorem prog_clamp : prog.funcs[0]? = some f0 := rfl
theorem prog_spec : prog.funcs[4]? = some f4 := rfl

-- The ops the two functions use, at width 64.
theorem denote_slt (or : Oracle) (h : Heap Val) (a b : BitVec 64) :
    denote or h .slt [.int 64 a, .int 64 b] = .ok (.bool (BitVec.slt a b), h) := rfl
theorem denote_sgt (or : Oracle) (h : Heap Val) (a b : BitVec 64) :
    denote or h .sgt [.int 64 a, .int 64 b] = .ok (.bool (BitVec.slt b a), h) := rfl
theorem denote_eq (or : Oracle) (h : Heap Val) (a b : BitVec 64) :
    denote or h .eq [.int 64 a, .int 64 b] = .ok (.bool (a == b), h) := rfl
theorem denote_const (or : Oracle) (h : Heap Val) (v : Val) :
    denote or h (.const v) [] = .ok (v, h) := rfl

/-- Run Clamp (FuncId 0) on the inputs, then SpecClampIdentity (FuncId 4)
on the inputs and Clamp's result; true when the spec returns true. -/
def specHolds (v lo hi : BitVec 64) : Bool :=
  match run prog or0 h0 0 [.int 64 v, .int 64 lo, .int 64 hi] 64 with
  | (.ok r, s) =>
    match runFunc prog or0 h0 false 4 [.int 64 v, .int 64 lo, .int 64 hi, r] s 64 with
    | (.ok (.bool b), _) => b
    | _ => false
  | _ => false

theorem clampIdentity (v lo hi : BitVec 64) : specHolds v lo hi = true := by
  simp [specHolds, run, runFunc, runBlock, execInstrs, enter, bindParams, prog_clamp, prog_spec,
    f0, f4, Dialect.denote, Dialect.denoteH, GoSSA.denoteH_spec, Dialect.toBool, Dialect.assumption, GoSSA.assumption, denote_slt, denote_sgt, denote_const]
  cases h1 : v.slt lo <;> cases h2 : hi.slt v <;> cases h3 : hi.slt lo <;> simp_all <;> bv_decide

#print axioms clampIdentity
