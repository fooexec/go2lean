package initcells

import "sync"

// Package variables the package initializer builds (#55): a registry map
// behind a pointer, as a package's default registry is. The goroutines
// share what the initializer built.

type registry struct {
	mu sync.Mutex
	m  map[string]int
}

func (r *registry) add(name string, v int) {
	r.mu.Lock()
	r.m[name] = v
	r.mu.Unlock()
}

func (r *registry) addUnlocked(name string, v int) { r.m[name] = v }

var defaultReg = &registry{m: map[string]int{"default": 0}}

// RegisterBoth registers a name from each of two goroutines. Race-free.
func RegisterBoth() int {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defaultReg.add("a", 1)
	}()
	defaultReg.add("b", 2)
	wg.Wait()
	defaultReg.mu.Lock()
	defer defaultReg.mu.Unlock()
	return len(defaultReg.m)
}

// RegisterRacy is RegisterBoth without the lock in the goroutine. Bug: a
// data race on the registry's map.
func RegisterRacy() int {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defaultReg.addUnlocked("a", 1)
	}()
	defaultReg.add("b", 2)
	wg.Wait()
	return len(defaultReg.m)
}

// handlers is made and filled by a func init(), as a package's default
// table often is.
var handlers map[string]int

func init() {
	handlers = make(map[string]int)
	handlers["default"] = 0
}

// AddHandlersRacy adds a handler from each of two goroutines. Bug: a data
// race on the map the func init() made.
func AddHandlersRacy() int {
	done := make(chan bool)
	go func() {
		handlers["a"] = 1
		done <- true
	}()
	handlers["b"] = 2
	<-done
	return len(handlers)
}

// AddHandlersInTurn adds one handler in a goroutine, then one after it
// ended. Race-free.
func AddHandlersInTurn() int {
	done := make(chan bool)
	go func() {
		handlers["a"] = 1
		done <- true
	}()
	<-done
	handlers["b"] = 2
	return len(handlers)
}
