package gossa

import (
	_ "embed"
	"fmt"
	"go/ast"
	"go/parser"
	"go/types"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// netloopPath is the import path the loopback model is checked under.
const netloopPath = "go2lean/netloop"

//go:embed netloop/netloop.go
var netloopSrc string

// netloopCalls are the net functions whose calls go to the loopback model
// instead (#126).
var netloopCalls = map[string]string{"net.Listen": "Listen", "net.Dial": "Dial"}

// osmodelPath is the import path the environment and temporary-directory
// model is checked under.
const osmodelPath = "go2lean/osmodel"

//go:embed osmodel/osmodel.go
var osmodelSrc string

// osmodelCalls are the os functions whose calls go to package osmodel: the
// environment the program writes reads back (eval 7's k009), and
// MkdirTemp returns a fresh path (k023, k042).
var osmodelCalls = map[string]string{
	"os.Getenv": "Getenv", "os.LookupEnv": "LookupEnv", "os.Setenv": "Setenv",
	"os.Unsetenv": "Unsetenv", "os.Clearenv": "Clearenv", "os.MkdirTemp": "MkdirTemp",
}

// shimPaths are the models added as Go source; their globals are shared
// by every caller, with or without goroutines (planCells).
var shimPaths = map[string]bool{netloopPath: true, osmodelPath: true}

// addNetLoop adds the loopback model (package netloop) to a program that
// calls net.Listen or net.Dial, and makes those calls call it: a listener
// in a table, a connection a net.Pipe pair through its backlog. Both ends
// are then the program's own code over channels.
func addNetLoop(sprog *ssa.Program) error {
	return addShim(sprog, "net", netloopCalls, netloopPath, netloopSrc)
}

// addOSModel adds package osmodel to a program that calls the os functions
// it models, and makes those calls call it.
func addOSModel(sprog *ssa.Program) error {
	return addShim(sprog, "os", osmodelCalls, osmodelPath, osmodelSrc)
}

// addShim type-checks src as package path into the program when it calls
// any of the functions of package pkg named in calls, and points those
// calls at the shim's functions. The shim's own calls of them are left
// alone: they read what the model does not know.
func addShim(sprog *ssa.Program, pkg string, calls map[string]string, path, src string) error {
	stdPkg := sprog.ImportedPackage(pkg)
	if stdPkg == nil {
		return nil
	}
	targets := map[*ssa.Function]string{}
	for name, shim := range calls {
		if f := stdPkg.Func(name[len(pkg)+1:]); f != nil {
			targets[f] = shim
		}
	}
	type site struct {
		c    *ssa.CallCommon
		shim string
	}
	var sites []site
	for f := range ssautil.AllFunctions(sprog) {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				c, ok := in.(ssa.CallInstruction)
				if !ok {
					continue
				}
				if callee, ok := c.Common().Value.(*ssa.Function); ok && targets[callee] != "" {
					sites = append(sites, site{c.Common(), targets[callee]})
				}
			}
		}
	}
	if len(sites) == 0 {
		return nil
	}
	file, err := parser.ParseFile(sprog.Fset, "go2lean_"+path[len("go2lean/"):]+".go", src, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	conf := types.Config{Importer: importerFunc(func(ipath string) (*types.Package, error) {
		if p := sprog.ImportedPackage(ipath); p != nil {
			return p.Pkg, nil
		}
		return nil, fmt.Errorf("%s: package %s is not in the program", path, ipath)
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
	tpkg, err := conf.Check(path, sprog.Fset, []*ast.File{file}, info)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	spkg := sprog.CreatePackage(tpkg, []*ast.File{file}, info, true)
	spkg.Build()
	for _, s := range sites {
		s.c.Value = spkg.Func(s.shim)
	}
	return nil
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
