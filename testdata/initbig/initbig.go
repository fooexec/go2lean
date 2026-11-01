// Package initbig has two variables its initializer builds: a registry
// the goroutines share, and a buffer larger than the model allocates. The
// buffer's part of the initializer does not translate; the registry's
// still does (#120).
package initbig

import "sync"

type registry struct {
	mu sync.Mutex
	m  map[string]int
}

var reg = &registry{m: map[string]int{}}

var buf = make([]byte, 131070)

// Use registers a name from a goroutine and reads the buffer's length.
func Use() int {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reg.mu.Lock()
		reg.m["a"] = 1
		reg.mu.Unlock()
	}()
	wg.Wait()
	return len(reg.m) + len(buf)
}
