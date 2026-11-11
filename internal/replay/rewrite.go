// Package replay builds the overlay that makes a package's real run follow
// the schedule the model found (docs/concurrency.md, C1c): each
// synchronisation operation is rewritten to pass a gate of the replay
// controller (controller.go) first.
package replay

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// ControllerFile is the name of the file the overlay adds to the package.
const ControllerFile = "zz_go2lean_replay.go"

// Rewrite loads package pkg (test files of the package included) from the
// module at root and returns the overlay for a replay: every file of the
// package rewritten, by base name, and the controller.
func Rewrite(root, pkg string) (map[string][]byte, error) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax |
		packages.NeedTypes | packages.NeedTypesInfo, Tests: true, Dir: root}
	load := pkg
	if base, ok := strings.CutSuffix(pkg, "_test"); ok {
		// An external test package loads through its directory's package.
		if probe, err := packages.Load(&packages.Config{Mode: packages.NeedName, Dir: root}, pkg); err != nil || len(probe) == 0 || len(probe[0].Errors) > 0 {
			load = base
		}
	}
	pkgs, err := packages.Load(cfg, load)
	if err != nil {
		return nil, err
	}
	// The variant with the package's own test files: the most files.
	var p *packages.Package
	for _, q := range pkgs {
		if q.PkgPath == pkg && (p == nil || len(q.Syntax) > len(p.Syntax)) {
			p = q
		}
	}
	if p == nil {
		return nil, fmt.Errorf("package %s not found", pkg)
	}
	if len(p.Errors) > 0 {
		return nil, fmt.Errorf("package %s: %v", pkg, p.Errors[0])
	}
	out := map[string][]byte{}
	for i, f := range p.Syntax {
		name := filepath.Base(p.CompiledGoFiles[i])
		if strings.HasPrefix(name, "zz_go2lean_") {
			continue
		}
		r := &rewriter{fset: p.Fset, info: p.TypesInfo, pkg: p.Types, file: f}
		src, err := r.rewrite()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = src
	}
	pkgLine := "package " + p.Types.Name()
	out[ControllerFile] = []byte(strings.Replace(controllerSrc, "package PKG", pkgLine, 1))
	out["zz_go2lean_replay_race.go"] = []byte(strings.Replace(raceOnSrc, "package PKG", pkgLine, 1))
	out["zz_go2lean_replay_norace.go"] = []byte(strings.Replace(raceOffSrc, "package PKG", pkgLine, 1))
	return out, nil
}

type rewriter struct {
	fset *token.FileSet
	info *types.Info
	pkg  *types.Package
	file *ast.File
	n    int // site counter, for unique names
	// selComm: receives and sends that are select cases (handled with
	// their select).
	selComm map[ast.Node]bool
	// before: statements to insert before a statement (a labeled one's
	// label, for selects and ranges under a label).
	before map[ast.Stmt][]ast.Stmt
	// chanType: the spelled type of channel expressions, recorded before
	// the expressions are rewritten.
	chanType map[ast.Expr]string
	// calls: the model operation or replacement of each call to rewrite;
	// recvTypes: the receiver type of a sync.Cond method call.
	calls     map[*ast.CallExpr]string
	recvTypes map[*ast.CallExpr]types.Type
	imports   []string // packages a spelled type needs
	err       error
}

// syncMethods maps sync's methods to the model's operations.
var syncMethods = map[string]string{
	"Mutex.Lock": "lock", "Mutex.Unlock": "unlock", "Mutex.TryLock": "tryLock",
	"RWMutex.Lock": "wLock", "RWMutex.Unlock": "wUnlock", "RWMutex.RLock": "rLock", "RWMutex.RUnlock": "rUnlock",
	"WaitGroup.Add": "wgAdd", "WaitGroup.Done": "wgAdd", "WaitGroup.Wait": "wgWait",
	"Once.Do":     "onceEnter",
	"Locker.Lock": "lockL", "Locker.Unlock": "unlockL",
}

