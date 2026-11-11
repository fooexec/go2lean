package iface

import "reflect"

// Shape has an area.
type Shape interface{ Area() int }

// Sq is a square (value receiver).
type Sq struct{ S int }

func (s Sq) Area() int { return s.S * s.S }

// Rect is a rectangle (pointer receiver).
type Rect struct{ W, H int }

func (r *Rect) Area() int { return r.W * r.H }

// Area asks a shape it builds: a method call on an interface value.
func Area(n int) int {
	var sh Shape = Sq{n}
	if n > 10 {
		sh = &Rect{n, 2}
	}
	return sh.Area()
}

// Ratio divides by the area of a square. Bug: a square of side 0 has area
// 0.
func Ratio(n, total int) int {
	var sh Shape = Sq{n}
	return total / sh.Area()
}

// zeroer is what Omitted asserts, like param.IsOmitted in the OpenAI SDK.
type zeroer interface{ isZero() bool }

// Opt is an optional string.
type Opt struct {
	V   string
	Set bool
}

func (o Opt) isZero() bool { return o == Opt{} }

// Omitted asserts v to an interface and calls through it.
func Omitted(v any) bool {
	if v == nil {
		return false
	}
	if z, ok := v.(zeroer); ok {
		return z.isZero()
	}
	return reflect.ValueOf(v).IsZero()
}

// Msg is a message without methods.
type Msg struct{ S string }

// Set reports whether a message is set, through Omitted's reflect path.
func Set(m *Msg) bool { return !Omitted(m) }

// Name returns the option's value, or "none" when it is omitted.
func Name(o Opt) string {
	if Omitted(o) {
		return "none"
	}
	return o.V
}

// Resetter is a collaborator no type of the package implements: the
// model does not follow its methods.
type Resetter interface{ Reset() }

// Restart resets r before use. Bug: a nil r panics in Go (a method call
// through a nil interface), which the model once ignored as a call
// without result (#174).
func Restart(r Resetter) int {
	r.Reset()
	return 1
}
