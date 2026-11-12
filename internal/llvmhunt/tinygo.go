package llvmhunt

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/fooexec/go2lean/internal/contract"
	llvmfe "github.com/fooexec/go2lean/internal/frontend/llvm"
	"github.com/fooexec/go2lean/internal/sandbox"
)

// FuncResult is what the search found for one function.
type FuncResult struct {
	Func string
	// Status: "panic" (a counterexample), "holds" (none found), "exhausted"
	// (none in the whole bounded domain), or "not modelled".
	Status string
	Panic  contract.PanicKind
	Inputs map[string]string
	Memory map[string][]string
	// Stuck counts trials that met something the model does not cover.
	Stuck      int
	StuckSites map[string]int
	Issues     []string
	// Verdict is the replay's: "confirmed <kind>" (Go panicked), "spurious"
	// (it returned), or "not replayed (why)".
	Verdict string
}

// RunTinyGo models a Go package through TinyGo's LLVM IR (TinyGo at -opt=0,
// so Go's own checks are explicit calls to runtime panic helpers) and
// searches each of its exported functions for a panic, as the Go line's
// no_panic does.
func RunTinyGo(root string, ws contract.Workspace, moduleDir, pattern string, opts Options, log io.Writer) ([]FuncResult, error) {
	if err := ws.Init(); err != nil {
		return nil, err
	}
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedFiles | packages.NeedSyntax, Dir: moduleDir}, pattern)
	if err != nil {
		return nil, err
	}
	if len(pkgs) != 1 || pkgs[0].Types == nil {
		return nil, fmt.Errorf("tinygo: %s: want one package", pattern)
	}
	pkg := pkgs[0]
	var funcs []string
	sigs := map[string]*types.Signature{}
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		f, ok := scope.Lookup(name).(*types.Func)
		// Spec* functions are specs in the Go line, not targets.
		if !ok || !f.Exported() || strings.HasPrefix(name, "Spec") || f.Type().(*types.Signature).TypeParams() != nil {
			continue
		}
		funcs = append(funcs, name)
		sigs[name] = f.Type().(*types.Signature)
	}
	sort.Strings(funcs)
	if len(funcs) == 0 {
		return nil, fmt.Errorf("tinygo: %s has no exported functions", pkg.PkgPath)
	}
	work := filepath.Join(ws.Root, "tinygo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	key := "tg_" + contract.PackageKey(pkg.PkgPath)
	ll := filepath.Join(work, key+".ll")
	if err := tinygoIR(moduleDir, pkg, funcs, ll); err != nil {
		return nil, err
	}
	model, err := llvmfe.Load(ll, llvmfe.Options{
		// The package's own functions, its initialisation included.
		Keep:    func(name string) bool { return strings.HasPrefix(name, pkg.PkgPath+".") },
		GoABI:   true,
		Callees: true,
	})
	if err != nil {
		return nil, err
	}
	leanDir := filepath.Join(ws.Root, "lean-llvm")
	if err := llvmfe.WriteModule(model, leanDir, key); err != nil {
		return nil, err
	}
	if err := ensureProject(leanDir, filepath.Join(root, "lean-llvm")); err != nil {
		return nil, err
	}
	fmt.Fprintf(log, "tinygo %s -> %s (%d functions modelled)\n", pkg.PkgPath, key, len(model.Funcs))
	if out, err := lake(leanDir, "build", "go2lean-llvm-search"); err != nil {
		return nil, fmt.Errorf("lake build: %v\n%s", err, out)
	}
	byName := map[string]llvmfe.Func{}
	for _, f := range model.Funcs {
		byName[f.Name] = f
	}
	opts.Init = -1
	if f, ok := byName[pkg.PkgPath+".init"]; ok {
		opts.Init = f.Index
	}
	opts.Strs = stringConsts(pkg)
	var results []FuncResult
	for _, name := range funcs {
		fn, ok := byName[pkg.PkgPath+"."+name]
		if !ok {
			results = append(results, FuncResult{Func: name, Status: "not modelled"})
			continue
		}
		goGens(fn.Gens, sigs[name])
		res, err := search(leanDir, key, fn, opts)
		if err != nil {
			return nil, err
		}
		r := FuncResult{Func: name, Stuck: res.Stuck, Issues: fn.Issues}
		switch {
		case res.Found:
			r.Status, r.Panic, r.Inputs, r.Memory = "panic", res.ModelOutput.Panic, res.Inputs, res.Memory
			if res.HavocDependent {
				r.Issues = append(r.Issues, "counterexample through havoc or an assumption: "+strings.Join(res.Assumptions, "; "))
			}
			r.Verdict = replayGo(moduleDir, pkg, name, sigs[name], r)
		case res.Exhausted && len(res.Assumptions) > 0:
			r.Status = "exhausted_assuming"
			r.Issues = append(r.Issues, res.Assumptions...)
		case res.Exhausted:
			r.Status = "exhausted"
		case res.Stuck > 0:
			r.Status = fmt.Sprintf("partial (%d of %d trials stuck)", res.Stuck, res.Trials)
			r.StuckSites = res.StuckSites
		default:
			r.Status = "holds"
		}
		fmt.Fprintf(log, "  %-24s %s %s %v %v %s\n", name, r.Status, r.Panic, r.Inputs, r.Memory, r.Verdict)
		if res.HavocDependent {
			fmt.Fprintf(log, "      through havoc or an assumption: %s\n", strings.Join(res.Assumptions, "; "))
		}
		for site, n := range r.StuckSites {
			fmt.Fprintf(log, "      stuck %d× at %s\n", n, site)
		}
		results = append(results, r)
	}
	return results, nil
}

