package semantics

import "reflect"

// go2lean's own tests (#215): reflect.Value's accessors on values the
// program built, as encoders over reflect read them.

type rvPoint struct{ X, Y int }

func testReflectValueScalars() bool {
	i := reflect.ValueOf(int32(-7))
	u := reflect.ValueOf(uint8(200))
	b := reflect.ValueOf(true)
	s := reflect.ValueOf("héllo")
	return i.Int() == -7 && u.Uint() == 200 && b.Bool() && s.String() == "héllo" && s.Len() == 6
}

func testReflectValueLenInterface() bool {
	v := reflect.ValueOf([]int{1, 2, 3})
	w, ok := reflect.ValueOf(42).Interface().(int)
	return v.Len() == 3 && ok && w == 42
}

func testReflectValueElem() bool {
	p := &rvPoint{X: 3, Y: 4}
	e := reflect.ValueOf(p).Elem()
	q, ok := e.Interface().(rvPoint)
	var nilp *rvPoint
	return e.IsValid() && ok && q.X == 3 && q.Y == 4 && !reflect.ValueOf(nilp).Elem().IsValid()
}

func testReflectValueValid() bool {
	var v reflect.Value
	return !v.IsValid() && reflect.ValueOf(1).IsValid() && !reflect.ValueOf(nil).IsValid()
}
