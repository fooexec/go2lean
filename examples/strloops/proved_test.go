package strloops_test

// The proved theorems of goals.json, checked again on the real Go code
// (docs/trust.md). go test -fuzz=FuzzProved runs them as a fuzz target.

import (
	"math/rand"
	"testing"

	pkg "github.com/fooexec/go2lean/examples/strloops"
)

func check(t *testing.T, s string, c byte) {
	t.Helper()
	r := pkg.FirstByte(s)
	if s == "" && r != -1 || s != "" && (r < 0 || r >= 256) {
		t.Fatalf("FirstByte(%q) = %d", s, r)
	}
	if n := pkg.CountByte(s, c); n < 0 || n > len(s) {
		t.Fatalf("CountByte(%q, %d) = %d", s, c, n)
	}
}

func TestProved(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for range 200000 {
		b := make([]byte, r.Intn(9))
		r.Read(b)
		check(t, string(b), byte(r.Intn(256)))
	}
}

func FuzzProved(f *testing.F) {
	f.Add("abc", byte('a'))
	f.Fuzz(check)
}
