package semantics

import (
	"bytes"
	"crypto/rand"
	"io"
)

// go2lean's own tests of package randmodel: crypto/rand's Read and Reader
// never fail and always fill the buffer (Go 1.24 on); two reads differ
// (with probability 1 - 2^-128 in Go).

// io.ReadFull from rand.Reader, as nkeys draws a seed.
func testRandReaderReadFull() bool {
	var seed [32]byte
	n, err := io.ReadFull(rand.Reader, seed[:])
	return n == 32 && err == nil
}

// rand.Read fills the buffer, and two draws differ.
func testRandReadDiffers() bool {
	a, b := make([]byte, 16), make([]byte, 16)
	n1, err1 := rand.Read(a)
	n2, err2 := rand.Read(b)
	return n1 == 16 && n2 == 16 && err1 == nil && err2 == nil && !bytes.Equal(a, b)
}
