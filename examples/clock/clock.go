// Package clock reads the clock and compares times (#59): time.Now never
// goes back within a run, and Add, Sub, Before and After agree with it,
// as in Go.
package clock

import "time"

// Elapsed times a loop. Correct: the time since a reading is never
// negative.
func Elapsed(n int) time.Duration {
	start := time.Now()
	s := 0
	for i := 0; i < n && i < 8; i++ {
		s += i
	}
	_ = s
	d := time.Since(start)
	if d < 0 {
		panic("negative elapsed time")
	}
	return d
}

// NotBeforeItself adds a non-negative duration to a reading. Correct: the
// result is not before the reading.
func NotBeforeItself(d time.Duration) bool {
	if d < 0 {
		return true
	}
	t := time.Now()
	if t.Add(d).Before(t) {
		panic("t + d before t")
	}
	return t.Add(d).Sub(t) == d
}

// MustPositive takes a timeout. Bug: it panics when the timeout is not
// positive (the deadline is not after now).
func MustPositive(timeout time.Duration) time.Time {
	now := time.Now()
	deadline := now.Add(timeout)
	if !deadline.After(now) {
		panic("timeout must be positive")
	}
	return deadline
}
