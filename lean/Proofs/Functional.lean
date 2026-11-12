import Std.Tactic.BVDecide
import Go2Lean.Proof.Simp
import Go2Lean.Proof.Tactic
import Go2Lean.Generated.gossa.github_com_fooexec_go2lean_examples_proofs

/-! Functional proofs over the model of `examples/proofs` (issue #1): each
theorem holds for every input, and its proof is checked by the kernel alone
(`#print axioms` lists no `Lean.ofReduceBool`). The model is the deep
embedding the search engines run; `Go2Lean.Proof.WP` reads a weakest
precondition off it. Check from a workspace, as `OobClampIdentity.lean`:

    go run ./cmd/go2lean compile -workspace out ./examples/proofs
    cd out/lean && lake lean ../../lean/Proofs/Functional.lean

Not part of the `Go2Lean` library build: the FuncIds below are the
generated model's. -/

open Go2Lean.SSA Go2Lean.GoSSA Go2Lean.GoSSA.WP
open Go2Lean.Generated.gossa.github_com_fooexec_go2lean_examples_proofs

set_option maxHeartbeats 1000000

namespace Go2Lean.Proofs.Functional

-- Every theorem holds for every memory oracle and every havoc function
-- (the values the model does not know).
variable {orc : Oracle} {hv : Havoc Val}

/-- Partial correctness at a point of this package's model. -/
local notation "wp" => wpPt prog orc hv
local notation "wpN" => wpPtN prog orc hv

theorem prog_f0 : prog.funcs[0]? = some f0 := rfl
theorem prog_f1 : prog.funcs[1]? = some f1 := rfl
theorem prog_f2 : prog.funcs[2]? = some f2 := rfl
theorem prog_f3 : prog.funcs[3]? = some f3 := rfl
theorem prog_f4 : prog.funcs[4]? = some f4 := rfl

/-! ## `Max2`: no loop -/

theorem max2_correct (a b : BitVec 64) (s : HState Val) :
    wp ⟨3, 0⟩ (fun v _ => ∃ m, v = .int 64 m ∧ b.sle m ∧ a.sle m)
      (bindParams (Array.replicate 3 default) [0, 1] [.int 64 a, .int 64 b]) s := by
  wp_auto prog_f3 [f3]

/-! ## `Max3`: calls, through `Max2`'s contract -/

/-- `Max2`'s contract for a call: from any state, with any two integers. -/
theorem max2_spec (a b : BitVec 64) (s : HState Val) :
    wpFun prog orc hv 3 [.int 64 a, .int 64 b]
      (fun v _ => ∃ m, v = .int 64 m ∧ b.sle m ∧ a.sle m) s :=
  wpFun_of_wpPt prog_f3 (max2_correct a b s)

theorem max3_correct (a b c : BitVec 64) :
    wp ⟨4, 0⟩ (fun v _ => ∃ m, v = .int 64 m ∧ a.sle m ∧ b.sle m ∧ c.sle m)
      (bindParams (Array.replicate 5 default) [0, 1, 2] [.int 64 a, .int 64 b, .int 64 c]) {} := by
  wp_auto prog_f4 [f4] using max2_spec

/-! ## `CountUp`: one loop -/

/-- At the header (block 3): `i` (value 1) between 0 and `max n 0`, `n`
(value 0) unchanged, and no under-approximation taken (`s.assumed`: the
state carries it, so the invariant does). -/
def countInv (n : BitVec 64) (env : Env Val) (s : HState Val) : Prop :=
  env.size = 6 ∧ env[0]? = some (.int 64 n) ∧ s.assumed = #[] ∧
  ∃ i : BitVec 64, env[1]? = some (.int 64 i) ∧ 0 ≤ i.toInt ∧ i.toInt ≤ max n.toInt 0

/-- Every run that ends returns `max n 0` and takes no under-approximation
(the form `go2lean prove` states: `s'.assumed = #[] ∧ post`). -/
theorem countUp_correct (n : BitVec 64) :
    wp ⟨0, 0⟩ (fun v s' => s'.assumed = #[] ∧ v = .int 64 (if n.toInt < 0 then 0 else n))
      (bindParams (Array.replicate 6 default) [0] [.int 64 n]) {} := by
  apply wpPt_of_block prog_f0 rfl
  intro k
  wp_simp [f0]
  apply wpPtN_of_wpPt
  apply wpPt_loop prog_f0 (countInv n)
  · intro m env s hI ih
    obtain ⟨hsz, h0, ha, i, h1, hlo, hhi⟩ := hI
    obtain ⟨_, e0⟩ := Array.getElem?_eq_some_iff.mp h0
    obtain ⟨_, e1⟩ := Array.getElem?_eq_some_iff.mp h1
    apply wpPtN_of_block prog_f0 rfl
    wp_simp [f0]
    simp [e0, e1, hsz]
    cases hc : i.slt n
    · -- exit: block 2 returns i
      dsimp only
      wp_step prog_f0 [f0] as m
      simp [e1, hsz, ha]
      simp only [BitVec.slt, decide_eq_false_iff_not, Int.not_lt] at hc
      split
      · apply BitVec.eq_of_toInt_eq; simp; omega
      · apply BitVec.eq_of_toInt_eq; omega
    · -- body: block 1 adds 1 and jumps back to the header
      dsimp only
      wp_step prog_f0 [f0] as m
      simp [e1, hsz]
      apply wpPtN_le (Nat.le_succ m)
      apply ih
      simp only [BitVec.slt, decide_eq_true_eq] at hc
      have hn : n.toInt < 2 ^ 63 := by have := @BitVec.toInt_lt 64 n; simpa using this
      refine ⟨by simp [hsz], by simp [hsz]; exact e0, ha, i + 1, by simp [hsz], ?_, ?_⟩
      · rw [BitVec.toInt_add_of_not_saddOverflow] <;> simp [BitVec.saddOverflow] <;> omega
      · rw [BitVec.toInt_add_of_not_saddOverflow] <;> simp [BitVec.saddOverflow] <;> omega
  · refine ⟨by simp, by simp, rfl, 0, by simp, by simp, Int.le_max_right _ _⟩

/-! ## `Max`: a loop reading a slice -/

/-- At the header (block 3: `m` is value 2, `i` value 3): every element
before `i` is at most `m`; the slice and the heap object under it are
unchanged. -/
def maxInv (xs : List (BitVec 64)) (len : BitVec 64) (env : Env Val) (s : HState Val) : Prop :=
  env.size = 22 ∧ env[0]? = some (.slice 1 0 len len) ∧
  s.heap[1]? = some (.arr (xs.map (.int 64))) ∧
  ∃ m i : BitVec 64, env[2]? = some (.int 64 m) ∧ env[3]? = some (.int 64 i) ∧
    1 ≤ i.toInt ∧ i.toInt ≤ max len.toInt 1 ∧
    ∀ j (hj : j < xs.length), (j : Int) < i.toInt → xs[j].toInt ≤ m.toInt

abbrev maxPost (xs : List (BitVec 64)) : Val → HState Val → Prop :=
  fun v _ => ∃ m, v = .int 64 m ∧ ∀ x ∈ xs, x.toInt ≤ m.toInt

theorem max_step (xs : List (BitVec 64)) (len : BitVec 64) (hlen : len.toInt = xs.length)
    (hL : len.toInt < 2 ^ 63) :
    ∀ n env s, maxInv xs len env s →
      (∀ env' s', maxInv xs len env' s' → wpN n ⟨2, 3⟩ (maxPost xs) env' s') →
      wpN (n + 1) ⟨2, 3⟩ (maxPost xs) env s := by
  intro n env s hI ih
  obtain ⟨hsz, h0, hh, m, i, h2, h3, hi1, hi2, hall⟩ := hI
  obtain ⟨_, e0⟩ := Array.getElem?_eq_some_iff.mp h0
  obtain ⟨_, e2⟩ := Array.getElem?_eq_some_iff.mp h2
  obtain ⟨_, e3⟩ := Array.getElem?_eq_some_iff.mp h3
  apply wpPtN_of_block prog_f2 rfl
  wp_simp [f2]
  simp [e0, e3, hsz]
  cases hc : i.slt len
  · -- exit: i ≥ len, return m
    simp only [BitVec.slt, decide_eq_false_iff_not, Int.not_lt] at hc
    dsimp only
    wp_step prog_f2 [f2] as n
    simp [e2, hsz, maxPost]
    intro x hx
    obtain ⟨j, hj, rfl⟩ := List.mem_iff_getElem.mp hx
    exact hall j hj (by omega)
  · -- body: x := xs[i]; m := max m x; i := i + 1
    simp only [BitVec.slt, decide_eq_true_eq] at hc
    dsimp only
    have hiu : i.ult len = true := ult_of_toInt (by omega) hc
    have hin : (i.toNat : Int) = i.toInt := toNat_of_toInt_nonneg (by omega)
    have hix : i.toNat < xs.length := by omega
    have hload := heapLoad_arr (k := i.toNat) (by decide) hh (by simpa using hix)
    wp_step prog_f2 [f2] as n
    simp [e0, e2, e3, hsz, dp_indexAddr hiu, hload]
    have hi1' : (i + 1).toInt = i.toInt + 1 := by
      rw [BitVec.toInt_add_of_not_saddOverflow] <;> simp [BitVec.saddOverflow] <;> omega
    -- the invariant after one round, for the new maximum m'
    have next : ∀ m' : BitVec 64, m.toInt ≤ m'.toInt → xs[i.toNat].toInt ≤ m'.toInt →
        ∀ j (hj : j < xs.length), (j : Int) < (i + 1).toInt → xs[j].toInt ≤ m'.toInt := by
      intro m' hm hx j hj hji
      rw [hi1'] at hji
      by_cases hji' : (j : Int) < i.toInt
      · exact Int.le_trans (hall j hj hji') hm
      · have : j = i.toNat := by omega
        subst this; exact hx
    cases hx : m.slt xs[i.toNat]
    · -- no update: block 7 with m
      simp only [BitVec.slt, decide_eq_false_iff_not, Int.not_lt] at hx
      dsimp only
      wp_step prog_f2 [f2] as n
      simp [hsz, e3]
      apply wpPtN_le (show n ≤ n + 1 + 1 by omega) (ih _ _ _)
      refine ⟨by simp [hsz], by simp [hsz]; exact e0, hh, m, i + 1, by simp [hsz], by simp [hsz],
        by omega, by omega, next m (Int.le_refl _) hx⟩
    · -- update: block 6 loads xs[i] again, block 7 with it
      simp only [BitVec.slt, decide_eq_true_eq] at hx
      dsimp only
      wp_step prog_f2 [f2] as n
      simp [e0, e3, hsz, dp_indexAddr hiu, hload]
      wp_step prog_f2 [f2] as n
      simp [hsz, e3]
      apply wpPtN_le (show n ≤ n + 1 + 1 + 1 by omega) (ih _ _ _)
      refine ⟨by simp [hsz], by simp [hsz]; exact e0, hh, xs[i.toNat], i + 1, by simp [hsz],
        by simp [hsz], by omega, by omega, next _ (Int.le_of_lt hx) (Int.le_refl _)⟩

/-- `Max` returns a value no element of the slice exceeds, for every slice of
64-bit integers (its length below 2^63, as any Go slice). -/
theorem max_correct (xs : List (BitVec 64)) (len : BitVec 64) (hlen : len.toInt = xs.length)
    (s : HState Val) (hs : s.heap[1]? = some (.arr (xs.map (.int 64)))) :
    wp ⟨2, 0⟩ (maxPost xs) (bindParams (Array.replicate 22 default) [0] [.slice 1 0 len len]) s := by
  have hL : len.toInt < 2 ^ 63 := by have := @BitVec.toInt_lt 64 len; simpa using this
  apply wpPt_of_block prog_f2 rfl
  intro k
  wp_simp [f2]
  cases hc : (0#64).slt len
  · -- empty: m = 0, i = 1
    dsimp only
    wp_step prog_f2 [f2] as k
    apply wpPtN_of_wpPt
    apply wpPt_loop prog_f2 (maxInv xs len) (max_step xs len hlen hL)
    refine ⟨by simp, by simp, hs, 0, 1, by simp, by simp, by simp, ?_, ?_⟩
    · simp; omega
    · intro j hj hj1
      simp only [BitVec.slt, decide_eq_false_iff_not, Int.not_lt] at hc
      simp at hc; omega
  · -- non-empty: m = xs[0], i = 1
    dsimp only
    simp only [BitVec.slt, decide_eq_true_eq] at hc
    simp at hc
    have hne : 0 < xs.length := by omega
    have h0lt : (0#64).ult len = true := ult_of_toInt (by simp) (by simpa using hc)
    wp_step prog_f2 [f2] as k
    simp [dp_indexAddr h0lt, heapLoad_arr (k := 0) (by decide) hs (by simpa using hne)]
    wp_step prog_f2 [f2] as k
    apply wpPtN_of_wpPt
    apply wpPt_loop prog_f2 (maxInv xs len) (max_step xs len hlen hL)
    refine ⟨by simp, by simp, hs, xs[0], 1, by simp, by simp, by simp, ?_, ?_⟩
    · simp; omega
    · intro j hj hj1
      have : j = 0 := by simp at hj1; omega
      subst this; exact Int.le_refl _

/-! ## `InsertionSort`: nested loops writing the slice -/

/-- `ys[0..n)` ascending, as signed integers. -/
def SortedTo (ys : List (BitVec 64)) (n : Nat) : Prop :=
  ∀ a b (ha : a < ys.length) (hb : b < ys.length), a < b → b < n → ys[a].toInt ≤ ys[b].toInt

/-- The inner loop's invariant on the list, with `J` and `I` as naturals. -/
def InnerOK (ys : List (BitVec 64)) (J I : Nat) : Prop :=
  SortedTo ys J ∧
  (∀ a b (ha : a < ys.length) (hb : b < ys.length), J ≤ a → a < b → b ≤ I → ys[a].toInt ≤ ys[b].toInt) ∧
  (∀ a b (ha : a < ys.length) (hb : b < ys.length), a < J → J < b → b ≤ I → ys[a].toInt ≤ ys[b].toInt)

theorem innerOK_entry {ys : List (BitVec 64)} {I : Nat} (h : SortedTo ys I) : InnerOK ys I I := by
  refine ⟨h, ?_, ?_⟩
  · intro a b ha hb h1 h2 h3; omega
  · intro a b ha hb h1 h2 h3; omega

theorem innerOK_exit {ys : List (BitVec 64)} {J I : Nat} (_hI : I < ys.length) (hJI : J ≤ I)
    (h : InnerOK ys J I)
    (hstop : J = 0 ∨ ∃ (h1 : J - 1 < ys.length) (h2 : J < ys.length), ys[J - 1].toInt ≤ ys[J].toInt) :
    SortedTo ys (I + 1) := by
  obtain ⟨p1, p2, p3⟩ := h
  intro a b ha hb hab hbI
  by_cases hbJ : b < J
  · exact p1 a b ha hb hab hbJ
  by_cases haJ : J ≤ a
  · exact p2 a b ha hb haJ hab (by omega)
  -- a < J ≤ b
  by_cases hbJ' : J < b
  · exact p3 a b ha hb (by omega) hbJ' (by omega)
  have hb' : b = J := by omega
  subst hb'
  rcases hstop with h0 | ⟨h1, h2, hle⟩
  · omega
  · -- ys[a] ≤ ys[J-1] ≤ ys[J]
    have : ys[a].toInt ≤ ys[b - 1].toInt := by
      by_cases hae : a = b - 1
      · subst hae; exact Int.le_refl _
      · exact p1 a (b - 1) ha h1 (by omega) (by omega)
    exact Int.le_trans this hle

set_option linter.deprecated false in
theorem innerOK_swap {ys : List (BitVec 64)} {J I : Nat} (hJ : 1 ≤ J) (hJI : J ≤ I)
    (hI : I < ys.length) (h : InnerOK ys J I)
    (hgt : ys[J].toInt < ys[J - 1].toInt) :
    InnerOK ((ys.set (J - 1) ys[J]).set J ys[J - 1]) (J - 1) I := by
  obtain ⟨p1, p2, p3⟩ := h
  refine ⟨?_, ?_, ?_⟩
  · intro a b ha hb hab hbJ
    simp only [List.length_set] at ha hb
    simp only [List.getElem_set]
    rw [if_neg (by omega), if_neg (by omega), if_neg (by omega), if_neg (by omega)]
    exact p1 a b ha hb hab (by omega)
  · intro a b ha hb haJ hab hbI
    simp only [List.length_set] at ha hb
    simp only [List.getElem_set]
    by_cases ha1 : a = J - 1
    · subst ha1
      rw [if_neg (by omega), if_pos rfl]
      by_cases hb1 : b = J
      · subst hb1; rw [if_pos rfl]; exact Int.le_of_lt hgt
      · rw [if_neg (by omega), if_neg (by omega)]
        exact p2 J b (by omega) hb (Nat.le_refl _) (by omega) hbI
    · by_cases haJ' : a = J
      · subst haJ'
        rw [if_pos rfl, if_neg (by omega), if_neg (by omega)]
        exact p3 (a - 1) b (by omega) (by simpa using hb) (by omega) (by omega) hbI
      · rw [if_neg (by omega), if_neg (by omega), if_neg (by omega), if_neg (by omega)]
        exact p2 a b ha hb (by omega) hab hbI
  · intro a b ha hb haJ hbJ hbI
    simp only [List.length_set] at ha hb
    simp only [List.getElem_set]
    rw [if_neg (by omega), if_neg (by omega)]
    by_cases hb1 : b = J
    · subst hb1; rw [if_pos rfl]
      by_cases hae : a = b - 1
      · omega
      · exact p1 a (b - 1) (by simpa using ha) (by omega) (by omega) (by omega)
    · rw [if_neg (by omega), if_neg (by omega)]
      exact p3 a b ha hb (by omega) (by omega) hbI

/-- At the outer header (block 1, `i` is value 1). -/
def sortOuter (xs : List (BitVec 64)) (len : BitVec 64) (env : Env Val) (s : HState Val) : Prop :=
  env.size = 32 ∧ env[0]? = some (.slice 1 0 len len) ∧
  ∃ (ys : List (BitVec 64)) (i : BitVec 64), s.heap[1]? = some (.arr (ys.map (.int 64))) ∧
    ys.Perm xs ∧ env[1]? = some (.int 64 i) ∧ 1 ≤ i.toInt ∧ i.toInt ≤ max len.toInt 1 ∧
    SortedTo ys i.toNat

/-- At the inner header (block 4, `j` is value 2), inside round `i`. -/
def sortInner (xs : List (BitVec 64)) (len i : BitVec 64) (env : Env Val) (s : HState Val) : Prop :=
  env.size = 32 ∧ env[0]? = some (.slice 1 0 len len) ∧ env[1]? = some (.int 64 i) ∧
  1 ≤ i.toInt ∧ i.toInt < len.toInt ∧
  ∃ (ys : List (BitVec 64)) (j : BitVec 64), s.heap[1]? = some (.arr (ys.map (.int 64))) ∧
    ys.Perm xs ∧ env[2]? = some (.int 64 j) ∧ 0 ≤ j.toInt ∧ j.toInt ≤ i.toInt ∧
    InnerOK ys j.toNat i.toNat

abbrev sortPost (xs : List (BitVec 64)) : Val → HState Val → Prop :=
  fun _ s => ∃ ys : List (BitVec 64), s.heap[1]? = some (.arr (ys.map (.int 64))) ∧
    ys.Perm xs ∧ SortedTo ys ys.length


set_option maxHeartbeats 4000000 in
/-- One round of the outer loop, given the outer header for less fuel. -/
theorem sort_step (xs : List (BitVec 64)) (len : BitVec 64) (hlen : len.toInt = xs.length)
    (hL : len.toInt < 2 ^ 63) :
    ∀ n env s, sortOuter xs len env s →
      (∀ env' s', sortOuter xs len env' s' → wpN n ⟨1, 1⟩ (sortPost xs) env' s') →
      wpN (n + 1) ⟨1, 1⟩ (sortPost xs) env s := by
  intro n env s hI ih
  obtain ⟨hsz, h0, ys, i, hh, hp, h1, hi1, hi2, hsorted⟩ := hI
  obtain ⟨_, e0⟩ := Array.getElem?_eq_some_iff.mp h0
  obtain ⟨_, e1⟩ := Array.getElem?_eq_some_iff.mp h1
  have hylen : ys.length = xs.length := hp.length_eq
  apply wpPtN_of_block prog_f1 rfl
  wp_simp [f1]
  simp [e0, e1, hsz]
  cases hc : i.slt len
  · -- exit: i ≥ len, block 3 returns
    simp only [BitVec.slt, decide_eq_false_iff_not, Int.not_lt] at hc
    dsimp only
    wp_step prog_f1 [f1] as n
    refine ⟨ys, hh, hp, fun a b ha hb hab _ => hsorted a b ha hb hab ?_⟩
    have := toNat_of_toInt_nonneg (i := i) (by omega)
    omega
  · -- body: block 2 jumps to the inner header with j := i
    simp only [BitVec.slt, decide_eq_true_eq] at hc
    dsimp only
    have hin : (i.toNat : Int) = i.toInt := toNat_of_toInt_nonneg (by omega)
    -- leaving the inner loop: block 6 adds 1 to i and goes back to the outer header
    have leave : ∀ k, k ≤ n → ∀ (env' : Env Val) (s' : HState Val) (zs : List (BitVec 64)) (J : Nat),
        env'.size = 32 → env'[0]? = some (.slice 1 0 len len) → env'[1]? = some (.int 64 i) →
        s'.heap[1]? = some (.arr (zs.map (.int 64))) → zs.Perm xs → J ≤ i.toNat →
        InnerOK zs J i.toNat →
        (J = 0 ∨ ∃ (h1 : J - 1 < zs.length) (h2 : J < zs.length), zs[J - 1].toInt ≤ zs[J].toInt) →
        wpN (k + 1) ⟨1, 6⟩ (sortPost xs) env' s' := by
      intro k hk env' s' zs J ksz k0 k1 kh kp kJ hok hstop
      obtain ⟨_, g1⟩ := Array.getElem?_eq_some_iff.mp k1
      have hzlen : zs.length = xs.length := kp.length_eq
      apply wpPtN_of_block prog_f1 rfl
      wp_simp [f1]
      simp [g1, ksz]
      apply wpPtN_le hk (ih _ _ _)
      have hi1' : (i + 1#64).toInt = i.toInt + 1 := by
        rw [BitVec.toInt_add_of_not_saddOverflow] <;> simp [BitVec.saddOverflow] <;> omega
      have hi1n : (i + 1#64).toNat = i.toNat + 1 := by
        have := toNat_of_toInt_nonneg (i := i + 1#64) (by omega); omega
      refine ⟨by simp [ksz], by simp [ksz]; exact (Array.getElem?_eq_some_iff.mp k0).2, zs, i + 1#64, kh, kp, by simp [ksz],
        by omega, by omega, ?_⟩
      rw [hi1n]
      exact innerOK_exit (by omega) kJ hok hstop
    wp_step prog_f1 [f1] as n
    -- the inner loop, by induction on its fuel
    have inner : ∀ M, M ≤ n → ∀ env s, sortInner xs len i env s →
        wpN M ⟨1, 4⟩ (sortPost xs) env s := by
      intro M
      induction M using Nat.strongRecOn with
      | ind M ihm =>
      intro hMn env s hJ
      obtain ⟨jsz, j0, j1, ji1, ji2, zs, j, jh, jp, j2, jj0, jji, hok⟩ := hJ
      obtain ⟨_, f0⟩ := Array.getElem?_eq_some_iff.mp j0
      obtain ⟨_, f2⟩ := Array.getElem?_eq_some_iff.mp j2
      have hzlen : zs.length = xs.length := jp.length_eq
      have hjn : (j.toNat : Int) = j.toInt := toNat_of_toInt_nonneg (by omega)
      have hJi : j.toNat ≤ i.toNat := by omega
      wp_step prog_f1 [f1] as M1
      simp [f2, jsz]
      cases hj : (0#64).slt j
      · -- j ≤ 0: leave
        simp only [BitVec.slt, decide_eq_false_iff_not, Int.not_lt] at hj
        dsimp only
        cases M1 with
        | zero => exact wpPtN_zero prog_f1
        | succ M2 =>
          apply leave M2 (by omega) _ _ zs j.toNat (by simp [jsz]) (by simp [jsz]; exact f0)
            (by simp [jsz]; exact (Array.getElem?_eq_some_iff.mp j1).2) jh jp hJi hok
          left; simp at hj; omega
      · -- j > 0: block 7 compares a[j-1] with a[j]
        simp only [BitVec.slt, decide_eq_true_eq] at hj
        simp at hj
        dsimp only
        have hj0 : 1 ≤ j.toNat := by omega
        have hju : j.ult len = true := ult_of_toInt (by omega) (by omega)
        have hj1i : (j - 1#64).toInt = j.toInt - 1 := by
          rw [BitVec.toInt_sub_of_not_ssubOverflow] <;> simp [BitVec.ssubOverflow] <;> omega
        have hj1n : (j - 1#64).toNat = j.toNat - 1 := by
          have := toNat_of_toInt_nonneg (i := j - 1#64) (by omega); omega
        have hj1u : (j - 1#64).ult len = true := ult_of_toInt (by omega) (by omega)
        have hjx : j.toNat < zs.length := by omega
        have hj1x : j.toNat - 1 < zs.length := by omega
        have lj := heapLoad_arr (k := j.toNat) (by decide) jh (by simpa using hjx)
        have lj1 := heapLoad_arr (k := j.toNat - 1) (by decide) jh (by simpa using hj1x)
        have hk : (18446744073709551615 + j.toNat) % 18446744073709551616 = j.toNat - 1 := by
          have := j.isLt; omega
        wp_step prog_f1 [f1] as M2
        simp [jsz, f0, f2, dp_indexAddr hju, dp_indexAddr hj1u, hk, lj, lj1]
        cases hgt : zs[j.toNat].slt zs[j.toNat - 1]
        · -- a[j-1] ≤ a[j]: leave
          simp only [BitVec.slt, decide_eq_false_iff_not, Int.not_lt] at hgt
          dsimp only
          cases M2 with
          | zero => exact wpPtN_zero prog_f1
          | succ M3 =>
            apply leave M3 (by omega) _ _ zs j.toNat (by simp [jsz]) (by simp [jsz]; exact f0)
              (by simp [jsz]; exact (Array.getElem?_eq_some_iff.mp j1).2) jh jp hJi hok
            right; exact ⟨hj1x, hjx, hgt⟩
        · -- swap in block 5 and go round with j - 1
          simp only [BitVec.slt, decide_eq_true_eq] at hgt
          dsimp only
          have st1 := heapStore_arr (r := 1) (k := j.toNat - 1) (by decide) jh
            (by simpa using hj1x) (.int 64 zs[j.toNat])
          have jh1 : (s.heap.setIfInBounds 1 (.arr ((zs.map (.int 64)).set (j.toNat - 1)
              (.int 64 zs[j.toNat]))))[1]? =
              some (.arr ((zs.map (.int 64)).set (j.toNat - 1) (.int 64 zs[j.toNat]))) := by
            simp [Array.getElem?_setIfInBounds]
            exact (Array.getElem?_eq_some_iff.mp jh).1
          have st2 := heapStore_arr (r := 1) (k := j.toNat) (by decide) jh1
            (by simpa using hjx) (.int 64 zs[j.toNat - 1])
          wp_step prog_f1 [f1] as M3
          simp [jsz, f0, f2, dp_indexAddr hju, dp_indexAddr hj1u, hk, lj, lj1, st1, st2]
          apply ihm M3 (by omega) (by omega)
          refine ⟨by simp [jsz], by simp [jsz]; exact f0, by simp [jsz]; exact (Array.getElem?_eq_some_iff.mp j1).2, ji1, ji2,
            (zs.set (j.toNat - 1) zs[j.toNat]).set j.toNat zs[j.toNat - 1], j - 1#64, ?_, ?_,
            by simp [jsz], by omega, by omega, ?_⟩
          · simp [List.map_set, Array.getElem?_setIfInBounds]
            exact (Array.getElem?_eq_some_iff.mp jh).1
          · exact (List.set_set_perm hj1x hjx).trans jp
          · rw [hj1n]
            exact innerOK_swap hj0 hJi (by omega) hok hgt
    apply inner n (Nat.le_refl _)
    refine ⟨by simp [hsz], by simp [hsz]; exact e0, by simp [hsz]; exact e1, hi1, hc, ys, i, hh, hp,
      by simp [hsz, e1], by omega, by omega, innerOK_entry hsorted⟩

/-- `InsertionSort` leaves the slice sorted, and a permutation of what it
held, for every slice of 64-bit integers. -/
theorem insertionSort_correct (xs : List (BitVec 64)) (len : BitVec 64) (hlen : len.toInt = xs.length)
    (s : HState Val) (hs : s.heap[1]? = some (.arr (xs.map (.int 64)))) :
    wp ⟨1, 0⟩ (sortPost xs) (bindParams (Array.replicate 32 default) [0] [.slice 1 0 len len]) s := by
  have hL : len.toInt < 2 ^ 63 := by have := @BitVec.toInt_lt 64 len; simpa using this
  apply wpPt_of_block prog_f1 rfl
  intro k
  wp_simp [f1]
  apply wpPtN_of_wpPt
  apply wpPt_loop prog_f1 (sortOuter xs len) (sort_step xs len hlen hL)
  refine ⟨by simp, by simp, xs, 1, hs, List.Perm.refl _, by simp, by simp, ?_, ?_⟩
  · simp; omega
  · intro a b ha hb hab hb1; simp at hb1; omega

#print axioms max2_correct
#print axioms countUp_correct
#print axioms max_correct
#print axioms insertionSort_correct
#print axioms max3_correct

end Go2Lean.Proofs.Functional
