package semantics

import (
	"math/rand"
	randv2 "math/rand/v2"
)

// math/rand's and math/rand/v2's draws stay in the ranges Go documents
// (#256): the backoffs of go-retryablehttp, backoff and retry-go jitter
// with them.

func testMathRandRanges() bool {
	f := rand.Float64()
	g := rand.Float32()
	r := rand.New(rand.NewSource(1))
	h := r.Float64()
	return f >= 0 && f < 1 && g >= 0 && g < 1 && h >= 0 && h < 1 &&
		rand.Int63() >= 0 && rand.Int31() >= 0 && rand.Int() >= 0 && r.Int63() >= 0
}

func testMathRandV2Ranges() bool {
	f := randv2.Float64()
	n := randv2.IntN(10)
	return f >= 0 && f < 1 && n >= 0 && n < 10 && randv2.Int64() >= 0 && randv2.Int32() >= 0
}

func testMathRandJitter() bool {
	// A backoff with jitter of at most half the interval, as the retry
	// libraries compute it, stays between half and one and a half times it.
	base := 1000.0
	d := base + (rand.Float64()-0.5)*base
	return d >= base/2 && d < base*1.5
}
