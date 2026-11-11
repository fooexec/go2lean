package semantics

// go2lean's own tests (#181): a deferred function that calls recover()
// stops a panic, and the function returns its named results as they are.

// try runs f under a recover, as samber/lo's Try does.
func try(f func() error) (ok bool) {
	ok = true
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	if err := f(); err != nil {
		ok = false
	}
	return
}

func boom() error {
	var xs []int
	return errorOf(xs[3])
}

func errorOf(int) error { return nil }

// A nil callback, and a callee's index out of range, are recovered.
func testRecoverNilAndCallee() bool {
	return !try(nil) && !try(boom) && try(func() error { return nil })
}

// An explicit panic of the function itself is recovered; the named result
// keeps the value it had.
func half(n int) (r int, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	r, ok = n/2, true
	if n < 0 {
		panic("negative")
	}
	return
}

func testRecoverExplicitPanic() bool {
	r, ok := half(-4)
	r2, ok2 := half(6)
	return r == -2 && !ok && r2 == 3 && ok2
}

// recover() is nil at a normal return.
func testRecoverNilWithoutPanic() bool {
	got := true
	func() {
		defer func() { got = recover() == nil }()
	}()
	return got
}
