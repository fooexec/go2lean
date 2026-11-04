// Package floats holds float bugs the model reaches since #148: an index
// computed from a float at its edge (1.0), math.Mod of a negative angle, and
// a NaN that gets through range checks.
package floats

import "math"

// Bucket maps x in [0, 1] to one of four buckets; x == 1 indexes past the end.
func Bucket(x float64) int {
	b := []int{0, 1, 2, 3}
	if x < 0 || x > 1 {
		return -1
	}
	return b[int(x*4)]
}

// Clamp keeps x in [0, 1]; a NaN gets through.
func Clamp(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// Mean of a slice; empty divides by zero (NaN, no panic).
func Mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// Hue folds an angle into [0, 360) with math.Mod; a negative angle stays negative.
func Hue(deg float64) int {
	h := math.Mod(deg, 360)
	table := make([]int, 360)
	return table[int(h)]
}