var sizes = types.SizesFor("gc", "amd64")

// goGens sets what the LLVM parameters point to from the Go signature: a
// slice's or string's element size, and a pointer is one object of its
// pointee's size (random bytes unless it holds pointers). Maps, channels,
// functions and interfaces are nil.
func goGens(gens []llvmfe.Gen, sig *types.Signature) {
	params := map[string]types.Type{}
	for i := 0; i < sig.Params().Len(); i++ {
		params[sig.Params().At(i).Name()] = sig.Params().At(i).Type()
	}
	for i := range gens {
		g := &gens[i]
		if g.K != "buf" && g.K != "zbuf" {
			continue
		}
		// TinyGo names the parts of a value `x.part`.
		base, _, _ := strings.Cut(g.Name, ".")
		pt, ok := params[base]
		if !ok {
			continue
		}
		if types.IsInterface(pt) {
			// An interface (typecode and value pointers): nil.
			g.K = "null"
			continue
		}
		switch t := pt.Underlying().(type) {
		case *types.Slice:
			g.EB = int(sizes.Sizeof(t.Elem()))
			if hasPointers(t.Elem()) || (g.EB != 1 && g.EB != 2 && g.EB != 4 && g.EB != 8) {
				g.K = "zbuf"
			}
		case *types.Basic:
			g.EB = 1
		case *types.Map, *types.Chan, *types.Signature:
			// A pointer to a runtime structure: nil (a valid Go value; random
			// bytes would break the runtime's own invariants).
			g.K = "null"
		case *types.Pointer:
			g.K, g.EB, g.MaxLen, g.Random = "obj", int(sizes.Sizeof(t.Elem())), 0, !hasPointers(t.Elem())
		}
	}
}

func hasPointers(t types.Type) bool {
	switch t := t.Underlying().(type) {
	case *types.Basic:
		return t.Info()&types.IsString != 0 || t.Kind() == types.UnsafePointer
	case *types.Array:
		return hasPointers(t.Elem())
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			if hasPointers(t.Field(i).Type()) {
				return true
			}
		}
		return false
	}
	return true
}

