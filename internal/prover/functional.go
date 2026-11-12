package prover

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/sandbox"
)

// A Goal is a functional property of one function, for every input: every
// run that ends returns without panicking, in a state satisfying Post.
// Post is a Lean proposition over the parameters (by their Go names, a
// slice as a `List (BitVec w)` of its elements), `ret` (the result) and
// `s'` (the state the run ends in).
//
// The theorem go2lean states for it is the function's contract in
// `Go2Lean.Proof.WP` (`wpFun`), so a proved goal is also what a caller's
// proof applies at a call: goals are proved in order, each with the ones
// before it as contracts.
type Goal struct {
	Func string `json:"func"`
	// Name is the theorem's name; default the spec's name, or Func_spec.
	Name string `json:"name,omitempty"`
	Post string `json:"post,omitempty"`
	// Spec names a `go2lean:spec` function of Func to take as the
	// postcondition instead of Post: the spec, run in the model on the
	// arguments and the result from the state the run ends in, returns
	// true whenever it returns (SpecPost). The theorem is then about the
	// same property the search checks, with no translation by hand.
	Spec    string `json:"spec,omitempty"`
	SpecFid int    `json:"-"` // the spec's model id, set by the caller (SpecPost)
	// Empty names the slice and string parameters the goal takes nil or
	// empty (`.slice 0 0 0 0`, `.str 0 0 0`), which the theorem for a
	// non-nil one does not cover: the companions go2lean adds (Companions).
	Empty []string `json:"empty,omitempty"`
	// EmptyPost is the companions' postcondition, when Post speaks of
	// what a nil slice does not have (its backing array); default Post.
	EmptyPost string `json:"empty_post,omitempty"`
	// Prelude holds definitions and lemmas the proof uses (invariants),
	// written before the theorem; Proof is its tactic block (default:
	// wp_auto with the earlier goals' contracts).
	Prelude string `json:"prelude,omitempty"`
	Proof   string `json:"proof,omitempty"`
	// Base is, for a companion, the theorem name of the goal it covers the
	// nil or empty inputs of (Companions).
	Base string `json:"-"`
}

// GoalResult is what Lean made of a goal.
type GoalResult struct {
	Func    string `json:"func"`
	Theorem string `json:"theorem"`
	Proved  bool   `json:"proved"`
	Error   string `json:"error,omitempty"`
	// Certificate is the judge's record, when it accepted the theorem.
	Certificate *Certificate `json:"certificate,omitempty"`
}

// Statement is a goal's theorem, stated from the ABI: the binders, the
// hypotheses that tie a slice parameter to the heap, and the conclusion.
// name is the theorem's name and fid the function's model id.
func Statement(abi *contract.ABI, fid int, name, post string) (string, error) {
	return statement(abi, fid, name, post, "prog", nil)
}

