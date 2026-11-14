package poc

import (
	"encoding/json"
	"github.com/fooexec/go2lean/internal/contract"
	"os"
	"path/filepath"
	"testing"
)

func TestExportedFunc(t *testing.T) {
	for f, want := range map[string]bool{
		"Join": true, "join": false, "Counter.Share": true, "counter.Share": false,
		"Counter.share": false, "Last$1": false,
	} {
		if got := exportedFunc(baseFunc(f)); got != want {
			t.Errorf("exportedFunc(%q) = %v", f, got)
		}
	}
	for f, want := range map[string]bool{
		"Mux.Use~NewMux": true, "Mux.routeHTTP~NewMux": false, "Go2LeanGen_Map__int__string": true,
		"tag.Remove": false, "Map[int]": true,
	} {
		if got := APIFunc(f); got != want {
			t.Errorf("APIFunc(%q) = %v", f, got)
		}
	}
	if got := baseFunc("Counter.Share~NewCounter"); got != "Counter.Share" {
		t.Errorf("baseFunc = %q", got)
	}
	if got := baseFunc("ContainsFunc[[]int, int]"); got != "ContainsFunc" {
		t.Errorf("baseFunc = %q", got)
	}
}

func TestNilSites(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(`package m

type T struct{ p *int }

func (t *T) M(p *int, q *int) int { return *p + *q }

func F(p *int) int { return *p }
func G(p *int) int { return *p }
func H(p *int) int { return *p }
func K(p *int) int { return *p }

func find(ok bool) *int {
	if ok {
		x := 1
		return &x
	}
	return nil
}

func relay(p *int) int { return G(p) }

func L(p *int) int { return *p }

func trunc(s string, n int) string { return s[:n] }

func useTrunc() { _ = trunc("abc", 2); _ = trunc("abcd", 3) }

type E struct{ msg string }

func (e *E) Error() string { return e.msg }

func check(bad bool) error {
	var e *E
	if bad {
		e = &E{"bad"}
	}
	return e // a typed nil when !bad
}

func guardedUse(ok bool) {
	if p := find(ok); p != nil {
		L(p)
	}
}

func scan(b []byte, in func(byte) bool) int { n := 0; for _, c := range b { if in(c) { n++ } }; return n }

func digits(b []byte) int {
	var in func(byte) bool
	switch b[0] {
	case 'x':
		in = func(c byte) bool { return c < 'g' }
	case 'b':
		in = func(c byte) bool { return c < '2' }
	}
	if len(b) < 2 || !in(b[1]) {
		return 0
	}
	return scan(b, in) // in was called just above: never nil here
}

func use(t T, ok bool) {
	x := 1
	F(&x)
	F(nil)
	relay(nil)
	H(find(ok))
	K(t.p)
	K(&x)
	var s T
	s.M(&x, &x)
}
`), 0o644)
	c := newCallerIndex(dir)
	for _, tc := range []struct {
		fn                string
		yes, maybe, calls int
	}{
		{"F", 1, 0, 2},       // a nil literal at one of two sites
		{"G", 1, 0, 1},       // nil through relay's parameter
		{"H", 1, 0, 1},       // nil returned by find
		{"K", 0, 1, 2},       // a field: unknown; &x: never nil
		{"T.M", 0, 0, 1},     // &x only
		{"L", 0, 0, 1},       // find may return nil, but p != nil guards the call
		{"E.Error", 1, 0, 0}, // no static call; a nil *E is boxed in check
		{"scan", 0, 0, 1},    // in may be the nil zero, but digits calls it first (#194)
	} {
		names := []string{"p"}
		if tc.fn == "T.M" {
			names = []string{"q"}
		}
		if tc.fn == "E.Error" {
			names = []string{"e"}
		}
		if tc.fn == "scan" {
			names = []string{"in"}
		}
		yes, maybe, calls, ok := c.nilSites("m", tc.fn, names)
		if !ok || len(yes) != tc.yes || len(maybe) != tc.maybe || calls != tc.calls {
			t.Errorf("%s: yes %v maybe %v calls %d ok %v", tc.fn, yes, maybe, calls, ok)
		}
	}
}