// tinygoIR builds a main package that refers to the functions (TinyGo only
// builds commands) inside the module, so it imports the package as the
// module sees it, and emits its LLVM IR; the directory is removed after.
func tinygoIR(moduleDir string, pkg *packages.Package, funcs []string, out string) error {
	dir, err := os.MkdirTemp(moduleDir, "zz_go2lean_tinygo_")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	refs := make([]string, len(funcs))
	for i, f := range funcs {
		refs[i] = "p." + f
	}
	src := fmt.Sprintf("// Generated by go2lean for TinyGo; removed after the build.\npackage main\n\nimport p %q\n\nvar keep = []any{%s}\n\nfunc main() { _ = keep }\n",
		pkg.PkgPath, strings.Join(refs, ", "))
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("tinygo", "build", "-opt=0", "-o", out, "./"+filepath.Base(dir))
	cmd.Dir = moduleDir
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tinygo build: %v\n%s", err, b)
	}
	return nil
}

// replayGo runs a TinyGo-line counterexample with the gc toolchain: a test
// in the package calls the function on the same inputs, rebuilt as Go
// values from the signature, and reports the panic it raises.
func replayGo(moduleDir string, pkg *packages.Package, name string, sig *types.Signature, r FuncResult) string {
	if len(pkg.GoFiles) == 0 {
		return "not replayed (no files)"
	}
	imports := map[string]string{"fmt": "fmt", "testing": "testing"}
	qual := func(p *types.Package) string {
		if p == pkg.Types {
			return ""
		}
		imports[p.Path()] = p.Name()
		return p.Name()
	}
	var decls, args []string
	for i := 0; i < sig.Params().Len(); i++ {
		v := sig.Params().At(i)
		a := fmt.Sprintf("a%d", i)
		ts := types.TypeString(v.Type(), qual)
		expr, ok := goValue(v.Name(), v.Type(), ts, r, imports)
		if !ok {
			return "not replayed (parameter " + v.Name() + " of type " + ts + ")"
		}
		decls = append(decls, fmt.Sprintf("\t%s := %s", a, expr))
		args = append(args, a)
	}
	var imps []string
	for path := range imports {
		imps = append(imps, fmt.Sprintf("\t%q", path))
	}
	sort.Strings(imps)
	src := fmt.Sprintf(`// Generated by go2lean: replay of a TinyGo-line counterexample; removed after.
package %s

import (
%s
)

func go2leanI64(v int64) int64 { return v }

func TestGo2LeanTinyGoReplay(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("GO2LEAN_PANIC %%v\n", r)
		}
	}()
%s
	%s(%s)
	fmt.Println("GO2LEAN_OK")
}
`, pkg.Name, strings.Join(imps, "\n"), strings.Join(decls, "\n"), name, strings.Join(args, ", "))
	pkgDir := filepath.Dir(pkg.GoFiles[0])
	tmp, err := os.MkdirTemp("", "go2lean-replay-")
	if err != nil {
		return "not replayed (" + err.Error() + ")"
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "replay.test")
	buildOut, exit, err := sandbox.BuildTest(moduleDir, pkg.PkgPath, pkgDir, map[string][]byte{"zz_go2lean_tinygo_replay_test.go": []byte(src)}, bin)
	if err != nil || exit != 0 {
		return "not replayed (" + firstLine(buildOut) + ")"
	}
	stdout, stderr, _, err := sandbox.Run(bin, pkgDir, []string{"-test.count=1", "-test.v", "-test.run", "^TestGo2LeanTinyGoReplay$"}, nil, sandbox.Default)
	if err != nil {
		return "not replayed (" + err.Error() + ")"
	}
	text := stdout + stderr
	switch {
	case strings.Contains(text, "GO2LEAN_PANIC"):
		_, msg, _ := strings.Cut(text, "GO2LEAN_PANIC ")
		msg, _, _ = strings.Cut(msg, "\n")
		return "confirmed " + string(panicKindOf(msg))
	case strings.Contains(text, "GO2LEAN_OK"):
		return "spurious"
	}
	return "not replayed (" + firstLine(text) + ")"
}

