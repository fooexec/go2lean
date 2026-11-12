package invariants_test

// The proved theorems of goals.json, checked again on the real Go code:
// each postcondition restated in Go, on random and boundary inputs
// (docs/trust.md: the theorems are about the model; this is evidence the
// model is the program). go test -fuzz=Fuzz... runs them as fuzz targets.

import (
	"math"
	"math/rand"
	"testing"

	pkg "github.com/fooexec/go2lean/examples/invariants"
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

func check(t *testing.T, xs []int, x int) {
	t.Helper()
	if n := pkg.CountPositive(xs); n < 0 || n > len(xs) {
		t.Fatalf("CountPositive(%v) = %d", xs, n)
	}
	if r := pkg.IndexOf(xs, x); r != -1 && (r < 0 || r >= len(xs) || xs[r] != x) {
		t.Fatalf("IndexOf(%v, %d) = %d", xs, x, r)
	}
	if pkg.AllNonNeg(xs) {
		for _, y := range xs {
			if y < 0 {
				t.Fatalf("AllNonNeg(%v) = true", xs)
			}
		}
	}
	ys := append([]int(nil), xs...)
	pkg.Fill(ys, x)
	if len(ys) != len(xs) {
		t.Fatalf("Fill changed the length")
	}
	for _, y := range ys {
		if y != x {
			t.Fatalf("Fill(%v, %d) = %v", xs, x, ys)
		}
	}
}

func TestProved(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for range 200000 {
		check(t, randSlice(r), randInt(r))
	}
}

func FuzzProved(f *testing.F) {
	f.Add([]byte{1, 2, 3}, 2)
	f.Fuzz(func(t *testing.T, b []byte, x int) {
		xs := make([]int, len(b))
		for i, c := range b {
			xs[i] = int(int8(c))
		}
		check(t, xs, x)
	})
}
