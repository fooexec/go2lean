package llvm

import (
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// The C types of the parameters, from the debug information (#269). With
// LLVM's opaque pointers, a parameter's IR type says only `ptr`, so a
// `cJSON *item` was generated as a buffer of integers and the replay
// reported overflows no caller can cause. The DWARF of a -g build gives
// the pointee: a struct's size and its members' offsets and types. A
// parameter whose type is a pointer to a struct is then generated as that
// struct, member by member (Gen kind "typed", Lean's CTy).

// typeDepth bounds how far a struct's pointer members are unrolled: past
// it, a pointer member is null.
const typeDepth = 2

// ParamTypes compiles src with debug information and returns, for each
// function, the C type of each parameter as the search reads it (CTy as
// JSON), or nil where the parameter is not a pointer to a struct (the
// IR-based generators stay).
func ParamTypes(src, dir string) (map[string][]any, error) {
	obj := filepath.Join(dir, strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))+".dbg.o")
	if out, err := exec.Command("clang", "-g", "-O0", "-c", src, "-o", obj).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("clang -g: %v\n%s", err, out)
	}
	f, err := elf.Open(obj)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d, err := f.DWARF()
	if err != nil {
		return nil, err
	}
	return paramTypes(d)
}

func paramTypes(d *dwarf.Data) (map[string][]any, error) {
	out := map[string][]any{}
	r := d.Reader()
	for {
		e, err := r.Next()
		if err != nil {
			return nil, err
		}
		if e == nil {
			break
		}
		if e.Tag != dwarf.TagSubprogram {
			continue
		}
		name, _ := e.Val(dwarf.AttrName).(string)
		if name == "" || !e.Children {
			continue
		}
		var params []any
		for {
			c, err := r.Next()
			if err != nil {
				return nil, err
			}
			if c == nil || c.Tag == 0 {
				break
			}
			if c.Tag != dwarf.TagFormalParameter {
				if c.Children {
					r.SkipChildren()
				}
				continue
			}
			off, ok := c.Val(dwarf.AttrType).(dwarf.Offset)
			if !ok {
				params = append(params, nil)
				continue
			}
			t, err := d.Type(off)
			if err != nil {
				params = append(params, nil)
				continue
			}
			params = append(params, paramCTy(t))
		}
		out[name] = params
	}
	return out, nil
}

// strip removes typedefs and qualifiers.
func strip(t dwarf.Type) dwarf.Type {
	for {
		switch x := t.(type) {
		case *dwarf.TypedefType:
			t = x.Type
		case *dwarf.QualType:
			t = x.Type
		default:
			return t
		}
	}
}

// paramCTy is a parameter's CTy when it is a pointer to a struct, else nil.
func paramCTy(t dwarf.Type) any {
	p, ok := strip(t).(*dwarf.PtrType)
	if !ok {
		return nil
	}
	s, ok := strip(p.Type).(*dwarf.StructType)
	if !ok || s.Kind != "struct" || s.Incomplete {
		return nil
	}
	return map[string]any{"k": "ptr", "t": cty(s, typeDepth)}
}

// cty is the CTy of t, with pointers to structs unrolled depth levels.
func cty(t dwarf.Type, depth int) map[string]any {
	zero := func(n int64) map[string]any {
		if n < 0 {
			n = 0
		}
		return map[string]any{"k": "zero", "b": n}
	}
	switch x := strip(t).(type) {
	case *dwarf.IntType, *dwarf.UintType, *dwarf.CharType, *dwarf.UcharType, *dwarf.BoolType, *dwarf.EnumType:
		n := x.Size()
		if n == 1 || n == 2 || n == 4 || n == 8 {
			return map[string]any{"k": "int", "b": n}
		}
		return zero(n)
	case *dwarf.PtrType:
		target := strip(x.Type)
		if c, ok := target.(*dwarf.CharType); ok && c.Size() == 1 {
			return map[string]any{"k": "str"}
		}
		if _, ok := target.(*dwarf.IntType); ok && target.Size() == 1 {
			return map[string]any{"k": "str"}
		}
		if s, ok := target.(*dwarf.StructType); ok && s.Kind == "struct" && !s.Incomplete && depth > 0 {
			return map[string]any{"k": "ptr", "t": cty(s, depth-1)}
		}
		return map[string]any{"k": "null"}
	case *dwarf.StructType:
		if x.Kind != "struct" || x.Incomplete {
			return zero(x.Size())
		}
		var fields []any
		for _, f := range x.Field {
			if f.BitSize != 0 {
				continue // bit-fields stay zero
			}
			fields = append(fields, map[string]any{"off": f.ByteOffset, "t": cty(f.Type, depth)})
		}
		if fields == nil {
			fields = []any{}
		}
		return map[string]any{"k": "struct", "size": x.Size(), "fields": fields}
	default:
		// Floats, arrays, unions, function pointers: zero bytes.
		return zero(t.Size())
	}
}