// statement spells the program as prog: FunctionalFile names it in full,
// so that nothing a prelude defines can stand in for the model.
func statement(abi *contract.ABI, fid int, name, post, prog string, empty []string) (string, error) {
	if abi.Method != "" || abi.Ctor != nil || abi.Variadic {
		return "", fmt.Errorf("%s: methods, constructor-built receivers and variadic functions are not supported", abi.GoFunc)
	}
	if len(abi.Results) > 1 {
		return "", fmt.Errorf("%s: more than one result is not supported", abi.GoFunc)
	}
	var binders, args, lets []string
	// Values the model does not know are values of their Go type.
	hyps := []string{fmt.Sprintf("(h_hv : _root_.Go2Lean.GoSSA.WP.HavocTyped %s.havocWidths hv)", prog)}
	for _, p := range abi.Params {
		x := LeanName(p.Go)
		if x == "" || x == "_" {
			return "", fmt.Errorf("%s: parameter name %q cannot be a Lean binder here", abi.GoFunc, p.Go)
		}
		if slices.Contains(empty, p.Go) {
			// Nil or empty: the canonical value, and its names bound for the
			// postcondition.
			w, ctor := p.ElemWidth, ".slice 0 0 0 0"
			if p.Kind == contract.ABIString {
				w, ctor = 8, ".str 0 0 0"
			} else if p.Kind != contract.ABISlice {
				return "", fmt.Errorf("%s: %s is not a slice or a string, it cannot be empty", abi.GoFunc, x)
			}
			args = append(args, ctor)
			lets = append(lets, fmt.Sprintf("let %[1]s : List (BitVec %[2]d) := []; let %[1]s_len : BitVec 64 := 0; let %[1]s_ref : Nat := 0; ", x, w))
			continue
		}
		switch {
		case p.Kind == contract.ABIBool:
			binders = append(binders, fmt.Sprintf("(%s : Bool)", x))
			args = append(args, fmt.Sprintf(".bool %s", x))
		case p.Kind == contract.ABIInt && p.Width > 0:
			binders = append(binders, fmt.Sprintf("(%s : BitVec %d)", x, p.Width))
			args = append(args, fmt.Sprintf(".int %d %s", p.Width, x))
		case p.Kind == contract.ABISlice && p.ElemParam == nil && p.ElemWidth > 0:
			// A slice of integers: its elements in the heap object at x_ref,
			// from offset 0, with length and capacity x_len.
			binders = append(binders, fmt.Sprintf("(%[1]s : List (BitVec %[2]d)) (%[1]s_len : BitVec 64) (%[1]s_ref : Nat)", x, p.ElemWidth))
			hyps = append(hyps,
				fmt.Sprintf("(h_%[1]s_len : %[1]s_len.toInt = %[1]s.length)", x),
				fmt.Sprintf("(h_%[1]s_ref : %[1]s_ref ≠ 0)", x),
				fmt.Sprintf("(h_%[1]s : s.heap[%[1]s_ref]? = some (.arr (%[1]s.map (.int %[2]d))))", x, p.ElemWidth))
			args = append(args, fmt.Sprintf(".slice %[1]s_ref 0 %[1]s_len %[1]s_len", x))
		case p.Kind == contract.ABIString:
			// A string: its bytes in the heap object at x_ref, from offset 0.
			// The empty string go2lean builds is `.str 0 0 0` (Empty).
			binders = append(binders, fmt.Sprintf("(%[1]s : List (BitVec 8)) (%[1]s_len : BitVec 64) (%[1]s_ref : Nat)", x))
			hyps = append(hyps,
				fmt.Sprintf("(h_%[1]s_len : %[1]s_len.toInt = %[1]s.length)", x),
				fmt.Sprintf("(h_%[1]s_ref : %[1]s_ref ≠ 0)", x),
				fmt.Sprintf("(h_%[1]s : s.heap[%[1]s_ref]? = some (.arr (%[1]s.map (.int 8))))", x))
			args = append(args, fmt.Sprintf(".str %[1]s_ref 0 %[1]s_len", x))
		default:
			return "", fmt.Errorf("%s: parameter %s (%s) is not supported (integers, bools, strings and slices of integers are)", abi.GoFunc, x, p.GoType)
		}
	}
	binders = append(binders, "(s : HState Val)")
	hyps = append(hyps, "(h_assumed : s.assumed = #[])")
	if post == "" {
		return "", fmt.Errorf("%s: no postcondition", abi.GoFunc)
	}
	// Every oracle and every havoc function that gives each site a value of
	// its Go type: a value the model does not know is any such value. No
	// under-approximation taken (s'.assumed), from a state that had taken
	// none.
	return fmt.Sprintf("theorem %s (orc : Oracle) (hv : Havoc Val) %s\n    %s :\n    _root_.Go2Lean.GoSSA.WP.wpFun %s orc hv %d [%s]\n      (fun ret s' => s'.assumed = #[] ∧ (%s%s)) s",
		name, strings.Join(binders, " "), strings.Join(hyps, " "), prog, fid, strings.Join(args, ", "), strings.Join(lets, ""), post), nil
}

// maxEmptyParams bounds the slice and string parameters companions are
// made for: k of them take 2^k-1 companions. Past it, Uncovered says which
// inputs the theorems leave out.
const maxEmptyParams = 3

