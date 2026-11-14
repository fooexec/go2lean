package constraint

import (
	"encoding/json"
	"testing"

	"github.com/fooexec/go2lean/internal/contract"
)

func TestGensAndTypeTable(t *testing.T) {
	abi := &contract.ABI{
		Params: []contract.ABIParam{
			{Go: "n", GoType: "int8", Width: 8, Signed: true, Model: []string{"n"}, Kind: contract.ABIInt},
			{Go: "xs", GoType: "[]uint", ElemWidth: 64, Model: []string{"xs_ref", "xs_off", "xs_len", "xs_cap"}, Kind: contract.ABISlice, Elem: "uint"},
			{Go: "s", GoType: "string", Model: []string{"s"}, Kind: contract.ABIString},
			{Go: "l", GoType: "*Node", Model: []string{"l"}, Kind: contract.ABIPtr,
				Pointee: &contract.ABIParam{Go: "l", GoType: "Node", Model: []string{"l"}, Kind: contract.ABIStruct, TypeRef: "Node"}},
		},
		Types: []contract.ABIType{{GoType: "Node", Fields: []contract.ABIParam{
			{Go: "Val", GoType: "int", Width: 64, Signed: true, Model: []string{"Val"}, Kind: contract.ABIInt},
			{Go: "Next", GoType: "*Node", Model: []string{"Next"}, Kind: contract.ABIPtr,
				Pointee: &contract.ABIParam{Go: "Next", GoType: "Node", Model: []string{"Next"}, Kind: contract.ABIStruct, TypeRef: "Node"}},
		}}},
	}
	g, err := gens(abi, 5)
	if err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(g)
	want := `[{"k":"int","name":"n","signed":true,"w":8},{"k":"slice","maxLen":5,"name":"xs","signed":false,"w":64},{"k":"str","maxLen":5,"name":"s"},{"k":"ptr","name":"l","pointee":{"k":"ref","name":"l","ty":0}}]`
	if string(gj) != want {
		t.Fatalf("gens: got %s\nwant %s", gj, want)
	}
	tbl, err := typeTable(abi, 5)
	if err != nil {
		t.Fatal(err)
	}
	tj, _ := json.Marshal(tbl)
	if string(tj) != `[[{"k":"int","name":"Val","signed":true,"w":64},{"k":"link","name":"Next","pointee":{"k":"ref","name":"Next","ty":0}}]]` {
		t.Fatalf("type table: %s", tj)
	}
}

func TestHotEnumsAndVariants(t *testing.T) {
	g := []map[string]any{{"k": "structv", "name": "item", "fields": []map[string]any{
		{"k": "str", "name": "Type", "enum": []string{"message", "reasoning", "function_call", "web_search_call"}},
		{"k": "ptr", "name": "OfTool"},
		{"k": "ptr", "name": "OfBash"},
	}}}
	hotEnums(g, []string{"reasoning"})
	hotVariants(g, []string{"OfTool"})
	fields := g[0]["fields"].([]map[string]any)
	enum := fields[0]["enum"].([]string)
	n := 0
	for _, e := range enum {
		if e == "reasoning" {
			n++
		}
	}
	if 2*n < len(enum) {
		t.Errorf("compared value %d of %d enum entries, want half: %v", n, len(enum), enum)
	}
	if fields[1]["k"] != "hot" || fields[1]["name"] != "OfTool" || fields[2]["k"] != "ptr" {
		t.Errorf("variants: %v", fields)
	}
}

// A slice whose elements can only be nil (functional options) stays empty:
// dateparse's `opts ...ParserOption` otherwise ended every search at a nil
// option call.
func TestNilOnlySliceEmpty(t *testing.T) {
	abi := &contract.ABI{Variadic: true, Params: []contract.ABIParam{
		{Go: "s", GoType: "string", Model: []string{"s"}, Kind: contract.ABIString},
		{Go: "opts", GoType: "[]ParserOption", Model: []string{"opts_ref", "opts_off", "opts_len", "opts_cap"}, Kind: contract.ABISlice,
			ElemParam: &contract.ABIParam{Go: "opts", GoType: "ParserOption", Model: []string{"opts"}, Kind: contract.ABIOpaque, Zero: "nil"}},
	}}
	g, err := gens(abi, 5)
	if err != nil {
		t.Fatal(err)
	}
	if n := g[1]["maxLen"]; n != 0 {
		t.Errorf("opts: maxLen %v, want 0", n)
	}
}
