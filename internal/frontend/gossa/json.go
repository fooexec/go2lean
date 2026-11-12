package gossa

import (
	"encoding/json"
	"go/constant"
	"go/types"
	"reflect"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/fooexec/go2lean/internal/ir"
)

// jsonTable collects the JSON descriptions (Go2Lean.GoSSA.JDesc) of the
// types a package encodes or decodes with encoding/json, and of the types
// its interfaces may hold. The Lean module gets it as `jsonTable`; the
// jsonMarshal / jsonDecode ops refer to entries by index.
type jsonTable struct {
	descs []map[string]any // JDesc entries as Lean's JTable.ofJsonString reads them
	zeros []any            // the zero value of each entry's type (zero-model JSON)
	index map[string]int
	sprog *ssa.Program
	// keys are the JSON keys of the struct fields described: decoding
	// inputs need them.
	keys []string
	// ifaces records that some description is an interface, which needs
	// the boxed types' descriptions.
	ifaces bool
}

func newJSONTable(sprog *ssa.Program) *jsonTable {
	return &jsonTable{index: map[string]int{}, sprog: sprog}
}

// sdkPkg says whether path is (inside) the OpenAI or Anthropic Go SDK,
// whose types encode by their own rules (param objects, unions, Opt,
// constants) and decode with JSON metadata.
func sdkPkg(path string) bool {
	return strings.HasPrefix(path, "github.com/openai/openai-go") || strings.HasPrefix(path, "github.com/anthropics/anthropic-sdk-go")
}

func namedIn(t types.Type, pkgSuffix, name string) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && strings.HasSuffix(n.Obj().Pkg().Path(), pkgSuffix) && n.Obj().Name() == name
}

func hasMethod(t types.Type, name string) bool {
	for _, mt := range []types.Type{t, types.NewPointer(t)} {
		ms := types.NewMethodSet(mt)
		for i := 0; i < ms.Len(); i++ {
			if ms.At(i).Obj().Name() == name {
				return true
			}
		}
	}
	return false
}

// constDefault is what an SDK constant type's Default method returns
// (`func (c Text) Default() Text { return "text" }`), if it is a literal.
func (j *jsonTable) constDefault(t types.Type) string {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || j.sprog == nil {
		return ""
	}
	for i := 0; i < n.NumMethods(); i++ {
		m := n.Method(i)
		if m.Name() != "Default" {
			continue
		}
		fn := j.sprog.FuncValue(m)
		if fn == nil {
			return ""
		}
		for _, b := range fn.Blocks {
			for _, in := range b.Instrs {
				if r, ok := in.(*ssa.Return); ok && len(r.Results) == 1 {
					if c, ok := r.Results[0].(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
						return constant.StringVal(c.Value)
					}
				}
			}
		}
	}
	return ""
}

// desc returns the table index of t's description, building it (and the
// descriptions it refers to) on first use.
func (j *jsonTable) desc(t types.Type) int {
	key := types.TypeString(t, nil)
	if i, ok := j.index[key]; ok {
		return i
	}
	i := len(j.descs)
	j.index[key] = i
	j.descs = append(j.descs, opaqueDesc)
	var zero any = map[string]any{"k": "unit"}
	if z, ok := zeroVal(t); ok {
		zero = zeroModel(z)
	} else if isNilable(t) {
		zero = zeroModel(ir.OpaqueNil{})
	}
	j.zeros = append(j.zeros, zero)
	j.descs[i] = j.build(t)
	return i
}

var opaqueDesc = map[string]any{"k": "opaque"}

