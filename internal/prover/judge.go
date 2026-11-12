package prover

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/repo"
	"github.com/fooexec/go2lean/internal/sandbox"
)

// PermittedAxioms are the axioms a proved theorem may depend on: Lean's
// standard ones. Lean.ofReduceBool (native_decide), sorryAx and any axiom
// a solution declares are not among them.
var PermittedAxioms = []string{"propext", "Quot.sound", "Classical.choice"}

// judgeCommand is how the judge checks a solution: Lean's comparator,
// which replays the solution's exported environment in Lean's kernel, and
// with paranoid in every external checker bundled with Lean too.
func judgeCommand(paranoid bool) []string {
	if paranoid {
		return []string{"lake", "comparator", "--paranoid"}
	}
	return []string{"lake", "comparator"}
}

// A Certificate is the judge's record of one accepted theorem.
type Certificate = contract.ProofCertificate

// JudgeInput is what the judge needs.
type JudgeInput struct {
	Root   string // the go2lean checkout: the core sources are built from it
	WSLean string // the workspace's Lean project: the model file is read from it
	Dir    string // a fresh directory for the judge's project
	Report *contract.ModelReport
	Goals  []Goal
	ABIs   []*contract.ABI
	Fids   []int
	// Candidates are the goals to judge (indices into Goals), in order;
	// the others' proofs are replaced by sorry.
	Candidates []int
	Budget     time.Duration
	// Paranoid also runs the external checkers (lean4lean, nanoda, ...).
	Paranoid bool
	Log      io.Writer
}

// Judge decides which candidate goals are proved, by Lean's comparator: a
// challenge holds go2lean's statements alone (no prelude, nothing an agent
// wrote), the solution the full file with the agents' preludes and
// proofs, and `lake comparator --paranoid` accepts a theorem only if the
// solution's has the very type the challenge's has, depends on no axiom
// outside PermittedAxioms, and is accepted by Lean's kernel (and, with
// Paranoid, the external checkers bundled with Lean) replaying the
// solution's exported environment. The solution is built and exported in comparator's own
// sandbox. Nothing the agent wrote is ever loaded into this process or
// built outside a sandbox: the project is made from the checkout's core
// sources and the model file whose hash the compile stage recorded, and
// the trusted modules are built before any solution is in it.
//
// It returns a certificate per accepted candidate, by index.
func Judge(in JudgeInput) (map[int]*Certificate, error) {
	if in.Log == nil {
		in.Log = io.Discard
	}
	if len(in.Candidates) == 0 {
		return map[int]*Certificate{}, nil
	}
	// Diagnose host capability before expensive trusted builds; never treat a
	// missing comparator sandbox feature as a rejected mathematical proof.
	if !sandbox.Unconfined() {
		if err := sandbox.CheckComparator(); err != nil {
			return nil, err
		}
	}
	cert, err := judgeProject(in)
	if err != nil {
		return nil, err
	}
	// All at once; on a rejection, one at a time, each with the accepted
	// ones before it (a later goal's proof may use them as contracts).
	ok, err := judgeRun(in, in.Candidates, in.Candidates, cert)
	if err != nil {
		return nil, err
	}
	if !ok {
		fmt.Fprintf(in.Log, "judge: not all %d candidates accepted, judging one at a time\n", len(in.Candidates))
		var accepted []int
		for _, i := range in.Candidates {
			ok, err := judgeRun(in, append(append([]int{}, accepted...), i), []int{i}, cert)
			if err != nil {
				return nil, err
			}
			if ok {
				accepted = append(accepted, i)
			}
		}
		in.Candidates = accepted
		if len(accepted) > 0 {
			// The files left in Dir are the ones the certificates name.
			if ok, err := judgeRun(in, accepted, accepted, cert); err != nil || !ok {
				return nil, fmt.Errorf("judge: the accepted goals were rejected together (%v)", err)
			}
		}
	}
	out := map[int]*Certificate{}
	sol, _ := os.ReadFile(filepath.Join(in.Dir, "Solution.lean"))
	chal, _ := os.ReadFile(filepath.Join(in.Dir, "Challenge.lean"))
	for _, i := range in.Candidates {
		c := *cert
		c.Theorem = Namespace(in.Report) + "." + TheoremName(in.Goals[i])
		c.Func = in.Goals[i].Func
		st, _ := statement(in.ABIs[i], in.Fids[i], TheoremName(in.Goals[i]), in.Goals[i].Post, "_root_."+in.Report.LeanModule+".prog", in.Goals[i].Empty)
		c.StatementSHA256 = hash([]byte(st))
		c.ChallengeSHA256 = hash(chal)
		c.SolutionSHA256 = hash(sol)
		out[i] = &c
	}
	return out, nil
}

