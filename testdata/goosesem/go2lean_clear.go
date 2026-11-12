package semantics

// go2lean's own tests: the builtins clear (on maps and slices) and print,
// which the standard-library audit found unmodelled (docs/eval/std-audit.md).

func testClearMap() bool {
	m := map[string]int{"a": 1, "b": 2}
	clear(m)
	_, ok := m["a"]
	m["c"] = 3
	return len(m) == 1 && !ok
}

func testClearSlice() bool {
	s := []int{1, 2, 3, 4}
	clear(s[1:3])
	return len(s) == 4 && s[0] == 1 && s[1] == 0 && s[2] == 0 && s[3] == 4
}

func testClearNilMap() bool {
	var m map[int]bool
	clear(m)
	return len(m) == 0
}

func testPrintIgnored() bool {
	n := 1
	println("go2lean", n)
	print(n, "\n")
	return n == 1
}