// Uncovered names the slice and string parameters whose nil or empty
// values no companion covers (more than maxEmptyParams of them).
func Uncovered(abi *contract.ABI) []string {
	var opt []string
	for _, p := range abi.Params {
		if p.Kind == contract.ABISlice || p.Kind == contract.ABIString {
			opt = append(opt, p.Go)
		}
	}
	if len(opt) > maxEmptyParams {
		return opt
	}
	return nil
}

// Companions are the goals that cover what goal g's theorem leaves out:
// for each nonempty set of its slice and string parameters (at most
// maxEmptyParams of them), the same postcondition with
// those taken nil or empty, named after g with _empty. A goal that is a
// companion has none.
func Companions(g Goal, abi *contract.ABI) []Goal {
	if len(g.Empty) > 0 {
		return nil
	}
	var opt []string
	for _, p := range abi.Params {
		if p.Kind == contract.ABISlice || p.Kind == contract.ABIString {
			opt = append(opt, p.Go)
		}
	}
	if len(opt) == 0 || len(opt) > maxEmptyParams {
		return nil
	}
	var out []Goal
	for mask := 1; mask < 1<<len(opt); mask++ {
		var empty []string
		for k, x := range opt {
			if mask&(1<<k) != 0 {
				empty = append(empty, x)
			}
		}
		c := g
		c.Name = TheoremName(g) + "_empty"
		if len(opt) > 1 {
			c.Name += "_" + strings.Join(empty, "_")
		}
		c.Empty, c.Prelude, c.Proof, c.Base = empty, "", "", TheoremName(g)
		if g.EmptyPost != "" {
			c.Post, c.Spec = g.EmptyPost, ""
		}
		out = append(out, c)
	}
	return out
}

// SpecPost is the postcondition a Go spec gives (Goal.Spec): the spec's
// run on the arguments, then the result (when the function has one), from
// the state the function's run ended in, returns true if it ends. module
// is the model's Lean module and sid the spec's model id.
func SpecPost(abi *contract.ABI, module string, sid int) (string, error) {
	st, err := statement(abi, 0, "_", "True", "prog", nil)
	if err != nil {
		return "", err
	}
	// The argument list of the statement, then ret.
	i := strings.Index(st, " 0 [")
	j := strings.Index(st, "]\n")
	args := st[i+4 : j]
	if len(abi.Results) == 1 {
		if args != "" {
			args += ", "
		}
		args += "ret"
	}
	return fmt.Sprintf("_root_.Go2Lean.GoSSA.WP.wpFun _root_.%s.prog orc hv %d [%s] (fun r s'' => r = .bool true ∧ s''.assumed = #[]) s'", module, sid, args), nil
}

// reserved are the names the statement binds itself and the Lean keywords
// a Go parameter may be called.
var reserved = map[string]bool{"s": true, "ret": true, "orc": true, "hv": true, "r": true, "prog": true,
	"at": true, "by": true, "do": true, "end": true, "fun": true, "have": true, "in": true, "let": true,
	"match": true, "show": true, "then": true, "with": true, "from": true, "where": true, "open": true}

// LeanName is a Go parameter's name in a statement: itself, or with an
// underscore after it when the statement binds the name itself (s, ret)
// or it is a Lean keyword. A postcondition uses this name.
func LeanName(goName string) string {
	if reserved[goName] {
		return goName + "_"
	}
	return goName
}

// binderNames are the statement's binders and hypotheses, in order.
func binderNames(abi *contract.ABI, empty []string) []string {
	var names []string
	hyps := []string{"h_hv"}
	for _, p := range abi.Params {
		x := LeanName(p.Go)
		if slices.Contains(empty, p.Go) {
			continue
		}
		if p.Kind == contract.ABISlice || p.Kind == contract.ABIString {
			names = append(names, x, x+"_len", x+"_ref")
			hyps = append(hyps, "h_"+x+"_len", "h_"+x+"_ref", "h_"+x)
		} else {
			names = append(names, x)
		}
	}
	return append(append(append([]string{"orc", "hv"}, names...), "s"), append(hyps, "h_assumed")...)
}

// TheoremName is the name of a goal's theorem.
func TheoremName(g Goal) string {
	switch {
	case g.Name != "":
		return ident.ReplaceAllString(g.Name, "_")
	case g.Spec != "":
		return ident.ReplaceAllString(g.Spec, "_")
	}
	return ident.ReplaceAllString(g.Func, "_") + "_spec"
}