func TestConstArgs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(`package m

func trunc(s string, n int) string { return s[:n] }

func use(k int) {
	_ = trunc("abc", 2)
	_ = trunc("abcd", 3)
}

func clip(n int) int { return 10 / n }

func use2(k int) { _ = clip(k); _ = clip(4) }
`), 0o644)
	c := newCallerIndex(dir)
	vals, all, calls := c.constArgs("m", "trunc", []string{"n"})
	if !all || calls != 2 || len(vals["n"]) != 2 {
		t.Errorf("trunc: %v %v %d", vals, all, calls)
	}
	if _, all, _ := c.constArgs("m", "clip", []string{"n"}); all {
		t.Error("clip: one caller passes a variable")
	}
}

func TestPanicSite(t *testing.T) {
	stack := "goroutine 7 [running]:\nruntime/debug.Stack()\n\t/usr/local/go/src/runtime/debug/stack.go:26 +0x5e\n" +
		"m.TestGo2LeanPoC_x.func1.1()\n\t/src/m/zz_go2lean_poc_1_test.go:20 +0x3b\n" +
		"panic({0x1, 0x2})\n\t/usr/local/go/src/runtime/panic.go:787 +0x132\n" +
		"m.Last(...)\n\t/src/m/oob.go:12\n" +
		"m.TestGo2LeanPoC_x.func1()\n\t/src/m/zz_go2lean_poc_1_test.go:24 +0x5d\n"
	if got := panicSite(stack, "/src"); got != "/src/m/oob.go:12" {
		t.Errorf("panicSite = %q", got)
	}
	if got := panicSite(stack, "/elsewhere"); got != "" {
		t.Errorf("panicSite outside the package = %q", got)
	}
}

func TestCalleeOf(t *testing.T) {
	for site, want := range map[string]string{
		"processEvent: call (github.com/openai/openai-go/v3/responses.ResponseStreamEventUnion).AsResponseIncomplete.Response.ID": "(github.com/openai/openai-go/v3/responses.ResponseStreamEventUnion).AsResponseIncomplete",
		"newResponsesToAnthropicConverter: call github.com/tingly-dev/tingly-box/internal/protocol/ids.Message":                   "github.com/tingly-dev/tingly-box/internal/protocol/ids.Message",
		"f: call strings.SplitSeq":               "strings.SplitSeq",
		"f: call t11 (callee stuck).InputTokens": "",
		"f: map lookup #0":                       "",
	} {
		if got := calleeOf(site); got != want {
			t.Errorf("calleeOf(%q) = %q, want %q", site, got, want)
		}
	}
}

func TestStdCallee(t *testing.T) {
	for c, want := range map[string]bool{
		"(*regexp.Regexp).FindAllStringSubmatch": true,
		"strings.SplitSeq":                       true,
		"encoding/json.Marshal":                  true,
		"github.com/tingly-dev/tingly-box/internal/protocol/ids.Message":                           false,
		"(github.com/openai/openai-go/v3/responses.ResponseStreamEventUnion).AsResponseIncomplete": false,
	} {
		if got := stdCallee(c); got != want {
			t.Errorf("stdCallee(%q) = %v, want %v", c, got, want)
		}
	}
}

func TestConstructorOf(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(`package m

type Context struct{ Keys, Values []string }

func NewRouteContext() *Context { return &Context{} }

func (c *Context) URLParam(k string) string {
	for i, x := range c.Keys {
		if x == k {
			return c.Values[i]
		}
	}
	return ""
}

type Collection []int

func (c Collection) Less(i, j int) bool { return c[i] < c[j] }

type Plain struct{ n int }

func (p *Plain) Get() int { return p.n }
`), 0o644)
	c := newCallerIndex(dir)
	for fn, want := range map[string]string{"Context.URLParam": "NewRouteContext", "Collection.Less": "", "Plain.Get": "", "NewRouteContext": ""} {
		if got := c.constructorOf("m", fn); got != want {
			t.Errorf("constructorOf(%q) = %q, want %q", fn, got, want)
		}
	}
}