// replaced maps the functions the controller stands in for.
var replaced = map[string]string{
	"time.After": "go2leanAfter", "time.NewTimer": "go2leanNewTimer", "time.NewTicker": "go2leanNewTicker",
	"time.AfterFunc": "go2leanAfterFunc", "time.Sleep": "go2leanSleep", "runtime.Gosched": "go2leanGosched",
	"context.WithTimeout": "go2leanWithTimeout", "context.WithDeadline": "go2leanWithDeadline",
}

func (r *rewriter) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf(format, args...)
	}
}

// chanString spells a channel's type by its underlying type: the gate
// wraps the channel as func(c T) T, and a named channel type's name may be
// shadowed where the channel is used (conc's `for callbackCh := range
// s.queue`, of type chan callbackCh). A value of the named type is
// assignable to its unnamed underlying type.
func (r *rewriter) chanString(t types.Type) string {
	return r.typeString(t.Underlying())
}

// typeString spells t in this file, importing the packages it needs.
func (r *rewriter) typeString(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string {
		if p == r.pkg {
			return ""
		}
		for _, imp := range r.file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path == p.Path() {
				if imp.Name != nil {
					return imp.Name.Name
				}
				return p.Name()
			}
		}
		// Added after the walk: adding now would change the declarations
		// being walked.
		r.imports = append(r.imports, p.Path())
		return p.Name()
	})
}

// expr parses src, putting the expressions holes in for the identifiers
// named by their keys.
func expr(src string, holes map[string]ast.Expr) ast.Expr {
	e, err := parser.ParseExpr(src)
	if err != nil {
		panic(fmt.Sprintf("replay: %v in %s", err, src))
	}
	return fill(e, holes).(ast.Expr)
}

func stmts(src string, holes map[string]ast.Expr) []ast.Stmt {
	f, err := parser.ParseFile(token.NewFileSet(), "", "package p\nfunc _() {\n"+src+"\n}", 0)
	if err != nil {
		panic(fmt.Sprintf("replay: %v in %s", err, src))
	}
	body := f.Decls[0].(*ast.FuncDecl).Body
	return fill(body, holes).(*ast.BlockStmt).List
}

func fill(n ast.Node, holes map[string]ast.Expr) ast.Node {
	// The snippet's positions are in a file of its own: clear them, so the
	// printer does not lay it out by them.
	ast.Inspect(n, func(m ast.Node) bool {
		if m != nil {
			nopos(m)
		}
		return true
	})
	return astutil.Apply(n, nil, func(c *astutil.Cursor) bool {
		if id, ok := c.Node().(*ast.Ident); ok {
			if h, ok := holes[id.Name]; ok {
				c.Replace(h)
			}
		}
		return true
	})
}

// nopos clears the token.Pos fields of node n (not of its children).
func nopos(n ast.Node) {
	v := reflect.ValueOf(n)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return
	}
	pos := reflect.TypeOf(token.NoPos)
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Type() == pos && f.CanSet() {
			f.SetInt(0)
		}
	}
}

// syncOp names the model operation a call is, through a method of sync's
// Mutex, RWMutex, WaitGroup, Once or the Locker interface.
func (r *rewriter) syncOp(call *ast.CallExpr) (string, *ast.SelectorExpr, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", nil, false
	}
	s := r.info.Selections[sel]
	if s == nil || s.Kind() != types.MethodVal {
		return "", nil, false
	}
	fn := s.Obj().(*types.Func)
	if fn.Pkg() == nil || fn.Pkg().Path() != "sync" {
		return "", nil, false
	}
	recv := fn.Type().(*types.Signature).Recv().Type()
	if p, ok := recv.(*types.Pointer); ok {
		recv = p.Elem()
	}
	n, ok := recv.(*types.Named)
	if !ok {
		return "", nil, false
	}
	key := n.Obj().Name() + "." + fn.Name()
	if n.Obj().Name() == "Cond" {
		return "cond." + fn.Name(), sel, true
	}
	op, ok := syncMethods[key]
	return op, sel, ok
}

