package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fooexec/go2lean/internal/agent"
	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/pipeline"
	"github.com/fooexec/go2lean/internal/prover"
	"github.com/fooexec/go2lean/internal/repo"
)

// cmdProve states the functional theorems of a package's goals and asks
// Lean to prove them (issue #22).
func cmdProve(args []string) error {
	fs := flag.NewFlagSet("prove", flag.ExitOnError)
	ws := workspaceFlag(fs)
	goalsFile := fs.String("goals", "", `JSON file of goals, in order: [{"func": "Max2", "post": "∃ m, ret = .int 64 m ∧ a.sle m"}, ...]`)
	fn := fs.String("func", "", "one goal's function (with -post)")
	post := fs.String("post", "", "one goal's postcondition, a Lean proposition over the parameters, ret and s'")
	budget := fs.Duration("budget", 10*time.Minute, "time Lean may take on the file")
	llmSpec := fs.String("llm", "", "agent for the goals left open (loops): claude, claude-code or mock:<file-or-dir>; empty: none")
	model := fs.String("model", "", "model id for the agent")
	outFile := fs.String("out", "", "write the goals with the proofs that worked (an agent's included) to this JSON file, to check again without the agent")
	rounds := fs.Int("rounds", 6, "agent attempts per goal, Lean's errors fed back after each")
	leanTools := fs.Bool("lean-tools", true, "give the claude-code agent Lean tools (lean-lsp-mcp, when on PATH) to check its answers itself")
	thinking := fs.Bool("thinking", true, "let the agent think at length (adaptive thinking; --effort high for claude-code)")
	judgeAll := fs.Bool("judge-all", false, "judge every goal, also those Lean's check in the workspace rejected (the verdict is the judge's alone; for testing the judge)")
	deps := fs.Bool("deps", true, "link the models of the dependency functions the package calls (translated from source, as run -deps does) into the model the theorems are about; off: they are unmodelled")
	paranoid := fs.Bool("paranoid", true, "the judge also replays proofs in the external checkers bundled with Lean (lean4lean, nanoda, ...), not only Lean's kernel")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("prove: exactly one package expected")
	}
	var goals []prover.Goal
	switch {
	case *goalsFile != "":
		b, err := os.ReadFile(*goalsFile)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &goals); err != nil {
			return fmt.Errorf("prove: %s: %v", *goalsFile, err)
		}
	case *fn != "" && *post != "":
		goals = []prover.Goal{{Func: *fn, Post: *post}}
	default:
		return fmt.Errorf("prove: -goals, or -func with -post, expected")
	}
	w, env, err := openWorkspace(ws)
	if err != nil {
		return err
	}
	var report *contract.ModelReport
	if *deps {
		report, err = pipeline.CompileLinked(w, env, fs.Arg(0), os.Stdout)
	} else {
		report, err = pipeline.Compile(w, env, fs.Arg(0), os.Stdout)
	}
	if err != nil {
		return err
	}
	ids := map[string]int{}
	loops := map[string][]int{}
	specFor := map[string]string{}
	var fnames []string
	for _, f := range report.Funcs {
		fnames = append(fnames, f.Name)
		if _, dup := ids[f.Name]; dup {
			continue // a linked dependency's function of the same name
		}
		ids[f.Name] = f.ID
		loops[f.Name] = f.LoopHeaders
		specFor[f.Name] = f.SpecFor
	}
	// Target ids as the compile stage wrote them: names that differ only
	// in case (Match, match) get keys of their own.
	keys := contract.FuncKeys(fnames)
	names := map[string]bool{}
	for _, g := range goals {
		n := prover.TheoremName(g)
		if names[n] {
			return fmt.Errorf("prove: two goals are named %s: give one a \"name\"", n)
		}
		names[n] = true
	}
	var abis []*contract.ABI
	var fids []int
	for i, g := range goals {
		id, ok := ids[g.Func]
		if !ok {
			return fmt.Errorf("prove: %s is not a function of %s", g.Func, report.Package)
		}
		t, err := w.Target(contract.TargetID(report.Package, keys[g.Func]))
		if err != nil {
			return fmt.Errorf("prove: %s is not a target: %v", g.Func, err)
		}
		abi, err := w.ABI(t, report.Frontend)
		if err != nil {
			return err
		}
		if g.Spec != "" {
			sid, ok := ids[g.Spec]
			if !ok || specFor[g.Spec] != g.Func {
				return fmt.Errorf("prove: %s is not a go2lean:spec of %s", g.Spec, g.Func)
			}
			if g.Post != "" {
				return fmt.Errorf("prove: %s: a goal takes a post or a spec, not both", g.Func)
			}
			post, err := prover.SpecPost(abi, report.LeanModule, sid)
			if err != nil {
				return fmt.Errorf("prove: %v", err)
			}
			goals[i].Post, goals[i].SpecFid = post, sid
			if goals[i].Proof == "" && len(loops[g.Spec]) > 0 && len(loops[g.Func]) == 0 {
				goals[i].Proof = prover.LoopProof(loops[g.Spec])
			}
		}
		if _, err := prover.Statement(abi, id, prover.TheoremName(g), goals[i].Post); err != nil {
			return fmt.Errorf("prove: %v", err)
		}
		abis = append(abis, abi)
		fids = append(fids, id)
		if goals[i].Proof == "" && len(loops[g.Func]) > 0 && len(g.Empty) == 0 {
			goals[i].Proof = prover.LoopProof(loops[g.Func])
		}
	}
	// Each goal, then its companions: the same postcondition with its slice
	// and string parameters nil or empty, which its theorem does not cover.
	var allGoals []prover.Goal
	var allABIs []*contract.ABI
	var allFids []int
	var given []bool
	for i, g := range goals {
		allGoals, allABIs, allFids, given = append(allGoals, g), append(allABIs, abis[i]), append(allFids, fids[i]), append(given, true)
		for _, c := range prover.Companions(g, abis[i]) {
			if names[prover.TheoremName(c)] {
				continue
			}
			allGoals, allABIs, allFids, given = append(allGoals, c), append(allABIs, abis[i]), append(allFids, fids[i]), append(given, false)
		}
	}
	goals, abis, fids = allGoals, allABIs, allFids
	leanDir := repo.WorkspaceLeanDir(w.Root)
	file, err := prover.FunctionalFile(leanDir, report, goals, abis, fids)
	if err != nil {
		return err
	}
	fmt.Printf("theorems: %s\n", file.Rel)
	var rs []prover.GoalResult
	if *llmSpec == "" {
		// One goal at a time, the goals before it as contracts: a package's
		// goals in one file can take longer than any budget.
		for i := range goals {
			r, _, _, err := prover.CheckGoal(leanDir, report.LeanModule, report, goals, abis, fids, i, *budget)
			if err != nil {
				return err
			}
			rs = append(rs, r)
		}
	} else {
		llm, err := agent.New(*llmSpec, agent.ClaudeConfig{Model: *model, Thinking: *thinking})
		if err != nil {
			return err
		}
		lib, err := proofLibrary(env.Root, report.Package)
		if err != nil {
			return err
		}
		tools := false
		if _, isCC := llm.(*agent.ClaudeCode); *leanTools && isCC {
			if bin, err := exec.LookPath("lean-lsp-mcp"); err != nil {
				fmt.Println("prove: lean-lsp-mcp is not on PATH: the agent works without Lean tools")
			} else {
				if err := os.MkdirAll(filepath.Join(w.Root, "agent"), 0o755); err != nil {
					return err
				}
				// The tools get a copy of the project with the model built.
				if err := prover.BuildTrusted(leanDir, report.LeanModule); err != nil {
					return err
				}
				if llm, err = agent.WithLeanTools(llm, bin, leanDir, filepath.Join(w.Root, "agent", "lean-mcp.json")); err != nil {
					return err
				}
				tools = true
			}
		}
		rs, err = agent.ProveGoals(context.Background(), llm, w, leanDir, report, goals, abis, fids,
			agent.ProveOptions{Rounds: *rounds, Budget: *budget, Library: lib, LeanTools: tools}, os.Stdout)
		if err != nil {
			return err
		}
	}
	// The verdict: Lean's check above ran on the agent's text in the
	// workspace; the judge holds each theorem it accepted to go2lean's
	// statement in a project of its own (prover.Judge).
	var cands []int
	for i, r := range rs {
		if (r.Proved || *judgeAll) && !strings.HasPrefix(goals[i].Proof, "  sorry") {
			cands = append(cands, i)
		}
	}
	judgeDir := filepath.Join(w.Root, "judge", fmt.Sprintf("%s-%d", contract.PackageKey(report.Package), time.Now().UnixNano()))
	certs, err := prover.Judge(prover.JudgeInput{Root: env.Root, WSLean: leanDir, Dir: judgeDir, Report: report,
		Goals: goals, ABIs: abis, Fids: fids, Candidates: cands, Budget: *budget, Paranoid: *paranoid, Log: os.Stdout})
	if err != nil {
		return err
	}
	for _, i := range cands {
		switch {
		case certs[i] != nil:
			rs[i].Proved, rs[i].Error, rs[i].Certificate = true, "", certs[i]
		case rs[i].Proved:
			rs[i].Proved = false
			rs[i].Error = "the judge rejected it (Lean accepted it in the workspace): see " + filepath.Join(judgeDir, "judge.log")
		default:
			rs[i].Error += " (the judge rejected it too)"
		}
	}
	if len(cands) > 0 {
		b, _ := json.MarshalIndent(rs, "", "  ")
		if err := os.WriteFile(filepath.Join(judgeDir, "results.json"), append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("judge: %s (results.json: the certificates)\n", judgeDir)
	}
	if *outFile != "" {
		// Only proved goals keep their proof text: an open one is written
		// as given, so that the file states what is proved.
		// Companions are not saved: go2lean adds them on every run.
		var saved []prover.Goal
		for i, g := range goals {
			if !given[i] {
				continue
			}
			sg := g
			if g.Spec != "" {
				sg.Post = ""
			}
			if !rs[i].Proved || strings.HasPrefix(g.Proof, "  sorry") {
				sg.Prelude, sg.Proof = "", ""
			}
			saved = append(saved, sg)
		}
		b, err := json.MarshalIndent(saved, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*outFile, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	failed := 0
	for i, r := range rs {
		if r.Proved {
			fmt.Printf("  proved   %s (%s), kernel only, judged\n", r.Func, r.Theorem)
		} else {
			failed++
			why := r.Error
			if h := loops[r.Func]; len(h) > 0 && strings.HasPrefix(goals[i].Proof, "  sorry") {
				why = fmt.Sprintf("loops at blocks %v need invariants", h)
			}
			fmt.Printf("  open     %s (%s): %s\n", r.Func, r.Theorem, why)
			if bl := prover.Blockers(report, fids[i]); len(bl) > 0 && goals[i].Base == "" {
				fmt.Printf("           what in the model of %s or its callees may keep it open (a run through an assumption breaks the contract; a havoc value is any value of its type):\n", r.Func)
				for _, b := range bl {
					fmt.Printf("             - %s\n", b)
				}
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("prove: %d of %d goals not proved", failed, len(rs))
	}
	return nil
}

// proofLibrary is what the proof agent reads before its goal: the
// weakest-precondition library, its tactics and lemmas, and the hand
// proofs as worked examples.
func proofLibrary(root, pkg string) (string, error) {
	var b strings.Builder
	for _, f := range []string{"lean/Go2Lean/Proof/WP.lean", "lean/Go2Lean/Proof/Tactic.lean",
		"lean/Go2Lean/Proof/Simp.lean", "lean/Proofs/Functional.lean"} {
		text, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "## %s\n\n```lean\n%s```\n\n", f, text)
	}
	// Proofs the agent wrote before (examples/*/proofs.json): the goals
	// with their preludes and proofs; not the proved package's own, whose
	// answers they would give away.
	files, _ := filepath.Glob(filepath.Join(root, "examples", "*", "proofs.json"))
	for _, f := range files {
		dir, _ := filepath.Rel(root, filepath.Dir(f))
		if strings.HasSuffix(pkg, "/"+filepath.ToSlash(dir)) {
			continue
		}
		text, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "## %s/proofs.json (goals of %s, proved)\n\n```json\n%s```\n\n", dir, dir, text)
	}
	return b.String(), nil
}
