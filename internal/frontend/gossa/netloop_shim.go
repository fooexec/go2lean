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

// randmodelPath is the import path crypto/rand's model is checked under.
const randmodelPath = "go2lean/randmodel"

//go:embed randmodel/randmodel.go
var randmodelSrc string

// randmodelCalls and randmodelVars: crypto/rand.Read and the reader
// crypto/rand.Reader never fail (k023: nkeys reads a server's seed with
// io.ReadFull(rand.Reader, ...), and a havoc error made a spurious run).
var (
	randmodelCalls = map[string]string{"crypto/rand.Read": "Read"}
	randmodelVars  = map[string]string{"crypto/rand.Reader": "Reader"}
)

// shimPaths are the models added as Go source; their globals are shared
// by every caller, with or without goroutines (planCells).
var shimPaths = map[string]bool{netloopPath: true, osmodelPath: true, randmodelPath: true}

// addNetLoop adds the loopback model (package netloop) to a program that
// calls net.Listen or net.Dial, and makes those calls call it: a listener
// in a table, a connection a net.Pipe pair through its backlog. Both ends
// are then the program's own code over channels.
func addNetLoop(sprog *ssa.Program) error {
	return addShim(sprog, "net", netloopCalls, nil, netloopPath, netloopSrc)
}

// addOSModel adds package osmodel to a program that calls the os functions
// it models, and makes those calls call it.
func addOSModel(sprog *ssa.Program) error {
	return addShim(sprog, "os", osmodelCalls, nil, osmodelPath, osmodelSrc)
}

// addRandModel adds package randmodel to a program that uses crypto/rand's
// Read or Reader.
func addRandModel(sprog *ssa.Program) error {
	return addShim(sprog, "crypto/rand", randmodelCalls, randmodelVars, randmodelPath, randmodelSrc)
}

// addShim type-checks src as package path into the program when it calls
// any of the functions of package pkg named in calls, or reads any of its
// variables named in vars, and points those calls and reads at the shim's
// functions and variables (of the same types). The shim's own calls of
// them are left alone: they read what the model does not know.
func addShim(sprog *ssa.Program, pkg string, calls, vars map[string]string, path, src string) error {
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
	globals := map[*ssa.Global]string{}
	for name, shim := range vars {
		if g := stdPkg.Var(name[len(pkg)+1:]); g != nil {
			globals[g] = shim
		}
	}
	type site struct {
		c    *ssa.CallCommon
		shim string
	}
	type use struct {
		op   *ssa.Value
		shim string
	}
	var sites []site
	var uses []use
	for f := range ssautil.AllFunctions(sprog) {
		if f.Pkg == stdPkg {
			continue
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if c, ok := in.(ssa.CallInstruction); ok {
					if callee, ok := c.Common().Value.(*ssa.Function); ok && targets[callee] != "" {
						sites = append(sites, site{c.Common(), targets[callee]})
					}
				}
				if len(globals) > 0 {
					for _, op := range in.Operands(nil) {
						if g, ok := (*op).(*ssa.Global); ok && globals[g] != "" {
							uses = append(uses, use{op, globals[g]})
						}
					}
				}
			}
		}
	}
	if len(sites) == 0 && len(uses) == 0 {
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
	for _, u := range uses {
		*u.op = spkg.Var(u.shim)
	}
	return nil
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
