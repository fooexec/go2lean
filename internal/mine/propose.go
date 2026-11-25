package mine

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

// Completer is the one LLM call proposing makes (agent.LLM satisfies it).
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// Target is a function of the project a property is about.
type Target struct {
	Package string `json:"package"` // the package's directory, relative to the module ("." for the root)
	Func    string `json:"func"`    // Name, or Type.Method
}

func (t Target) String() string { return t.Package + " " + t.Func }

// Candidate is a property drafted from evidence (#315 P2), before a spec is
// written for it.
type Candidate struct {
	ID       string   `json:"id"`
	Property string   `json:"property"` // one sentence a reader can check
	Harm     string   `json:"harm"`     // what goes wrong when it fails, for whom
	Oracle   string   `json:"oracle"`   // how to decide it, independently of the code
	Inputs   []string `json:"inputs"`   // classes of input pieces, the mechanisms among them
	Kind     string   `json:"kind"`     // security, correctness or conformance
	Priority int      `json:"priority"` // 1 (check first) to 3
	Targets  []Target `json:"targets"`
	Evidence []string `json:"evidence"` // refs of the evidence it comes from
	// Before and After are the revisions to calibrate a spec on: violated
	// before, holding after. From the first evidence item that has them.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Proposal is propose's output.
type Proposal struct {
	Module     string      `json:"module"`
	Candidates []Candidate `json:"candidates"`
	// Dropped are what the LLM proposed that did not survive validation,
	// and why (an unknown target, no target, not checkable).
	Dropped []string `json:"dropped,omitempty"`
}

// Functions lists the functions and methods of every package of the module
// in dir (tests excluded), by package directory.
func Functions(dir string) (map[string][]string, map[string]string, error) {
	funcs := map[string][]string{}
	docs := map[string]string{} // package dir -> first sentence of its doc
	err := goSources(dir, func(rel string, file *ast.File, fset *token.FileSet) {
		pkg := filepath.ToSlash(filepath.Dir(rel))
		if file.Doc != nil && docs[pkg] == "" {
			if s := sentences(file.Doc.Text()); len(s) > 0 {
				docs[pkg] = s[0]
			}
		}
		for _, d := range file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				name := fd.Name.Name
				if fd.Recv != nil && len(fd.Recv.List) > 0 {
					name = recvName(fd.Recv.List[0].Type) + "." + name
				}
				funcs[pkg] = append(funcs[pkg], name)
			}
		}
	})
	if err == nil {
		err = cSources(dir, func(rel string, src []byte) {
			if !strings.HasSuffix(rel, ".c") {
				return
			}
			pkg := filepath.ToSlash(filepath.Dir(rel))
			for _, f := range cFunctions(src) {
				if !contains(funcs[pkg], f) {
					funcs[pkg] = append(funcs[pkg], f)
				}
			}
		})
	}
	for p := range funcs {
		sort.Strings(funcs[p])
	}
	return funcs, docs, err
}

const proposeSystem = `You read evidence about a Go project and propose properties a bug-finding tool can check by running the code. The tool writes each property as a Go function (a spec) over the project's functions, searches for inputs that violate it, and replays every violation as a real test.

Evidence kinds:
- advisory: a published security advisory, of this project or of a similar one (another implementation of the same protocol, possibly in another language). The property is the guarantee the advisory shows was missing.
- commit: a commit of this project that fixed a defect. The property is the guarantee the fix establishes: it must fail on the code before the commit and hold after it.
- pr: a merged pull request of this project that fixed a defect, with the issues it closes. As for a commit: the property must fail before the fix and hold after it.
- issue: an open issue of this project that reports a bug. Nothing has fixed it; the property is the guarantee the report says is broken, so a spec for it may fail on the current code.
- rfc: a section of an RFC the code cites. The properties are the section's MUST / MUST NOT / SHOULD rules that the citing code implements.
- doc: a sentence of the project's documentation that states a contract.

Answer with a JSON array, and nothing else, of objects with these fields:
- "property": one sentence, checkable by running the code, stated in general terms.
- "harm": what goes wrong when it fails, and who can cause it (a peer, a caller).
- "oracle": how to decide whether it holds, independently of the project's code (the RFC text, a reference computation, a documented contract). Say what is compared.
- "inputs": the classes of input pieces a search should combine, as a list of short strings. Include the mechanism the evidence names as one class among the others that reach the same harm.
- "kind": "security", "correctness" or "conformance".
- "priority": 1 (a peer can trigger it, or it breaks a MUST), 2, or 3 (minor).
- "targets": the functions the spec drives, as {"package": "<package directory>", "func": "<Name or Type.Method>"}, chosen from the functions listed. At least one.
- "evidence": the refs of the evidence items the property comes from.

Rules:
- State the property by the harm the victim observes, not by the mechanism the evidence names: a property about one header misses the next defect, which reaches the same harm through another input. Put the mechanisms in "inputs".
- Never write the input that triggers a defect, or a property that only the vulnerable version breaks for that one input.
- Propose properties only for what running the code can show. Skip evidence that is about the build, the tests, performance, logging or documentation.
- An advisory of another project counts only if this project has the same kind of code; name the functions where the same harm could occur.
- Several items may give the same property: give it once, with all their refs.
- An empty array is a valid answer.`

