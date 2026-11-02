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

// reflect.DeepEqual (#46), as Go's deepequal tests check it.

type deepPoint struct {
	X, Y uint64
	Name string
}

func testDeepEqualSlices() bool {
	a := []uint64{1, 2, 3}
	b := []uint64{1, 2, 3}
	c := []uint64{1, 2, 4}
	return reflect.DeepEqual(a, b) && !reflect.DeepEqual(a, c) && !reflect.DeepEqual(a, a[:2])
}

// A nil slice is not deeply equal to an empty one.
func testDeepEqualNilEmpty() bool {
	var n []uint64
	e := []uint64{}
	return !reflect.DeepEqual(n, e) && reflect.DeepEqual(n, []uint64(nil)) && reflect.DeepEqual(e, []uint64{})
}

func testDeepEqualMaps() bool {
	a := map[string]uint64{"x": 1, "y": 2}
	b := map[string]uint64{"y": 2, "x": 1}
	c := map[string]uint64{"x": 1, "y": 3}
	var n map[string]uint64
	return reflect.DeepEqual(a, b) && !reflect.DeepEqual(a, c) &&
		!reflect.DeepEqual(n, map[string]uint64{}) && reflect.DeepEqual(n, map[string]uint64(nil))
}

// Pointers are deeply equal when they point to deeply equal values.
func testDeepEqualPointers() bool {
	p := &deepPoint{1, 2, "a"}
	q := &deepPoint{1, 2, "a"}
	r := &deepPoint{1, 2, "b"}
	return reflect.DeepEqual(p, q) && !reflect.DeepEqual(p, r) && reflect.DeepEqual(*p, *q)
}

// Values of different types are never deeply equal.
func testDeepEqualTypes() bool {
	return !reflect.DeepEqual(uint64(1), int64(1)) && reflect.DeepEqual("ab", "a"+"b") &&
		reflect.DeepEqual(nil, nil) && !reflect.DeepEqual(nil, []uint64(nil))
}
