package agent

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fooexec/go2lean/internal/constraint"
	"github.com/fooexec/go2lean/internal/contract"
	"github.com/fooexec/go2lean/internal/fuzzexport"
	"github.com/fooexec/go2lean/internal/pipeline"
)

// SpecFile is the file the agent owns inside the target package, unless
// Options.SpecFile names another (to sit next to hand-written specs).
const SpecFile = "go2lean_specs_test.go"

// Options tune a run.
type Options struct {
	Search      constraint.SearchOptions
	MaxRepairs  int // rounds spent fixing specs the frontend rejected
	ReportRound bool
	// Guidance is extra text for the proposal round: known bug classes,
	// invariants from the issue tracker, functions to focus on.
	Guidance string
	// SpecFile overrides the name of the file the agent writes (default
	// SpecFile); it must end in _test.go.
	SpecFile string
	// FuzzTime, when positive, also hands every spec to Go's fuzzer for
	// that long (internal/fuzzexport) after the pipeline ran; the report
	// round sees what it found.
	FuzzTime time.Duration
	// Memory (C only) searches and replays for memory errors only.
	Memory bool
	// Lib (C only) are the library's .c files, linked with the specs; the
	// source argument is then its public header, which the specs include
	// and the model reads. CFlags go to every compilation (-I, -D).
	Lib    []string
	CFlags []string
}

// Run drives the agent on one package: propose specs, repair until they
// model, run the pipeline, then (optionally) ask for a report and a revised
// spec file and run once more.
func Run(ctx context.Context, ws contract.Workspace, env pipeline.Env, pattern string, llm LLM, opts Options, out io.Writer) error {
	if opts.MaxRepairs == 0 {
		opts.MaxRepairs = 2
	}
	specFile := SpecFile
	if opts.SpecFile != "" {
		if !strings.HasSuffix(opts.SpecFile, "_test.go") {
			return fmt.Errorf("agent: spec file %s must end in _test.go", opts.SpecFile)
		}
		specFile = opts.SpecFile
	}
	moduleDir := env.Module
	if moduleDir == "" {
		moduleDir = env.Root
	}
	pkgDir, pkgName, pkgPath, err := goList(moduleDir, pattern)
	if err != nil {
		return err
	}
	// One log directory per package, so runs on different packages of the
	// same workspace keep their prompts, responses and reports.
	logDir := filepath.Join(ws.Root, "agent", contract.PackageKey(pkgPath))
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	call := 0
	ask := func(system, user string) (string, error) {
		call++
		_ = os.WriteFile(filepath.Join(logDir, fmt.Sprintf("call-%d-prompt.md", call)), []byte(system+"\n\n---\n\n"+user), 0o644)
		fmt.Fprintf(out, "== agent (%s) call %d\n", llm.Name(), call)
		resp, err := completeRetrying(ctx, llm, system, user, out)
		if err != nil {
			return "", err
		}
		_ = os.WriteFile(filepath.Join(logDir, fmt.Sprintf("call-%d-response.md", call)), []byte(resp), 0o644)
		return resp, nil
	}

	fmt.Fprintln(out, "== Compile stage (before specs)")
	report, err := pipeline.Compile(ws, env, pattern, out)
	if err != nil {
		return err
	}
	source, err := packageSource(pkgDir)
	if err != nil {
		return err
	}

	// install writes a spec file into the package and compiles, sending
	// build errors and rejected specs back to the model until everything is
	// modelled or the repair budget is spent. It returns the installed spec.
	install := func(spec string) (string, *contract.ModelReport, error) {
		for repair := 0; ; repair++ {
			spec = normalizeSpec(spec, pkgName)
			if err := os.WriteFile(filepath.Join(pkgDir, specFile), []byte(spec), 0o644); err != nil {
				return "", nil, err
			}
			fmt.Fprintf(out, "== Compile stage (specs, attempt %d)\n", repair+1)
			var report *contract.ModelReport
			// The compile stage tolerates type errors (it builds what
			// loads), so the spec file is first built as go test builds the
			// package's tests: the replay runs them that way.
			issues := goTestBuild(moduleDir, pattern)
			var err error
			switch {
			case issues != nil:
				err = fmt.Errorf("go test build of %s failed", pattern)
			default:
				report, err = pipeline.Compile(ws, env, pattern, out)
				if err != nil {
					// A spec that does not even type-check is an issue for the agent, not a crash.
					issues = []string{"go build: " + err.Error()}
				} else {
					issues = specIssues(report)
				}
			}
			if len(issues) == 0 {
				_ = os.WriteFile(filepath.Join(logDir, specFile), []byte(spec), 0o644)
				return spec, report, nil
			}
			if repair >= opts.MaxRepairs {
				if err != nil {
					return "", nil, fmt.Errorf("specs still do not compile after %d repairs: %w", repair, err)
				}
				_ = os.WriteFile(filepath.Join(logDir, specFile), []byte(spec), 0o644)
				return spec, report, nil // rejected specs are reported, not fatal
			}
			resp, err := ask(systemPrompt, repairPrompt(issues, spec))
			if err != nil {
				return "", nil, err
			}
			if next := extractGo(resp); next != "" {
				spec = next
			}
		}
	}

	spec, err := propose(ask, proposePrompt(pkgPath, pkgName, source, report)+guidanceSection(opts.Guidance))
	if err != nil {
		return err
	}
	spec, report, err = install(spec)
	if err != nil {
		return err
	}

	res, err := runRest(ws, env, report, spec, opts.Search, out)
	if err != nil {
		return err
	}
	fuzzNote := ""
	if opts.FuzzTime > 0 {
		fmt.Fprintf(out, "== go test -fuzz (%s per spec)\n", opts.FuzzTime)
		results, skipped, err := fuzzexport.Run(ws, moduleDir, pkgPath, fuzzexport.Options{FuzzTime: opts.FuzzTime})
		if err != nil {
			fmt.Fprintf(out, "  fuzz: %v\n", err)
		}
		fuzzNote = fuzzSummary(results, skipped, out)
	}
	if !opts.ReportRound {
		return nil
	}

	// Report round: source and verdicts back to the agent; a revised spec
	// file triggers one more pass.
	resp, err := askReport(ask, reportUserPrompt(pkgPath, pkgName, source, spec, verdictSummary(ws, res)+fuzzNote))
	if err != nil {
		return err
	}
	reportPath := filepath.Join(logDir, "report.md")
	if err := os.WriteFile(reportPath, []byte(stripGo(resp)), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "== report written to %s\n", reportPath)
	if revised := extractGo(resp); hasSpecBlock(resp) && normalizeSpec(revised, pkgName) != spec {
		fmt.Fprintln(out, "== agent revised the specs; running again")
		if _, report, err = install(revised); err != nil {
			return err
		}
		if _, err := runRest(ws, env, report, revised, opts.Search, out); err != nil {
			return err
		}
	}
	return nil
}

