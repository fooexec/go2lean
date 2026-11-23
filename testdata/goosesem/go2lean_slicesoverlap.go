package semantics

import "slices"

// slices.Insert and slices.Replace check whether the inserted values alias
// the slice they go into (the unexported slices.overlaps, which compares
// element addresses through unsafe) and copy differently when they do.
// quic-go's frameSorter inserts and replaces gaps this way.

func testSlicesInsertFresh() bool {
	s := make([]int, 3, 8)
	s[0], s[1], s[2] = 1, 2, 3
	s = slices.Insert(s, 1, 7, 8)
	return slices.Equal(s, []int{1, 7, 8, 2, 3})
}

func testSlicesInsertAliased() bool {
	// The values come from the slice itself, in its spare capacity's way.
	s := make([]int, 4, 10)
	s[0], s[1], s[2], s[3] = 1, 2, 3, 4
	s = slices.Insert(s, 1, s[2:4]...)
	return slices.Equal(s, []int{1, 3, 4, 2, 3, 4})
}

func testSlicesInsertAliasedTail() bool {
	s := make([]int, 4, 10)
	s[0], s[1], s[2], s[3] = 1, 2, 3, 4
	s = slices.Insert(s, 0, s[1:]...)
	return slices.Equal(s, []int{2, 3, 4, 1, 2, 3, 4})
}

func testSlicesReplaceAliased() bool {
	s := []int{1, 2, 3, 4, 5}
	s = slices.Replace(s, 1, 2, s[3:5]...)
	return slices.Equal(s, []int{1, 4, 5, 3, 4, 5})
}

func testSlicesReplaceFresh() bool {
	s := []int{1, 2, 3, 4, 5}
	s = slices.Replace(s, 0, 3, 9)
	return slices.Equal(s, []int{9, 4, 5})
}

func testSlicesInsertEmptyStruct() bool {
	s := make([]struct{}, 2, 4)
	s = slices.Insert(s, 1, s[0:1]...)
	return len(s) == 3
}
