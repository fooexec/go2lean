package semantics

import "sync"

// go2lean's own tests (#105): sync.Pool.Get on a pool with New gives an
// object from New. An object is reset before it is Put back, as pool users
// do, so Get gives a zero one whichever it returns.

type poolBuf struct {
	n   int
	buf []byte
}

var poolBufs = sync.Pool{New: func() any { return new(poolBuf) }}

func testPoolGetFromNew() bool {
	p := poolBufs.Get().(*poolBuf)
	ok := p.n == 0 && len(p.buf) == 0
	p.n = 5
	p.buf = append(p.buf, 'x')
	ok = ok && p.n == 5 && len(p.buf) == 1
	p.n, p.buf = 0, p.buf[:0]
	poolBufs.Put(p)
	return ok
}
