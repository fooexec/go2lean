// Package andnotfold holds the shapes of `a & ^b` the frontend writes as one
// andNot (#291).
package andnotfold

// Fold is a & ^m: one andNot, no bnot.
func Fold(a, m int64) int64 { return a & ^m }

// FoldLeft is ^m & a: the same.
func FoldLeft(a, m int64) int64 { return ^m & a }

// Shared uses ^m twice: it stays a bnot.
func Shared(a, m int64) (int64, int64) {
	n := ^m
	return a & n, n
}

// Plain has no `^`: an and.
func Plain(a, m int64) int64 { return a & m }
