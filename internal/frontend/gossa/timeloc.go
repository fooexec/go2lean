package gossa

import (
	"go/types"
	"hash/fnv"

	"golang.org/x/tools/go/ssa"

	"github.com/fooexec/go2lean/internal/ir"
)

// Package time's two built-in locations, utcLoc and localLoc, which its
// modelled functions (modelledTime, #262) compare by address
// (`loc == &utcLoc`, `l == &localLoc`) and which time.UTC and time.Local
// point to. Each is a cell of the model, so every use of its address is the
// same object, starting at a zero Location: a zone lookup with no zones
// gives "UTC" at offset 0, which is what Go does for utcLoc. utcLoc's name
// field stays "" (only Location.String reads it, not modelled), and
// localLoc's zones are those initLocal loads, an unmodelled call that marks
// any run reaching it.

// timeLocation gives the cell of utcLoc or localLoc.
func timeLocation(g *ssa.Global) (uint32, ir.Val, bool) {
	if g.Pkg == nil || g.Pkg.Pkg.Path() != "time" || (g.Name() != "utcLoc" && g.Name() != "localLoc") {
		return 0, nil, false
	}
	zero, ok := zeroVal(g.Type().(*types.Pointer).Elem())
	if !ok {
		return 0, nil, false
	}
	h := fnv.New32a()
	h.Write([]byte("time." + g.Name()))
	return h.Sum32(), zero, true
}

// timeLocAlias gives the location time.UTC or time.Local points to.
func timeLocAlias(g *ssa.Global) (*ssa.Global, bool) {
	if g.Pkg == nil || g.Pkg.Pkg.Path() != "time" {
		return nil, false
	}
	var name string
	switch g.Name() {
	case "UTC":
		name = "utcLoc"
	case "Local":
		name = "localLoc"
	default:
		return nil, false
	}
	loc, ok := g.Pkg.Members[name].(*ssa.Global)
	return loc, ok
}
