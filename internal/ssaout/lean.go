// Package ssaout prints an ir.Prog as a Lean module over the GoSSA dialect
// and writes M1's side records (ABI, model report, targets).
package ssaout

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andreasfoo/go2lean/internal/contract"
	"github.com/andreasfoo/go2lean/internal/ir"
)

// ModuleName is the Lean module a package's model is written to. The full
// import path is folded into one identifier so packages with the same base
// name get distinct modules.
func ModuleName(frontend, pkgPath string) string {
	return "Go2Lean.Generated." + frontend + "." + contract.PackageKey(pkgPath)
}

// RegistryKey is the name under which the driver's registry holds a
// package's model: the module name without the Go2Lean.Generated prefix.
func RegistryKey(frontend, pkgPath string) string {
	return strings.TrimPrefix(ModuleName(frontend, pkgPath), "Go2Lean.Generated.")
}

// ModuleFile is the path of a module relative to the lake project.
func ModuleFile(module string) string {
	return strings.ReplaceAll(module, ".", string(filepath.Separator)) + ".lean"
}

func vars(vs []ir.Var) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = fmt.Sprint(int(v))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func term(frontend string, t ir.Term) string {
	switch t.Kind {
	case ir.TermRet:
		return fmt.Sprintf(".ret %d", t.V)
	case ir.TermJump:
		return fmt.Sprintf(".jump %d %s", t.B, vars(t.Args))
	case ir.TermBr:
		return fmt.Sprintf(".br %d %d %s %d %s", t.Cond, t.T, vars(t.TArgs), t.F, vars(t.FArgs))
	case ir.TermPanic:
		return ".panic ." + panicCtor(t.Panic)
	case ir.TermCall:
		return fmt.Sprintf(".call %d %s %d %d %s %d", t.Callee, vars(t.CArgs), t.Dst, t.Next, vars(t.NArgs), t.Fallback)
	case ir.TermCallV:
		return fmt.Sprintf(".callv %d %s %d %d %s %d", t.FV, vars(t.CArgs), t.Dst, t.Next, vars(t.NArgs), t.Fallback)
	case ir.TermXCall:
		return fmt.Sprintf(".xcall %q %d %s %d %d %s %d", RegistryKey(frontend, t.CalleePkg), t.Callee, vars(t.CArgs), t.Dst, t.Next, vars(t.NArgs), t.Fallback)
	case ir.TermSpawn:
		return fmt.Sprintf(".spawn %d %s %d", t.FV, vars(t.CArgs), t.Next)
	case ir.TermSync:
		return fmt.Sprintf(".sync %s %s %d %d", t.Sync.Lean(), vars(t.CArgs), t.Dst, t.Next)
	}
	panic("unknown terminator")
}

func panicCtor(k contract.PanicKind) string {
	switch k {
	case contract.PanicIndexOutOfRange:
		return "indexOutOfRange"
	case contract.PanicDivideByZero:
		return "divideByZero"
	case contract.PanicNilDeref:
		return "nilDereference"
	case contract.PanicNegativeShift:
		return "negativeShift"
	case contract.PanicSliceBounds:
		return "sliceBounds"
	case contract.PanicMakeSliceLen:
		return "makeSliceLen"
	case contract.PanicTypeAssertion:
		return "typeAssertion"
	case contract.PanicNilMapWrite:
		return "nilMapWrite"
	case contract.PanicExplicit:
		return "explicit"
	}
	return "other"
}

