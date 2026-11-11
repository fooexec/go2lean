package semantics

import "sync/atomic"

// go2lean's own tests (#192): sync/atomic's Pointer[T] holds the pointer
// Store put, nil at first, as sourcegraph/conc's panics.Catcher uses it.

type recovered struct{ v int }

type catcher struct{ p atomic.Pointer[recovered] }

func testAtomicPointerZero() bool {
	var c catcher
	return c.p.Load() == nil
}

func testAtomicPointerStoreLoad() bool {
	var c catcher
	r := &recovered{v: 7}
	c.p.Store(r)
	got := c.p.Load()
	return got == r && got.v == 7
}

func testAtomicPointerSwapCAS() bool {
	var c catcher
	a, b := &recovered{v: 1}, &recovered{v: 2}
	if !c.p.CompareAndSwap(nil, a) || c.p.CompareAndSwap(nil, b) {
		return false
	}
	old := c.p.Swap(b)
	return old == a && c.p.Load() == b
}