// batch is how many evidence items go into one call.
const batch = 10

// maxItemText bounds an item's text in the prompt.
const maxItemText = 1500

// Propose asks the LLM for candidate properties, a batch of evidence at a
// time, and keeps those whose targets exist in the module in dir.
func Propose(ctx context.Context, llm Completer, r *Report, dir string) (*Proposal, error) {
	funcs, docs, err := Functions(dir)
	if err != nil {
		return nil, err
	}
	known := map[Target]bool{}
	for p, fs := range funcs {
		for _, f := range fs {
			known[Target{p, f}] = true
		}
	}
	lang := Language(dir)
	byRef := map[string]Evidence{}
	for _, e := range r.Evidence {
		byRef[e.Ref] = e
	}
	out := &Proposal{Module: r.Module}
	seen := map[string]bool{}
	for start := 0; start < len(r.Evidence); start += batch {
		items := r.Evidence[start:min(start+batch, len(r.Evidence))]
		resp, err := llm.Complete(ctx, systemFor(proposeSystem, lang), proposePrompt(r.Module, items, funcs, docs))
		if err != nil {
			return out, fmt.Errorf("propose: batch %d: %w", start/batch+1, err)
		}
		cs, err := parseCandidates(resp)
		if err != nil {
			out.Dropped = append(out.Dropped, fmt.Sprintf("batch %d: %v", start/batch+1, err))
			continue
		}
		cs = repairTargets(ctx, llm, cs, known, funcs, r.Module)
		for _, c := range cs {
			var ok []Target
			for _, t := range c.Targets {
				t = normTarget(t, r.Module)
				if known[t] {
					ok = append(ok, t)
				} else {
					out.Dropped = append(out.Dropped, fmt.Sprintf("%q: unknown target %s", c.Property, t))
				}
			}
			if len(ok) == 0 {
				out.Dropped = append(out.Dropped, fmt.Sprintf("%q: no target in the module", c.Property))
				continue
			}
			c.Targets = ok
			key := candidateKey(c)
			if seen[key] {
				continue
			}
			seen[key] = true
			for i, ref := range c.Evidence {
				c.Evidence[i] = evidenceRef(ref, byRef)
			}
			for _, ref := range c.Evidence {
				if e, ok := byRef[ref]; ok && e.After != "" {
					c.Before, c.After = e.Before, e.After
					break
				}
			}
			if c.Priority < 1 || c.Priority > 3 {
				c.Priority = 2
			}
			out.Candidates = append(out.Candidates, c)
		}
	}
	sort.SliceStable(out.Candidates, func(i, j int) bool {
		a, b := out.Candidates[i], out.Candidates[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if (a.Kind == "security") != (b.Kind == "security") {
			return a.Kind == "security"
		}
		return a.After != "" && b.After == ""
	})
	for i := range out.Candidates {
		out.Candidates[i].ID = fmt.Sprintf("c%03d", i+1)
	}
	return out, nil
}

// normTarget spells a target as Functions does: the package directory
// relative to the module, a method as Type.Method.
func normTarget(t Target, module string) Target {
	t.Package = strings.TrimSuffix(strings.TrimPrefix(t.Package, module+"/"), "/")
	if t.Package == module || t.Package == "" {
		t.Package = "."
	}
	t.Func = strings.TrimPrefix(strings.TrimPrefix(t.Func, "(*"), "*")
	t.Func = strings.Replace(t.Func, ").", ".", 1)
	return t
}

const repairSystem = `You proposed properties whose target functions do not exist in the project; you guessed their names. For each candidate below, choose its targets from the functions listed for its packages: the functions the property's spec would drive. Answer with a JSON array of the same length and order, each element {"targets": [{"package": "...", "func": "..."}]}; give an empty targets list when none of the listed functions fits. Nothing else.`

// repairTargets gives the candidates none of whose targets exist, but whose
// packages do, one more call with those packages' functions listed (an
// advisory of another project names no file, so the first call saw only
// the package index and guessed the names).
func repairTargets(ctx context.Context, llm Completer, cs []Candidate, known map[Target]bool, funcs map[string][]string, module string) []Candidate {
	var idx []int
	pkgs := map[string]bool{}
	for i, c := range cs {
		hit, inPkg := false, false
		for _, t := range c.Targets {
			t = normTarget(t, module)
			if known[t] {
				hit = true
			}
			if funcs[t.Package] != nil {
				inPkg = true
				pkgs[t.Package] = true
			}
		}
		if !hit && inPkg {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return cs
	}
	var b strings.Builder
	for n, i := range idx {
		var ts []string
		for _, t := range cs[i].Targets {
			ts = append(ts, normTarget(t, module).String())
		}
		fmt.Fprintf(&b, "Candidate %d: %s\nGuessed targets: %s\n\n", n+1, cs[i].Property, strings.Join(ts, "; "))
	}
	var ps []string
	for p := range pkgs {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	for _, p := range ps {
		fmt.Fprintf(&b, "package %s: %s\n", p, strings.Join(funcs[p], ", "))
	}
	resp, err := llm.Complete(ctx, repairSystem, b.String())
	if err != nil {
		return cs
	}
	var fixed []struct {
		Targets []Target `json:"targets"`
	}
	s := strings.TrimSpace(resp)
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			s = s[i : j+1]
		}
	}
	if json.Unmarshal([]byte(s), &fixed) != nil || len(fixed) != len(idx) {
		return cs
	}
	for n, i := range idx {
		cs[i].Targets = fixed[n].Targets
	}
	return cs
}

func candidateKey(c Candidate) string {
	var ts []string
	for _, t := range c.Targets {
		ts = append(ts, t.String())
	}
	sort.Strings(ts)
	p := strings.ToLower(strings.Join(strings.Fields(c.Property), " "))
	if len(p) > 80 {
		p = p[:80]
	}
	return strings.Join(ts, ",") + "|" + p
}

// proposePrompt lists the batch's evidence, the functions of the files it
// concerns, and the packages, for the items that name no file.
func proposePrompt(module string, items []Evidence, funcs map[string][]string, docs map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s\n\n", module)
	pkgs := map[string]bool{}
	needIndex := false
	for i, e := range items {
		fmt.Fprintf(&b, "## Evidence %d: %s %s\n", i+1, e.Kind, e.Ref)
		if e.Project != "" && e.Project != module {
			fmt.Fprintf(&b, "(an advisory of a similar project: %s)\n", e.Project)
		}
		if e.After != "" {
			fmt.Fprintf(&b, "(it records a fix: before %s, after %s)\n", e.Before, e.After)
		}
		fmt.Fprintf(&b, "%s\n\n%s\n\n", e.Title, clip(e.Text, maxItemText))
		if len(e.Paths) == 0 {
			needIndex = true
		}
		for _, p := range e.Paths {
			if strings.HasSuffix(p, ".go") {
				pkgs[filepath.ToSlash(filepath.Dir(p))] = true
			}
		}
	}
	var ps []string
	for p := range pkgs {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	if len(ps) > 0 {
		b.WriteString("## Functions of the packages these items concern\n")
		for _, p := range ps {
			fs := funcs[p]
			if len(fs) > 150 {
				fs = fs[:150]
			}
			fmt.Fprintf(&b, "package %s: %s\n", p, strings.Join(fs, ", "))
		}
		b.WriteString("\n")
	}
	if needIndex {
		b.WriteString("## Packages of the project (for items that name no file); name any of their functions\n")
		var all []string
		for p := range funcs {
			all = append(all, p)
		}
		sort.Strings(all)
		for _, p := range all {
			fmt.Fprintf(&b, "package %s: %s\n", p, docs[p])
		}
	}
	return b.String()
}

// parseCandidates reads the JSON array of the answer, inside a code fence
// or not.
func parseCandidates(resp string) ([]Candidate, error) {
	s := strings.TrimSpace(resp)
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			s = s[i : j+1]
		}
	}
	var cs []Candidate
	if err := json.Unmarshal([]byte(s), &cs); err != nil {
		return nil, fmt.Errorf("the answer is not a JSON array of candidates: %v", err)
	}
	return cs, nil
}