// EnsureProject makes leanDir (a workspace's `lean/`) the lake project its
// models live in: a lakefile requiring go2lean's core library (coreDir, the
// checkout's `lean/`) by path, the core's toolchain, and the search
// driver's main module. Each workspace builds and runs its own driver, so
// workspaces do not share generated files and can run side by side.
func EnsureProject(leanDir, coreDir string) error {
	core, err := filepath.Abs(coreDir)
	if err != nil {
		return err
	}
	toolchain, err := os.ReadFile(filepath.Join(core, "lean-toolchain"))
	if err != nil {
		return err
	}
	files := map[string]string{
		"lakefile.toml": fmt.Sprintf(`# Written by go2lean: this workspace's models and search driver.
name = "go2lean-models"
defaultTargets = ["go2lean-search"]

[[require]]
name = "go2lean"
path = %q

[[lean_lib]]
name = "Go2LeanModels"
globs = ["Go2Lean.Generated.+"]

[[lean_exe]]
name = "go2lean-search"
root = "Main"
`, core),
		"lean-toolchain": string(toolchain),
		"Main.lean": `-- Written by go2lean: the search driver over this workspace's models.
import Go2Lean.Driver
import Go2Lean.Generated.Registry

def main (args : List String) : IO UInt32 :=
  Go2Lean.Search.driverMain Go2Lean.Generated.registry Go2Lean.Generated.jsonTables args
`,
	}
	if err := os.MkdirAll(filepath.Join(leanDir, "Go2Lean", "Generated", "gossa"), 0o755); err != nil {
		return err
	}
	for name, content := range files {
		path := filepath.Join(leanDir, name)
		if old, err := os.ReadFile(path); err == nil && string(old) == content {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		if name == "lakefile.toml" {
			// lake keeps resolving a path dependency from its manifest, so a
			// lakefile pointing at a new core snapshot would still build
			// against the old one: drop the manifest and let lake rewrite it.
			if err := os.Remove(filepath.Join(leanDir, "lake-manifest.json")); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return WriteRegistry(leanDir)
}

// WriteRegistry lists every generated model in Generated/Registry.lean,
// which the native search driver links so one executable serves all
// packages. It is regenerated on every emit.
func WriteRegistry(leanDir string) error {
	dir := filepath.Join(leanDir, "Go2Lean", "Generated", "gossa")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var mods []string
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, ".lean") {
			mods = append(mods, strings.TrimSuffix(name, ".lean"))
		}
	}
	sort.Strings(mods)
	var b strings.Builder
	b.WriteString("-- Generated by go2lean: every model under Generated/gossa. Do not edit.\n\n")
	for _, m := range mods {
		fmt.Fprintf(&b, "import Go2Lean.Generated.gossa.%s\n", m)
	}
	b.WriteString("\nnamespace Go2Lean.Generated\n\n/-- Model programs by package key. -/\ndef registry : List (String × Go2Lean.GoSSA.Prog) := [\n")
	for i, m := range mods {
		sep := ","
		if i == len(mods)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "  (\"gossa.%s\", Go2Lean.Generated.gossa.%s.prog)%s\n", m, m, sep)
	}
	b.WriteString("]\n\n/-- Each model's JSON table (SDK values generated as decoded documents). -/\ndef jsonTables : List (String × Go2Lean.GoSSA.JTable) := [\n")
	for i, m := range mods {
		sep := ","
		if i == len(mods)-1 {
			sep = ""
		}
		// A model generated before tables were always emitted has none.
		table := "{ descs := #[], zeros := #[] }"
		if src, err := os.ReadFile(filepath.Join(dir, m+".lean")); err == nil && (strings.Contains(string(src), "\ndef jsonTable ") || strings.HasPrefix(string(src), "import Go2Lean.Generated.gossa."+m+".Part0\n")) {
			table = "Go2Lean.Generated.gossa." + m + ".jsonTable"
		}
		fmt.Fprintf(&b, "  (\"gossa.%s\", %s)%s\n", m, table, sep)
	}
	b.WriteString("]\n\nend Go2Lean.Generated\n")
	return os.WriteFile(filepath.Join(leanDir, "Go2Lean", "Generated", "Registry.lean"), []byte(b.String()), 0o644)
}

// Print renders the program as a Lean module.
func Print(p *ir.Prog, module string) string {
	var b strings.Builder
	header(&b, p, module, "import Go2Lean.Dialect.GoSSA")
	printTable(&b, p)
	for i := range p.Funcs {
		printFunc(&b, p, i)
	}
	printProg(&b, p, module)
	return b.String()
}

// splitBytes is the model size above which a package is written as part
// modules, and partBytes the size of a part. Lean elaborates and compiles
// a module in one process, and its memory grows with the module: nats-server's
// 35 MB model needed more than 13 GB (#45). Smaller models stay one file.
const (
	splitBytes = 4 << 20
	partBytes  = 2 << 20
)

// PartModule is the module of a split model's i-th part.
func PartModule(module string, i int) string {
	return fmt.Sprintf("%s.Part%d", module, i)
}

