package poc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andreasfoo/go2lean/internal/contract"
)

func intParam(name string) contract.ABIParam {
	return contract.ABIParam{Go: name, GoType: "int", Width: 64, Signed: true, Model: []string{name}, Kind: contract.ABIInt}
}

func TestLiteralStructAndPointer(t *testing.T) {
	node := contract.ABIType{GoType: "Node", Fields: []contract.ABIParam{
		intParam("Val"),
		{Go: "Next", GoType: "*Node", Model: []string{"Next"}, Kind: contract.ABIPtr,
			Pointee: &contract.ABIParam{Go: "Next", GoType: "Node", Model: []string{"Next"}, Kind: contract.ABIStruct, TypeRef: "Node"}},
	}}
	abi := &contract.ABI{Types: []contract.ABIType{node}}
	p := contract.ABIParam{Go: "n", GoType: "*Node", Model: []string{"n"}, Kind: contract.ABIPtr,
		Pointee: &contract.ABIParam{Go: "n", GoType: "Node", Model: []string{"n"}, Kind: contract.ABIStruct, TypeRef: "Node"}}
	var v any
	if err := json.Unmarshal([]byte(`["1", ["2", null]]`), &v); err != nil {
		t.Fatal(err)
	}
	got, err := literal(abi, p, v)
	if err != nil {
		t.Fatal(err)
	}
	want := "&Node{Val: int(1), Next: &Node{Val: int(2)}}"
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestLiteralScalarPointerAndString(t *testing.T) {
	p := contract.ABIParam{Go: "p", GoType: "*int", Model: []string{"p"}, Kind: contract.ABIPtr, Pointee: ptr(intParam("p"))}
	got, err := literal(&contract.ABI{}, p, "5")
	if err != nil || got != "func() *int { v := int(5); return &v }()" {
		t.Fatalf("got %q, %v", got, err)
	}
	s := contract.ABIParam{Go: "s", GoType: "string", Model: []string{"s"}, Kind: contract.ABIString}
	got, err = literal(&contract.ABI{}, s, "a\"b")
	if err != nil || got != `string("a\"b")` {
		t.Fatalf("got %q, %v", got, err)
	}
}

func ptr(p contract.ABIParam) *contract.ABIParam { return &p }

func TestHarnessMethodCallAndImports(t *testing.T) {
	abi := &contract.ABI{Method: "Withdraw", Params: []contract.ABIParam{
		{Go: "a", GoType: "*Account", Model: []string{"a"}, Kind: contract.ABIPtr,
			Pointee: &contract.ABIParam{Go: "a", GoType: "Account", Model: []string{"a"}, Kind: contract.ABIStruct,
				Fields: []contract.ABIParam{intParam("Balance"), {Go: "D", GoType: "time.Duration", TypePkg: "time", Width: 64, Signed: true, Model: []string{"D"}, Kind: contract.ABIInt}}}},
		intParam("amt"),
	}, Results: []contract.ABIResult{{GoType: "bool"}}}
	cx := &contract.Counterexample{ID: "cx1", Inputs: map[string]string{"amt": "-3"},
		Objects: map[string]json.RawMessage{"a": json.RawMessage(`["10", "7"]`)}}
	src, err := Harness("alias", &contract.Target{Func: "Account.Withdraw"}, &contract.Property{Kind: contract.KindNoPanic}, cx, abi)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"time"`, "a := &Account{Balance: int(10), D: time.Duration(7)}", "ret = a.Withdraw(amt)"} {
		if !strings.Contains(src, want) {
			t.Fatalf("harness lacks %q:\n%s", want, src)
		}
	}
}

func TestParseResultsMapsByTestName(t *testing.T) {
	out := "=== RUN   TestGo2LeanPoC_a\nGO2LEAN_RESULT {\"panicked\":false,\"ret\":\"1\"}\n--- PASS: TestGo2LeanPoC_a (0.00s)\n=== RUN   TestGo2LeanPoC_b\nGO2LEAN_RESULT {\"panicked\":true,\"panic\":\"runtime error: index out of range [2] with length 2\"}\n"
	rs := parseResults(out)
	if len(rs) != 2 || rs["a"].Ret != "1" || !rs["b"].Panicked {
		t.Fatalf("unexpected %+v", rs)
	}
	if ClassifyPanic(rs["b"].Panic) != contract.PanicIndexOutOfRange {
		t.Fatal("panic not classified")
	}
}

func TestCanImport(t *testing.T) {
	cases := []struct {
		from, pkg string
		ok        bool
	}{
		{"example.com/m/internal/typ", "example.com/m/internal/other", true},
		{"example.com/m/cli", "example.com/m/internal/typ", true},
		{"example.com/other", "example.com/m/internal/typ", false},
		{"example.com/m", "example.com/m/internal", true},
		{"", "example.com/m/internal/typ", false},
		{"example.com/other", "example.com/m/typ", true},
	}
	for _, c := range cases {
		if got := canImport(c.from, c.pkg); got != c.ok {
			t.Errorf("canImport(%q, %q) = %v, want %v", c.from, c.pkg, got, c.ok)
		}
	}
}

func TestUsesPackage(t *testing.T) {
	cases := []struct {
		code, pkg string
		ok        bool
	}{
		{`var r io.Reader`, "io", true},
		{`s := string("ratio.x io.")`, "io", false},
		{`x := ratio.New()`, "io", false},
		{`x := []http.Header{}`, "http", true},
		{`s := string("a\"io.b")`, "io", false},
	}
	for _, c := range cases {
		if got := usesPackage(c.code, c.pkg); got != c.ok {
			t.Errorf("usesPackage(%q, %q) = %v, want %v", c.code, c.pkg, got, c.ok)
		}
	}
}

func TestCompileErrWithoutColumn(t *testing.T) {
	line := "internal/protocol/stream/zz_go2lean_poc_6421_test.go:19: cannot use struct{raw string}{} (value of type struct{raw string}) as struct{raw string} value in struct literal"
	if compileErr.FindString(line) == "" {
		t.Fatal("a compile error without a column is not recognised")
	}
}

func TestEmptyComposite(t *testing.T) {
	for e, want := range map[string]bool{
		"T{}":                         true,
		"struct{A int; raw string}{}": true,
		"T{A: 1}":                     false,
		"[]int{1}":                    false,
		"struct{A int}{A: 1}":         false,
		"nil":                         false,
	} {
		if got := emptyComposite(e); got != want {
			t.Errorf("emptyComposite(%q) = %v, want %v", e, got, want)
		}
	}
}

func TestLiteralMapAndInterface(t *testing.T) {
	str := contract.ABIParam{Go: "v", GoType: "string", Model: []string{"v"}, Kind: contract.ABIString, Tag: 7}
	inner := contract.ABIParam{Go: "value", GoType: "interface{}", Model: []string{"value"}, Kind: contract.ABIAny, Zero: "nil",
		Alts: []contract.ABIParam{str}}
	m := contract.ABIParam{Go: "v", GoType: "map[string]interface{}", Model: []string{"v"}, Kind: contract.ABIMap, Zero: "nil",
		Key: ptr(contract.ABIParam{Go: "key", GoType: "string", Model: []string{"key"}, Kind: contract.ABIString}), Value: &inner, Tag: 9}
	p := contract.ABIParam{Go: "x", GoType: "any", Model: []string{"x"}, Kind: contract.ABIAny, Zero: "nil", Alts: []contract.ABIParam{str, m}}
	var v any
	if err := json.Unmarshal([]byte(`{"tag": 9, "v": {"map": [["role", {"tag": 7, "v": "system"}], ["n", null]]}}`), &v); err != nil {
		t.Fatal(err)
	}
	got, err := literal(&contract.ABI{}, p, v)
	want := `map[string]interface{}{string("role"): string("system"), string("n"): nil}`
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
	if got, _ := literal(&contract.ABI{}, m, nil); got != "(map[string]interface{})(nil)" {
		t.Fatalf("nil map: got %q", got)
	}
}

// An interface field holding an empty map is not nil: it must be set (the
// field is the JSON schema of a structured-output format: nil means none).
func TestLiteralInterfaceFieldHoldingEmptyMap(t *testing.T) {
	inner := contract.ABIParam{Go: "value", GoType: "interface{}", Model: []string{"value"}, Kind: contract.ABIAny, Zero: "nil"}
	m := contract.ABIParam{Go: "v", GoType: "map[string]interface{}", Model: []string{"v"}, Kind: contract.ABIMap, Zero: "nil",
		Key: ptr(contract.ABIParam{Go: "key", GoType: "string", Model: []string{"key"}, Kind: contract.ABIString}), Value: &inner, Tag: 9}
	schema := contract.ABIParam{Go: "Schema", GoType: "any", Model: []string{"Schema"}, Kind: contract.ABIAny, Zero: "nil", Alts: []contract.ABIParam{m}}
	st := contract.ABIParam{Go: "f", GoType: "Format", Model: []string{"f"}, Kind: contract.ABIStruct, Fields: []contract.ABIParam{schema}}
	var v any
	if err := json.Unmarshal([]byte(`[{"tag": 9, "v": {"map": []}}]`), &v); err != nil {
		t.Fatal(err)
	}
	got, err := literal(&contract.ABI{}, st, v)
	if want := `Format{Schema: map[string]interface{}{}}`; err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestLiteralRawJSONValueIsDecoded(t *testing.T) {
	meta := contract.ABIParam{Go: "JSON", GoType: "struct{raw string}", Model: []string{"JSON"}, Kind: contract.ABIStruct, JSONName: "-",
		Fields: []contract.ABIParam{{Go: "raw", GoType: "string", Model: []string{"raw"}, Kind: contract.ABIString, JSONText: true}}}
	n := intParam("N")
	n.JSONName = "n"
	inner := contract.ABIParam{Go: "Delta", GoType: "sdk.Delta", Model: []string{"Delta"}, Kind: contract.ABIStruct, JSONName: "delta",
		Fields: []contract.ABIParam{n, meta}}
	st := contract.ABIParam{Go: "c", GoType: "sdk.Chunk", Model: []string{"c"}, Kind: contract.ABIStruct,
		Fields: []contract.ABIParam{inner, meta}}
	p := contract.ABIParam{Go: "c", GoType: "*sdk.Chunk", Model: []string{"c"}, Kind: contract.ABIPtr, Pointee: &st}
	got, err := literal(&contract.ABI{}, p, []any{[]any{"1", []any{`{"x":null,"n":5}`}}, []any{""}})
	if err != nil {
		t.Fatal(err)
	}
	want := `func() *sdk.Chunk { v := func() sdk.Chunk { var go2leanV sdk.Chunk; _ = json.Unmarshal([]byte("{\"delta\":{\"n\":1,\"x\":null}}"), &go2leanV); return go2leanV }(); return &v }()`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestJSONDocInlinesUnionVariant(t *testing.T) {
	meta := contract.ABIParam{Go: "JSON", GoType: "struct{raw string}", Model: []string{"JSON"}, Kind: contract.ABIStruct, JSONName: "-",
		Fields: []contract.ABIParam{{Go: "raw", GoType: "string", Model: []string{"raw"}, Kind: contract.ABIString, JSONText: true}}}
	args := contract.ABIParam{Go: "Arguments", GoType: "sdk.Args", Model: []string{"Arguments"}, Kind: contract.ABIStruct, JSONName: "arguments",
		Fields: []contract.ABIParam{{Go: "OfString", GoType: "string", Model: []string{"OfString"}, Kind: contract.ABIString, JSONName: "OfString", JSONInline: true}}}
	item := contract.ABIParam{Go: "item", GoType: "sdk.Item", Model: []string{"item"}, Kind: contract.ABIStruct, Fields: []contract.ABIParam{args, meta}}
	doc, _ := json.Marshal(jsonDoc(&contract.ABI{}, item, []any{[]any{`{"a":1}`}, []any{`{"x":true}`}}))
	if string(doc) != `{"arguments":"{\"a\":1}","x":true}` {
		t.Fatalf("got %s", doc)
	}
}

func TestProgramStructHoldingSDKValueIsALiteral(t *testing.T) {
	meta := contract.ABIParam{Go: "JSON", GoType: "struct{raw string}", Model: []string{"JSON"}, Kind: contract.ABIStruct, JSONName: "-",
		Fields: []contract.ABIParam{{Go: "raw", GoType: "string", Model: []string{"raw"}, Kind: contract.ABIString, JSONText: true}}}
	n := intParam("N")
	n.JSONName = "n"
	resp := contract.ABIParam{Go: "response", GoType: "sdk.Response", Model: []string{"response"}, Kind: contract.ABIStruct,
		Fields: []contract.ABIParam{n, meta}}
	count := intParam("count")
	st := contract.ABIParam{Go: "a", GoType: "Assembler", Model: []string{"a"}, Kind: contract.ABIStruct,
		Fields: []contract.ABIParam{{Go: "response", GoType: "*sdk.Response", Model: []string{"response"}, Kind: contract.ABIPtr, Pointee: &resp}, count}}
	got, err := literal(&contract.ABI{}, st, []any{[]any{"1", []any{`{"x":true}`}}, "2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "Assembler{") || !strings.Contains(got, "json.Unmarshal") || !strings.Contains(got, "count: int(2)") {
		t.Fatalf("want a literal holding a decoded SDK value, got %s", got)
	}
}

// A test function as the target (-tests) whose model run deadlocked: its t
// is the harness's own, the call runs under the concurrency watchdog, and
// the harness compiles in a module older than Go 1.18 (no `any`).
func TestHarnessTestEntryConc(t *testing.T) {
	abi := &contract.ABI{Params: []contract.ABIParam{
		{Go: "t", GoType: "*testing.T", Kind: contract.ABIOpaque, Model: []string{"t"}, Zero: "go2leanT"},
	}}
	cx := &contract.Counterexample{ID: "cx1", Objects: map[string]json.RawMessage{"t": json.RawMessage(`null`)},
		ModelOutput: &contract.ModelOutput{Res: "panic", Panic: contract.PanicDeadlock}}
	src, err := Harness("m", &contract.Target{Func: "TestKernel", Package: "example.com/m"}, &contract.Property{Kind: contract.KindNoPanic}, cx, abi)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"t := go2leanT", "TestKernel(t)", "time.After(", `"runtime"`} {
		if !strings.Contains(src, want) {
			t.Fatalf("harness lacks %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "any{") || strings.Contains(src, "]any") {
		t.Fatalf("harness uses any (needs Go 1.18):\n%s", src)
	}
	if got := rankOf(&prepared{prop: &contract.Property{Kind: contract.KindNoPanic}, target: &contract.Target{Func: "TestKernel"},
		cx: &contract.Counterexample{}, v: &contract.Verdict{}, test: true}, nil); got.Tier != 1 {
		t.Fatalf("rank of a failing test = %+v", got)
	}
}

// TestVerdictReasonsForBuildFailures tells the harness's own compile error
// from a package whose tests do not build and from a target that exited.
func TestVerdictReasonsForBuildFailures(t *testing.T) {
	p := &prepared{cx: &contract.Counterexample{ID: "cx1"}}
	for _, c := range []struct{ stdout, stderr, want string }{
		{"", "./zz_go2lean_poc_12_test.go:3:2: undefined: x", "harness_compile_error"},
		{"", "# example.com/p\n./p_test.go:114:11: Errorf call has arguments but no formatting directives", "package_build_error"},
		{"=== RUN   TestGo2LeanPoC_cx1\n", "exit status 3", "target_exited"},
		{"", "# example.com/p.test\n/go/pkg/tool/linux_amd64/link: mapping output file failed: no space left on device", "package_build_error"},
	} {
		v := verdictOf(p, nil, "go test", 1, c.stdout, c.stderr, "")
		if v.Diagnosis == nil || v.Diagnosis.Reason != c.want {
			t.Errorf("stderr %q: diagnosis %+v, want %s", c.stderr, v.Diagnosis, c.want)
		}
	}
}
