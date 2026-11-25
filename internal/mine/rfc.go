package mine

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The two ways code cites an RFC section: "RFC 9000, Section 13.2" (or
// "RFC 9000 §13.2", "RFC9000 section 13.2") and "Section 13.2 of RFC 9000".
var (
	citeRFCFirst = regexp.MustCompile(`RFC ?(\d{3,5})(?:,?\s*(?:[Ss]ection|[Ss]ec\.|§)\s*(\d+(?:\.\d+)*))?`)
	citeSecFirst = regexp.MustCompile(`(?:[Ss]ection|§)\s*(\d+(?:\.\d+)*)\s+of\s+RFC ?(\d{3,5})`)
)

type cite struct{ rfc, section string }

func (c cite) ref() string {
	if c.section == "" {
		return "RFC " + c.rfc
	}
	return "RFC " + c.rfc + " " + c.section
}

// Citations lists the RFC sections the project's comments cite, one item
// per section, with every place that cites it and the citing comments as
// its text. The section's own text is added by FetchRFCs.
func Citations(dir string) ([]Evidence, error) {
	where := map[cite][]string{}
	ctx := map[cite][]string{}
	var order []cite
	visit := func(rel string, line int, text string) {
		{
			var found []cite
			for _, m := range citeSecFirst.FindAllStringSubmatch(text, -1) {
				found = append(found, cite{m[2], m[1]})
			}
			for _, m := range citeRFCFirst.FindAllStringSubmatch(text, -1) {
				found = append(found, cite{m[1], m[2]})
			}
			for _, c := range found {
				if c.section == "" && hasSectionCite(found, c.rfc) {
					continue // "Section 4 of RFC 9000" also matched as bare "RFC 9000"
				}
				if where[c] == nil {
					order = append(order, c)
				}
				at := rel + ":" + strconv.Itoa(line)
				if n := len(where[c]); n == 0 || where[c][n-1] != at {
					where[c] = append(where[c], at)
					if len(ctx[c]) < 3 {
						ctx[c] = append(ctx[c], at+": "+strings.Join(strings.Fields(text), " "))
					}
				}
			}
		}
	}
	err := goSources(dir, func(rel string, file *ast.File, fset *token.FileSet) {
		for _, g := range file.Comments {
			visit(rel, fset.Position(g.Pos()).Line, g.Text())
		}
	})
	if err == nil {
		err = cSources(dir, func(rel string, src []byte) {
			for _, c := range cComments(src) {
				visit(rel, c.line, c.text)
			}
		})
	}
	if err != nil {
		return nil, err
	}
	var ev []Evidence
	for _, c := range order {
		paths := map[string]bool{}
		for _, w := range where[c] {
			paths[strings.SplitN(w, ":", 2)[0]] = true
		}
		var ps []string
		for p := range paths {
			ps = append(ps, p)
		}
		sort.Strings(ps)
		ev = append(ev, Evidence{
			Kind: KindRFC, Ref: c.ref(), Title: fmt.Sprintf("%s, cited %d times", c.ref(), len(where[c])),
			Text: clip("Cited as:\n"+strings.Join(ctx[c], "\n"), maxText), Paths: ps, Cites: where[c],
		})
	}
	return ev, nil
}

func hasSectionCite(cs []cite, rfc string) bool {
	for _, c := range cs {
		if c.rfc == rfc && c.section != "" {
			return true
		}
	}
	return false
}

// FetchRFCs adds to each citation of a section the section's text, from
// base (default rfc-editor.org), keeping the RFCs fetched in cache.
func FetchRFCs(ctx context.Context, cites []Evidence, base, cache string, note func(string, ...any)) []Evidence {
	if base == "" {
		base = "https://www.rfc-editor.org/rfc/"
	}
	texts := map[string]string{}
	failed := map[string]bool{}
	for i, e := range cites {
		f := strings.Fields(e.Ref) // RFC, number, [section]
		if len(f) < 3 {
			continue
		}
		num, sec := f[1], f[2]
		if failed[num] {
			continue
		}
		t, ok := texts[num]
		if !ok {
			var err error
			t, err = fetchRFC(ctx, base, cache, num)
			if err != nil {
				note("rfc %s: %v", num, err)
				failed[num] = true
				continue
			}
			texts[num] = t
		}
		if s := Section(t, sec); s != "" {
			cites[i].Text = clip(e.Text+"\n\nRFC "+num+", section "+sec+":\n"+s, maxText)
		}
	}
	return cites
}

func fetchRFC(ctx context.Context, base, cache, num string) (string, error) {
	path := ""
	if cache != "" {
		path = filepath.Join(cache, "rfc"+num+".txt")
		if b, err := os.ReadFile(path); err == nil {
			return string(b), nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(base, "/")+"/rfc"+num+".txt", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if path != "" {
		if os.MkdirAll(cache, 0o755) == nil {
			os.WriteFile(path, b, 0o644)
		}
	}
	return string(b), nil
}

// rfcHeading is a section heading of an RFC's text form: "13.2.  Title"
// at the start of a line.
var rfcHeading = regexp.MustCompile(`^(\d+(?:\.\d+)*)\.\s+\S`)

// rfcPageLine is the footer and header the text form repeats at page
// breaks.
var rfcPageLine = regexp.MustCompile(`(\[Page \d+\]\s*$)|(^RFC \d+ .* \d{4}\s*$)|\f`)

// Section is the text of section sec of an RFC's text form, with its
// subsections, without the page headers and footers; "" when absent.
func Section(rfc, sec string) string {
	var out []string
	in := false
	for _, l := range strings.Split(rfc, "\n") {
		if m := rfcHeading.FindStringSubmatch(l); m != nil {
			n := m[1]
			switch {
			case n == sec:
				in = true
			case in && !strings.HasPrefix(n, sec+"."):
				return strings.TrimSpace(strings.Join(out, "\n"))
			}
		}
		if in && !rfcPageLine.MatchString(l) {
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
