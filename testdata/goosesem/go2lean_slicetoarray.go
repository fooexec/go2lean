package semantics

// Conversions from a slice to an array, [N]T(s) (Go 1.20): the array is a
// copy of the slice's first N elements; a slice shorter than N panics.
// net/netip's AddrFromSlice converts this way.

func testSliceToArrayCopies() bool {
	s := []byte{1, 2, 3, 4, 5}
	a := [4]byte(s)
	s[0] = 9
	return a == [4]byte{1, 2, 3, 4} && s[0] == 9
}

func testSliceToArrayEmpty() bool {
	var s []int
	a := [0]int(s)
	return len(a) == 0
}

func testSliceToArrayShortPanics() bool { return sliceToArrayShort() }

func sliceToArrayShort() (ok bool) {
	defer func() {
		ok = recover() != nil
	}()
	s := []int{1, 2, 3}
	_ = [4]int(s)
	return false
}
