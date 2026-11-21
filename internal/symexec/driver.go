package symexec

import (
	"fmt"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// Package is a loaded package with SSA.
type Package struct {
	Dir  string
	Pkg  *packages.Package
	SSA  *ssa.Package
	Prog *ssa.Program
}

// Load loads the package matching pattern in the module at dir.
func Load(dir, pattern string) (*Package, error) {
	cfg := &packages.Config{Mode: packages.LoadAllSyntax | packages.NeedModule, Dir: dir}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		return nil, err
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("symexec: %q matches %d packages", pattern, len(pkgs))
	}
	if len(pkgs[0].Errors) > 0 {
		return nil, fmt.Errorf("symexec: %v", pkgs[0].Errors[0])
	}
	prog, ssapkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	return &Package{Dir: dir, Pkg: pkgs[0], SSA: ssapkgs[0], Prog: prog}, nil
}

// Functions are the package-level functions whose parameters the executor
// supports, exported ones only unless unexported is set: an unexported
// function may rely on what its callers guarantee, and a panic that breaks
// that is not reachable from the API.
func (p *Package) Functions(unexported bool) []*ssa.Function {
	var out []*ssa.Function
	for _, m := range p.SSA.Members {
		fn, ok := m.(*ssa.Function)
		if !ok || len(fn.Blocks) == 0 || fn.Name() == "init" || (!unexported && !token.IsExported(fn.Name())) {
			continue
		}
		okp := true
		for _, prm := range fn.Params {
			if !supported(prm.Type()) {
				okp = false
			}
		}
		if okp && len(fn.Params) > 0 {
			out = append(out, fn)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func supported(t types.Type) bool {
	if _, _, ok := intInfo(t); ok {
		return true
	}
	if b, ok := t.Underlying().(*types.Basic); ok && b.Kind() == types.Bool {
		return true
	}
	if sl, ok := t.Underlying().(*types.Slice); ok {
		_, _, ok := intInfo(sl.Elem())
		return ok
	}
	return false
}

// Replay runs the inputs of every finding on the real functions, in one
// generated test of the package, and returns for each whether it panicked
// and the panic message.
func (p *Package) Replay(fs []Finding) ([]bool, []string) {
	ok := make([]bool, len(fs))
	msg := make([]string, len(fs))
	if len(fs) == 0 {
		return ok, msg
	}
	var b strings.Builder
	imps := map[string]bool{}
	for _, f := range fs {
		for _, i := range f.Imports {
			imps[i] = true
		}
	}
	fmt.Fprintf(&b, "package %s\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n", p.Pkg.Name)
	var il []string
	for i := range imps {
		il = append(il, i)
	}
	sort.Strings(il)
	for _, i := range il {
		fmt.Fprintf(&b, "\t%q\n", i)
	}
	fmt.Fprintf(&b, ")\n\nvar _ = fmt.Sprint\n\n")
	for i, f := range fs {
		var args []string
		for _, in := range f.Inputs {
			args = append(args, in.Go)
		}
		fmt.Fprintf(&b, `func TestSymexecReplay%d(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("SYMEXEC_PANIC %d %%v\n", r)
		} else {
			fmt.Printf("SYMEXEC_NOPANIC %d\n")
		}
	}()
	%s(%s)
}

`, i, i, i, f.Func, strings.Join(args, ", "))
	}
	dir := p.Pkg.Dir
	if len(p.Pkg.GoFiles) > 0 {
		dir = filepath.Dir(p.Pkg.GoFiles[0])
	}
	path := filepath.Join(dir, "zz_symexec_replay_test.go")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		for i := range msg {
			msg[i] = err.Error()
		}
		return ok, msg
	}
	defer os.Remove(path)
	cmd := exec.Command("go", "test", "-vet=off", "-count=1", "-v", "-run", "TestSymexecReplay", ".")
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	if os.Getenv("SYMEXEC_STATS") != "" {
		fmt.Fprintf(os.Stderr, "%s\n", out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		var idx int
		if strings.HasPrefix(line, "SYMEXEC_PANIC ") {
			rest := strings.TrimPrefix(line, "SYMEXEC_PANIC ")
			if n, _ := fmt.Sscanf(rest, "%d", &idx); n == 1 && idx < len(fs) {
				ok[idx] = true
				msg[idx] = strings.TrimSpace(strings.TrimPrefix(rest, fmt.Sprint(idx)))
			}
		} else if strings.HasPrefix(line, "SYMEXEC_NOPANIC ") {
			if n, _ := fmt.Sscanf(strings.TrimPrefix(line, "SYMEXEC_NOPANIC "), "%d", &idx); n == 1 && idx < len(fs) {
				msg[idx] = "no panic"
			}
		}
	}
	for i := range msg {
		if msg[i] == "" {
			msg[i] = "did not run: " + firstLines(string(out), 2)
		}
	}
	return ok, msg
}

func firstLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[:n]
	}
	return strings.Join(l, " | ")
}

// Root is the directory of the module, so that panics in the standard
// library or in dependencies are not reported.
func (p *Package) Root() string {
	if p.Pkg.Module != nil {
		return p.Pkg.Module.Dir + "/"
	}
	return p.Dir + "/"
}
