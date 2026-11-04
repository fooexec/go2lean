// Package maptable has lookup tables keyed by arrays and structs of
// constants, in a package that starts no goroutines (so no init cells,
// #55): the frontend builds them from their constants (#46, c022).
package maptable

var tagTable = map[[6]byte]int16{
	{'a', 'r', 't', '-', 'l', 'o'}: -1,
	{'i', '-', 'a', 'm', 'i'}:      7,
}

type cell struct {
	Row, Col int
	On       bool
}

var cellTable = map[cell]int{{1, 2, true}: 12, {Col: 3}: 3}

// A string inside a key: not folded (the model compares strings by their
// bytes, not element by element), so the table stays unmodelled.
type named struct{ Name string }

var namedTable = map[named]int{{"a"}: 1}

func Tag(k [6]byte) (int16, bool) { v, ok := tagTable[k]; return v, ok }

func Cell(r, c int) int { return cellTable[cell{r, c, true}] }

func Named(s string) int { return namedTable[named{s}] }
