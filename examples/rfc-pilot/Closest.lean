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
