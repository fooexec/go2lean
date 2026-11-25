package mine

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// C sources (#315 on C projects): the comments, for the RFC citations, and
// the function definitions, for the targets a candidate names. No C
// parser: a function definition is a header ending in ")" followed by a
// "{" at the start of a line, the style of C codebases such as FRR, the
// Linux kernel or cJSON.

// Language is the project's language as the mining steps see it: "Go" for
// a module (a go.mod), else "C".
func Language(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return "Go"
	}
	return "C"
}

func isCSource(p string) bool {
	return strings.HasSuffix(p, ".c") || strings.HasSuffix(p, ".h")
}

// cTestPath: a C file of the tests, not of the code.
func cTestPath(p string) bool {
	for _, d := range strings.Split(filepath.ToSlash(p), "/") {
		if d == "tests" || d == "test" || d == "testsuite" || d == "fuzz" {
			return true
		}
	}
	return false
}

// cSources walks dir for the project's C files, tests excluded.
func cSources(dir string, f func(rel string, src []byte)) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			if p != dir && (skipDir(d.Name()) || cTestPath(rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isCSource(p) {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		f(filepath.ToSlash(rel), src)
		return nil
	})
}

// cComment is a comment of a C file and the line it starts on.
type cComment struct {
	line int
	text string
}

// cComments lists the comments of a C source: /* ... */ and // ...,
// consecutive // lines as one; string and character literals are skipped.
func cComments(src []byte) []cComment {
	var out []cComment
	line := 1
	lastLine := -1 // the line of the last // comment, to join a run of them
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '\n':
			line++
		case c == '"' || c == '\'':
			for i++; i < len(src) && src[i] != c; i++ {
				if src[i] == '\\' {
					i++
				} else if src[i] == '\n' {
					line++
					break
				}
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			start := line
			j := i + 2
			for ; j+1 < len(src) && !(src[j] == '*' && src[j+1] == '/'); j++ {
				if src[j] == '\n' {
					line++
				}
			}
			out = append(out, cComment{start, cleanComment(string(src[min(i+2, len(src)):min(j, len(src))]))})
			i = j + 1
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			j := i + 2
			for j < len(src) && src[j] != '\n' {
				j++
			}
			text := strings.TrimSpace(string(src[i+2 : j]))
			if lastLine == line-1 && len(out) > 0 {
				out[len(out)-1].text += "\n" + text
			} else {
				out = append(out, cComment{line, text})
			}
			lastLine = line
			i = j - 1
		}
	}
	return out
}

// cleanComment drops the leading " * " of a block comment's lines.
func cleanComment(s string) string {
	ls := strings.Split(s, "\n")
	for i, l := range ls {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "*")
		ls[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(strings.Join(ls, "\n"))
}

var cIdentBeforeParen = regexp.MustCompile(`([A-Za-z_]\w*)\s*\($`)

// cFunctions lists the functions a C source defines: a line that starts
// with "{" (or a header line that ends with ") {"), whose header, back to
// the previous ';', '}' or preprocessor line, names a function before its
// parameter list. Static functions are listed too: a spec that includes
// the file can call them.
func cFunctions(src []byte) []string {
	var out []string
	for _, d := range cDefs(src) {
		out = append(out, d.name)
	}
	return out
}

// cDef is a function definition: its name, its header (the lines before
// the body, joined) and the line its header starts on (0-based).
type cDef struct {
	name, header string
	start        int
}

// cDefs lists the function definitions of a C source, as cFunctions.
func cDefs(src []byte) []cDef {
	lines := strings.Split(string(stripComments(src)), "\n")
	var out []cDef
	seen := map[string]bool{}
	for i, l := range lines {
		var header []string
		start := i
		switch {
		case strings.HasPrefix(l, "{"):
			for j := i - 1; j >= 0 && len(header) < 12; j-- {
				t := strings.TrimSpace(lines[j])
				if t == "" || strings.HasPrefix(t, "#") || strings.HasSuffix(t, ";") || strings.HasSuffix(t, "}") {
					break
				}
				header = append([]string{t}, header...)
				start = j
			}
		case !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") && strings.HasSuffix(strings.TrimSpace(l), ") {"):
			header = []string{strings.TrimSuffix(strings.TrimSpace(l), "{")}
		default:
			continue
		}
		h := cAttribute.ReplaceAllString(strings.Join(header, " "), " ")
		if h == "" || strings.Contains(h, "=") || strings.HasPrefix(h, "struct ") && !strings.Contains(h, "(") {
			continue
		}
		open := strings.Index(h, "(")
		if open < 0 {
			continue
		}
		m := cIdentBeforeParen.FindStringSubmatch(h[:open+1])
		if m == nil || cKeyword[m[1]] {
			continue
		}
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, cDef{m[1], strings.Join(header, " "), start})
		}
	}
	return out
}

// cSpecsByCandidate reads a C spec file: the spec functions (marked
// annotate("go2lean:spec"), directly or through a SPEC macro) each
// candidate's comment above them names.
func cSpecsByCandidate(src []byte) map[string][]string {
	lines := strings.Split(string(src), "\n")
	out := map[string][]string{}
	for _, d := range cDefs(src) {
		if !strings.Contains(d.header, "SPEC") && !strings.Contains(d.header, "go2lean:spec") {
			continue
		}
		// The comment just above the header.
		var doc []string
		for k := d.start - 1; k >= 0; k-- {
			t := strings.TrimSpace(lines[k])
			if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*") || strings.HasSuffix(t, "*/") {
				doc = append(doc, t)
				continue
			}
			break
		}
		for _, m := range candidateMark.FindAllStringSubmatch(strings.Join(doc, " "), -1) {
			if !contains(out[m[1]], d.name) {
				out[m[1]] = append(out[m[1]], d.name)
			}
		}
	}
	return out
}

// cAttribute is a GCC/clang attribute clause, __attribute__((...)), whose
// parentheses would otherwise be read as a parameter list.
var cAttribute = regexp.MustCompile(`__attribute__\s*\(\((?:[^()]|\([^()]*\))*\)\)`)

var cKeyword = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "return": true, "sizeof": true, "defined": true}

// stripComments blanks the comments of a C source, keeping its lines.
func stripComments(src []byte) []byte {
	out := []byte(string(src))
	for i := 0; i+1 < len(out); i++ {
		if out[i] == '/' && out[i+1] == '*' {
			for j := i; j < len(out); j++ {
				if j+1 < len(out) && out[j] == '*' && out[j+1] == '/' {
					out[j], out[j+1] = ' ', ' '
					i = j + 1
					break
				}
				if out[j] != '\n' {
					out[j] = ' '
				}
			}
		} else if out[i] == '/' && out[i+1] == '/' {
			for ; i < len(out) && out[i] != '\n'; i++ {
				out[i] = ' '
			}
		}
	}
	return out
}
