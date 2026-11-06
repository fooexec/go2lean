import Go2Lean.Dialect.GoSSA

/-! Partial correctness over the model: a weakest precondition read off the
interpreter the search engines run (`runBlock`), a rule to unfold one block,
and a cut-point rule for loops. Proofs built on it are about the same model
the search uses, and need only the kernel (issue #1, `docs/related-work.md`).

go/ssa's control-flow graph has no structured loops, so the program points
are blocks: `wpPt p or hv ⟨fid, b⟩ Q env s` says that running block `b` of
function `fid` from environment `env` and state `s`, with any fuel, either
runs out of fuel or returns a value satisfying `Q`; it never panics and
never gets stuck. A loop is proved with an invariant at its header
(`wpPt_loop`): the fuel-bounded `wpPtN` makes the induction go through. -/

namespace Go2Lean.GoSSA.WP

open Go2Lean.SSA Go2Lean.GoSSA

variable (p : GoSSA.Prog) (or : Oracle) (hv : Havoc Val)

/-- A program point: block `bid` of function `fid`. -/
structure Pt where
  fid : Nat
  bid : Nat

/-- Partial correctness from a program point, for every fuel. -/
def wpPt (pt : Pt) (Q : Val → HState Val → Prop) (env : Env Val) (s : HState Val) : Prop :=
  match p.funcs[pt.fid]? with
  | none => False
  | some fn => ∀ fuel, match runBlock p or hv false fn pt.bid env s fuel with
    | (.ok v, s') => Q v s'
    | (.panic _, _) => False
    | (.outOfFuel, _) => True

/-- Partial correctness for runs with at most `n` fuel. -/
def wpPtN (n : Nat) (pt : Pt) (Q : Val → HState Val → Prop) (env : Env Val) (s : HState Val) : Prop :=
  match p.funcs[pt.fid]? with
  | none => False
  | some fn => ∀ fuel, fuel ≤ n → match runBlock p or hv false fn pt.bid env s fuel with
    | (.ok v, s') => Q v s'
    | (.panic _, _) => False
    | (.outOfFuel, _) => True

/-- The kind of value a havoc site of width `w` holds (`Prog.havocWidths`,
written by the frontend from the Go type of the value): 0 an `error`,
1 a `bool`, 2 a `string`, 3 an assumption marker (a value never used),
and any other width an integer of that many bits. -/
def HavocKind : Nat → Val → Prop
  | 0, v => ∃ b, v = .err b
  | 1, v => ∃ b, v = .bool b
  | 2, v => ∃ r o l, v = .str r o l
  | 3, v => v = .unit
  | w, v => ∃ x : BitVec w, v = .int w x

/-- A havoc function that gives each site a value of its Go type: what Go
can return where the model does not know the value. A theorem quantified
over every `h` with `HavocTyped p.havocWidths h` covers every value of
the right type, and no value Go cannot produce there. -/
def HavocTyped (widths : Array Nat) (h : Havoc Val) : Prop :=
  ∀ site iter w, widths[site]? = some w → HavocKind w (h site iter)

theorem HavocTyped.int {widths : Array Nat} {h : Havoc Val} (ht : HavocTyped widths h)
    {site w : Nat} (hs : widths[site]? = some w) (h0 : w ≠ 0) (h1 : w ≠ 1) (h2 : w ≠ 2) (h3 : w ≠ 3)
    (iter : Nat) : ∃ x : BitVec w, h site iter = .int w x := by
  have := ht site iter w hs
  match w, h0, h1, h2, h3 with
  | w + 4, _, _, _, _ => exact this

theorem HavocTyped.bool {widths : Array Nat} {h : Havoc Val} (ht : HavocTyped widths h)
    {site : Nat} (hs : widths[site]? = some 1) (iter : Nat) : ∃ b, h site iter = .bool b :=
  ht site iter 1 hs

theorem HavocTyped.err {widths : Array Nat} {h : Havoc Val} (ht : HavocTyped widths h)
    {site : Nat} (hs : widths[site]? = some 0) (iter : Nat) : ∃ b, h site iter = .err b :=
  ht site iter 0 hs

/-- Partial correctness of a call: running function `f` on `args` from
state `s`, with any fuel, runs out of fuel or returns a value satisfying
`Q`; it never panics (a callee the model gets stuck in included: the
caller's fallback block is not followed). -/
def wpFun (f : Nat) (args : List Val) (Q : Val → HState Val → Prop) (s : HState Val) : Prop :=
  ∀ fuel, match runFunc p or hv false f args s fuel with
    | (.ok v, s') => Q v s'
    | (.panic _, _) => False
    | (.outOfFuel, _) => True

/-- What a block's terminator requires once its instructions ran, with the
successors under fuel bound `n`. A call requires the callee's partial
correctness with the rest of the block's continuation as postcondition: a
contract proved for the callee (`wpFun_of_wpPt`) discharges it. -/
def wpTermN (n : Nat) (fid : Nat) (fn : GoSSA.Func) (b : Nat) (t : Term)
    (Q : Val → HState Val → Prop) (env : Env Val) (s : HState Val) : Prop :=
  match t with
  | .ret v => Q env[v]! { s with exit := some (fn.name, b) }
  | .call f args dst next nargs _ =>
    wpFun p or hv f (args.map (env[·]!)) (fun v s' =>
      wpPtN p or hv n ⟨fid, next⟩ Q (enter fn next (env.set! dst v) nargs) s') s
  | .jump b' args => wpPtN p or hv n ⟨fid, b'⟩ Q (enter fn b' env args) s
  | .br c t ta f fa =>
    match Dialect.toBool (Op := GoSSA.Op) env[c]! with
    | some true => wpPtN p or hv n ⟨fid, t⟩ Q (enter fn t env ta) s
    | some false => wpPtN p or hv n ⟨fid, f⟩ Q (enter fn f env fa) s
    | none => False
  | _ => False

/-- One block: its instructions, then its terminator. -/
def wpBlockN (n : Nat) (fid : Nat) (fn : GoSSA.Func) (b : Nat) (blk : GoSSA.Block)
    (Q : Val → HState Val → Prop) (env : Env Val) (s : HState Val) : Prop :=
  match execInstrs (Op := GoSSA.Op) or hv false blk.instrs.toList env s with
  | (.ok env', s') => wpTermN p or hv n fid fn b blk.term Q env' s'
  | (.panic _, _) => False
  | (.outOfFuel, _) => True

variable {p or hv}

theorem wpPt_of_N {pt : Pt} {Q : Val → HState Val → Prop} {env : Env Val} {s : HState Val}
    (h : ∀ n, wpPtN p or hv n pt Q env s) : wpPt p or hv pt Q env s := by
  unfold wpPt
  rcases hf : p.funcs[pt.fid]? with _ | fn
  · have := h 0; unfold wpPtN at this; rw [hf] at this; exact this
  · intro fuel
    have := h fuel
    unfold wpPtN at this; rw [hf] at this
    exact this fuel (Nat.le_refl _)

theorem wpPtN_of_wpPt {n : Nat} {pt : Pt} {Q : Val → HState Val → Prop} {env : Env Val}
    {s : HState Val} (h : wpPt p or hv pt Q env s) : wpPtN p or hv n pt Q env s := by
  unfold wpPt at h; unfold wpPtN
  rcases hf : p.funcs[pt.fid]? with _ | fn
  · rw [hf] at h; exact h
  · rw [hf] at h; intro fuel _; exact h fuel

theorem wpPtN_le {m n : Nat} (hmn : m ≤ n) {pt : Pt} {Q : Val → HState Val → Prop}
    {env : Env Val} {s : HState Val} (h : wpPtN p or hv n pt Q env s) : wpPtN p or hv m pt Q env s := by
  unfold wpPtN at h ⊢
  rcases hf : p.funcs[pt.fid]? with _ | fn
  · rw [hf] at h; exact h
  · rw [hf] at h; intro fuel hf'; exact h fuel (Nat.le_trans hf' hmn)

theorem wpPtN_zero {pt : Pt} {fn : GoSSA.Func} (hf : p.funcs[pt.fid]? = some fn)
    {Q : Val → HState Val → Prop} {env : Env Val} {s : HState Val} : wpPtN p or hv 0 pt Q env s := by
  unfold wpPtN; rw [hf]
  intro fuel hfuel
  rw [Nat.le_zero.mp hfuel]; simp [runBlock]

/-- Unfold one block: its instructions and terminator, with one unit of fuel
less for the successor. -/
theorem wpPtN_of_block {n fid b : Nat} {fn : GoSSA.Func} {blk : GoSSA.Block}
    (hf : p.funcs[fid]? = some fn) (hb : fn.blocks[b]? = some blk)
    {Q : Val → HState Val → Prop} {env : Env Val} {s : HState Val}
    (h : wpBlockN p or hv n fid fn b blk Q env s) : wpPtN p or hv (n + 1) ⟨fid, b⟩ Q env s := by
  unfold wpPtN; simp only [hf]
  intro fuel hfuel
  cases fuel with
  | zero => simp [runBlock]
  | succ m =>
    have hm : m ≤ n := Nat.le_of_succ_le_succ hfuel
    unfold wpBlockN at h
    simp only [runBlock, hb]
    rcases he : execInstrs (Op := GoSSA.Op) or hv false blk.instrs.toList env s with ⟨r, s'⟩
    rw [he] at h
    cases r with
    | panic k => exact h.elim
    | outOfFuel => simp
    | ok env' =>
      dsimp only at h ⊢
      unfold wpTermN at h
      cases ht : blk.term with
      | ret v => rw [ht] at h; dsimp only at h ⊢; exact h
      | jump b' args =>
        rw [ht] at h; dsimp only at h ⊢; unfold wpPtN at h; simp only [hf] at h; exact h m hm
      | br c t ta f fa =>
        rw [ht] at h
        dsimp only at h ⊢
        cases hc : Dialect.toBool (Op := GoSSA.Op) env'[c]! with
        | none => rw [hc] at h; exact h.elim
        | some bv =>
          rw [hc] at h
          cases bv <;> (dsimp only at h ⊢; unfold wpPtN at h; simp only [hf] at h; exact h m hm)
      | call f args dst next nargs fb =>
        rw [ht] at h
        dsimp only at h ⊢
        unfold wpFun at h
        have hc := h m
        rcases hr : runFunc p or hv false f (args.map (env'[·]!)) s' m with ⟨r', s''⟩
        rw [hr] at hc
        cases r' with
        | ok v =>
          dsimp only at hc ⊢
          unfold wpPtN at hc; simp only [hf] at hc
          exact hc m hm
        | panic k => exact hc.elim
        | outOfFuel => simp
      | _ => rw [ht] at h; exact h.elim

/-- One block of a walk whose fuel is a variable: out of fuel, or the block
unfolded with one fuel less (`wp_step`). -/
theorem wpPtN_step {n fid b : Nat} {fn : GoSSA.Func} {blk : GoSSA.Block}
    (hf : p.funcs[fid]? = some fn) (hb : fn.blocks[b]? = some blk)
    {Q : Val → HState Val → Prop} {env : Env Val} {s : HState Val}
    (h : ∀ m, n = m + 1 → wpBlockN p or hv m fid fn b blk Q env s) : wpPtN p or hv n ⟨fid, b⟩ Q env s := by
  cases n with
  | zero => exact wpPtN_zero hf
  | succ m => exact wpPtN_of_block hf hb (h m rfl)

/-- A callee's partial correctness from its entry block is its contract
for every call: `wpFun` on the arguments, bound to its parameters. -/
theorem wpFun_of_wpPt {f : Nat} {fn : GoSSA.Func} (hf : p.funcs[f]? = some fn)
    {args : List Val} {Q : Val → HState Val → Prop} {s : HState Val}
    (h : wpPt p or hv ⟨f, fn.entry⟩ Q (bindParams (Array.replicate fn.nvals default) fn.params args) s) :
    wpFun p or hv f args Q s := by
  intro fuel
  unfold wpPt at h; simp only [hf] at h
  cases fuel with
  | zero => simp [runFunc]
  | succ m =>
    have hm := h m
    simp only [runFunc, hf]
    exact hm

/-- A weaker postcondition. -/
theorem wpFun_mono {f : Nat} {args : List Val} {Q Q' : Val → HState Val → Prop} {s : HState Val}
    (hq : ∀ v s', Q v s' → Q' v s') (h : wpFun p or hv f args Q s) : wpFun p or hv f args Q' s := by
  intro fuel
  have := h fuel
  rcases hr : runFunc p or hv false f args s fuel with ⟨r, s'⟩
  rw [hr] at this
  cases r with
  | ok v => exact hq v s' this
  | panic k => exact this.elim
  | outOfFuel => simp

/-- Unfold one block without a fuel bound (for code before the first loop). -/
theorem wpPt_of_block {fid b : Nat} {fn : GoSSA.Func} {blk : GoSSA.Block}
    (hf : p.funcs[fid]? = some fn) (hb : fn.blocks[b]? = some blk)
    {Q : Val → HState Val → Prop} {env : Env Val} {s : HState Val}
    (h : ∀ n, wpBlockN p or hv n fid fn b blk Q env s) : wpPt p or hv ⟨fid, b⟩ Q env s := by
  apply wpPt_of_N
  intro n
  cases n with
  | zero => exact wpPtN_zero hf
  | succ n => exact wpPtN_of_block hf hb (h n)

/-- The cut-point rule: an invariant `I` at a loop header that every run from
the header re-establishes when it comes back (with less fuel), or leaves
through `Q`, holds for the whole loop. -/
theorem wpPt_loop {pt : Pt} {fn : GoSSA.Func} (hf : p.funcs[pt.fid]? = some fn)
    {Q : Val → HState Val → Prop} (I : Env Val → HState Val → Prop)
    (step : ∀ n env s, I env s → (∀ env' s', I env' s' → wpPtN p or hv n pt Q env' s') →
      wpPtN p or hv (n + 1) pt Q env s)
    {env : Env Val} {s : HState Val} (hI : I env s) : wpPt p or hv pt Q env s := by
  apply wpPt_of_N
  intro n
  induction n generalizing env s with
  | zero => exact wpPtN_zero hf
  | succ n ih => exact step n env s hI (fun env' s' h' => ih h')

/-! Rewrites for the operations the examples use that `Go2Lean.Proof.Simp`
does not cover. -/

@[simp] theorem dp_len {h : Heap Val} {r : Nat} {o l c : BitVec 64} :
    denotePure or h .len [.slice r o l c] = .ok (.int 64 l) := rfl

theorem dp_indexAddr {h : Heap Val} {r : Nat} {o l c i : BitVec 64} (hi : i.ult l = true) :
    denotePure or h .indexAddr [.slice r o l c, .int 64 i] = .ok (.ptr r [(o + i).toNat]) := by
  show (match asIndex (.int 64 i) with
    | some i => if i.ult l then Res.ok (Val.ptr r [(o + i).toNat]) else .panic .indexOutOfRange
    | none => .panic .modelStuck) = _
  simp [asIndex, hi]

theorem heapLoad_arr {h : Heap Val} {r k : Nat} {es : List Val} (hr : r ≠ 0)
    (he : h[r]? = some (.arr es)) (hk : k < es.length) :
    heapLoad h (.ptr r [k]) = .ok es[k] := by
  cases r with
  | zero => exact absurd rfl hr
  | succ r => simp [heapLoad, heapLoad.heapLoadP, normPtr, he, getPath, hk]

theorem heapStore_arr {h : Heap Val} {r k : Nat} {es : List Val} (hr : r ≠ 0)
    (he : h[r]? = some (.arr es)) (hk : k < es.length) (v : Val) :
    heapStore h (.ptr r [k]) v = .ok (h.setIfInBounds r (.arr (es.set k v))) := by
  cases r with
  | zero => exact absurd rfl hr
  | succ r => simp [heapStore, heapStoreP, normPtr, he, setPath, hk, Array.set!]

/-! Signed and unsigned views of a 64-bit index. -/

theorem toNat_of_toInt_nonneg {i : BitVec 64} (h0 : 0 ≤ i.toInt) : (i.toNat : Int) = i.toInt := by
  have hi := BitVec.toInt_eq_toNat_cond i
  have := i.isLt
  split at hi <;> omega

theorem ult_of_toInt {i l : BitVec 64} (h0 : 0 ≤ i.toInt) (h : i.toInt < l.toInt) : i.ult l = true := by
  have hi := BitVec.toInt_eq_toNat_cond i
  have hl := BitVec.toInt_eq_toNat_cond l
  have := i.isLt; have := l.isLt
  simp only [BitVec.ult, decide_eq_true_eq]
  split at hi <;> split at hl <;> omega

end Go2Lean.GoSSA.WP