// File is a written FunctionalFile: its path relative to the Lean project.
type File struct {
	Rel string
	// Assumed are the full names of the theorems the file states as
	// axioms (CheckGoal): a goal may depend on them.
	Assumed []string
}

// Namespace is the namespace a package's theorems are stated in.
func Namespace(report *contract.ModelReport) string {
	return "Go2Lean.Proofs.Functional." + ident.ReplaceAllString(contract.PackageKey(report.Package), "_")
}

// FunctionalFile writes the theorems of the goals, in order, into one file
// of the workspace's Lean project and returns its path relative to leanDir.
// abis are the goals' functions' ABIs and fids their model ids, whose
// lookup equations the proofs use. The file is for checking an attempt
// quickly (CheckGoals, in a sandbox); what counts as proved is the judge's
// verdict (Judge).
func FunctionalFile(leanDir string, report *contract.ModelReport, goals []Goal, abis []*contract.ABI, fids []int) (*File, error) {
	rel := filepath.Join("Proofs", "Functional_"+ident.ReplaceAllString(contract.PackageKey(report.Package), "_")+".lean")
	return FunctionalFileAt(leanDir, rel, report, goals, abis, fids)
}

// FunctionalFileAt is FunctionalFile at the path rel of the Lean project.
func FunctionalFileAt(leanDir, rel string, report *contract.ModelReport, goals []Goal, abis []*contract.ABI, fids []int) (*File, error) {
	text, err := SolutionText(report, goals, abis, fids, nil)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n")
	for _, g := range goals {
		fmt.Fprintf(&b, "#print axioms %s.%s\n", Namespace(report), TheoremName(g))
	}
	path := filepath.Join(leanDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &File{Rel: rel}, os.WriteFile(path, []byte(b.String()), 0o644)
}

// CheckGoal runs Lean, as CheckGoals does, on a file of goal i alone: the
// goals before it with their preludes, stated as axioms (contracts it may
// use, proved on their own), goal i with its prelude and proof. One goal
// at a time keeps each check small: a package's goals in one file can
// take longer than any budget. Only feedback; the judge checks every
// proof without these axioms.
func CheckGoal(leanDir, module string, report *contract.ModelReport, goals []Goal, abis []*contract.ABI, fids []int, i int, budget time.Duration) (GoalResult, string, *File, error) {
	var b strings.Builder
	assumed := map[int]bool{}
	for j := 0; j < i; j++ {
		assumed[j] = true
	}
	text, err := solutionText(report, goals[:i+1], abis[:i+1], fids[:i+1], nil, assumed)
	if err != nil {
		return GoalResult{}, "", nil, err
	}
	b.WriteString(text)
	fmt.Fprintf(&b, "\n#print axioms %s.%s\n", Namespace(report), TheoremName(goals[i]))
	rel := filepath.Join("Proofs", fmt.Sprintf("Check_%s_%d.lean", ident.ReplaceAllString(contract.PackageKey(report.Package), "_"), i))
	path := filepath.Join(leanDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return GoalResult{}, "", nil, err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return GoalResult{}, "", nil, err
	}
	f := &File{Rel: rel}
	for j := 0; j < i; j++ {
		f.Assumed = append(f.Assumed, Namespace(report)+"."+TheoremName(goals[j]))
	}
	rs, out, err := CheckGoals(leanDir, module, f, goals[i:i+1], budget)
	if err != nil {
		return GoalResult{}, out, f, err
	}
	return rs[0], out, f, nil
}

// SolutionText is the goals' theorems with their preludes and proofs, in
// order, in the package's namespace. A goal whose index is in sorried
// keeps its statement but gets `sorry` for a proof (it failed, and must
// not stop the others from building).
func SolutionText(report *contract.ModelReport, goals []Goal, abis []*contract.ABI, fids []int, sorried map[int]bool) (string, error) {
	return solutionText(report, goals, abis, fids, sorried, nil)
}

// solutionText is SolutionText with the goals in assumed stated as axioms
// (CheckGoal).
func solutionText(report *contract.ModelReport, goals []Goal, abis []*contract.ABI, fids []int, sorried, assumed map[int]bool) (string, error) {
	var b strings.Builder
	b.WriteString(fileHeader(report, goals, fids, true))
	var before []string
	for i, g := range goals {
		proof := g.Proof
		if proof == "" {
			proof = AutoProof(fids[i], before)
			if g.Spec != "" {
				// What wp_auto leaves at each return is no under-approximation
				// so far, and the spec's run.
				proof += fmt.Sprintf("\n  all_goals (refine ⟨by simp [*], ?_⟩; apply wpFun_of_wpPt prog_f%[1]d; wp_auto prog_f%[1]d [f%[1]d])", g.SpecFid)
			}
		}
		if sorried[i] {
			proof = "  sorry"
		}
		st, err := statement(abis[i], fids[i], TheoremName(g), g.Post, "_root_."+report.LeanModule+".prog", g.Empty)
		if err != nil {
			return "", err
		}
		if g.Prelude != "" && !sorried[i] {
			fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(g.Prelude))
		}
		if assumed[i] {
			fmt.Fprintf(&b, "\naxiom%s\n", strings.TrimPrefix(st, "theorem"))
		} else {
			fmt.Fprintf(&b, "\n%s := by\n%s\n", st, proof)
		}
		before = append(before, TheoremName(g))
	}
	fmt.Fprintf(&b, "\nend %s\n", Namespace(report))
	return b.String(), nil
}

