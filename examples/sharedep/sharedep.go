// Package sharedep calls a dependency's functions that write its package
// variable from two goroutines (#55): whether the variable is a shared
// cell is one decision for every package, so the dependency's writes and
// this package's reads agree.
package sharedep

import (
	"sync"

	"github.com/andreasfoo/go2lean/testdata/sharedep"
)

// HitTwice hits from two goroutines. Bug: a data race on the dependency's
// counter (Hit is not safe for concurrent use).
func HitTwice() int {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sharedep.Hit()
	}()
	sharedep.Hit()
	wg.Wait()
	return sharedep.Hits()
}

// HitInTurn hits from a goroutine, then from the caller after it ended.
// Race-free.
func HitInTurn() int {
	done := make(chan bool)
	go func() {
		sharedep.Hit()
		done <- true
	}()
	<-done
	sharedep.Hit()
	return sharedep.Hits()
}