// judgeProject makes the judge's lake project in in.Dir and builds its
// trusted modules: the core library from the checkout's sources, and the
// model, copied only if its hash is the one the compile stage recorded.
func judgeProject(in JudgeInput) (*Certificate, error) {
	if in.Report.LeanSHA256 == "" {
		return nil, fmt.Errorf("judge: the model report has no hash of the model file (compile again)")
	}
	if _, err := os.Stat(in.Dir); err == nil {
		return nil, fmt.Errorf("judge: %s exists: the judge's project must be fresh", in.Dir)
	}
	core := filepath.Join(in.Dir, "core")
	built, err := judgeCore(in.Root, in.Budget)
	if err != nil {
		return nil, err
	}
	if err := copyTree(built, core); err != nil {
		return nil, err
	}
	// The certificate names the sources the judge builds from.
	files, err := repo.CoreFiles(in.Root)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(core, f))
		if err != nil {
			return nil, err
		}
		if f == "lakefile.toml" {
			b = bytes.Replace(b, []byte(generatedGlob), nil, 1)
		}
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
		h.Write(b)
	}
	coreSum := fmt.Sprintf("%x", h.Sum(nil))
	if snap := filepath.Base(filepath.Dir(built)); snap != "core-"+coreSum[:16] {
		return nil, fmt.Errorf("judge: the core snapshot %s does not hold the checkout's core sources (%s)", snap, coreSum[:16])
	}
	// A large model is split into parts (#45); the hash covers the parts
	// in order, then the main module.
	modelRel := strings.ReplaceAll(in.Report.LeanModule, ".", "/") + ".lean"
	var rels []string
	for i := range in.Report.LeanParts {
		rels = append(rels, strings.ReplaceAll(fmt.Sprintf("%s.Part%d", in.Report.LeanModule, i), ".", "/")+".lean")
	}
	rels = append(rels, modelRel)
	var model []byte
	texts := make([][]byte, len(rels))
	for i, rel := range rels {
		b, err := os.ReadFile(filepath.Join(in.WSLean, rel))
		if err != nil {
			return nil, err
		}
		texts[i] = b
		model = append(model, b...)
	}
	if got := hash(model); got != in.Report.LeanSHA256 {
		return nil, fmt.Errorf("judge: %s changed since the compile stage wrote it (sha256 %s, recorded %s)", modelRel, got, in.Report.LeanSHA256)
	}
	for i, rel := range rels {
		if err := writeFile(filepath.Join(core, rel), texts[i]); err != nil {
			return nil, err
		}
	}
	toolchain, err := os.ReadFile(filepath.Join(core, "lean-toolchain"))
	if err != nil {
		return nil, err
	}
	for name, text := range map[string]string{
		"lean-toolchain": string(toolchain),
		"lakefile.toml": `# Written by go2lean: the proof judge's project (lake comparator).
name = "judge"

[[require]]
name = "go2lean"
path = "core"

[[lean_lib]]
name = "Judge"
globs = ["Challenge", "Solution"]
`,
	} {
		if err := writeFile(filepath.Join(in.Dir, name), []byte(text)); err != nil {
			return nil, err
		}
	}
	// Trusted: only the checkout's sources and the hashed model are here.
	// lake writes the manifest the comparator's sandbox needs.
	if out, err := judgeCmd(in.Dir, in.Budget, "lake", "update"); err != nil {
		return nil, fmt.Errorf("judge: lake update: %v: %s", err, firstError(out))
	}
	fmt.Fprintf(in.Log, "judge: building the model in %s\n", in.Dir)
	if out, err := judgeCmd(in.Dir, in.Budget, "lake", "build", "Go2Lean.Proof.Simp", "Go2Lean.Proof.Tactic", "+"+in.Report.LeanModule); err != nil {
		return nil, fmt.Errorf("judge: lake build: %v: %s", err, firstError(out))
	}
	mode := "bwrap"
	if sandbox.Unconfined() {
		mode = "off"
	}
	return &Certificate{
		Toolchain:   strings.TrimSpace(string(toolchain)),
		CoreSHA256:  coreSum,
		ModelSHA256: in.Report.LeanSHA256,
		Axioms:      PermittedAxioms,
		Judge:       strings.Join(judgeCommand(in.Paranoid), " "),
		Sandbox:     mode,
		Dir:         in.Dir,
	}, nil
}

