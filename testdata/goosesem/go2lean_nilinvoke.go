package semantics

// go2lean's own tests (#174): a method call through a nil interface
// panics, whatever the method, also one the model does not follow (an
// interface no type of the program implements), with or without results.

type resetter interface{ Reset() }

type sizer interface{ Size() int }

func callReset(r resetter) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	r.Reset()
	return false
}

func callSize(s sizer) (n int, panicked bool) {
	defer func() { panicked = recover() != nil }()
	return s.Size(), false
}

func testNilInterfaceCallPanics() bool {
	var r resetter
	var s sizer
	_, p := callSize(s)
	return callReset(r) && p
}
