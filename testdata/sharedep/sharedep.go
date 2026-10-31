// Package sharedep is a dependency of examples/sharedep with a package
// variable its own functions write (#55).
package sharedep

var hits int

// Hit counts one hit. Not safe for concurrent use.
func Hit() { hits++ }

// Hits is the count so far.
func Hits() int { return hits }