func runRest(ws contract.Workspace, env pipeline.Env, report *contract.ModelReport, spec string, so constraint.SearchOptions, out io.Writer) (*pipeline.Result, error) {
	fmt.Fprintln(out, "== Spec & search stage: properties")
	props, err := pipeline.Propose(ws, report, out)
	if err != nil {
		return nil, err
	}
	// The properties of the specs the agent wrote are the agent's: a
	// violation may be the spec's fault (internal/poc/rank.go ranks it so).
	mine := specFuncs(spec)
	for _, p := range props {
		fn := ""
		switch {
		case p.Spec != nil:
			fn = p.Spec.GoFunc
		case p.Kind == contract.KindSequence:
			if t, err := ws.Target(p.Target); err == nil {
				fn = t.Func
			}
		}
		if mine[fn] {
			p.Source = "agent"
			if err := ws.WriteProperty(p); err != nil {
				return nil, err
			}
		}
	}
	fmt.Fprintln(out, "== Spec & search stage: hunt")
	res := &pipeline.Result{Report: report, Properties: props}
	var cxs []*contract.Counterexample
	for _, p := range props {
		cx, err := pipeline.Hunt(ws, env, p, so, out)
		if errors.Is(err, pipeline.ErrStoppedForMemory) {
			continue // reported in its line; -resume searches it again
		}
		if err != nil {
			return nil, err
		}
		if cx != nil {
			cxs = append(cxs, cx)
		}
	}
	fmt.Fprintln(out, "== Replay stage: generate PoC and verify")
	if len(cxs) == 0 {
		fmt.Fprintln(out, "  nothing to verify")
	}
	for _, cx := range cxs {
		v, err := pipeline.Verify(ws, env, cx.ID, out)
		if err != nil {
			return nil, err
		}
		res.Verdicts = append(res.Verdicts, v)
	}
	return res, nil
}

