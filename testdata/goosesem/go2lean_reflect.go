package semantics

import "reflect"

// go2lean's own tests (#128): a func value called through reflect, as
// go-metrics' GetOrRegister builds a metric lazily.

type reflMeter struct{ n uint64 }

type reflNamer interface{ name() uint64 }

func (m *reflMeter) name() uint64 { return m.n }

func newReflMeter() *reflMeter { return &reflMeter{n: 7} }

func newReflNamer() reflNamer { return &reflMeter{n: 9} }

// A func() *T, called through reflect.ValueOf(i).Call(nil)[0].Interface().
func testReflectCallNoArgs() bool {
	var i any = newReflMeter
	if v := reflect.ValueOf(i); v.Kind() == reflect.Func {
		i = v.Call(nil)[0].Interface()
	}
	m, ok := i.(*reflMeter)
	return ok && m.n == 7
}

// A func() I: the result is the interface value the func returned.
func testReflectCallIfaceResult() bool {
	r := reflect.ValueOf(newReflNamer).Call(nil)
	if len(r) != 1 {
		return false
	}
	n, ok := r[0].Interface().(reflNamer)
	return ok && n.name() == 9
}
