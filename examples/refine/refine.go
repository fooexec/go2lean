// Package refine is the ground truth for the refinement loop (#85): a
// counterexample the replay finds spurious because a dependency's call was
// havoc, and that goes away once the loop translates that call.
package refine

import "github.com/andreasfoo/go2lean/testdata/refinedeep"

// Digit returns the last decimal digit of x, which the dependency
// computes five calls deep. It panics if the digit is out of range, which
// never happens in Go; in the model, until refinement, it can.
func Digit(x uint8) int {
	d := refinedeep.Level1(x)
	if d < 0 || d > 9 {
		panic("digit out of range")
	}
	return d
}
