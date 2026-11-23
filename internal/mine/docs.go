package mine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// normative marks a sentence that states what the code guarantees: a
// contract a spec can check.
var normative = regexp.MustCompile(`(?i)\b(must( not)?|never|always|at most|at least|guarantee[sd]?|is guaranteed|returns? (an? )?(error|nil|false|true)\b[^.]*\b(if|when|unless)|panics? (if|when)|is (safe|not safe) for concurrent|should not|may not|is not allowed|is rejected|are rejected|is limited to|no more than|is idempotent|in constant time)\b`)

// readmeNormative is the stricter test for a README, whose prose
// ("always happy to welcome contributors", "aims to support") is mostly
// not a contract: only must, shall, never and rejections.
var readmeNormative = regexp.MustCompile(`(?i)\b(must|shall|never|is rejected|are rejected|returns an error (if|when))\b`)

// skipDir are directories that hold no code of the project's own.
func skipDir(name string) bool {
	return name == "vendor" || name == "testdata" || name == "node_modules" || (strings.HasPrefix(name, ".") && name != ".")
}

// goSources walks dir for the project's Go files, tests excluded.
func goSources(dir string, f func(path string, file *ast.File, fset *token.FileSet)) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		f(rel, file, fset)
		return nil
	})
}

// Docs lists the normative sentences of the package and exported
// declaration doc comments, and of the README: each with the declaration
// it documents.
func Docs(dir string) ([]Evidence, error) {
	var ev []Evidence
	add := func(where, decl, doc string) {
		test := normative
		if strings.HasPrefix(where, "README") {
			test = readmeNormative
		}
		for _, s := range sentences(doc) {
			if test.MatchString(s) {
				title := s
				if decl != "" {
					title = decl + ": " + s
				}
				ev = append(ev, Evidence{Kind: KindDoc, Ref: where, Title: clip(title, 200), Text: clip(doc, 2000), Paths: []string{strings.SplitN(where, ":", 2)[0]}})
			}
		}
	}
	err := goSources(dir, func(rel string, file *ast.File, fset *token.FileSet) {
		if file.Doc != nil {
			add(rel+":"+strconv.Itoa(fset.Position(file.Doc.Pos()).Line), "package "+file.Name.Name, file.Doc.Text())
		}
		for _, d := range file.Decls {
			var doc *ast.CommentGroup
			var name string
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				doc, name = d.Doc, d.Name.Name
				if d.Recv != nil && len(d.Recv.List) > 0 {
					name = recvName(d.Recv.List[0].Type) + "." + name
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.IsExported() {
						doc, name = ts.Doc, ts.Name.Name
						if doc == nil {
							doc = d.Doc
						}
					}
				}
			}
			if doc != nil {
				add(rel+":"+strconv.Itoa(fset.Position(doc.Pos()).Line), name, doc.Text())
			}
		}
	})
	if err != nil {
		return nil, err
	}
	for _, n := range []string{"README.md", "README"} {
		if b, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
			line := 1
			for _, para := range strings.Split(string(b), "\n\n") {
				add(n+":"+strconv.Itoa(line), "", para)
				line += strings.Count(para, "\n") + 2
			}
			break
		}
	}
	return ev, nil
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	}
	return "?"
}

var sentenceEnd = regexp.MustCompile(`([.!?])\s+`)

// sentences splits a comment into sentences, joining wrapped lines.
func sentences(doc string) []string {
	doc = strings.Join(strings.Fields(doc), " ")
	var out []string
	for _, s := range strings.Split(sentenceEnd.ReplaceAllString(doc, "$1\x00"), "\x00") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
