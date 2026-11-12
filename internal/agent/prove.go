package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/prover"
)

// ProveOptions configure the proof agent.
type ProveOptions struct {
	Rounds int           // attempts per goal, Lean's errors fed back after each (default 4)
	Budget time.Duration // Lean's time on the file, per round
	// Library is the text of the proof library and the worked examples the
	// prompt carries (WP.lean, Tactic.lean, the hand proofs).
	Library string
	// LeanTools: the LLM can check Lean code itself (lean-lsp-mcp); the
	// prompt then carries a file to check its answer in.
	LeanTools bool
}

const proveSystem = `You are the proof agent of go2lean, which translates Go functions into a
deep embedding in Lean 4 (v4.35, core and Std only: no Mathlib) and proves
functional properties of them over that model.

go2lean has stated a theorem: the contract of one Go function, in the form
wpFun prog orc hv <id> [args] (fun ret s' => s'.assumed = #[] ∧ <postcondition>) s,
for every memory oracle orc and havoc function hv giving each site a
value of its Go type (h_hv : HavocTyped prog.havocWidths hv: the values the
model does not know are any values of their type; HavocTyped.int,
HavocTyped.bool and HavocTyped.err turn a havoc read into a value of that
shape) and every state s that has taken no under-approximation
(h_assumed : s.assumed = #[]), meaning that every run
of the function on these arguments that ends returns without panicking,
takes no under-approximation, and has a result ret and final state s'
satisfying the postcondition (partial correctness). wp_simp and wp_step
keep s.assumed = #[] along the walk when nothing is assumed; carry it in
your invariants. You write its proof. The function has
loops: each needs an invariant at its header block, applied with
wpPt_loop, as in the worked examples.

Answer with exactly two fenced blocks and nothing that matters outside them:

` + "```lean prelude" + `
-- definitions and lemmas the proof uses (invariants, helper lemmas);
-- may be empty
` + "```" + `

` + "```lean proof" + `
-- the tactic block that proves the theorem (what follows ":= by")
` + "```" + `

How a proof goes (the worked examples do exactly this): apply
wpFun_of_wpPt prog_fN, walk the blocks before the loop, then apply
wpPtN_of_wpPt and wpPt_loop prog_fN <invariant> <step lemma>. In the step,
walk every block of each path from the header, one wp_step per block
(branches split with cases on the condition), until the path either
returns (prove the postcondition) or jumps back to the header (close with
wpPtN_le and the induction hypothesis, then prove the invariant again).
Missing a block on a path is the most common error: the goal then still
shows a match on the next block's instructions. The lookup equation
prog_fN is already stated; do not restate it.

Rules: the statement is fixed and not yours to write or restate. Never use
sorry, admit, axiom, native_decide, macros, notation, syntax extensions,
namespace or section commands, or anything that runs code at elaboration.
A judge checks the result apart from your text: it holds the theorem to
the statement go2lean wrote, allows no axiom but propext,
Classical.choice and Quot.sound, and replays the proof in Lean's kernel
and independent checkers; your Lean runs in a sandbox. Prefer small lemmas in the
prelude over one long proof. When Lean's errors come back, fix the cause;
the goal states in the errors show what is left to prove.`