// funcName is the package-qualified name a call's function denotes
// ("time.After"), or "".
func (r *rewriter) funcName(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	fn, ok := r.info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Type().(*types.Signature).Recv() != nil {
		return ""
	}
	return fn.Pkg().Path() + "." + fn.Name()
}

func (r *rewriter) isChan(e ast.Expr) bool {
	t := r.info.TypeOf(e)
	if t == nil {
		return false
	}
	_, ok := t.Underlying().(*types.Chan)
	return ok
}

func (r *rewriter) rewrite() ([]byte, error) {
	r.selComm = map[ast.Node]bool{}
	r.before = map[ast.Stmt][]ast.Stmt{}
	r.chanType = map[ast.Expr]string{}
	r.calls = map[*ast.CallExpr]string{}
	r.recvTypes = map[*ast.CallExpr]types.Type{}
	// Keep only the comments ahead of the package clause (build
	// constraints); the rest would be misplaced around new code.
	var keep []*ast.CommentGroup
	for _, cg := range r.file.Comments {
		if cg.End() < r.file.Package {
			keep = append(keep, cg)
		}
	}
	r.file.Comments, r.file.Doc = keep, nil
	astutil.Apply(r.file, r.pre, r.post)
	if r.err != nil {
		return nil, r.err
	}
	for _, path := range r.imports {
		astutil.AddImport(r.fset, r.file, path)
	}
	r.keepImports()
	var b bytes.Buffer
	if err := (&printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}).Fprint(&b, r.fset, r.file); err != nil {
		return nil, err
	}
	src, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("rewritten source does not parse: %w", err)
	}
	return src, nil
}

// keepImports keeps the imports a rewrite may have left unused in use.
func (r *rewriter) keepImports() {
	uses := map[string]string{"time": "Sleep", "context": "Background", "runtime": "Gosched", "sync": "NewCond"}
	for _, imp := range r.file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		fn, ok := uses[path]
		if !ok || (imp.Name != nil && (imp.Name.Name == "_" || imp.Name.Name == ".")) {
			continue
		}
		name := path
		if imp.Name != nil {
			name = imp.Name.Name
		}
		r.file.Decls = append(r.file.Decls, &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{&ast.ValueSpec{
			Names: []*ast.Ident{ast.NewIdent("_")}, Values: []ast.Expr{&ast.SelectorExpr{X: ast.NewIdent(name), Sel: ast.NewIdent(fn)}}}}})
	}
}

func (r *rewriter) pre(c *astutil.Cursor) bool {
	switch n := c.Node().(type) {
	case *ast.SelectStmt:
		for _, cc := range n.Body.List {
			cl := cc.(*ast.CommClause)
			if cl.Comm == nil {
				continue
			}
			switch s := cl.Comm.(type) {
			case *ast.SendStmt:
				r.selComm[s] = true
			case *ast.ExprStmt:
				r.selComm[s.X] = true
			case *ast.AssignStmt:
				r.selComm[s.Rhs[0]] = true
			}
		}
	case *ast.UnaryExpr:
		if n.Op == token.ARROW {
			r.chanType[n.X] = r.chanString(r.info.TypeOf(n.X))
		}
	case *ast.SendStmt:
		r.chanType[n.Chan] = r.chanString(r.info.TypeOf(n.Chan))
	case *ast.RangeStmt:
		if r.isChan(n.X) {
			r.chanType[n.X] = r.chanString(r.info.TypeOf(n.X))
		}
	case *ast.CallExpr:
		if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "close" && len(n.Args) == 1 {
			if _, builtin := r.info.Uses[id].(*types.Builtin); builtin {
				r.chanType[n.Args[0]] = r.chanString(r.info.TypeOf(n.Args[0]))
			}
		}
		// Calls are rewritten bottom-up: classify them now, while the
		// types of their parts are still known.
		if op, sel, ok := r.syncOp(n); ok {
			r.calls[n] = op
			r.recvTypes[n] = r.info.TypeOf(sel.X)
		} else if name := r.funcName(n); replaced[name] != "" {
			r.calls[n] = "fn:" + replaced[name]
		}
	}
	return true
}

