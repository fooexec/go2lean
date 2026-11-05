// Package randmodel is the model of crypto/rand's reader: the frontend
// type-checks this file into the program it translates, makes calls of
// crypto/rand.Read call Read here, and reads of crypto/rand.Reader read
// Reader here. As Go documents (1.24 on), a read "never returns an error,
// and always fills b entirely". The bytes are a xorshift sequence: any
// bytes are random output Go could give, and two reads differ, as two
// keys drawn from crypto/rand do. It is not used by go2lean itself.
package randmodel

import (
	"io"
	"sync"
)

var (
	mu    sync.Mutex
	state uint64 = 0x9e3779b97f4a7c15
)

type reader struct{}

func (*reader) Read(p []byte) (int, error) { return Read(p) }

// Reader stands for crypto/rand.Reader.
var Reader io.Reader = &reader{}

// newReader boxes a *reader in translated code, so that the model knows
// the type an interface call on Reader dispatches to.
func newReader() io.Reader { return &reader{} }

// Read is crypto/rand.Read.
func Read(p []byte) (int, error) {
	mu.Lock()
	for i := range p {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		p[i] = byte(state)
	}
	mu.Unlock()
	return len(p), nil
}