// ProveGoals asks the LLM for proofs of the goals Lean did not prove,
// one goal at a time in order, up to Rounds attempts each with Lean's
// errors fed back. goals are updated in place with the proofs that
// worked; a goal that stays open gets its original proof back, so that
// the goals after it are checked as before.
func ProveGoals(ctx context.Context, llm LLM, ws contract.Workspace, leanDir string, report *contract.ModelReport,
	goals []prover.Goal, abis []*contract.ABI, fids []int, opts ProveOptions, out io.Writer) ([]prover.GoalResult, error) {
	if opts.Rounds <= 0 {
		opts.Rounds = 4
	}
	if opts.Budget <= 0 {
		opts.Budget = 10 * time.Minute
	}
	// One goal at a time, the goals before it as contracts (CheckGoal).
	rs := make([]prover.GoalResult, len(goals))
	check := func(i int) (*prover.File, string, error) {
		r, text, f, err := prover.CheckGoal(leanDir, report.LeanModule, report, goals, abis, fids, i, opts.Budget)
		if err == nil {
			rs[i] = r
		}
		return f, text, err
	}
	for i := range goals {
		if _, _, err := check(i); err != nil {
			return nil, err
		}
	}
	provedGoals := map[string]bool{}
	// A large model is split into parts (#45): read them all.
	var model []byte
	for _, f := range append(append([]string{}, report.LeanParts...), report.LeanFile) {
		b, err := os.ReadFile(filepath.Join(ws.Root, f))
		if err != nil {
			return nil, err
		}
		model = append(model, b...)
	}
	var err error
	logDir := filepath.Join(ws.Root, "agent", "prove", contract.PackageKey(report.Package))
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	for i := range goals {
		if rs[i].Proved {
			provedGoals[prover.TheoremName(goals[i])] = true
			continue
		}
		if b := goals[i].Base; b != "" && !provedGoals[b] {
			// A companion of a goal that stays open: most often open for
			// the same reason, so not worth the agent's rounds.
			fmt.Fprintf(out, "  %s: not attempted (%s is not proved)\n", prover.TheoremName(goals[i]), b)
			continue
		}
		orig := goals[i]
		var proved []string
		for j := 0; j < i; j++ {
			if rs[j].Proved {
				proved = append(proved, prover.TheoremName(goals[j]))
			}
		}
		feedback := ""
		for round := 1; round <= opts.Rounds; round++ {
			user := provePrompt(opts.Library, string(model), fids[i], goals[i].Func, goalStatement(goals[i], abis[i], fids[i]), proved, feedback)
			if paths := loopPaths(report, fids[i], goals[i].SpecFid, goals[i].Spec != ""); paths != "" {
				user += "\n\nThe loops, from the control-flow graph: every block on a path needs its own step (wp_step), and a path ends where marked.\n\n" + paths
			}
			if opts.LeanTools {
				if scratch, err := prover.ScratchFile(report, goals, abis, fids, i); err == nil {
					user += "\n\n# Checking your answer yourself\n\nYou have Lean tools (lean_run_code, lean_goal, lean_multi_attempt, ...) on the project the model is built in. " +
						"Before you answer, check your prelude and proof with lean_run_code on this complete file, your prelude and proof put in its two places " +
						"(the goals before yours are stated with sorry there; in the real file they are proved). Read the goal states in the errors, fix, and run again " +
						"until it reports no error but the sorry of earlier goals; then answer with the two blocks as usual. Each run takes some seconds; prefer one complete run " +
						"to many small ones.\n\n```lean\n" + scratch + "```\n"
				}
			}
			if taken := declared(goals, i); len(taken) > 0 {
				user += "\n\nNames other goals' preludes already declare in the file (choose others): " + strings.Join(taken, ", ") + "\n"
			}
			base := filepath.Join(logDir, fmt.Sprintf("%s-round-%d", prover.TheoremName(goals[i]), round))
			os.WriteFile(base+".prompt.md", []byte(user), 0o644)
			start := time.Now()
			resp, err := llm.Complete(ctx, proveSystem, user)
			if err != nil {
				return nil, fmt.Errorf("%s round %d: %w", goals[i].Func, round, err)
			}
			os.WriteFile(base+".response.md", []byte(resp), 0o644)
			turns := ""
			if t, ok := llm.(interface{ Turns() int }); ok && t.Turns() > 1 {
				turns = fmt.Sprintf(", %d turns", t.Turns())
			}
			prelude, proof, perr := parseProof(resp)
			if perr == nil {
				perr = vetProof(prelude + "\n" + proof)
			}
			if perr != nil {
				fmt.Fprintf(out, "  %s round %d (%s): %v\n", prover.TheoremName(goals[i]), round, time.Since(start).Round(time.Second), perr)
				feedback = "Your answer was not used: " + perr.Error()
				continue
			}
			goals[i].Prelude, goals[i].Proof = prelude, proof
			var f *prover.File
			var text string
			f, text, err = check(i)
			if err != nil {
				return nil, err
			}
			os.WriteFile(base+".lean.log", []byte(text), 0o644)
			if rs[i].Proved {
				fmt.Fprintf(out, "  %s round %d (%s%s): proved\n", prover.TheoremName(goals[i]), round, time.Since(start).Round(time.Second), turns)
				break
			}
			fmt.Fprintf(out, "  %s round %d (%s%s): %s\n", prover.TheoremName(goals[i]), round, time.Since(start).Round(time.Second), turns, firstLine(rs[i].Error))
			feedback = "Lean rejected it. Your text in the file, with line numbers:\n\n" +
				numbered(leanDir, f.Rel, goals[i]) + "\nLean's errors:\n\n" + prover.Errors(text, f.Rel, 12000)
			if rs[i].Error != "" && !strings.Contains(feedback, "error") {
				feedback += "\n" + rs[i].Error
			}
		}
		if !rs[i].Proved {
			goals[i] = orig
			if _, _, err = check(i); err != nil {
				return nil, err
			}
		}
		if rs[i].Proved {
			provedGoals[prover.TheoremName(goals[i])] = true
		}
	}
	return rs, nil
}