func (r *rewriter) gateChan(ch ast.Expr, op string) ast.Expr {
	t := r.chanType[ch]
	return expr(fmt.Sprintf("func(c %s) %s { go2leanGate(%q); return c }(go2leanX)", t, t, op), map[string]ast.Expr{"go2leanX": ch})
}

func (r *rewriter) post(c *astutil.Cursor) bool {
	switch n := c.Node().(type) {
	case *ast.UnaryExpr:
		if n.Op == token.ARROW && !r.selComm[n] {
			n.X = r.gateChan(n.X, "recv")
		}
	case *ast.SendStmt:
		if !r.selComm[n] {
			n.Chan = r.gateChan(n.Chan, "send")
		}
	case *ast.CallExpr:
		r.call(c, n)
	case *ast.GoStmt:
		r.goStmt(c, n)
	case *ast.RangeStmt:
		if _, ok := r.chanType[n.X]; ok {
			r.rangeChan(c, n)
		}
	case *ast.SelectStmt:
		r.selectStmt(c, n)
	case *ast.LabeledStmt:
		if pre := r.before[n.Stmt]; len(pre) > 0 {
			for _, s := range pre {
				c.InsertBefore(s)
			}
		}
	}
	return true
}

func (r *rewriter) call(c *astutil.Cursor, n *ast.CallExpr) {
	if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "close" && len(n.Args) == 1 {
		if t, ok := r.chanType[n.Args[0]]; ok {
			c.Replace(expr(fmt.Sprintf(`func(c %s) { go2leanGate("close"); close(c) }(go2leanX)`, t), map[string]ast.Expr{"go2leanX": n.Args[0]}))
		}
		return
	}
	op, ok := r.calls[n]
	if !ok {
		return
	}
	switch {
	case strings.HasPrefix(op, "fn:"):
		n.Fun = ast.NewIdent(op[3:])
	case strings.HasPrefix(op, "cond."):
		sel := n.Fun.(*ast.SelectorExpr)
		recv := sel.X
		if !r.isPtr(n) {
			recv = &ast.UnaryExpr{Op: token.AND, X: recv}
		}
		switch op {
		case "cond.Wait":
			c.Replace(&ast.CallExpr{Fun: ast.NewIdent("go2leanCondWait"), Args: []ast.Expr{recv}})
		case "cond.Signal":
			c.Replace(&ast.CallExpr{Fun: ast.NewIdent("go2leanCondSignal"), Args: []ast.Expr{recv, ast.NewIdent("false")}})
		case "cond.Broadcast":
			c.Replace(&ast.CallExpr{Fun: ast.NewIdent("go2leanCondSignal"), Args: []ast.Expr{recv, ast.NewIdent("true")}})
		}
	default:
		// The method value binds the receiver now; the gate comes after
		// the arguments, right before the operation.
		helper := map[string]string{"tryLock": "go2leanCallB", "onceEnter": "go2leanCallF"}[op]
		if helper == "" {
			helper = "go2leanCall0"
			if len(n.Args) == 1 {
				helper = "go2leanCallI"
			}
		}
		args := append([]ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(op)}, n.Fun}, n.Args...)
		c.Replace(&ast.CallExpr{Fun: ast.NewIdent(helper), Args: args})
	}
}

// isPtr: the receiver of a Cond method call is already a pointer.
func (r *rewriter) isPtr(n *ast.CallExpr) bool {
	_, ok := r.recvTypes[n].(*types.Pointer)
	return ok
}

func (r *rewriter) goStmt(c *astutil.Cursor, n *ast.GoStmt) {
	// The call is rewritten already (a close is func(c T) { gate;
	// close(c) }(ch)): the goroutine runs it, its arguments evaluated now.
	call := n.Call
	fn := call.Fun
	helper := "go2leanGoCall"
	if call.Ellipsis.IsValid() {
		helper = "go2leanGoCallSlice"
	}
	c.Replace(&ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent(helper), Args: append([]ast.Expr{fn}, call.Args...)}})
}

