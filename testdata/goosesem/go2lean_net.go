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

// Loopback networking (#126): a listener and a dialler meet through
// net.Listen, net.Dial and Accept.

// One byte sent by the client comes back from the server.
func testNetListenDialEcho() bool {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	done := make(chan bool)
	go func() {
		c, err := l.Accept()
		if err != nil {
			done <- false
			return
		}
		b := make([]byte, 1)
		n, err := c.Read(b)
		if err == nil && n == 1 {
			_, err = c.Write(b)
		}
		done <- err == nil
	}()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		return false
	}
	if _, err := c.Write([]byte{7}); err != nil {
		return false
	}
	b := make([]byte, 1)
	n, err := c.Read(b)
	return <-done && err == nil && n == 1 && b[0] == 7 && c.Close() == nil && l.Close() == nil
}

// Nothing listens at a closed listener's address: Dial fails.
func testNetDialRefused() bool {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	addr := l.Addr().String()
	if l.Close() != nil {
		return false
	}
	_, err = net.Dial("tcp", addr)
	return err != nil
}

// Accept on a closed listener fails with net.ErrClosed.
func testNetAcceptAfterClose() bool {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil || l.Close() != nil {
		return false
	}
	_, err = l.Accept()
	return errors.Is(err, net.ErrClosed)
}

// Port 0 gives each listener a port of its own.
func testNetListenFreshPorts() bool {
	a, err1 := net.Listen("tcp", "127.0.0.1:0")
	b, err2 := net.Listen("tcp", "127.0.0.1:0")
	if err1 != nil || err2 != nil {
		return false
	}
	differ := a.Addr().String() != b.Addr().String()
	return differ && a.Close() == nil && b.Close() == nil
}
