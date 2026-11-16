// Package initorder is the ground truth for the order of package
// initializers in the model: Go initializes a package's imports before
// it, so errs's initializer finds table's status table filled. This
// package calls errs before table, the order in which the model once ran
// errs's initializer first, which left errs.NotFound nil and gave a
// spurious nil dereference.
package initorder

import (
	"github.com/fooexec/go2lean/testdata/initorder/errs"
	"github.com/fooexec/go2lean/testdata/initorder/table"
)

// Text returns NotFound's message for a positive n, and the message of
// code otherwise.
func Text(code, n int) string {
	if n > 0 {
		return errs.NotFoundMsg()
	}
	return table.Msg(code)
}
