// Package replaydep is the ground truth for replaying the model's schedule
// through a dependency (#56), after alitto/pond's dispatcher (evaluation 7,
// k024): the dependency's lock is not gated in the real run, and the
// closing goroutine sets an atomic flag before its first gated operation.
package replaydep

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/fooexec/go2lean/examples/replaydep/queue"
)

var errClosed = errors.New("dispatcher closed")

type dispatcher struct {
	q      queue.Queue
	ready  chan struct{}
	closed atomic.Bool
}

func (d *dispatcher) write(x int) error {
	if d.closed.Load() {
		return errClosed
	}
	d.q.Push(x)
	select {
	case d.ready <- struct{}{}:
	default:
	}
	return nil
}

func (d *dispatcher) close() {
	d.closed.Store(true)
	close(d.ready)
}

// WriteWhileClosing writes values while another goroutine closes the
// dispatcher. Bug: a write that saw it open can send on its channel after
// the close (send on a closed channel); Go's scheduler rarely takes that
// interleaving, the model's schedule replayed does.
func WriteWhileClosing(n uint8) int {
	d := &dispatcher{ready: make(chan struct{}, 1)}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < int(n%4)+1; i++ {
			if d.write(i) != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		d.close()
	}()
	wg.Wait()
	return d.q.Len()
}