// ChallengeText is what the judge holds a solution to: the statements of
// the goals at the indices in judged, each with `sorry`, generated by
// go2lean alone (no prelude, no lookup equation, nothing an agent wrote).
func ChallengeText(report *contract.ModelReport, goals []Goal, abis []*contract.ABI, fids []int, judged []int) (string, error) {
	var b strings.Builder
	b.WriteString(fileHeader(report, goals, fids, false))
	for _, i := range judged {
		st, err := statement(abis[i], fids[i], TheoremName(goals[i]), goals[i].Post, "_root_."+report.LeanModule+".prog", goals[i].Empty)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n%s := by\n  sorry\n", st)
	}
	fmt.Fprintf(&b, "\nend %s\n", Namespace(report))
	return b.String(), nil
}

// fileHeader is a file of theorems up to its theorems: the imports, the
// namespace and, with lookups, the lookup equations of the goals'
// functions and specs.
func fileHeader(report *contract.ModelReport, goals []Goal, fids []int, lookups bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `import Go2Lean.Proof.Simp
import Go2Lean.Proof.Tactic
import %[1]s

/-! Generated by go2lean prove: functional theorems of package %[2]s. Each
is the function's contract for every oracle and havoc function (partial
correctness: every run that ends returns without panicking and without an
under-approximation, in a state satisfying the postcondition). -/

open Go2Lean.SSA Go2Lean.GoSSA Go2Lean.GoSSA.WP
open %[1]s

set_option maxHeartbeats 4000000
set_option maxRecDepth 100000
set_option linter.unusedVariables false

namespace %[3]s

`, report.LeanModule, report.Package, Namespace(report))
	if !lookups {
		return b.String()
	}
	seen := map[int]bool{}
	ids := append([]int{}, fids...)
	for _, g := range goals {
		if g.Spec != "" {
			ids = append(ids, g.SpecFid)
		}
	}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			fmt.Fprintf(&b, "theorem prog_f%[1]d : prog.funcs[%[1]d]? = some f%[1]d := rfl\n", id)
		}
	}
	return b.String()
}

// ScratchFile is a file the proof agent can check its answer for goal i in
// (lean_run_code): the header, the goals before i as contracts stated
// with sorry, the place of the prelude, and goal i's statement.
func ScratchFile(report *contract.ModelReport, goals []Goal, abis []*contract.ABI, fids []int, i int) (string, error) {
	var b strings.Builder
	b.WriteString(fileHeader(report, goals, fids, true))
	for j := 0; j <= i; j++ {
		st, err := statement(abis[j], fids[j], TheoremName(goals[j]), goals[j].Post, "_root_."+report.LeanModule+".prog", goals[j].Empty)
		if err != nil {
			return "", err
		}
		if j < i {
			fmt.Fprintf(&b, "\n%s := by\n  sorry -- proved before (a contract you may use)\n", st)
			continue
		}
		fmt.Fprintf(&b, "\n-- <your prelude>\n\n%s := by\n  sorry -- <your proof>\n", st)
	}
	fmt.Fprintf(&b, "\nend %s\n", Namespace(report))
	return b.String(), nil
}

