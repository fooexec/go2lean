/-- RFC 9000 A.3 as a pure statement about integers.
    `win = 2^(8*len)`; `e = largest + 1`; `t` the truncated number. If `r` is in
    `[0, 2^62)`, congruent to `t` mod `win`, and lies in the half window around `e`
    (except where the range [0, 2^62) forbids it), then `r` is the closest such number
    to `e`, a tie going to the larger one. -/
theorem closest_of_window (win e t r : Int) (hwin : 0 < win) (heven : win % 2 = 0)
    (hr0 : 0 ≤ r) (hr1 : r < 2 ^ 62) (hrc : r % win = t)
    (hlo : e - win / 2 < r ∨ 2 ^ 62 - win ≤ r) (hhi : r ≤ e + win / 2 ∨ r < win) :
    ∀ m : Int, 0 ≤ m → m < 2 ^ 62 → m % win = t →
      ((r - e).natAbs < (m - e).natAbs ∨ ((r - e).natAbs = (m - e).natAbs ∧ m ≤ r)) := by
  intro m hm0 hm1 hmc
  have hm : win * (m / win) + m % win = m := Int.mul_ediv_add_emod m win
  have hr : win * (r / win) + r % win = r := Int.mul_ediv_add_emod r win
  -- m = r + k * win, with k = m / win - r / win
  obtain ⟨k, hk⟩ : ∃ k : Int, m = r + win * k :=
    ⟨m / win - r / win, by rw [Int.mul_sub]; omega⟩
  rcases Int.lt_trichotomy k 0 with hneg | hzero | hpos
  · have : win * k ≤ -win := by
      have : win * k ≤ win * (-1) := Int.mul_le_mul_of_nonneg_left (by omega) (by omega)
      omega
    omega
  · subst hzero; simp at hk; omega
  · have : win ≤ win * k := by
      have : win * 1 ≤ win * k := Int.mul_le_mul_of_nonneg_left (by omega) (by omega)
      omega
    omega

/-- The two model-side goals of `goals-packet-number.json` together give B.
`h1` and `h2` are the bodies of `DecodePN_congruent_in_range` and
`DecodePN_in_half_window` for the result `r` (their `∃ r, ret = .int 64 r ∧ …`),
`hpre` their common precondition: the decoder's result is the closest number to
`largest + 1` among those in `[0, 2^62)` with the truncated low bits, a tie
going to the larger. -/
theorem pn_closest (length : BitVec 8) (largest truncated r : BitVec 64)
    (hpre : 1 ≤ length.toNat ∧ length.toNat ≤ 4 ∧ 0 ≤ truncated.toInt ∧
      truncated.toInt < 2 ^ (8 * length.toNat) ∧ -1 ≤ largest.toInt ∧ largest.toInt < 2 ^ 62 - 1)
    (h1 : 0 ≤ r.toInt ∧ r.toInt < 2 ^ 62 ∧ r.toInt % 2 ^ (8 * length.toNat) = truncated.toInt)
    (h2 : (largest.toInt + 1 - 2 ^ (8 * length.toNat) / 2 < r.toInt ∨
            2 ^ 62 - 2 ^ (8 * length.toNat) ≤ r.toInt) ∧
          (r.toInt ≤ largest.toInt + 1 + 2 ^ (8 * length.toNat) / 2 ∨
            r.toInt < 2 ^ (8 * length.toNat))) :
    ∀ m : Int, 0 ≤ m → m < 2 ^ 62 → m % 2 ^ (8 * length.toNat) = truncated.toInt →
      ((r.toInt - (largest.toInt + 1)).natAbs < (m - (largest.toInt + 1)).natAbs ∨
       ((r.toInt - (largest.toInt + 1)).natAbs = (m - (largest.toInt + 1)).natAbs ∧ m ≤ r.toInt)) := by
  obtain ⟨hl1, hl4, -, -, -, -⟩ := hpre
  have hwin : (0 : Int) < 2 ^ (8 * length.toNat) := Int.pow_pos (by decide)
  have heven : (2 : Int) ^ (8 * length.toNat) % 2 = 0 := by
    obtain ⟨k, hk⟩ : ∃ k, 8 * length.toNat = k + 1 := ⟨8 * length.toNat - 1, by omega⟩
    rw [hk, Int.pow_succ]
    exact Int.mul_emod_left _ _
  exact closest_of_window (2 ^ (8 * length.toNat)) (largest.toInt + 1) truncated.toInt r.toInt
    hwin heven h1.1 h1.2.1 h1.2.2 h2.1 h2.2
