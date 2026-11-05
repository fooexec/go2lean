package semantics

// go2lean's own tests of slices of an array inside a struct (d.mem[2:]):
// the slice aliases the field, as xxhash's Digest.Write relies on.

type fsDigest struct {
	n   int
	mem [8]byte
}

// Writes through the slice, and copy into it, land in the field.
func testFieldSliceAlias() bool {
	d := &fsDigest{}
	m := d.mem[2:]
	n := copy(m, []byte{1, 2, 3})
	m[0] = 9
	return n == 3 && len(m) == 6 && cap(m) == 6 && d.mem[2] == 9 && d.mem[3] == 2 && d.mem[4] == 3
}

// A write to the field shows through the slice.
func testFieldSliceSeesField() bool {
	d := &fsDigest{}
	m := d.mem[:4]
	d.mem[1] = 7
	return m[1] == 7 && len(m) == 4 && cap(m) == 8
}

// append within the capacity writes into the field; past it, a new array.
func testFieldSliceAppend() bool {
	d := &fsDigest{}
	m := d.mem[:2]
	m = append(m, 5)
	ok := d.mem[2] == 5
	full := d.mem[:]
	grown := append(full, 6)
	grown[0] = 1
	return ok && d.mem[0] == 0 && len(grown) == 9
}

// A copy of the struct does not share the field.
func testFieldSliceStructCopy() bool {
	d := &fsDigest{}
	m := d.mem[:]
	e := *d
	m[0] = 4
	return d.mem[0] == 4 && e.mem[0] == 0
}

// The three-index form limits the capacity.
func testFieldSliceThreeIndex() bool {
	d := &fsDigest{}
	m := d.mem[1:3:5]
	m = append(m, 8)
	return len(m) == 3 && cap(m) == 4 && d.mem[3] == 8
}