// PrintParts renders the program as Lean modules: the main module, and
// for a model over splitBytes, the parts it imports in order. Every part
// shares the main module's namespace, so the functions keep their names;
// the first part holds the JSON table the others' functions read.
func PrintParts(p *ir.Prog, module string) (string, []string) {
	whole := Print(p, module)
	if len(whole) <= splitBytes {
		return whole, nil
	}
	var parts []string
	var b strings.Builder
	start := func() {
		imp := "import Go2Lean.Dialect.GoSSA"
		if len(parts) > 0 {
			imp = "import " + PartModule(module, 0)
		}
		header(&b, p, module, imp)
	}
	finish := func() {
		fmt.Fprintf(&b, "end %s\n", module)
		parts = append(parts, b.String())
		b.Reset()
	}
	start()
	printTable(&b, p)
	for i := range p.Funcs {
		if b.Len() > partBytes {
			finish()
			start()
		}
		printFunc(&b, p, i)
	}
	finish()
	imps := make([]string, len(parts))
	for i := range parts {
		imps[i] = "import " + PartModule(module, i)
	}
	header(&b, p, module, strings.Join(imps, "\n"))
	printProg(&b, p, module)
	return b.String(), parts
}

func header(b *strings.Builder, p *ir.Prog, module, imports string) {
	fmt.Fprintf(b, "%s\n\n", imports)
	fmt.Fprintf(b, "/-! Generated by go2lean (%s frontend) from package %s. Do not edit. -/\n\n", p.Frontend, p.Package)
	fmt.Fprintf(b, "namespace %s\n\nopen Go2Lean.SSA Go2Lean.GoSSA\n\n", module)
	// Large functions are long array literals; the default depth (512) is
	// too small for a few hundred instructions in one block (614 needs
	// more than 8192). A linked dependency can hold a block of tens of
	// thousands (rivo/uniseg's break tables), past 100000 and the default
	// heartbeats.
	fmt.Fprintf(b, "set_option maxRecDepth 2000000\n")
	fmt.Fprintf(b, "set_option maxHeartbeats 4000000\n\n")
}

func printTable(b *strings.Builder, p *ir.Prog) {
	table := p.JSONTable
	if table == "" {
		table = "{ descs := #[], zeros := #[] }"
	}
	fmt.Fprintf(b, "/-- The JSON descriptions of the types this package encodes or decodes. -/\ndef jsonTable : GoSSA.JTable :=\n  %s\n\n", table)
}

