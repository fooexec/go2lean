package semantics

import (
	"cmp"
	"slices"
)

// go2lean's own tests of package slices' generic sorts, translated from
// source: pdqsort with a comparison function (otel sorts attribute sets
// with SortStableFunc).

type kv struct {
	k string
	v int
}

func testSlicesSortFunc() bool {
	xs := []kv{{"c", 3}, {"a", 1}, {"d", 4}, {"b", 2}, {"e", 5}}
	slices.SortFunc(xs, func(a, b kv) int { return cmp.Compare(a.k, b.k) })
	return xs[0].k == "a" && xs[1].k == "b" && xs[4].k == "e" && slices.IsSortedFunc(xs, func(a, b kv) int { return cmp.Compare(a.k, b.k) })
}

// A stable sort keeps equal keys in their order.
func testSlicesSortStableFunc() bool {
	xs := []kv{{"b", 1}, {"a", 2}, {"b", 3}, {"a", 4}}
	slices.SortStableFunc(xs, func(a, b kv) int { return cmp.Compare(a.k, b.k) })
	return xs[0].v == 2 && xs[1].v == 4 && xs[2].v == 1 && xs[3].v == 3
}

func testSlicesSortInts() bool {
	xs := []int{5, 2, 9, 1, 5, 6}
	slices.Sort(xs)
	i, found := slices.BinarySearch(xs, 6)
	return slices.Equal(xs, []int{1, 2, 5, 5, 6, 9}) && found && i == 4
}