// TestUnexportedSet: a receiver whose unexported field the test set to a
// non-zero value is not a caller's plain input (grpc's mem.Reader, #220);
// one left at its zero value is.
func TestUnexportedSet(t *testing.T) {
	abi := &contract.ABI{Method: "Reader.ReadByte", Params: []contract.ABIParam{{Go: "r", Pointee: &contract.ABIParam{Fields: []contract.ABIParam{{Go: "data"}, {Go: "len"}, {Go: "Exported"}}}}}}
	for _, c := range []struct{ obj, want string }{
		{`[[], "106", "0"]`, "len"},
		{`[[], "0", "5"]`, ""},
		{`[null, "0", "0"]`, ""},
	} {
		// The same receiver through the type table, as a struct that
		// refers to itself is spelled.
		ref := &contract.ABI{Method: "Reader.ReadByte", Types: []contract.ABIType{{GoType: "Reader", Fields: abi.Params[0].Pointee.Fields}},
			Params: []contract.ABIParam{{Go: "r", Pointee: &contract.ABIParam{TypeRef: "Reader"}}}}
		if got := unexportedSet(ref, &contract.Counterexample{Objects: map[string]json.RawMessage{"r": json.RawMessage(c.obj)}}); got != c.want {
			t.Errorf("type table, %s: got %q, want %q", c.obj, got, c.want)
		}
		cx := &contract.Counterexample{Objects: map[string]json.RawMessage{"r": json.RawMessage(c.obj)}}
		if got := unexportedSet(abi, cx); got != c.want {
			t.Errorf("%s: got %q, want %q", c.obj, got, c.want)
		}
	}
}

func TestExportedTypeName(t *testing.T) {
	for in, want := range map[string]bool{"*buffer": false, "*mem.Buffer": true, "SliceBuffer": true, "[]*pkg.inner": false, "componentData": false} {
		if got := exportedTypeName(in); got != want {
			t.Errorf("exportedTypeName(%q) = %v, want %v", in, got, want)
		}
	}
}

// An argument (not the receiver) whose unexported field the test set:
// regexp2's FindNextMatch(m *Match) on a Match with a forged position
// (#237). The constructor's arguments are not checked.
func TestUnexportedArg(t *testing.T) {
	match := contract.ABIParam{Go: "m", GoType: "*Match", Kind: contract.ABIPtr, Pointee: &contract.ABIParam{
		Go: "m", GoType: "Match", Kind: contract.ABIStruct, Fields: []contract.ABIParam{
			{Go: "Index", Kind: contract.ABIInt}, {Go: "textpos", Kind: contract.ABIInt}}}}
	abi := &contract.ABI{Ctor: &contract.ABICtor{Func: "Compile", N: 1}, Params: []contract.ABIParam{
		{Go: "expr", Kind: contract.ABIString}, match}}
	for obj, want := range map[string]string{
		`[ "3", "-8" ]`: "m.textpos",
		`[ "3", "0" ]`:  "",
	} {
		cx := &contract.Counterexample{Objects: map[string]json.RawMessage{"m": json.RawMessage(obj)}}
		if got := unexportedArg(abi, cx); got != want {
			t.Errorf("%s: unexportedArg = %q, want %q", obj, got, want)
		}
	}
}

// An interface argument holding the package's struct with an unexported
// field set: blackfriday's Renderer as an *Html with a forged tocMarker.
func TestUnexportedArgInAlt(t *testing.T) {
	html := contract.ABIParam{Go: "renderer", GoType: "*Html", Kind: contract.ABIPtr, Tag: 7, Pointee: &contract.ABIParam{
		Go: "renderer", GoType: "Html", Kind: contract.ABIStruct, Fields: []contract.ABIParam{
			{Go: "flags", Kind: contract.ABIInt}, {Go: "tocMarker", Kind: contract.ABIInt}}}}
	abi := &contract.ABI{Params: []contract.ABIParam{{Go: "renderer", GoType: "Renderer", Kind: contract.ABIAny, Alts: []contract.ABIParam{html}}}}
	cx := &contract.Counterexample{Objects: map[string]json.RawMessage{"renderer": json.RawMessage(`{"tag": 7, "v": ["0", "91"]}`)}}
	if got := unexportedArg(abi, cx); got != "renderer.tocMarker" {
		t.Errorf("unexportedArg = %q, want renderer.tocMarker", got)
	}
}

func TestNamedType(t *testing.T) {
	for typ, want := range map[string]bool{"*buffer": true, "mem.BufferSlice": true, "[]interface{}": false, "map[string]interface{}": false, "string": false, "*int": false} {
		if got := namedType(typ); got != want {
			t.Errorf("namedType(%q) = %v, want %v", typ, got, want)
		}
	}
}
