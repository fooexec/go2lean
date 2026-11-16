// Package errs builds a package-level error from package table in its
// initializer (examples/initorder).
package errs

import "github.com/fooexec/go2lean/testdata/initorder/table"

// E is an error with a status code and its message.
type E struct {
	Code int
	Msg  string
}

func newE(code int) *E { return &E{code, table.Msg(code)} }

// NotFound is built when the package is initialized.
var NotFound = newE(404)

// NotFoundMsg returns NotFound's message.
func NotFoundMsg() string { return NotFound.Msg }