func kind(k string, kv ...any) map[string]any {
	m := map[string]any{"k": k}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func (j *jsonTable) build(t types.Type) map[string]any {
	t = types.Unalias(t)
	switch {
	case namedIn(t, "/packages/param", "Opt"):
		return kind("opt", "e", j.desc(t.(*types.Named).TypeArgs().At(0)))
	case namedIn(t, "encoding/json", "RawMessage"):
		return kind("raw")
	}
	n, named := t.(*types.Named)
	inSDK := named && n.Obj().Pkg() != nil && sdkPkg(n.Obj().Pkg().Path())
	if st, ok := t.Underlying().(*types.Struct); ok && inSDK && hasMethod(t, "MarshalJSON") && isUnionStruct(st) {
		vs := []any{}
		for i := 0; i < st.NumFields(); i++ {
			f := st.Field(i)
			if f.Exported() && strings.HasPrefix(f.Name(), "Of") {
				vs = append(vs, map[string]any{"key": "", "idx": i, "ty": j.desc(f.Type())})
			}
		}
		return kind("union", "variants", vs)
	}
	if isString(t) && inSDK && hasMethod(t, "Default") {
		return kind("str", "dflt", j.constDefault(t))
	}
	if named && !inSDK && (hasMethod(t, "MarshalJSON") || hasMethod(t, "UnmarshalJSON") ||
		hasMethod(t, "MarshalText") || hasMethod(t, "UnmarshalText") || namedIn(t, "encoding/json", "Number")) {
		// A custom encoding the model does not follow (a pointer has its
		// element's methods: its description is the element's).
		return opaqueDesc
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch {
		case u.Info()&types.IsString != 0:
			return kind("str")
		case u.Info()&types.IsBoolean != 0:
			return kind("bool")
		case u.Info()&types.IsInteger != 0:
			w, signed, _ := intWidth(t)
			return kind("int", "w", w, "s", signed)
		case u.Info()&types.IsFloat != 0:
			return kind("float")
		}
	case *types.Slice:
		if isByteSlice(t) {
			return kind("bytes")
		}
		return kind("slice", "e", j.desc(u.Elem()))
	case *types.Array:
		return kind("arr", "n", u.Len(), "e", j.desc(u.Elem()))
	case *types.Pointer:
		return kind("ptr", "e", j.desc(u.Elem()))
	case *types.Map:
		if _, _, isInt := intWidth(u.Key()); isString(u.Key()) || isInt {
			return kind("map", "key", j.desc(u.Key()), "val", j.desc(u.Elem()))
		}
	case *types.Interface:
		// Only an empty interface decodes (Go cannot pick a type for
		// another); one holding a value encodes by its dynamic type.
		if u.Empty() {
			j.ifaces = true
			return kind("iface")
		}
	case *types.Struct:
		// The SDKs' types decode with their own apijson decoder.
		return j.structDesc(u, inSDK, inSDK && hasMethod(t, "UnmarshalJSON"))
	}
	return opaqueDesc
}

// isUnionStruct says whether st is an SDK union: its exported fields are
// all variants `Of<Variant>`.
func isUnionStruct(st *types.Struct) bool {
	seen := false
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Exported() {
			continue
		}
		if !strings.HasPrefix(f.Name(), "Of") {
			return false
		}
		seen = true
	}
	return seen
}

func (j *jsonTable) structDesc(st *types.Struct, sdk, api bool) map[string]any {
	fields := []any{}
	var meta any
	jsonKeys := map[string]string{} // Go field name -> JSON key
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		tag := reflect.StructTag(st.Tag(i)).Get("json")
		if sdk && !f.Exported() && f.Anonymous() {
			// The SDKs' paramObj metadata (SetExtraFields, overrides,
			// null) changes the encoding: the model follows it only while
			// it is zero.
			fields = append(fields, map[string]any{"key": "", "idx": i, "ty": 0, "guard": true})
			continue
		}
		if sdk && strings.Contains(reflect.StructTag(st.Tag(i)).Get("api"), "extrafields") {
			// Typed ExtraFields: decoding puts the members no field reads
			// there; encoding merges them in (the model only encodes it
			// empty).
			fields = append(fields, map[string]any{"key": "", "idx": i, "ty": j.desc(f.Type()), "guard": true, "extras": true})
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		optList := strings.Split(opts, ",")
		if f.Name() == "JSON" && name == "-" {
			if mst, ok := f.Type().Underlying().(*types.Struct); ok && isRespJSONMeta(mst) {
				meta = j.metaDesc(i, mst, jsonKeys)
			}
			continue
		}
		if tag == "-" {
			continue // `json:"-,"` is the key "-"
		}
		inline := (f.Anonymous() && name == "") || (name == "" && contains(optList, "inline"))
		if !f.Exported() && !inline {
			continue
		}
		if !f.Exported() && f.Anonymous() {
			if _, isStruct := derefStruct(f.Type()); !isStruct {
				continue // an unexported embedded non-struct is ignored
			}
		}
		key := name
		if key == "" {
			key = f.Name()
		}
		jsonKeys[f.Name()] = key
		if !contains(j.keys, key) {
			j.keys = append(j.keys, key)
		}
		ty := j.desc(f.Type())
		if contains(optList, "string") {
			ty = j.desc(types.NewSignatureType(nil, nil, nil, nil, nil, false)) // `,string`: not modelled
		}
		fields = append(fields, map[string]any{"key": key, "idx": i, "ty": ty,
			"oe": contains(optList, "omitempty"), "oz": contains(optList, "omitzero"), "in": inline})
	}
	return kind("strct", "fields", fields, "meta", meta, "api", api)
}