var declRe = regexp.MustCompile(`(?m)^(?:@\[[^\]]*\]\s*)?(?:private\s+|protected\s+|noncomputable\s+)*(?:def|abbrev|theorem|lemma|structure|inductive|instance)\s+([^\s:({\[]+)`)

// loopPaths are LoopPaths of the goal's function and spec.
func loopPaths(report *contract.ModelReport, fid, sid int, spec bool) string {
	var out string
	for _, f := range report.Funcs {
		if f.ID == fid || spec && f.ID == sid {
			out += LoopPaths(f)
		}
	}
	return out
}

// declared are the names the preludes of the goals other than i declare.
func declared(goals []prover.Goal, i int) []string {
	var out []string
	for j, g := range goals {
		if j == i {
			continue
		}
		for _, m := range declRe.FindAllStringSubmatch(g.Prelude, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// LoopPaths describes the loops of a function for the prompt: for each
// loop header, the paths of blocks from it until one reaches a loop header
// (its own back edge, or another loop) or a block with no successor (a
// return or a panic). Each block on a path is one wp_step in the proof.
func LoopPaths(f contract.ModelFunc) string {
	if len(f.LoopHeaders) == 0 || len(f.Succs) == 0 {
		return ""
	}
	head := map[int]bool{}
	for _, h := range f.LoopHeaders {
		head[h] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "f%d (%s): loop headers %v.\n", f.ID, f.Name, f.LoopHeaders)
	const limit = 24
	for _, h := range f.LoopHeaders {
		var paths []string
		var walk func(path []int)
		walk = func(path []int) {
			if len(paths) >= limit {
				return
			}
			last := path[len(path)-1]
			var succs []int
			if last < len(f.Succs) {
				succs = f.Succs[last]
			}
			if len(path) > 1 && head[last] || len(succs) == 0 {
				end := "returns or panics"
				switch {
				case len(path) > 1 && last == h:
					end = "back edge to this header: the invariant again"
				case len(path) > 1 && head[last]:
					end = fmt.Sprintf("reaches header %d: that loop's invariant", last)
				}
				var parts []string
				for _, x := range path {
					parts = append(parts, fmt.Sprint(x))
				}
				paths = append(paths, fmt.Sprintf("  %s  (%s)", strings.Join(parts, " → "), end))
				return
			}
			for _, s := range succs {
				if slices.Contains(path[1:], s) {
					continue
				}
				walk(append(append([]int{}, path...), s))
			}
		}
		walk([]int{h})
		fmt.Fprintf(&b, "Paths from header %d:\n%s\n", h, strings.Join(paths, "\n"))
	}
	return b.String()
}

// goalStatement is the theorem a prompt shows for goal g.
func goalStatement(g prover.Goal, abi *contract.ABI, fid int) string {
	st, err := prover.Statement(abi, fid, prover.TheoremName(g), g.Post)
	if err != nil {
		return err.Error()
	}
	return st + " := by\n  <your proof>"
}

func provePrompt(library, model string, fid int, fn, statement string, contracts []string, feedback string) string {
	var b strings.Builder
	b.WriteString("# The proof library and worked examples\n\n")
	b.WriteString(library)
	b.WriteString("\n\n# The model of the function's package\n\n```lean\n")
	b.WriteString(ModelExcerpt(model, fid))
	fmt.Fprintf(&b, "```\n\n# Your goal\n\nThe function is %s, f%d in the model (lookup equation prog_f%d : prog.funcs[%d]? = some f%d, already stated). ", fn, fid, fid, fid, fid)
	b.WriteString("In the file, `prog` is in scope, ")
	b.WriteString("with `open Go2Lean.SSA Go2Lean.GoSSA Go2Lean.GoSSA.WP` and the model's namespace. The theorem:\n\n```lean\n")
	b.WriteString(statement)
	b.WriteString("\n```\n")
	if len(contracts) > 0 {
		fmt.Fprintf(&b, "\nProved contracts you may use at calls (wpFun_mono, or wp_auto's using; their h_hv is your h_hv): %s.\n", strings.Join(contracts, ", "))
	}
	if feedback != "" {
		b.WriteString("\n# Your previous attempt\n\n")
		b.WriteString(feedback)
	}
	return b.String()
}

var (
	funcDef  = regexp.MustCompile(`^def f(\d+) : GoSSA\.Func := \{`)
	funcUses = regexp.MustCompile(`\.call (\d+) |\(\.funcRef (\d+)\)`)
)

// ModelExcerpt is the model's text with only function fid and the
// functions it reaches (calls, func values), and the program's definition
// abridged: a model linked with its dependencies can run to megabytes.
func ModelExcerpt(model string, fid int) string {
	lines := strings.Split(model, "\n")
	// Chunks start at a doc comment, the program's definition or the end
	// of the namespace; a chunk is function N's when its def follows.
	type chunk struct{ id, start, end int }
	var chunks []chunk
	for i, l := range lines {
		if i == 0 || strings.HasPrefix(l, "/-- ") || strings.HasPrefix(l, "def prog ") || strings.HasPrefix(l, "end ") {
			if n := len(chunks); n > 0 {
				chunks[n-1].end = i
			}
			chunks = append(chunks, chunk{id: -1, start: i})
		}
		if m := funcDef.FindStringSubmatch(l); m != nil {
			fmt.Sscan(m[1], &chunks[len(chunks)-1].id)
		}
	}
	chunks[len(chunks)-1].end = len(lines)
	text := func(c chunk) string { return strings.Join(lines[c.start:c.end], "\n") }
	byID := map[int]chunk{}
	for _, c := range chunks {
		if c.id >= 0 {
			byID[c.id] = c
		}
	}
	keep := map[int]bool{fid: true}
	for work := []int{fid}; len(work) > 0; {
		c, ok := byID[work[0]]
		work = work[1:]
		if !ok {
			continue
		}
		for _, m := range funcUses.FindAllStringSubmatch(text(c), -1) {
			var id int
			fmt.Sscan(m[1]+m[2], &id)
			if !keep[id] {
				keep[id] = true
				work = append(work, id)
			}
		}
	}
	var b strings.Builder
	omitted := 0
	for _, c := range chunks {
		t := text(c)
		switch {
		case c.id >= 0 && !keep[c.id]:
			omitted++
			continue
		case c.id < 0 && strings.HasPrefix(t, "def prog ") && len(t) > 2000:
			t = fmt.Sprintf("-- def prog : GoSSA.Prog := { funcs := #[f0, …, f%d], havocWidths := … } (abridged)\n", len(byID)-1)
		}
		b.WriteString(t)
		b.WriteString("\n")
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n-- (%d functions f%d does not reach are left out here; the module has them all)\n", omitted, fid)
	}
	return b.String()
}

var proofFence = regexp.MustCompile("(?s)```lean (prelude|proof)[ \t]*\n(.*?)```")

// parseProof reads the two fenced blocks of an answer.
func parseProof(resp string) (prelude, proof string, err error) {
	for _, m := range proofFence.FindAllStringSubmatch(resp, -1) {
		switch m[1] {
		case "prelude":
			prelude = strings.TrimSpace(m[2])
		case "proof":
			proof = m[2]
		}
	}
	if strings.TrimSpace(proof) == "" {
		return "", "", fmt.Errorf("no ```lean proof block in the answer")
	}
	// The tactic block goes under ":= by": indent it if it is not.
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(proof, "\n "), "\n") {
		lines = append(lines, l)
	}
	indented := true
	for _, l := range lines {
		if strings.TrimSpace(l) != "" && !strings.HasPrefix(l, " ") {
			indented = false
		}
	}
	if !indented {
		for k, l := range lines {
			if strings.TrimSpace(l) != "" {
				lines[k] = "  " + l
			}
		}
	}
	return prelude, strings.Join(lines, "\n"), nil
}

// forbidden are what an answer may not use: terms that add axioms or
// trust the compiler, anywhere; commands that change how the statement
// parses, run code at elaboration, or step out of the namespace the answer
// is written in, where a command starts. Comments are not read.
var (
	forbiddenTerm = regexp.MustCompile(`\b(sorry|admit|axiom|native_decide|ofReduceBool|implemented_by|extern|unsafe)\b|_root_`)
	forbiddenCmd  = regexp.MustCompile(`(?m)^\s*(@\[[^\]]*\]\s*)?((local|scoped|private)\s+)?(notation|infix|infixl|infixr|prefix|postfix|macro|macro_rules|syntax|elab|elab_rules|declare_syntax_cat|run_cmd|run_tac|run_elab|initialize|builtin_initialize|export|namespace|section|end|#exit)\b|(?m)^\s*open\s.*\brenaming\b`)
	lineComment   = regexp.MustCompile(`--[^\n]*`)
	blockComment  = regexp.MustCompile(`(?s)/-.*?-/`)
)

func vetProof(text string) error {
	code := lineComment.ReplaceAllString(blockComment.ReplaceAllString(text, " "), "")
	for _, re := range []*regexp.Regexp{forbiddenTerm, forbiddenCmd} {
		if m := re.FindString(code); m != "" {
			return fmt.Errorf("the answer uses %q, which is not allowed", strings.TrimSpace(m))
		}
	}
	return nil
}

// numbered is goal g's text in the file at rel (its prelude, statement and
// proof) with the file's line numbers, for reading Lean's errors against.
func numbered(leanDir, rel string, g prover.Goal) string {
	b, err := os.ReadFile(filepath.Join(leanDir, rel))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	name := "theorem " + prover.TheoremName(g) + " "
	at := -1
	for k, l := range lines {
		if strings.HasPrefix(l, name) {
			at = k
			break
		}
	}
	if at < 0 {
		return ""
	}
	start := at
	if g.Prelude != "" {
		start = max(0, at-strings.Count(strings.TrimSpace(g.Prelude), "\n")-2)
	}
	end := at
	for end < len(lines)-1 && !strings.HasSuffix(lines[end], ":= by") {
		end++
	}
	end = min(len(lines)-1, end+strings.Count(g.Proof, "\n")+1)
	var out strings.Builder
	for k := start; k <= end; k++ {
		fmt.Fprintf(&out, "%4d  %s\n", k+1, lines[k])
	}
	return out.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
