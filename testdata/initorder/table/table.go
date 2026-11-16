// Package table holds a status table that package errs's initializer
// reads: the model must initialize it first, as Go does (examples/initorder).
package table

// Msg returns the message of a status code, or "" outside the table.
func Msg(code int) string {
	if code < 100 || code > 511 {
		return ""
	}
	return messages[code]
}

var messages = []string{
	100: "Continue",
	200: "OK",
	404: "Not Found",
	511: "Network Authentication Required",
}