// Select keeps the evidence of the given kinds (comma-separated; "" : all),
// and at most limit items (0: no limit).
func Select(ev []Evidence, kinds string, limit int) []Evidence {
	want := map[string]bool{}
	for _, k := range strings.Split(kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			want[k] = true
		}
	}
	var out []Evidence
	for _, e := range ev {
		if len(want) > 0 && !want[e.Kind] {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// Guidance renders the candidates of package pkg (its directory relative to
// the module, "." for the root) as the agent's guidance (#315 P3): one
// section per candidate, the spec to carry "candidate <id>" in its doc
// comment so that calibration can find it. ids, when not empty, keeps only
// those candidates.
func (p *Proposal) Guidance(pkg string, ids []string) (string, []Candidate) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var cs []Candidate
	for _, c := range p.Candidates {
		if len(want) > 0 && !want[c.ID] {
			continue
		}
		for _, t := range c.Targets {
			if t.Package == pkg {
				cs = append(cs, c)
				break
			}
		}
	}
	if len(cs) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("Candidate properties drafted from the project's evidence (its advisories, fix commits, cited RFC sections, documentation, and similar projects' advisories). Write at least one spec for each, checking the property against the oracle given, over inputs built from the classes listed. Put the words \"candidate <id>\" (for example \"candidate c003\") in the doc comment of every spec written for a candidate. Write the property as stated, not a weaker one the code happens to satisfy: a candidate drafted from a fixed defect must be able to fail on the code before the fix.\n")
	for _, c := range cs {
		fmt.Fprintf(&b, "\n### %s (%s, priority %d)\n", c.ID, c.Kind, c.Priority)
		fmt.Fprintf(&b, "Property: %s\n", c.Property)
		if c.Harm != "" {
			fmt.Fprintf(&b, "Harm: %s\n", c.Harm)
		}
		if c.Oracle != "" {
			fmt.Fprintf(&b, "Oracle: %s\n", c.Oracle)
		}
		if len(c.Inputs) > 0 {
			fmt.Fprintf(&b, "Input classes: %s\n", strings.Join(c.Inputs, "; "))
		}
		var ts []string
		for _, t := range c.Targets {
			if t.Package == pkg {
				ts = append(ts, t.Func)
			}
		}
		fmt.Fprintf(&b, "Functions: %s\n", strings.Join(ts, ", "))
		fmt.Fprintf(&b, "From: %s\n", strings.Join(c.Evidence, ", "))
	}
	return b.String(), cs
}

// evidenceRef is the evidence reference an answer means: the prompt heads
// each item "kind ref" ("pr #6295"), and an answer may copy the kind.
func evidenceRef(ref string, byRef map[string]Evidence) string {
	ref = strings.TrimSpace(ref)
	if _, ok := byRef[ref]; ok {
		return ref
	}
	for _, k := range []string{KindAdvisory, KindPR, KindCommit, KindIssue, KindRFC, KindDoc} {
		if r := strings.TrimSpace(strings.TrimPrefix(ref, k+" ")); r != ref {
			if _, ok := byRef[r]; ok {
				return r
			}
		}
	}
	return ref
}

// systemFor states the prompt for the project's language: a C project's
// specs are C functions, replayed under the sanitizers.
func systemFor(prompt, lang string) string {
	if lang == "Go" {
		return prompt
	}
	r := strings.NewReplacer(
		"about a Go project", "about a "+lang+" project",
		"as a Go function (a spec)", "as a "+lang+" function (a spec)",
		"replays every violation as a real test", "replays every violation as a real program under AddressSanitizer and UndefinedBehaviorSanitizer",
		`{"package": "<package directory>", "func": "<Name or Type.Method>"}`, `{"package": "<source directory>", "func": "<function name>"}`,
	)
	return r.Replace(prompt)
}