// globsRe is the core lakefile's list of module globs.
var globsRe = regexp.MustCompile(`(?m)^globs = \[`)

// generatedGlob makes the model a module of the core package in the
// judge's project, so that the solution, whose package cannot be written
// in comparator's sandbox, need build nothing but itself.
const generatedGlob = `"Go2Lean.Generated.+", `

func withGenerated(lakefile []byte) ([]byte, error) {
	loc := globsRe.FindIndex(lakefile)
	if loc == nil {
		return nil, fmt.Errorf("judge: the core lakefile has no globs")
	}
	return append(append(append([]byte{}, lakefile[:loc[1]]...), generatedGlob...), lakefile[loc[1]:]...), nil
}

// judgeCore is the core library as the judge builds on it: the checkout's
// core sources (their snapshot, repo.CoreDir) with the lakefile taking
// the model in, and the proof library built, once per snapshot. Only
// go2lean writes it, outside any sandbox; every sandbox sees it read-only.
func judgeCore(root string, budget time.Duration) (string, error) {
	snap, err := repo.CoreDir(root)
	if err != nil {
		return "", err
	}
	files, err := repo.CoreFiles(root)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(snap, "judge-core")
	lock, err := os.OpenFile(filepath.Join(snap, "judge-core.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		return dir, nil
	}
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(snap, f))
		if err != nil {
			return "", err
		}
		if f == "lakefile.toml" {
			if b, err = withGenerated(b); err != nil {
				return "", err
			}
		}
		if err := writeFile(filepath.Join(tmp, f), b); err != nil {
			return "", err
		}
	}
	if out, err := judgeCmd(tmp, budget, "lake", "build", "Go2Lean.Proof.Simp", "Go2Lean.Proof.Tactic"); err != nil {
		return "", fmt.Errorf("judge: lake build (core): %v: %s", err, firstError(out))
	}
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), nil, 0o644); err != nil {
		return "", err
	}
	return dir, os.Rename(tmp, dir)
}

// copyTree copies a directory's regular files, keeping their times (lake's
// traces are taken against them).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() || rel == ".complete" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

// judgeRun writes a solution with the proofs of the goals in proved (the
// others sorry) and a challenge with the statements of those in judged,
// and runs the comparator on them.
func judgeRun(in JudgeInput, proved, judged []int, cert *Certificate) (bool, error) {
	sorried := map[int]bool{}
	keep := map[int]bool{}
	for _, i := range proved {
		keep[i] = true
	}
	for i := range in.Goals {
		if !keep[i] {
			sorried[i] = true
		}
	}
	sol, err := SolutionText(in.Report, in.Goals, in.ABIs, in.Fids, sorried)
	if err != nil {
		return false, err
	}
	chal, err := ChallengeText(in.Report, in.Goals, in.ABIs, in.Fids, judged)
	if err != nil {
		return false, err
	}
	var names []string
	for _, i := range judged {
		names = append(names, Namespace(in.Report)+"."+TheoremName(in.Goals[i]))
	}
	cfg, _ := json.MarshalIndent(map[string]any{
		"challenge_module": "Challenge",
		"solution_module":  "Solution",
		"theorem_names":    names,
		"permitted_axioms": PermittedAxioms,
	}, "", "  ")
	for name, text := range map[string][]byte{"Solution.lean": []byte(sol), "Challenge.lean": []byte(chal), "comparator.json": cfg} {
		if err := writeFile(filepath.Join(in.Dir, name), text); err != nil {
			return false, err
		}
	}
	args := judgeCommand(in.Paranoid)
	if cert.Sandbox == "off" {
		args = append(args, "--inadvisably-no-sandbox")
	}
	fmt.Fprintf(in.Log, "judge: %s on %s\n", strings.Join(args, " "), strings.Join(names, ", "))
	out, err := judgeCmd(in.Dir, in.Budget, args...)
	_ = os.WriteFile(filepath.Join(in.Dir, "judge.log"), []byte(out), 0o644)
	if err != nil {
		fmt.Fprintf(in.Log, "judge: rejected: %s\n", lastLines(out, 6))
		return false, nil
	}
	return true, nil
}

// JudgeError is the first error of the judge's last run in dir.
func JudgeError(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "judge.log"))
	if err != nil {
		return "the judge did not run"
	}
	if strings.Contains(string(b), "error") {
		return firstError(string(b))
	}
	return lastLines(string(b), 1)
}

func judgeCmd(dir string, budget time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("timeout after %s", budget)
	}
	return string(out), err
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