// specIssues lists the specs the frontend rejected.
func specIssues(r *contract.ModelReport) []string {
	var out []string
	for _, f := range r.Funcs {
		if f.SpecFor != "" && f.Status == contract.ModelRejected {
			out = append(out, fmt.Sprintf("%s (spec for %s): %s", f.Name, f.SpecFor, strings.Join(f.Issues, "; ")))
		}
	}
	return out
}

// verdictSummary renders what the pipeline found for the report prompt.
func verdictSummary(ws contract.Workspace, res *pipeline.Result) string {
	var b strings.Builder
	b.WriteString("Properties:\n")
	for _, p := range res.Properties {
		fmt.Fprintf(&b, "- %s [%s]: %s\n", p.ID, p.Kind, p.NL)
	}
	b.WriteString("\nVerdicts:\n")
	byProp := map[string]bool{}
	for _, v := range res.Verdicts {
		cx, err := ws.Counterexample(v.Counterexample)
		if err != nil {
			continue
		}
		byProp[cx.Property] = true
		fmt.Fprintf(&b, "- %s on %s: %s\n    inputs %v", v.Counterexample, cx.Property, v.Status, cx.Inputs)
		if len(cx.Memory) > 0 {
			fmt.Fprintf(&b, " memory %v", cx.Memory)
		}
		if cx.ModelOutput != nil {
			fmt.Fprintf(&b, "\n    model: %s %s %s", cx.ModelOutput.Res, cx.ModelOutput.Value, cx.ModelOutput.Panic)
		}
		fmt.Fprintf(&b, "\n    real: %v", v.Observed)
		if v.Diagnosis != nil {
			fmt.Fprintf(&b, "\n    diagnosis: %s %s", v.Diagnosis.Reason, v.Diagnosis.ModelVsReal)
			for _, n := range v.Diagnosis.Notes {
				fmt.Fprintf(&b, "\n    note: %s", n)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\nProperties without a counterexample:\n")
	for _, p := range res.Properties {
		if byProp[p.ID] {
			continue
		}
		status := "no counterexample found by random search"
		if pr, err := readProof(ws, p.ID); err == nil {
			status = fmt.Sprintf("%s: %s", pr.Status, pr.Domain)
		}
		if m := ws.Mutation(p.ID); m != nil {
			if m.Killed == 0 {
				status += fmt.Sprintf("; mutation: none of %d mutants of the code under test fails it, so it checks nothing a one-line bug would break (survivors: %s)",
					m.Tried, strings.Join(first(m.Survived, 3), "; "))
			} else {
				status += fmt.Sprintf("; mutation: %d of %d mutants fail it", m.Killed, m.Tried)
			}
		}
		fmt.Fprintf(&b, "- %s: %s\n", p.ID, status)
	}
	return b.String()
}

// fuzzSummary prints what Go's fuzzer found on the specs and renders it
// for the report prompt.
func fuzzSummary(results []fuzzexport.Result, skipped []fuzzexport.Skipped, out io.Writer) string {
	var b strings.Builder
	b.WriteString("\nGo's fuzzer on the same specs (go test -fuzz, real code, coverage-guided; a postcondition skips inputs on which the target panics):\n")
	for _, r := range results {
		fmt.Fprintf(out, "  %-60s %s (%s execs)\n", r.Property, r.Status, r.Execs)
		fmt.Fprintf(&b, "- %s: %s after %.0fs", r.Property, r.Status, r.Seconds)
		if r.Status == "violated" {
			fmt.Fprintf(&b, ": %s; failing input %s", r.Message, strings.ReplaceAll(strings.TrimSpace(r.Input), "\n", " "))
		}
		b.WriteString("\n")
	}
	for _, s := range skipped {
		fmt.Fprintf(&b, "- %s: not fuzzed (%s)\n", s.Property, s.Reason)
	}
	return b.String()
}

func first(xs []string, n int) []string { return xs[:min(n, len(xs))] }

func readProof(ws contract.Workspace, propID string) (*contract.ProofResult, error) {
	var pr contract.ProofResult
	b, err := os.ReadFile(filepath.Join(ws.Root, "proofs", propID+".json"))
	if err != nil {
		return nil, err
	}
	return &pr, unmarshal(b, &pr)
}

// stripGo replaces the spec-file block (recognised by its go2lean:spec or
// go2lean:sequence comments) with a note, leaving fix snippets and other code in the report.
func stripGo(resp string) string {
	out := goBlock.ReplaceAllStringFunc(resp, func(block string) string {
		if hasMarker(block) {
			return "(revised spec file written to go2lean_specs_test.go)"
		}
		return block
	})
	return strings.TrimSpace(out) + "\n"
}

func goList(root, pattern string) (dir, name, path string, err error) {
	cmd := exec.Command("go", "list", "-f", "{{.Dir}}\t{{.Name}}\t{{.ImportPath}}", pattern)
	cmd.Dir = root
	b, err := cmd.Output()
	if err != nil {
		return "", "", "", fmt.Errorf("go list %s: %w", pattern, err)
	}
	parts := strings.Split(strings.TrimSpace(string(b)), "\t")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("go list %s: unexpected output %q", pattern, b)
	}
	return parts[0], parts[1], parts[2], nil
}

// llmAttempts is how many times one LLM call is tried: a long call can
// lose its connection (a proxy's reset), which should not end a run whose
// specs took minutes to write.
const llmAttempts = 3

// llmRetryWait is the first pause before trying an LLM call again.
var llmRetryWait = 5 * time.Second

// completeRetrying calls llm, trying again after an error, with a pause
// that doubles, unless ctx is done.
func completeRetrying(ctx context.Context, llm LLM, system, user string, out io.Writer) (string, error) {
	wait := llmRetryWait
	var err error
	for attempt := 1; attempt <= llmAttempts; attempt++ {
		var resp string
		if resp, err = llm.Complete(ctx, system, user); err == nil {
			return resp, nil
		}
		if attempt == llmAttempts || ctx.Err() != nil {
			break
		}
		fmt.Fprintf(out, "  %s: %v; trying again in %s\n", llm.Name(), err, wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		wait *= 2
	}
	return "", err
}

// goTestBuild builds the test binary of pattern (in root) as go test
// would, discarding it, and returns the compiler's error lines, or nil when
// it builds.
func goTestBuild(root, pattern string) []string {
	cmd := exec.Command("go", "test", "-c", "-o", os.DevNull, pattern)
	cmd.Dir = root
	b, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, "go build: "+l)
		}
	}
	if len(lines) == 0 {
		lines = []string{"go build: " + err.Error()}
	}
	return lines
}

