// Package counter has a type named like its package, built by New (#15):
// its methods are checked on a receiver New built, as for NewT.
package counter

// Counter counts keys in the order they first came.
type Counter struct {
	counts map[string]int
	order  []string
}

// New returns an empty Counter.
func New() *Counter { return &Counter{counts: map[string]int{}} }

// Add counts k. Correct on a Counter New built (a zero Counter's map is
// nil, which New never leaves).
func (c *Counter) Add(k string) {
	if c.counts[k] == 0 {
		c.order = append(c.order, k)
	}
	c.counts[k]++
}

// First is the first key added. Bug: panics before any Add.
func (c *Counter) First() string { return c.order[0] }
