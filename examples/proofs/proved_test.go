package proofs_test

// The proved theorems of goals.json, checked again on the real Go code
// (docs/trust.md). go test -fuzz=FuzzProved runs them as a fuzz target.

import (
	"math"
	"math/rand"
	"testing"

	pkg "github.com/fooexec/go2lean/examples/proofs"
)

var edges = []int{math.MinInt64, -1, 0, 1, math.MaxInt64}

func check(t *testing.T, a, b, c int) {
	t.Helper()
	if m := pkg.Max2(a, b); m < a || m < b {
		t.Fatalf("Max2(%d, %d) = %d", a, b, m)
	}
	if m := pkg.Max3(a, b, c); m < a || m < b || m < c {
		t.Fatalf("Max3(%d, %d, %d) = %d", a, b, c, m)
	}
}

func TestProved(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	pick := func() int {
		if r.Intn(2) == 0 {
			return edges[r.Intn(len(edges))]
		}
		return int(r.Uint64())
	}
	for range 200000 {
		check(t, pick(), pick(), pick())
	}
}

func FuzzProved(f *testing.F) {
	f.Add(1, 2, 3)
	f.Fuzz(check)
}
