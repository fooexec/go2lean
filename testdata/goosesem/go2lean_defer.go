package semantics

import (
	"io"
	"sync"
)

// go2lean's own tests (#62): defer statements in loops run last first,
// with the arguments evaluated at the defer, interleaved with the other
// defers of the function.

type deferLog struct{ s []uint64 }

func (l *deferLog) add(x uint64) { l.s = append(l.s, x) }

func deferOrder(l *deferLog) {
	defer l.add(100)
	for i := uint64(0); i < 3; i++ {
		defer l.add(i)
		defer l.add(10 + i)
	}
	defer l.add(200)
}

func testDeferLoopOrder() bool {
	l := &deferLog{}
	deferOrder(l)
	want := []uint64{200, 12, 2, 11, 1, 10, 0, 100}
	if len(l.s) != len(want) {
		return false
	}
	for i := range want {
		if l.s[i] != want[i] {
			return false
		}
	}
	return true
}

func deferEarlyReturn(l *deferLog, n uint64) uint64 {
	for i := uint64(0); ; i++ {
		if i == n {
			return i
		}
		defer l.add(i)
	}
}

func testDeferLoopEarlyReturn() bool {
	l := &deferLog{}
	r := deferEarlyReturn(l, 2)
	return r == 2 && len(l.s) == 2 && l.s[0] == 1 && l.s[1] == 0
}

func testDeferLoopNone() bool {
	l := &deferLog{}
	return deferEarlyReturn(l, 0) == 0 && len(l.s) == 0
}

func deferUnlockAll(ms []*sync.Mutex) {
	for _, m := range ms {
		m.Lock()
		defer m.Unlock()
	}
}

func testDeferLoopUnlock() bool {
	ms := []*sync.Mutex{new(sync.Mutex), new(sync.Mutex)}
	deferUnlockAll(ms)
	return ms[0].TryLock() && ms[1].TryLock()
}

// A deferred call through an interface (#123): the method of the value's
// dynamic type, chosen at the defer, runs at the return, last first with
// the other defers.
type closeLog struct {
	log *deferLog
	id  uint64
}

func (c closeLog) Close() error { c.log.add(c.id); return nil }

func testDeferInvoke() bool {
	l := &deferLog{}
	func() {
		var a, b io.Closer = closeLog{l, 1}, &closeLog{l, 2}
		defer a.Close()
		defer b.Close()
		l.add(0)
	}()
	return len(l.s) == 3 && l.s[0] == 0 && l.s[1] == 2 && l.s[2] == 1
}
