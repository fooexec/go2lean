package loops_test

// The proved theorems of goals.json, checked again on the real Go code:
// each postcondition (or Go spec) on random and boundary inputs
// (docs/trust.md). go test -fuzz=FuzzProved runs them as a fuzz target.

import (
	"math"
	"math/rand"
	"slices"
	"testing"

	pkg "github.com/fooexec/go2lean/examples/loops"
)

var edges = []int{math.MinInt64, math.MinInt64 + 1, -2, -1, 0, 1, 2, math.MaxInt64 - 1, math.MaxInt64}

func randInt(r *rand.Rand) int {
	switch r.Intn(3) {
	case 0:
		return edges[r.Intn(len(edges))]
	case 1:
		return r.Intn(9) - 4
	}
	return int(r.Uint64())
}

func randSlice(r *rand.Rand) []int {
	if r.Intn(10) == 0 {
		return nil
	}
	xs := make([]int, r.Intn(9))
	for i := range xs {
		xs[i] = randInt(r)
	}
	return xs
}

func check(t *testing.T, xs []int, v, lo, hi int) {
	t.Helper()
	m := pkg.Clamp(v, lo, hi)
	if lo <= hi && (m < lo || m > hi) {
		t.Fatalf("Clamp(%d, %d, %d) = %d", v, lo, hi, m)
	}
	if !pkg.SpecClamp(v, lo, hi, m) {
		t.Fatalf("SpecClamp fails on Clamp(%d, %d, %d)", v, lo, hi)
	}
	ys := append([]int(nil), xs...)
	pkg.ClampAll(ys, lo, hi)
	if !pkg.SpecClampAll(ys, lo, hi) {
		t.Fatalf("SpecClampAll fails on ClampAll(%v, %d, %d) = %v", xs, lo, hi, ys)
	}
	if pkg.HasDuplicate(xs) {
		found := false
		for i := range xs {
			for j := i + 1; j < len(xs); j++ {
				found = found || xs[i] == xs[j]
			}
		}
		if !found {
			t.Fatalf("HasDuplicate(%v) = true", xs)
		}
	}
	zs := append([]int(nil), xs...)
	pkg.BubbleSort(zs)
	if !pkg.SpecBubbleSortSorted(zs) {
		t.Fatalf("BubbleSort(%v) = %v, not sorted", xs, zs)
	}
	ws := slices.Clone(xs)
	slices.Sort(ws)
	if !slices.Equal(ws, zs) && len(xs) > 0 {
		t.Fatalf("BubbleSort(%v) = %v, not a permutation", xs, zs)
	}
}

func TestProved(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for range 200000 {
		check(t, randSlice(r), randInt(r), randInt(r), randInt(r))
	}
}

func FuzzProved(f *testing.F) {
	f.Add([]byte{3, 1, 2}, 5, 0, 3)
	f.Fuzz(func(t *testing.T, b []byte, v, lo, hi int) {
		xs := make([]int, len(b))
		for i, c := range b {
			xs[i] = int(int8(c))
		}
		check(t, xs, v, lo, hi)
	})
}
