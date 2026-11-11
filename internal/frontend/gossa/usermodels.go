package gossa

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// usermodelsPath is the import path the user's models are checked under.
const usermodelsPath = "go2lean/usermodels"

func init() {
	// Translated like the built-in shims, and their package variables are
	// cells every caller shares.
	modelledStdlib[usermodelsPath] = true
	shimPaths[usermodelsPath] = true
}

// UserModelsDir is where run looks for user models in a module when no
// directory is given (#204): the go tool skips a directory whose name
// starts with a dot, so ./... does not take the models for a package.
const UserModelsDir = ".go2lean/models"

// modelDirective marks a user model: //go2lean:model <target>, the target
// a function (pkg/path.Func) or a method (pkg/path.(*T).Method or
// pkg/path.T.Method).
const modelDirective = "//go2lean:model "

// addUserModels type-checks the Go files of dir (#204) into the program,
// and points every static call of each function or method a model names
// at the model. A method's receiver is its model's first parameter. Calls
// from the models themselves are left alone, so a model can call what it
// stands for. Replay runs the real code: a model only shapes the search.
func addUserModels(sprog *ssa.Program, dir string) (int, error) {
	if dir == "" {
		return 0, nil
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(names) == 0 {
		return 0, err
	}
	sort.Strings(names)
	var files []*ast.File
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		src, err := os.ReadFile(n)
		if err != nil {
			return 0, err
		}
		f, err := parser.ParseFile(sprog.Fset, n, src, parser.ParseComments)
		if err != nil {
			return 0, fmt.Errorf("user models: %w", err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return 0, nil
	}
	conf := types.Config{Importer: importerFunc(func(ipath string) (*types.Package, error) {
		if p := sprog.ImportedPackage(ipath); p != nil {
			return p.Pkg, nil
		}
		return nil, fmt.Errorf("package %s is not in the program (the module does not use it)", ipath)
	})}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes:     map[ast.Node]*types.Scope{},
		Instances:  map[*ast.Ident]types.Instance{},
	}
	tpkg, err := conf.Check(usermodelsPath, sprog.Fset, files, info)
	if err != nil {
		return 0, fmt.Errorf("user models in %s: %w", dir, err)
	}
	spkg := sprog.CreatePackage(tpkg, files, info, true)
	spkg.Build()
	redirect := map[*ssa.Function]*ssa.Function{}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Doc == nil || fd.Recv != nil {
				continue
			}
			for _, c := range fd.Doc.List {
				spec, ok := strings.CutPrefix(c.Text, modelDirective)
				if !ok {
					continue
				}
				spec = strings.TrimSpace(spec)
				pos := sprog.Fset.Position(c.Pos())
				target, err := modelTarget(sprog, spec)
				if err != nil {
					return 0, fmt.Errorf("%s: go2lean:model %s: %w", pos, spec, err)
				}
				model := spkg.Func(fd.Name.Name)
				if err := sameSignature(target, model); err != nil {
					return 0, fmt.Errorf("%s: go2lean:model %s: %w", pos, spec, err)
				}
				if prev, dup := redirect[target]; dup {
					return 0, fmt.Errorf("%s: go2lean:model %s: already modelled by %s", pos, spec, prev.Name())
				}
				redirect[target] = model
			}
		}
	}
	for f := range ssautil.AllFunctions(sprog) {
		if f.Pkg == spkg {
			continue
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				c, ok := in.(ssa.CallInstruction)
				if !ok {
					continue
				}
				if callee, ok := c.Common().Value.(*ssa.Function); ok {
					if m := redirect[callee]; m != nil {
						c.Common().Value = m
					}
				}
			}
		}
	}
	return len(redirect), nil
}

// modelTarget resolves a go2lean:model target to its function.
func modelTarget(sprog *ssa.Program, spec string) (*ssa.Function, error) {
	slash := strings.LastIndex(spec, "/")
	dot := strings.Index(spec[slash+1:], ".")
	if dot < 0 {
		return nil, fmt.Errorf("want pkg/path.Func, pkg/path.(*T).Method or pkg/path.T.Method")
	}
	path, rest := spec[:slash+1+dot], spec[slash+1+dot+1:]
	p := sprog.ImportedPackage(path)
	if p == nil {
		return nil, fmt.Errorf("package %s is not in the program (the module does not use it)", path)
	}
	ptr := false
	if strings.HasPrefix(rest, "(") {
		end := strings.Index(rest, ")")
		if end < 0 {
			return nil, fmt.Errorf("unbalanced parenthesis")
		}
		recv := rest[1:end]
		ptr = strings.HasPrefix(recv, "*")
		rest = strings.TrimPrefix(recv, "*") + rest[end+1:]
	}
	typ, method, isMethod := strings.Cut(rest, ".")
	if !isMethod {
		if f := p.Func(typ); f != nil {
			return f, nil
		}
		return nil, fmt.Errorf("no function %s in %s", typ, path)
	}
	tn, ok := p.Pkg.Scope().Lookup(typ).(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("no type %s in %s", typ, path)
	}
	var t types.Type = tn.Type()
	if ptr {
		t = types.NewPointer(t)
	}
	sel := sprog.MethodSets.MethodSet(t).Lookup(p.Pkg, method)
	if sel == nil {
		return nil, fmt.Errorf("no method %s in the method set of %s", method, t)
	}
	f := sprog.MethodValue(sel)
	if f == nil {
		return nil, fmt.Errorf("method %s of %s has no body to replace", method, t)
	}
	return f, nil
}

// sameSignature checks that model can stand for target: the same
// parameters (a method's receiver first) and results.
func sameSignature(target, model *ssa.Function) error {
	ts := target.Signature
	var params []*types.Var
	if r := ts.Recv(); r != nil {
		params = append(params, r)
	}
	for i := 0; i < ts.Params().Len(); i++ {
		params = append(params, ts.Params().At(i))
	}
	want := types.NewSignatureType(nil, nil, nil, types.NewTuple(params...), ts.Results(), ts.Variadic())
	got := model.Signature
	if !types.Identical(want, got) {
		return fmt.Errorf("the model is %s, want %s", got, want)
	}
	return nil
}