// specFuncs names the functions a spec file declares.
func specFuncs(spec string) map[string]bool {
	out := map[string]bool{}
	f, err := parser.ParseFile(token.NewFileSet(), "specs.go", spec, 0)
	if err != nil {
		return out
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
			out[fd.Name.Name] = true
		}
	}
	return out
}

// propose asks for the spec file. A model sometimes answers with a tool
// call it cannot make instead of code (seen on grpc-go's status package,
// #220), so an answer with no Go code block is asked for once more.
func propose(ask func(system, user string) (string, error), prompt string) (string, error) {
	for try := 0; try < 2; try++ {
		p := prompt
		if try > 0 {
			p = noCodeRetryPrompt + prompt
		}
		resp, err := ask(systemPrompt, p)
		if err != nil {
			return "", err
		}
		if spec := extractGo(resp); spec != "" {
			return spec, nil
		}
	}
	return "", fmt.Errorf("agent response contained no Go code block")
}

// askReport asks for the report. An answer with no Markdown heading is a
// model that set out to call tools it does not have (grpc-go's
// encoding/proto, #220); it is asked once more.
func askReport(ask func(system, user string) (string, error), prompt string) (string, error) {
	resp, err := ask(reportPrompt, prompt)
	if err != nil || isReport(resp) {
		return resp, err
	}
	return ask(reportPrompt, noToolsReportPrompt+prompt)
}

func isReport(resp string) bool {
	for _, l := range strings.Split(resp, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") && !strings.HasPrefix(strings.TrimSpace(l), "#!") {
			return true
		}
	}
	return false
}