func printFunc(b *strings.Builder, p *ir.Prog, i int) {
	f := p.Funcs[i]
	fmt.Fprintf(b, "/-- %s (status: %s) -/\n", f.GoSig, f.Status)
	fmt.Fprintf(b, "def f%d : GoSSA.Func := {\n  name := %q, pkg := %q, params := %s, nvals := %d, entry := %d,\n  blocks := #[\n",
		i, f.Name, p.Package, vars(f.Params), f.NVals, f.Entry)
	for bi, blk := range f.Blocks {
		fmt.Fprintf(b, "    -- %d: %s\n    { params := %s,\n      instrs := #[", bi, blk.Name, vars(blk.Params))
		for ii, in := range blk.Instrs {
			if ii > 0 {
				b.WriteString(",\n                 ")
			}
			if in.Probe != "" {
				fmt.Fprintf(b, ".probe %q %d", in.Probe, in.Args[0])
			} else if in.Assume != "" {
				fmt.Fprintf(b, ".assume %q", in.Assume)
			} else if in.Op == nil {
				fmt.Fprintf(b, ".havoc %d %d", in.Dst, in.HavocSite)
			} else {
				fmt.Fprintf(b, ".op %d %s %s", in.Dst, in.Op.Lean(), vars(in.Args))
			}
		}
		fmt.Fprintf(b, "],\n      term := %s }", term(p.Frontend, blk.Term))
		if bi < len(f.Blocks)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	if f.Sync && f.Daemon {
		b.WriteString("  ],\n  sync := true, daemon := true }\n\n")
	} else if f.Sync {
		b.WriteString("  ],\n  sync := true }\n\n")
	} else {
		b.WriteString("  ] }\n\n")
	}
}

func printProg(b *strings.Builder, p *ir.Prog, module string) {
	names := make([]string, len(p.Funcs))
	for i := range p.Funcs {
		names[i] = fmt.Sprintf("f%d", i)
	}
	widths := make([]string, len(p.HavocWidths))
	for i, w := range p.HavocWidths {
		widths[i] = fmt.Sprint(w)
	}
	sched := ""
	if p.HasSched {
		sched = fmt.Sprintf(", schedSite := some %d", p.SchedSite)
	}
	if len(p.Inits) > 0 {
		ids := make([]string, len(p.Inits))
		for i, id := range p.Inits {
			ids[i] = fmt.Sprint(id)
		}
		sched += fmt.Sprintf(", inits := #[%s]", strings.Join(ids, ", "))
	}
	fmt.Fprintf(b, "def prog : GoSSA.Prog := { funcs := #[%s], havocWidths := #[%s]%s }\n\n",
		strings.Join(names, ", "), strings.Join(widths, ", "), sched)
	fmt.Fprintf(b, "end %s\n", module)
}

// Emit writes the Lean module into the lake project and the ABI, report and
// target records into the workspace. It returns the model report.
func Emit(p *ir.Prog, leanDir string, ws contract.Workspace, modulePath string) (*contract.ModelReport, error) {
	module := ModuleName(p.Frontend, p.Package)
	leanFile := filepath.Join(leanDir, ModuleFile(module))
	if err := os.MkdirAll(filepath.Dir(leanFile), 0o755); err != nil {
		return nil, err
	}
	text, parts := PrintParts(p, module)
	// A part directory left by an earlier, larger model would be built
	// (the lakefile globs every Generated module) though nothing imports it.
	partDir := strings.TrimSuffix(leanFile, ".lean")
	if err := os.RemoveAll(partDir); err != nil {
		return nil, err
	}
	sum := sha256.New()
	var partFiles []string
	for i, part := range parts {
		f := filepath.Join(leanDir, ModuleFile(PartModule(module, i)))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(f, []byte(part), 0o644); err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(filepath.Dir(leanDir), f)
		partFiles = append(partFiles, rel)
		sum.Write([]byte(part))
	}
	sum.Write([]byte(text))
	if err := os.WriteFile(leanFile, []byte(text), 0o644); err != nil {
		return nil, err
	}
	if err := WriteRegistry(leanDir); err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(filepath.Dir(leanDir), leanFile)
	report := &contract.ModelReport{
		Package:    p.Package,
		Frontend:   p.Frontend,
		LeanModule: module,
		LeanFile:   rel,
		LeanParts:  partFiles,
		LeanSHA256: fmt.Sprintf("%x", sum.Sum(nil)),
		Havoc:      p.Havoc,
		Dependency: p.Dependency,
	}
	var names []string
	for _, f := range p.Funcs {
		names = append(names, f.Name)
	}
	key := contract.FuncKeys(names)
	for i, f := range p.Funcs {
		mf := contract.ModelFunc{Name: f.Name, GoSig: f.GoSig, ID: i, Status: f.Status, Issues: f.Issues, SpecFor: f.SpecFor, Closure: f.Closure, WrappedBy: f.WrappedBy, CtorOnly: f.CtorOnly, TestFile: f.TestFile, Init: f.Init, SpecMaxLen: f.SpecMaxLen, SequenceFor: f.SequenceFor, Consts: f.Consts, StrConsts: f.StrConsts, MapKeys: f.MapKeys, UsesJSON: f.UsesJSON, CmpStrs: f.CmpStrs, Variants: f.Variants, Calls: f.Calls, XCalls: f.XCalls, Exits: f.Exits, LoopHeaders: f.LoopHeaders()}
		if len(mf.LoopHeaders) > 0 {
			for _, b := range f.Blocks {
				mf.Succs = append(mf.Succs, b.Term.ProofSuccs())
			}
		}
		if f.Status != contract.ModelRejected {
			abi := f.ABI
			abiRel, err := ws.WriteABI(&abi, p.Package, f.Name)
			if err != nil {
				return nil, err
			}
			mf.ABI = abiRel
			if f.SpecFor == "" {
				t := &contract.Target{
					ID:      contract.TargetID(p.Package, key[f.Name]),
					Module:  modulePath,
					Package: p.Package,
					Func:    f.Name,
					GoSig:   f.GoSig,
					Models:  map[string]string{p.Frontend: abiRel},
				}
				if err := ws.WriteTarget(t); err != nil {
					return nil, err
				}
			}
		}
		report.Funcs = append(report.Funcs, mf)
	}
	return report, ws.WriteModelReport(report)
}