// stringConsts are the package's string literals (up to 32, at most 64
// bytes each), as the Go line mines them: inputs equal to a constant the
// code compares against are otherwise out of a random search's reach.
func stringConsts(pkg *packages.Package) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range pkg.Syntax {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || len(out) >= 32 {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && len(s) <= 64 && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
			return true
		})
	}
	return out
}

// panicKindOf classifies a Go runtime panic message.
func panicKindOf(msg string) contract.PanicKind {
	switch {
	case strings.Contains(msg, "index out of range"):
		return contract.PanicIndexOutOfRange
	case strings.Contains(msg, "slice bounds out of range"):
		return contract.PanicSliceBounds
	case strings.Contains(msg, "divide by zero"):
		return contract.PanicDivideByZero
	case strings.Contains(msg, "nil pointer dereference") || strings.Contains(msg, "invalid memory address"):
		return contract.PanicNilDeref
	case strings.Contains(msg, "interface conversion"):
		return contract.PanicTypeAssertion
	case strings.Contains(msg, "assignment to entry in nil map"):
		return contract.PanicNilMapWrite
	case strings.Contains(msg, "negative shift amount"):
		return contract.PanicNegativeShift
	}
	return contract.PanicExplicit
}

// goValue is a Go expression for the value the search gave parameter name.
func goValue(name string, t types.Type, ts string, r FuncResult, imports map[string]string) (string, bool) {
	ints := func(xs []string, elem string) string {
		parts := make([]string, len(xs))
		for i, x := range xs {
			parts[i] = fmt.Sprintf("%s(go2leanI64(%s))", elem, x)
		}
		return strings.Join(parts, ", ")
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch {
		case u.Info()&types.IsBoolean != 0:
			return fmt.Sprintf("%s(go2leanI64(%s) != 0)", ts, r.Inputs[name]), true
		case u.Info()&types.IsInteger != 0:
			v, ok := r.Inputs[name]
			if !ok {
				return "", false
			}
			return fmt.Sprintf("%s(go2leanI64(%s))", ts, v), true
		case u.Info()&types.IsString != 0:
			return fmt.Sprintf("%s(string([]byte{%s}))", ts, ints(r.Memory[name], "byte")), true
		}
	case *types.Slice:
		elem, isInt := u.Elem().Underlying().(*types.Basic)
		xs := r.Memory[name]
		if isInt && elem.Info()&types.IsInteger != 0 {
			return fmt.Sprintf("%s{%s}", ts, ints(xs, types.TypeString(u.Elem(), func(p *types.Package) string { return "" }))), true
		}
		return fmt.Sprintf("make(%s, %d)", ts, len(xs)), true
	case *types.Pointer:
		et := strings.TrimPrefix(ts, "*")
		bs := r.Memory[name]
		if hasPointers(u.Elem()) || len(bs) == 0 {
			return "new(" + et + ")", true
		}
		imports["unsafe"] = "unsafe"
		return fmt.Sprintf("func() %s { p := new(%s); copy(unsafe.Slice((*byte)(unsafe.Pointer(p)), unsafe.Sizeof(*p)), []byte{%s}); return p }()", ts, et, ints(bs, "byte")), true
	case *types.Array:
		elem, isInt := u.Elem().Underlying().(*types.Basic)
		if !isInt || elem.Info()&types.IsInteger == 0 {
			return "", false
		}
		var vals []string
		for _, m := range aggInts.FindAllStringSubmatch(r.Inputs[name], -1) {
			vals = append(vals, m[1])
		}
		return fmt.Sprintf("%s{%s}", ts, ints(vals, types.TypeString(u.Elem(), func(p *types.Package) string { return "" }))), true
	case *types.Interface, *types.Signature, *types.Map, *types.Chan:
		return "(" + ts + ")(nil)", true
	}
	return "", false
}

var aggInts = regexp.MustCompile(`i\d+ (-?\d+)`)
