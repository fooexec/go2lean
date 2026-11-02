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

// addNetLoop adds the loopback model (package netloop) to a program that
// calls net.Listen or net.Dial, and makes those calls call it: a listener
// in a table, a connection a net.Pipe pair through its backlog. Both ends
// are then the program's own code over channels.
func addNetLoop(sprog *ssa.Program) error {
	netPkg := sprog.ImportedPackage("net")
	if netPkg == nil {
		return nil
	}
	targets := map[*ssa.Function]string{}
	for name, shim := range netloopCalls {
		if f := netPkg.Func(name[len("net."):]); f != nil {
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
	file, err := parser.ParseFile(sprog.Fset, "go2lean_netloop.go", netloopSrc, 0)
	if err != nil {
		return fmt.Errorf("netloop: %w", err)
	}
	conf := types.Config{Importer: importerFunc(func(path string) (*types.Package, error) {
		if p := sprog.ImportedPackage(path); p != nil {
			return p.Pkg, nil
		}
		return nil, fmt.Errorf("netloop: package %s is not in the program", path)
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
	tpkg, err := conf.Check(netloopPath, sprog.Fset, []*ast.File{file}, info)
	if err != nil {
		return fmt.Errorf("netloop: %w", err)
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
