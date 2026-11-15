package semantics

import (
	"encoding/json"
	"reflect"
)

// reflect.DeepEqual on values encoding/json decoded (gjson's and sjson's
// specs compare them): a decoded number is a float64 like the program's.

func testDeepEqualJSONMap() bool {
	var m map[string]any
	if json.Unmarshal([]byte(`{"a":1,"b":"x","c":[1.5,null]}`), &m) != nil {
		return false
	}
	return reflect.DeepEqual(m, map[string]any{"a": 1.0, "b": "x", "c": []any{1.5, nil}}) &&
		!reflect.DeepEqual(m, map[string]any{"a": 2.0, "b": "x", "c": []any{1.5, nil}})
}

func testDeepEqualJSONNumbers() bool {
	var a, b any
	if json.Unmarshal([]byte(`[1e3,-0.25]`), &a) != nil || json.Unmarshal([]byte(`[1000,-0.25]`), &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b) && reflect.DeepEqual(a, any([]any{1000.0, -0.25}))
}
