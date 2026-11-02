package semantics

import (
	"errors"
	"io"
	"net"
	"os"
	"time"
)

// go2lean's own tests (#122): net.Pipe, translated from net/pipe.go, and
// the host:port helpers, as Go's net tests check them.

// A write on one end is read on the other, in pieces when the reader's
// buffer is smaller: Pipe is synchronous and unbuffered. Two bytes, so
// the exhaustive engine runs every schedule.
func testNetPipeWriteRead() bool {
	a, b := net.Pipe()
	done := make(chan bool)
	go func() {
		n, err := a.Write([]byte("hi"))
		done <- n == 2 && err == nil
	}()
	buf := make([]byte, 1)
	n1, err1 := b.Read(buf)
	first := buf[0]
	n2, err2 := b.Read(buf)
	return <-done && err1 == nil && err2 == nil && n1 == 1 && n2 == 1 && first == 'h' && buf[0] == 'i'
}

// Closing one end: the other end's reads see io.EOF; writes on the closed
// end itself fail with io.ErrClosedPipe.
func testNetPipeClose() bool {
	a, b := net.Pipe()
	if a.Close() != nil {
		return false
	}
	_, rerr := b.Read(make([]byte, 1))
	_, werr := a.Write([]byte("x"))
	return rerr == io.EOF && errors.Is(werr, io.ErrClosedPipe)
}

// A deadline in the past fails a read at once with a timeout.
func testNetPipeDeadline() bool {
	a, b := net.Pipe()
	if b.SetReadDeadline(time.Now().Add(-time.Second)) != nil {
		return false
	}
	_, err := b.Read(make([]byte, 1))
	return errors.Is(err, os.ErrDeadlineExceeded) && a.Close() == nil && b.Close() == nil
}

// A pipe end closed by a deferred call (#123): the other end then reads
// io.EOF.
func testNetPipeDeferClose() bool {
	a, b := net.Pipe()
	func() {
		defer a.Close()
	}()
	_, err := b.Read(make([]byte, 1))
	return err == io.EOF
}

// The pipe's addresses are "pipe".
func testNetPipeAddr() bool {
	a, b := net.Pipe()
	return a.LocalAddr().Network() == "pipe" && b.RemoteAddr().String() == "pipe" &&
		a.Close() == nil && b.Close() == nil
}

func testNetSplitHostPort() bool {
	h, p, err := net.SplitHostPort("127.0.0.1:9092")
	h6, p6, err6 := net.SplitHostPort("[::1]:80")
	_, _, bad := net.SplitHostPort("localhost")
	return err == nil && h == "127.0.0.1" && p == "9092" &&
		err6 == nil && h6 == "::1" && p6 == "80" && bad != nil
}

func testNetJoinHostPort() bool {
	return net.JoinHostPort("127.0.0.1", "9092") == "127.0.0.1:9092" &&
		net.JoinHostPort("::1", "80") == "[::1]:80"
}
