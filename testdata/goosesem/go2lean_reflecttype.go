package semantics

import (
	"reflect"
	"sync"
)

// go2lean's own tests (#200): reflect.Type as msgpack's and go-toml's
// encoders use it: TypeOf, Kind, Elem, PtrTo, Implements, == and as a
// map key.

type textish interface{ Text() string }

type oops struct{ msg string }

func (e *oops) Error() string { return e.msg }

type word string

func (w word) Text() string { return string(w) }

var (
	errorType  = reflect.TypeOf((*error)(nil)).Elem()
	textType   = reflect.TypeOf((*textish)(nil)).Elem()
	typeCache2 sync.Map
)

func testReflectTypeKind() bool {
	var xs []interface{}
	var m map[string]interface{}
	return reflect.TypeOf(xs).Kind() == reflect.Slice &&
		reflect.TypeOf(m).Kind() == reflect.Map &&
		reflect.TypeOf("s").Kind() == reflect.String &&
		reflect.TypeOf(3).Kind() == reflect.Int &&
		reflect.TypeOf(xs).Elem().Kind() == reflect.Interface
}

func testReflectTypeImplements() bool {
	var w word = "w"
	tw := reflect.TypeOf(w)
	te := reflect.TypeOf(error(&oops{"e"}))
	return tw.Implements(textType) && !tw.Implements(errorType) &&
		te.Implements(errorType) && !reflect.TypeOf(1).Implements(textType) &&
		reflect.PtrTo(tw).Implements(textType)
}

func testReflectTypeEqual() bool {
	a, b := reflect.TypeOf(1), reflect.TypeOf(2)
	c := reflect.TypeOf("x")
	return a == b && a != c && reflect.TypeOf(error(&oops{"e"})) != errorType &&
		reflect.PtrTo(a) == reflect.TypeOf(new(int)) && reflect.TypeOf(new(int)).Elem() == a
}

func testReflectTypeKey() bool {
	typeCache2.Store(reflect.TypeOf(1), "int")
	v, ok := typeCache2.Load(reflect.TypeOf(7))
	_, ok2 := typeCache2.Load(reflect.TypeOf("s"))
	return ok && v.(string) == "int" && !ok2
}
