// Package sleeps waits on goroutines by sleeping, as tests often do
// (#59): a sleep ends once the shorter ones have, and code runs in no
// time, as in Go's testing/synctest.
package sleeps

import (
	"sync"
	"time"
)

// WaitLonger sleeps longer than a goroutine that sets a flag. Correct:
// the flag is set when the caller wakes.
func WaitLonger() bool {
	var mu sync.Mutex
	ready := false
	go func() {
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		ready = true
		mu.Unlock()
	}()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !ready {
		panic("woke before the shorter sleep")
	}
	return ready
}

// WaitShorter sleeps less than the goroutine. Bug: the flag is not set
// yet when the caller wakes.
func WaitShorter() bool {
	var mu sync.Mutex
	ready := false
	go func() {
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		ready = true
		mu.Unlock()
	}()
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !ready {
		panic("woke before the goroutine")
	}
	return ready
}

// TimeoutRace waits for a worker that takes 1ms, with a 1s timeout.
// Correct: the worker always wins.
func TimeoutRace() int {
	done := make(chan int, 1)
	go func() {
		time.Sleep(time.Millisecond)
		done <- 7
	}()
	select {
	case v := <-done:
		return v
	case <-time.After(time.Second):
		panic("timed out")
	}
}

// TimeoutFirst waits 1ms for a worker that takes 1s. Bug: it times out.
func TimeoutFirst() int {
	done := make(chan int, 1)
	go func() {
		time.Sleep(time.Second)
		done <- 7
	}()
	select {
	case v := <-done:
		return v
	case <-time.After(time.Millisecond):
		panic("timed out")
	}
}