// AutoProof is the proof of a loop-free function's goal: walk it with
// wp_auto, applying the contracts of the goals before it at its calls.
func AutoProof(fid int, contracts []string) string {
	using := ""
	if len(contracts) > 0 {
		using = " using " + strings.Join(contracts, ", ")
	}
	return fmt.Sprintf("  apply wpFun_of_wpPt prog_f%[1]d\n  wp_auto prog_f%[1]d [f%[1]d]%[2]s", fid, using)
}

// LoopProof stands for the proof of a function with loops until one is
// written: the theorem is stated, and a caller's proof may use it, but
// it and everything that uses it count as not proved.
func LoopProof(heads []int) string {
	return fmt.Sprintf("  sorry -- loops at blocks %v: each needs an invariant (wpPt_loop)", heads)
}

// Blockers are what in the model of function fid, or of a function it
// calls (transitively), keeps a theorem about it from holding however the
// proof goes: the under-approximations it records (a run through one
// takes it, and the contract says no run does) and the values it does not
// model (havoc, any value of its type). The search reports which runs reach
// them; here they name why a goal may stay open.
func Blockers(report *contract.ModelReport, fid int) []string {
	byID := map[int]contract.ModelFunc{}
	for _, f := range report.Funcs {
		byID[f.ID] = f
	}
	var out []string
	seen := map[int]bool{}
	var walk func(id int)
	walk = func(id int) {
		if seen[id] {
			return
		}
		seen[id] = true
		f, ok := byID[id]
		if !ok {
			return
		}
		for _, is := range f.Issues {
			if len(out) < 12 {
				out = append(out, f.Name+": "+is)
			}
		}
		for _, c := range f.Calls {
			walk(c)
		}
	}
	walk(fid)
	return out
}

// leanMemoryMB caps the memory of Lean's check of one file, so a proof
// that grows without bound fails alone rather than through the machine's
// out-of-memory killer.
const leanMemoryMB = 6144

// CheckGoals runs Lean on a FunctionalFile and reads, per goal, whether
// its theorem was proved from Lean's standard axioms alone (no sorry, no
// axiom of the answer's own, none of native_decide's). It is quick
// feedback for the agent, not the verdict: the file holds text an agent
// wrote, so Lean runs on it in a sandbox (sandbox.Confine: no network,
// nothing writable), after the trusted modules it imports (the library
// and the model, module) are built outside; what counts as proved is the
// judge's (Judge).
func CheckGoals(leanDir, module string, f *File, goals []Goal, budget time.Duration) ([]GoalResult, string, error) {
	rel := f.Rel
	// The library and the model are built once per workspace (checks run
	// in parallel), before any file an agent wrote is read.
	if err := buildTrusted(leanDir, module); err != nil {
		return nil, "", err
	}
	abs, err := filepath.Abs(leanDir)
	if err != nil {
		return nil, "", err
	}
	prefix, err := sandbox.Confine(append(sandbox.LeanReadOnly(), abs), nil, abs)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	args := append(prefix, "lake", "lean", rel, "--", fmt.Sprintf("--memory=%d", leanMemoryMB))
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = leanDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	text := string(out)
	// A proof that runs too long or too large leaves its goals unchecked;
	// the agent hears of it, and the other goals go on.
	unchecked := func(why string) []GoalResult {
		var rs []GoalResult
		for _, g := range goals {
			rs = append(rs, GoalResult{Func: g.Func, Theorem: TheoremName(g), Error: "not checked: " + why})
		}
		return rs
	}
	if ctx.Err() != nil {
		return unchecked(fmt.Sprintf("Lean took longer than %s", budget)), text, nil
	}
	if err != nil && !errors.Is(err, context.Canceled) && !strings.Contains(text, "error") {
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && (ee.ExitCode() == 137 || ee.ExitCode() == -1) {
			return unchecked("Lean was killed (out of memory)"), text, nil
		}
		return nil, text, fmt.Errorf("lean: %v: %s", err, firstError(text))
	}
	var rs []GoalResult
	for _, g := range goals {
		name := TheoremName(g)
		r := GoalResult{Func: g.Func, Theorem: name}
		ax := axiomsOf(text, name)
		switch {
		case ax == "":
			r.Error = "not checked: " + firstError(text)
		case strings.Contains(ax, "sorryAx") && !strings.Contains(text, "error"):
			r.Error = "not proved: it uses sorry"
		case strings.Contains(ax, "sorryAx"):
			r.Error = "not proved: " + firstError(text)
		case strings.Contains(text, "error") && dependsOnItself(ax, name):
			// Lean keeps a theorem whose proof failed to elaborate as an
			// axiom of its own.
			r.Error = "not proved: " + firstError(text)
		case extraAxioms(ax, f.Assumed...) != "":
			r.Error = "proved, but from axioms other than Lean's standard ones: " + extraAxioms(ax, f.Assumed...)
		default:
			r.Proved = true
		}
		rs = append(rs, r)
	}
	return rs, text, nil
}

