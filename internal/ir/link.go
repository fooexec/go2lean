package ir

import (
	"fmt"
	"slices"

	"github.com/fooexec/go2lean/internal/contract"
)

// Link merges main with the programs it reaches through cross-package
// calls and func values (deps, by package path) into one program, as the
// search driver's `link` does in Lean (Go2Lean.Driver): main first, so its
// function ids and havoc sites keep their numbers, then every program it
// reaches, renumbered; a cross-package call into a linked program becomes
// a plain call. A call into a package deps does not hold stays a
// cross-package call (unmodelled). key is the registry key of a package
// (what an XFuncRef names).
//
// A theorem about the linked program is about the dependencies' models
// too, so the callees the search translates from source are not havoc.
func Link(main *Prog, deps map[string]*Prog, key func(pkg string) string) (*Prog, error) {
	order := []string{main.Package}
	byKey := map[string]string{}
	for pkg := range deps {
		byKey[key(pkg)] = pkg
	}
	for i := 0; i < len(order); i++ {
		p := main
		if i > 0 {
			p = deps[order[i]]
		}
		for _, pkg := range reached(p, byKey) {
			if _, ok := deps[pkg]; ok && !slices.Contains(order, pkg) {
				order = append(order, pkg)
			}
		}
	}
	type base struct{ f, h int }
	bases := map[string]base{}
	fb, hb := 0, 0
	for i, pkg := range order {
		p := main
		if i > 0 {
			p = deps[pkg]
			if p.JSONTable != "" {
				return nil, fmt.Errorf("link %s: %s encodes JSON, whose tables are per package", main.Package, pkg)
			}
		}
		bases[pkg] = base{fb, hb}
		fb += len(p.Funcs)
		hb += len(p.HavocWidths)
	}
	out := &Prog{Package: main.Package, Frontend: main.Frontend, Dependency: main.Dependency,
		SchedSite: main.SchedSite, HasSched: main.HasSched, JSONTable: main.JSONTable}
	for i, pkg := range order {
		p := main
		if i > 0 {
			p = deps[pkg]
		}
		b := bases[pkg]
		for _, f := range p.Funcs {
			out.Funcs = append(out.Funcs, shiftFunc(f, b.f, b.h, func(callee string) (int, bool) {
				if c, ok := bases[callee]; ok {
					return c.f, true
				}
				return 0, false
			}, func(k string) (int, bool) {
				if pkg, ok := byKey[k]; ok {
					if c, ok := bases[pkg]; ok {
						return c.f, true
					}
				}
				return 0, false
			}))
		}
		out.HavocWidths = append(out.HavocWidths, p.HavocWidths...)
		for _, h := range p.Havoc {
			h.Idx += b.h
			out.Havoc = append(out.Havoc, h)
		}
		if i > 0 && p.HasSched && !out.HasSched {
			out.SchedSite, out.HasSched = p.SchedSite+b.h, true
		}
	}
	return out, out.Validate()
}

// reached lists the packages p calls into or names a function of.
func reached(p *Prog, byKey map[string]string) []string {
	var out []string
	add := func(pkg string) {
		if pkg != "" && !slices.Contains(out, pkg) {
			out = append(out, pkg)
		}
	}
	for _, f := range p.Funcs {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if x, ok := in.Op.(XFuncRef); ok {
					add(byKey[x.Key])
				}
			}
			if b.Term.Kind == TermXCall {
				add(b.Term.CalleePkg)
			}
		}
	}
	return out
}

// shiftFunc renumbers a function for the merged program: function ids by
// fbase, havoc sites by hbase, and a cross-package call or func value into
// a linked package becomes a plain one.
func shiftFunc(f *Func, fbase, hbase int, pkgBase, keyBase func(string) (int, bool)) *Func {
	g := *f
	g.Blocks = make([]Block, len(f.Blocks))
	g.Calls = nil
	for _, c := range f.Calls {
		g.Calls = append(g.Calls, c+fbase)
	}
	g.XCalls = nil
	for bi, b := range f.Blocks {
		nb := b
		nb.Instrs = make([]Instr, len(b.Instrs))
		for ii, in := range b.Instrs {
			switch op := in.Op.(type) {
			case nil:
				if in.Probe == "" && in.Assume == "" {
					in.HavocSite += hbase
				}
			case FuncRef:
				in.Op = FuncRef{ID: op.ID + fbase}
			case XFuncRef:
				if base, ok := keyBase(op.Key); ok {
					in.Op = FuncRef{ID: op.ID + base}
				}
			}
			nb.Instrs[ii] = in
		}
		switch nb.Term.Kind {
		case TermCall:
			nb.Term.Callee += fbase
		case TermXCall:
			if base, ok := pkgBase(nb.Term.CalleePkg); ok {
				nb.Term.Kind, nb.Term.Callee, nb.Term.CalleePkg = TermCall, nb.Term.Callee+base, ""
				g.Calls = append(g.Calls, nb.Term.Callee)
			} else {
				g.XCalls = append(g.XCalls, contract.XCall{Package: nb.Term.CalleePkg, ID: nb.Term.Callee})
			}
		}
		g.Blocks[bi] = nb
	}
	return &g
}