// keysOf lists the JSON keys a value of entry root reads, breadth first
// (its own members before those of nested types).
func (j *jsonTable) keysOf(root int) []string {
	var keys []string
	seen := map[int]bool{}
	queue := []int{root}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		if seen[i] || i >= len(j.descs) {
			continue
		}
		seen[i] = true
		d := j.descs[i]
		for _, k := range []string{"e", "val"} {
			if e, ok := d[k].(int); ok {
				queue = append(queue, e)
			}
		}
		for _, fk := range []string{"fields", "variants"} {
			fs, _ := d[fk].([]any)
			for _, f := range fs {
				m := f.(map[string]any)
				if g, _ := m["guard"].(bool); g {
					continue
				}
				if k, _ := m["key"].(string); k != "" && !m["in"].(bool) && !contains(keys, k) {
					keys = append(keys, k)
				}
				queue = append(queue, m["ty"].(int))
			}
		}
	}
	return keys
}

// metaDesc describes the JSON metadata field (index fi) of an SDK response
// type: where its raw text, ExtraFields and per-member respjson.Field go.
// It is called after the typed fields before it were seen; members are
// matched by Go field name.
func (j *jsonTable) metaDesc(fi int, mst *types.Struct, jsonKeys map[string]string) any {
	raw := -1
	var extra any
	members := []any{}
	for k := 0; k < mst.NumFields(); k++ {
		f := mst.Field(k)
		switch {
		case f.Name() == "raw":
			raw = k
		case f.Name() == "ExtraFields":
			extra = k
		default:
			if key, ok := jsonKeys[f.Name()]; ok {
				members = append(members, []any{key, k})
			}
		}
	}
	if raw < 0 {
		return nil
	}
	return map[string]any{"field": fi, "raw": raw, "extra": extra, "members": members}
}

// derefStruct is t's struct type, through one pointer.
func derefStruct(t types.Type) (*types.Struct, bool) {
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = p.Elem()
	}
	st, ok := t.Underlying().(*types.Struct)
	return st, ok
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// lean renders the table as the Lean term that parses it, with the
// descriptions of the boxed types (for encoding interface values) when
// some description is an interface, or "" when no JSON op uses it.
func (j *jsonTable) lean(boxed []types.Type) string {
	if len(j.descs) == 0 {
		return ""
	}
	bx := []any{}
	if j.ifaces {
		for _, t := range boxed {
			if i := j.desc(t); j.descs[i]["k"] != "opaque" {
				bx = append(bx, []any{typeTag(t), i})
			}
		}
	}
	empty := types.NewInterfaceType(nil, nil)
	basic := []any{}
	for _, t := range []types.Type{types.Typ[types.String], types.Typ[types.Bool], types.Typ[types.Float64],
		types.NewMap(types.Typ[types.String], empty), types.NewSlice(empty)} {
		basic = append(basic, typeTag(t))
	}
	b, err := json.Marshal(map[string]any{"descs": j.descs, "zeros": j.zeros, "boxed": bx, "basic": basic})
	if err != nil {
		return ""
	}
	return "GoSSA.JTable.ofJsonString " + leanString(string(b))
}

// leanString is s as a Lean string literal (s is JSON: escape the
// backslashes and quotes, nothing else).
func leanString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