// dependsOnItself reports whether what axiomsOf read lists the theorem
// name itself.
func dependsOnItself(ax, name string) bool {
	i, j := strings.Index(ax, "["), strings.LastIndex(ax, "]")
	if i < 0 || j < i {
		return false
	}
	for _, a := range strings.Split(ax[i+1:j], ",") {
		if a = strings.TrimSpace(a); a == name || strings.HasSuffix(a, "."+name) {
			return true
		}
	}
	return false
}

// extraAxioms are the axioms in what axiomsOf read that are neither
// PermittedAxioms nor assumed (an answer's own, native_decide's), or "".
func extraAxioms(ax string, assumed ...string) string {
	if ax == "none" {
		return ""
	}
	i, j := strings.Index(ax, "["), strings.LastIndex(ax, "]")
	if i < 0 || j < i {
		return ax
	}
	var extra []string
	for _, a := range strings.Split(ax[i+1:j], ",") {
		if a = strings.TrimSpace(a); a != "" && !slices.Contains(PermittedAxioms, a) && !slices.Contains(assumed, a) {
			extra = append(extra, a)
		}
	}
	return strings.Join(extra, ", ")
}

// Errors are Lean's error messages on the file at rel, each with the lines
// that follow it up to the next message, at most limit bytes in all.
func Errors(text, rel string, limit int) string {
	var b strings.Builder
	in := false
	for _, l := range strings.Split(text, "\n") {
		head := strings.Contains(l, ".lean:") && (strings.Contains(l, ": error") || strings.Contains(l, ": warning") || strings.Contains(l, ": info"))
		if head {
			in = strings.Contains(l, ": error") && strings.Contains(l, filepath.Base(rel))
		}
		if strings.HasPrefix(l, "'") && strings.Contains(l, "axioms") {
			in = false
		}
		if in {
			if b.Len()+len(l) > limit {
				b.WriteString("[...]\n")
				break
			}
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// errorLines are the lines of the file at rel that Lean reported errors on.
func errorLines(text, rel string) []int {
	var out []int
	base := filepath.Base(rel)
	for _, l := range strings.Split(text, "\n") {
		i := strings.Index(l, base+":")
		if i < 0 || !strings.Contains(l, ": error") {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(l[i+len(base)+1:], "%d:", &n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// axiomsOf is what `#print axioms` printed for the theorem (the list may
// wrap over several lines): "none", the list, or "" when it did not run.
// The commands come last in the file: what a prelude printed before them
// to look like their output is not read (it is still only feedback; the
// judge does not read Lean's output at all).
func axiomsOf(s, name string) string {
	i := strings.LastIndex(s, "."+name+"' depends on axioms")
	if i < 0 {
		if strings.Contains(s, "."+name+"' does not depend on any axioms") {
			return "none"
		}
		return ""
	}
	rest := s[i:]
	if j := strings.Index(rest, "]"); j >= 0 {
		rest = rest[:j+1]
	}
	return strings.Join(strings.Fields(rest), " ")
}
