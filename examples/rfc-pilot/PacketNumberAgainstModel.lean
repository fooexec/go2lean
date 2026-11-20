import Go2Lean.Search
import Go2Lean.Generated.gossa.github_com_quic_go_quic_go_internal_protocol

open Go2Lean Go2Lean.GoSSA Go2Lean.Search
open Go2Lean.Generated.gossa.github_com_quic_go_quic_go_internal_protocol (prog)

/-- B, from RFC 9000 appendix A.3's prose: the packet number is the one closest to the
expected one (`largest + 1`) among the numbers in `[0, 2^62)` whose low `8 * len` bits are
the truncated ones; a tie goes to the larger. Computed over the candidates around the
expected number, with no use of the pseudocode. -/
def closestRep (len : Nat) (largest truncated : Int) : Int :=
  let win : Int := 2 ^ (8 * len)
  let e : Int := largest + 1
  let base : Int := e - e % win + truncated
  let cands := [base - 2 * win, base - win, base, base + win, base + 2 * win].filter
    fun m => 0 ≤ m ∧ m < 2 ^ 62
  let dist := fun (m : Int) => (m - e).natAbs
  cands.foldl (init := (-1 : Int)) fun best m =>
    if best < 0 then m
    else if dist m < dist best then m
    else if dist m == dist best && best < m then m else best

/-- The preconditions of RFC 9000 A.3 on the inputs. -/
def validInput (len : Nat) (largest truncated : Int) : Bool :=
  1 ≤ len && len ≤ 4 && 0 ≤ truncated && truncated < 2 ^ (8 * len) && -1 ≤ largest && largest < 2 ^ 62 - 1

def toInt64 (v : Val) : Option Int := match v with | .int 64 x => some x.toInt | _ => none

/-- Run the model of `DecodePacketNumber` (function 8 of the model). -/
def modelDecode (len : Nat) (largest truncated : Int) : Option Int :=
  let or := oracleOf .mixed [] 0
  let h := havocOf 0 prog.havocWidths
  let args := [Val.int 8 (BitVec.ofNat 8 len), Val.int 64 (BitVec.ofInt 64 largest), Val.int 64 (BitVec.ofInt 64 truncated)]
  match GoConc.runAny prog or h 8 args 100000 with
  | (.ok v, _) => toInt64 v
  | _ => none

def lcg (s : Nat) : Nat := (s * 6364136223846793005 + 1442695040888963407) % 2 ^ 64

def main : IO Unit := do
  IO.println s!"function 8 = {(prog.funcs[8]?.map (·.name)).getD "?"}"
  let mut tried := 0
  let mut bad : List (Nat × Int × Int × Option Int × Int) := []
  let mut seed := 12345
  for len in [1, 2, 3, 4] do
    let win : Int := 2 ^ (8 * len)
    let lars : List Int := [-1, 0, 1, win/2 - 1, win/2, win/2 + 1, win - 1, win, win + 1, 2*win - 1,
      2 ^ 32 - 1, 2 ^ 32, 2 ^ 40, 2 ^ 61, 2 ^ 62 - win, 2 ^ 62 - win / 2, 2 ^ 62 - win - 1,
      2 ^ 62 - 3, 2 ^ 62 - 2]
    let truns : List Int := [0, 1, win/2 - 1, win/2, win/2 + 1, win - 2, win - 1]
    let mut inputs : List (Int × Int) := lars.flatMap fun l => truns.map fun t => (l, t)
    -- random inputs
    for _ in List.range 3000 do
      seed := lcg seed
      let l : Int := (seed % (2 ^ 62 - 1) : Nat) - 1
      seed := lcg seed
      let t : Int := (seed % win.toNat : Nat)
      inputs := (l, t) :: inputs
    for (l, t) in inputs do
      if validInput len l t then
        tried := tried + 1
        let r := modelDecode len l t
        let b := closestRep len l t
        if r != some b then bad := (len, l, t, r, b) :: bad
  -- every truncated value of a 1-byte number against every largest in [-1, 600]
  let mut ex := 0
  let mut exBad := 0
  for l in List.range 602 do
    let largest : Int := (l : Int) - 1
    for t in List.range 256 do
      ex := ex + 1
      if modelDecode 1 largest t != some (closestRep 1 largest t) then exBad := exBad + 1
  IO.println s!"exhaustive 1-byte: {ex} inputs, {exBad} disagreements"
  IO.println s!"inputs tried: {tried}; disagreements: {bad.length}"
  for (len, l, t, r, b) in bad.take 10 do
    IO.println s!"  len={len} largest={l} truncated={t}: model {r}, B {b}"

#eval main
