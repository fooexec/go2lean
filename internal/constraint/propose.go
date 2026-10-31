// Package constraint is the non-agentic core of M2: it turns M1's model
// report into property records and drives the Lean counterexample engines.
// The LLM agent (a later milestone) sits on top of it and calls the same
// functions.
package constraint

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/andreasfoo/go2lean/internal/contract"
)

var nonIdent = regexp.MustCompile(`[^A-Za-z0-9_]`)

// PropertyID builds a workspace-unique, Lean-module-safe identifier.
func PropertyID(parts ...string) string {
	id := "p_" + strings.ToLower(strings.Join(parts, "_"))
	return contract.ShortID(nonIdent.ReplaceAllString(id, "_"))
}

// Propose templates one no_panic property per target function and one
// postcondition property per spec-in-Go function, and writes them to the
// workspace. It returns the properties in creation order.
func Propose(ws contract.Workspace, report *contract.ModelReport) ([]*contract.Property, error) {
	byName := map[string]contract.ModelFunc{}
	for _, f := range report.Funcs {
		byName[f.Name] = f
	}
	var names []string
	for _, f := range report.Funcs {
		names = append(names, f.Name)
	}
	key := contract.FuncKeys(names)
	var out []*contract.Property
	for _, f := range report.Funcs {
		// Generic instances (Map[int]) are modelled for their callers; a
		// harness cannot name them.
		if f.Status == contract.ModelRejected || f.SpecFor != "" || f.SequenceFor != "" || f.Closure || f.WrappedBy != "" || f.CtorOnly != "" || f.TestFile || f.Init || strings.Contains(f.Name, "[") {
			continue
		}
		out = append(out, &contract.Property{
			ID:       PropertyID(contract.PackageKey(report.Package), key[f.Name], "nopanic"),
			Target:   contract.TargetID(report.Package, key[f.Name]),
			Kind:     contract.KindNoPanic,
			NL:       fmt.Sprintf("%s does not panic on any input", f.Name),
			Frontend: report.Frontend,
			Source:   "template",
		})
		if len(f.LoopHeaders) > 0 {
			// A function with a loop of its own: does every run end?
			out = append(out, &contract.Property{
				ID:       PropertyID(contract.PackageKey(report.Package), key[f.Name], "nohang"),
				Target:   contract.TargetID(report.Package, key[f.Name]),
				Kind:     contract.KindNoHang,
				NL:       fmt.Sprintf("%s returns on every input (no run comes back to a state)", f.Name),
				Frontend: report.Frontend,
				Source:   "template",
			})
		}
	}
	for _, f := range report.Funcs {
		if f.SpecFor == "" || f.Status == contract.ModelRejected {
			continue
		}
		target, ok := byName[f.SpecFor]
		if !ok || target.Status == contract.ModelRejected {
			continue
		}
		out = append(out, &contract.Property{
			ID:       PropertyID(contract.PackageKey(report.Package), key[f.SpecFor], key[f.Name]),
			Target:   contract.TargetID(report.Package, key[f.SpecFor]),
			Kind:     contract.KindPostcondition,
			NL:       fmt.Sprintf("%s satisfies the spec-in-Go oracle %s", f.SpecFor, f.Name),
			Spec:     &contract.SpecRef{GoFunc: f.Name, LeanDecl: fmt.Sprintf("%s.f%d", report.LeanModule, f.ID)},
			MaxLen:   f.SpecMaxLen,
			Frontend: report.Frontend,
			Source:   "template",
		})
	}
	for _, f := range report.Funcs {
		// A sequence spec is its own target: every input sequence makes it
		// return true (it drives SequenceFor and checks along the way).
		if f.SequenceFor == "" || f.Status == contract.ModelRejected {
			continue
		}
		out = append(out, &contract.Property{
			ID:       PropertyID(contract.PackageKey(report.Package), key[f.Name], "sequence"),
			Target:   contract.TargetID(report.Package, key[f.Name]),
			Kind:     contract.KindSequence,
			NL:       fmt.Sprintf("%s holds over every input sequence (it drives %s)", f.Name, f.SequenceFor),
			MaxLen:   f.SpecMaxLen,
			Frontend: report.Frontend,
			Source:   "template",
		})
	}
	for _, p := range out {
		if err := ws.WriteProperty(p); err != nil {
			return nil, err
		}
	}
	return out, nil
}