func (r *rewriter) rangeChan(c *astutil.Cursor, n *ast.RangeStmt) {
	r.n++
	ok := fmt.Sprintf("go2leanOk%d", r.n)
	src := n.X
	_, labeled := c.Parent().(*ast.LabeledStmt)
	if hasCall(n.X) {
		// The range expression is evaluated once, before the loop.
		v := fmt.Sprintf("go2leanR%d", r.n)
		decl := &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(v)}, Tok: token.DEFINE, Rhs: []ast.Expr{n.X}}
		if labeled {
			r.before[n] = append(r.before[n], decl)
		} else {
			c.InsertBefore(decl)
		}
		id := ast.NewIdent(v)
		r.chanType[id] = r.chanType[n.X]
		src = id
	}
	recv := &ast.UnaryExpr{Op: token.ARROW, X: r.gateChan(src, "recv")}
	key := n.Key
	if key == nil {
		key = ast.NewIdent("_")
	}
	var head []ast.Stmt
	switch {
	case n.Tok == token.DEFINE || n.Key == nil:
		head = append(head, &ast.AssignStmt{Lhs: []ast.Expr{key, ast.NewIdent(ok)}, Tok: token.DEFINE, Rhs: []ast.Expr{recv}})
	default:
		head = append(head,
			&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{&ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(ok)}, Type: ast.NewIdent("bool")}}}},
			&ast.AssignStmt{Lhs: []ast.Expr{key, ast.NewIdent(ok)}, Tok: token.ASSIGN, Rhs: []ast.Expr{recv}})
	}
	head = append(head, stmts(fmt.Sprintf("if !%s { break }", ok), nil)...)
	c.Replace(&ast.ForStmt{Body: &ast.BlockStmt{List: append(head, n.Body.List...)}})
}

// selectStmt: the case channels are evaluated first (into variables), then
// the gate, then every case but the one the model took is disabled (its
// channel set to nil, which is never ready).
func (r *rewriter) selectStmt(c *astutil.Cursor, n *ast.SelectStmt) {
	var hoist, disable []ast.Stmt
	// go/ssa lowers a select of one case and no default to the plain
	// operation: the model fires recv or send, not select.
	op, hasDefault := "select", false
	for _, cc := range n.Body.List {
		hasDefault = hasDefault || cc.(*ast.CommClause).Comm == nil
	}
	if len(n.Body.List) == 1 && !hasDefault {
		op = "recv"
		if _, send := n.Body.List[0].(*ast.CommClause).Comm.(*ast.SendStmt); send {
			op = "send"
		}
	}
	k := 0
	for _, cc := range n.Body.List {
		cl := cc.(*ast.CommClause)
		if cl.Comm == nil {
			continue
		}
		r.n++
		v := fmt.Sprintf("go2leanC%d", r.n)
		var slot *ast.Expr
		switch s := cl.Comm.(type) {
		case *ast.SendStmt:
			slot = &s.Chan
		case *ast.ExprStmt:
			slot = &s.X.(*ast.UnaryExpr).X
		case *ast.AssignStmt:
			slot = &s.Rhs[0].(*ast.UnaryExpr).X
		}
		hoist = append(hoist, &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(v)}, Tok: token.DEFINE, Rhs: []ast.Expr{*slot}})
		*slot = ast.NewIdent(v)
		disable = append(disable, stmts(fmt.Sprintf("if !go2leanPick(%d) { %s = nil }", k, v), nil)...)
		k++
	}
	if op != "select" {
		disable = nil // nothing to choose
	}
	pre := append(append(hoist, stmts(fmt.Sprintf("go2leanGate(%q)", op), nil)...), disable...)
	if _, labeled := c.Parent().(*ast.LabeledStmt); labeled {
		r.before[n] = append(r.before[n], pre...)
		return
	}
	for _, s := range pre {
		c.InsertBefore(s)
	}
}

func hasCall(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if _, ok := n.(*ast.CallExpr); ok {
			found = true
		}
		return !found
	})
	return found
}
