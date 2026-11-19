import Go2Lean.Dialect.GoSSA

/-! Simp lemmas for the integer fragment of the GoSSA dialect, used by the
proof engine (`internal/prover`): each op on integer operands of equal
width rewrites to its `BitVec` meaning, without unfolding `denotePure`'s
match over every op. Each lemma goes through the op's helper (`binInt`,
`divInt`, `cmpInt`) by `rfl`, then discharges the width check. -/

namespace Go2Lean.GoSSA
open Go2Lean.SSA

variable (or : Oracle) (h : Heap Val) {w : Nat} (a b : BitVec w)

@[simp] theorem dp_const (v : Val) : denotePure or h (.const v) [] = .ok v := rfl
@[simp] theorem dp_not (c : Bool) : denotePure or h .not [.bool c] = .ok (.bool !c) := rfl
@[simp] theorem dp_neg : denotePure or h .neg [.int w a] = .ok (.int w (-a)) := rfl
@[simp] theorem dp_bnot : denotePure or h .bnot [.int w a] = .ok (.int w (~~~a)) := rfl
@[simp] theorem dp_ite (c : Bool) (x y : Val) :
    denotePure or h .ite [.bool c, x, y] = .ok (if c then x else y) := rfl
@[simp] theorem dp_conv (w' : Nat) (se : Bool) :
    denotePure or h (.conv w' se) [.int w a] =
      .ok (.int w' (if se then a.signExtend w' else a.zeroExtend w')) := rfl
@[simp] theorem dp_eq_int : denotePure or h .eq [.int w a, .int w b] = .ok (.bool (decide (a = b))) := by
  show (match eqVal (.int w a) (.int w b) with | some r => SSA.Res.ok (Val.bool r) | none => .panic .modelStuck) = _
  simp [eqVal]
@[simp] theorem dp_ne_int : denotePure or h .ne [.int w a, .int w b] = .ok (.bool !(decide (a = b))) := by
  show (match eqVal (.int w a) (.int w b) with | some r => SSA.Res.ok (Val.bool !r) | none => .panic .modelStuck) = _
  simp [eqVal]
-- Tuples, structs and boxes: how multiple results and errors are built.
@[simp] theorem dp_box (tag : Nat) (v : Val) : denotePure or h (.box tag) [v] = .ok (.boxed tag v) := rfl
@[simp] theorem dp_mkTuple (vs : List Val) : denotePure or h .mkTuple vs = .ok (.tuple vs) := rfl
@[simp] theorem dp_extract (i : Nat) (vs : List Val) :
    denotePure or h (.extract i) [.tuple vs] =
      (match vs[i]? with | some v => .ok v | none => .panic .modelStuck) := rfl
@[simp] theorem dp_mkStruct (fs : List Val) : denotePure or h .mkStruct fs = .ok (.structv fs) := rfl
@[simp] theorem dp_field (i : Nat) (fs : List Val) :
    denotePure or h (.field i) [.structv fs] =
      (match fs[i]? with | some v => .ok v | none => .panic .modelStuck) := rfl
@[simp] theorem dp_isNil_err (n : Bool) : denotePure or h .isNil [.err n] = .ok (.bool !n) := rfl
@[simp] theorem dp_isNil_boxed (t : Nat) (v : Val) : denotePure or h .isNil [.boxed t v] = .ok (.bool false) := rfl
@[simp] theorem dp_isNil_ptr (r : Nat) (p : List Nat) :
    denotePure or h .isNil [.ptr r p] = .ok (.bool (r == 0)) := rfl

@[simp] theorem dp_eq_bool (x y : Bool) : denotePure or h .eq [.bool x, .bool y] = .ok (.bool (x == y)) := rfl


@[simp] theorem dp_add : denotePure or h .add [.int w a, .int w b] = .ok (.int w (a + b)) := by
  show binInt (· + ·) [.int w a, .int w b] = _; simp [binInt]

@[simp] theorem dp_sub : denotePure or h .sub [.int w a, .int w b] = .ok (.int w (a - b)) := by
  show binInt (· - ·) [.int w a, .int w b] = _; simp [binInt]

@[simp] theorem dp_mul : denotePure or h .mul [.int w a, .int w b] = .ok (.int w (a * b)) := by
  show binInt (· * ·) [.int w a, .int w b] = _; simp [binInt]

@[simp] theorem dp_and : denotePure or h .and [.int w a, .int w b] = .ok (.int w (a &&& b)) := by
  show binInt (· &&& ·) [.int w a, .int w b] = _; simp [binInt]

@[simp] theorem dp_or : denotePure or h .or [.int w a, .int w b] = .ok (.int w (a ||| b)) := by
  show binInt (· ||| ·) [.int w a, .int w b] = _; simp [binInt]

@[simp] theorem dp_xor : denotePure or h .xor [.int w a, .int w b] = .ok (.int w (a ^^^ b)) := by
  show binInt (· ^^^ ·) [.int w a, .int w b] = _; simp [binInt]

@[simp] theorem dp_andNot : denotePure or h .andNot [.int w a, .int w b] = .ok (.int w (a &&& ~~~b)) := by
  show binInt (fun a b => a &&& ~~~b) [.int w a, .int w b] = _; simp [binInt]

@[simp] theorem dp_sdiv : denotePure or h .sdiv [.int w a, .int w b] =
    if b = 0#w then .panic .divideByZero else .ok (.int w (BitVec.sdiv a b)) := by
  show divInt _ [.int w a, .int w b] = _; simp [divInt]

@[simp] theorem dp_udiv : denotePure or h .udiv [.int w a, .int w b] =
    if b = 0#w then .panic .divideByZero else .ok (.int w (BitVec.udiv a b)) := by
  show divInt _ [.int w a, .int w b] = _; simp [divInt]

@[simp] theorem dp_srem : denotePure or h .srem [.int w a, .int w b] =
    if b = 0#w then .panic .divideByZero else .ok (.int w (BitVec.srem a b)) := by
  show divInt _ [.int w a, .int w b] = _; simp [divInt]

@[simp] theorem dp_urem : denotePure or h .urem [.int w a, .int w b] =
    if b = 0#w then .panic .divideByZero else .ok (.int w (BitVec.umod a b)) := by
  show divInt _ [.int w a, .int w b] = _; simp [divInt]

@[simp] theorem dp_slt : denotePure or h .slt [.int w a, .int w b] = .ok (.bool (BitVec.slt a b)) := by
  show cmpInt _ [.int w a, .int w b] = _; simp [cmpInt]

@[simp] theorem dp_sle : denotePure or h .sle [.int w a, .int w b] = .ok (.bool (BitVec.sle a b)) := by
  show cmpInt _ [.int w a, .int w b] = _; simp [cmpInt]

@[simp] theorem dp_sgt : denotePure or h .sgt [.int w a, .int w b] = .ok (.bool (BitVec.slt b a)) := by
  show cmpInt (fun a b => BitVec.slt b a) [.int w a, .int w b] = _; simp [cmpInt]

@[simp] theorem dp_sge : denotePure or h .sge [.int w a, .int w b] = .ok (.bool (BitVec.sle b a)) := by
  show cmpInt (fun a b => BitVec.sle b a) [.int w a, .int w b] = _; simp [cmpInt]

@[simp] theorem dp_ult : denotePure or h .ult [.int w a, .int w b] = .ok (.bool (BitVec.ult a b)) := by
  show cmpInt _ [.int w a, .int w b] = _; simp [cmpInt]

@[simp] theorem dp_ule : denotePure or h .ule [.int w a, .int w b] = .ok (.bool (BitVec.ule a b)) := by
  show cmpInt _ [.int w a, .int w b] = _; simp [cmpInt]

@[simp] theorem dp_ugt : denotePure or h .ugt [.int w a, .int w b] = .ok (.bool (BitVec.ult b a)) := by
  show cmpInt (fun a b => BitVec.ult b a) [.int w a, .int w b] = _; simp [cmpInt]

@[simp] theorem dp_uge : denotePure or h .uge [.int w a, .int w b] = .ok (.bool (BitVec.ule b a)) := by
  show cmpInt (fun a b => BitVec.ule b a) [.int w a, .int w b] = _; simp [cmpInt]


-- Shifts: the count may have another width than the value (Go's `x << n` with
-- `n` of any integer type); the model's shift count is `n.toNat`, and a count at
-- or beyond the width gives 0 (or the sign fill).
@[simp] theorem dp_shl (cs : Bool) :
    denotePure or h (.shl cs) [.int w a, .int w' n] =
    if cs && n.msb then .panic .negativeShift
    else .ok (.int w (if n.toNat ≥ w then 0#w else a <<< n.toNat)) := by
  show (match shiftCount cs (.int w' n) with
        | .ok k => (.ok (.int w (if k ≥ w then 0#w else a <<< k)) : Res Val)
        | .panic p => .panic p
        | .outOfFuel => .outOfFuel) = _
  simp only [shiftCount]
  by_cases hc : cs = true ∧ n.msb = true
  · simp [hc]
  · simp [hc]

@[simp] theorem dp_shr (signed cs : Bool) :
    denotePure or h (.shr signed cs) [.int w a, .int w' n] =
    if cs && n.msb then .panic .negativeShift
    else .ok (.int w (if n.toNat ≥ w then (if signed && a.msb then -1#w else 0#w)
                      else (if signed then a.sshiftRight n.toNat else a >>> n.toNat))) := by
  show (match shiftCount cs (.int w' n) with
        | .ok k =>
          if k ≥ w then (.ok (.int w (if signed && a.msb then -1#w else 0#w)) : Res Val)
          else .ok (.int w (if signed then a.sshiftRight k else a >>> k))
        | .panic p => .panic p
        | .outOfFuel => .outOfFuel) = _
  simp only [shiftCount]
  by_cases hc : cs = true ∧ n.msb = true
  · simp [hc]
  · simp [hc]
    split <;> rfl


end Go2Lean.GoSSA
