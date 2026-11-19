// Package advisory turns a published advisory (OSV) into a catalog
// property (docs/specs): the first step of the CVE-to-spec pipeline (#287).
// It fetches the advisory and asks an LLM for the general property, its
// threat model and its oracle, never for the input that triggers it.
package advisory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Advisory is the part of an OSV record the property is drafted from.
type Advisory struct {
	ID        string
	Aliases   []string
	Summary   string
	Details   string
	Packages  []Package
	Fix       []string // fix commits and pull requests, from the references
	Reference []string // other references
}

// Package is an affected package and the versions around the fix.
type Package struct {
	Ecosystem  string
	Name       string
	Introduced []string
	Fixed      []string
}

type osvRecord struct {
	ID       string   `json:"id"`
	Aliases  []string `json:"aliases"`
	Summary  string   `json:"summary"`
	Details  string   `json:"details"`
	Affected []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Events []struct {
				Introduced string `json:"introduced"`
				Fixed      string `json:"fixed"`
			} `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
	References []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"references"`
}

// Parse reads an OSV record.
func Parse(data []byte) (*Advisory, error) {
	var r osvRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("advisory: %w", err)
	}
	if r.ID == "" {
		return nil, fmt.Errorf("advisory: no id in the record")
	}
	a := &Advisory{ID: r.ID, Aliases: r.Aliases, Summary: r.Summary, Details: r.Details}
	for _, af := range r.Affected {
		p := Package{Ecosystem: af.Package.Ecosystem, Name: af.Package.Name}
		for _, rg := range af.Ranges {
			for _, e := range rg.Events {
				if e.Introduced != "" {
					p.Introduced = append(p.Introduced, e.Introduced)
				}
				if e.Fixed != "" {
					p.Fixed = append(p.Fixed, e.Fixed)
				}
			}
		}
		a.Packages = append(a.Packages, p)
	}
	for _, ref := range r.References {
		if ref.Type == "FIX" || strings.Contains(ref.URL, "/commit/") || strings.Contains(ref.URL, "/pull/") {
			a.Fix = append(a.Fix, ref.URL)
		} else {
			a.Reference = append(a.Reference, ref.URL)
		}
	}
	return a, nil
}

// Fetch gets an advisory from api.osv.dev by id (GHSA-..., CVE-..., GO-...).
func Fetch(ctx context.Context, id string) (*Advisory, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.osv.dev/v1/vulns/"+id, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("advisory: osv answered %s for %s", resp.Status, id)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Text renders the advisory as the prompt's input.
func (a *Advisory) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Advisory %s", a.ID)
	if len(a.Aliases) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(a.Aliases, ", "))
	}
	fmt.Fprintf(&b, "\nSummary: %s\n\n%s\n\n", a.Summary, a.Details)
	for _, p := range a.Packages {
		fmt.Fprintf(&b, "Affected: %s %s; introduced %v, fixed %v\n", p.Ecosystem, p.Name, p.Introduced, p.Fixed)
	}
	for _, u := range a.Fix {
		fmt.Fprintf(&b, "Fix: %s\n", u)
	}
	return b.String()
}

// Completer is the one call the drafting makes (agent.LLM satisfies it).
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

const system = `You write one entry of a property catalog for a bug-finding tool. Input: a published security advisory. Output: a Markdown file, and nothing else, with these sections in this order:

# <the property, as a sentence a reader can check>
## Applies to
General: the kind of code that has the property (any project of that kind), not the advisory's project.
## Property
One paragraph: the guarantee the code must give, stated in general terms.
## Threat model
Who controls which inputs, what the attacker cannot control, what the victim does.
## Oracle
How to decide, independently of the package's own code, whether the property holds (a reference implementation, a specification, a browser's behaviour, a documented contract). Say what is compared and what is not (error text, ordering).
## Inputs
The small pieces inputs are built from, so that a search can combine them: characters, tokens, headers, option values. List the classes of pieces; do not combine them into the failing input.
## Pitfalls
How a spec for this property goes wrong for the wrong reason (comparing exact formats, stricter-than-reference implementations, missing preconditions).

Rules:
- State the property in general terms. Never write the advisory's trigger or any input that violates it, and do not name the advisory's project, function or file.
- The property must hold for the fixed version and fail for the vulnerable one; do not restrict it to the vulnerable version.
- If the advisory is not a property that can be checked by running code (a build, configuration or documentation issue), answer with the single line "NOT CHECKABLE: <reason>".`

// Draft asks the LLM for the catalog entry of an advisory.
func Draft(ctx context.Context, llm Completer, a *Advisory) (string, error) {
	out, err := llm.Complete(ctx, system, a.Text())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out) + "\n", nil
}

// Checkable reports whether the draft is a property, not a refusal.
func Checkable(draft string) bool {
	return !strings.HasPrefix(strings.TrimSpace(draft), "NOT CHECKABLE")
}
