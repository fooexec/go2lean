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

// UDP on loopback: net.ListenPacket and net.Dial("udp", ...) (netloop).

// A datagram sent by a connected client reaches the server, which sees the
// client's address and answers it.
func testNetUDPEcho() bool {
	s, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	c, err := net.Dial("udp", s.LocalAddr().String())
	if err != nil {
		return false
	}
	if n, err := c.Write([]byte("abc")); n != 3 || err != nil {
		return false
	}
	b := make([]byte, 16)
	n, from, err := s.ReadFrom(b)
	if err != nil || n != 3 || string(b[:n]) != "abc" || from.String() != c.LocalAddr().String() {
		return false
	}
	if _, err := s.WriteTo([]byte("xy"), from); err != nil {
		return false
	}
	n, err = c.Read(b)
	return err == nil && n == 2 && string(b[:2]) == "xy" && c.Close() == nil && s.Close() == nil
}

// Datagrams keep their boundaries and their order.
func testNetUDPBoundaries() bool {
	s, _ := net.ListenPacket("udp", "127.0.0.1:0")
	c, _ := net.Dial("udp", s.LocalAddr().String())
	c.Write([]byte("ab"))
	c.Write([]byte("cde"))
	b := make([]byte, 16)
	n1, _, err1 := s.ReadFrom(b)
	first := string(b[:n1])
	n2, _, err2 := s.ReadFrom(b)
	ok := err1 == nil && err2 == nil && first == "ab" && string(b[:n2]) == "cde"
	return ok && c.Close() == nil && s.Close() == nil
}

// A read into a shorter buffer gets the start of the datagram; the rest is
// discarded, and the next read gets the next datagram.
func testNetUDPTruncate() bool {
	s, _ := net.ListenPacket("udp", "127.0.0.1:0")
	c, _ := net.Dial("udp", s.LocalAddr().String())
	c.Write([]byte("hello"))
	c.Write([]byte("w"))
	b := make([]byte, 2)
	n1, _, err1 := s.ReadFrom(b)
	first := string(b[:n1])
	n2, _, err2 := s.ReadFrom(b)
	ok := err1 == nil && err2 == nil && n1 == 2 && first == "he" && n2 == 1 && b[0] == 'w'
	return ok && c.Close() == nil && s.Close() == nil
}

// The addresses are *net.UDPAddr, as the kernel's.
func testNetUDPAddrType() bool {
	s, _ := net.ListenPacket("udp", "127.0.0.1:0")
	c, _ := net.Dial("udp", s.LocalAddr().String())
	c.Write([]byte{1})
	_, from, err := s.ReadFrom(make([]byte, 1))
	la, ok1 := s.LocalAddr().(*net.UDPAddr)
	fa, ok2 := from.(*net.UDPAddr)
	ok := err == nil && ok1 && ok2 && la.Port != 0 && fa.Port == c.LocalAddr().(*net.UDPAddr).Port &&
		s.LocalAddr().Network() == "udp" && fa.IP.String() == "127.0.0.1"
	return ok && c.Close() == nil && s.Close() == nil
}

// Sending to a port nothing is bound to succeeds: the datagram is lost.
func testNetUDPWriteToNowhere() bool {
	s, _ := net.ListenPacket("udp", "127.0.0.1:0")
	gone, _ := net.ListenPacket("udp", "127.0.0.1:0")
	to := gone.LocalAddr()
	gone.Close()
	n, err := s.WriteTo([]byte("x"), to)
	return n == 1 && err == nil && s.Close() == nil
}

// A read on a closed socket, and a second Close, fail with net.ErrClosed.
func testNetUDPClosed() bool {
	s, _ := net.ListenPacket("udp", "127.0.0.1:0")
	if s.Close() != nil {
		return false
	}
	_, _, err := s.ReadFrom(make([]byte, 1))
	return errors.Is(err, net.ErrClosed) && errors.Is(s.Close(), net.ErrClosed)
}

// A read deadline in the past fails a read at once with a timeout; cleared,
// the next read gets the datagram.
func testNetUDPDeadline() bool {
	s, _ := net.ListenPacket("udp", "127.0.0.1:0")
	if s.SetReadDeadline(time.Now().Add(-time.Second)) != nil {
		return false
	}
	_, _, err := s.ReadFrom(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		return false
	}
	s.SetReadDeadline(time.Time{})
	c, _ := net.Dial("udp", s.LocalAddr().String())
	c.Write([]byte{9})
	b := make([]byte, 1)
	n, _, err := s.ReadFrom(b)
	return err == nil && n == 1 && b[0] == 9 && c.Close() == nil && s.Close() == nil
}

// A deadline set while a read waits ends that read (how a server stops
// its read loop).
func testNetUDPDeadlineWakes() bool {
	s, _ := net.ListenPacket("udp", "127.0.0.1:0")
	done := make(chan error)
	go func() {
		_, _, err := s.ReadFrom(make([]byte, 1))
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	s.SetReadDeadline(time.Now())
	err := <-done
	return errors.Is(err, os.ErrDeadlineExceeded) && s.Close() == nil
}

// Close ends a read that waits.
func testNetUDPCloseWakes() bool {
	s, _ := net.ListenPacket("udp", "127.0.0.1:0")
	done := make(chan error)
	go func() {
		_, _, err := s.ReadFrom(make([]byte, 1))
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	s.Close()
	return errors.Is(<-done, net.ErrClosed)
}

// Two sockets cannot share a port; a connected socket reads only its peer.
func testNetUDPPorts() bool {
	s, _ := net.ListenPacket("udp", "127.0.0.1:0")
	_, err := net.ListenPacket("udp", s.LocalAddr().String())
	if err == nil {
		return false
	}
	c, _ := net.Dial("udp", s.LocalAddr().String())
	other, _ := net.ListenPacket("udp", "127.0.0.1:0")
	other.WriteTo([]byte("no"), c.LocalAddr())
	s.WriteTo([]byte("ok"), c.LocalAddr())
	b := make([]byte, 4)
	n, err := c.Read(b)
	return err == nil && string(b[:n]) == "ok" && c.Close() == nil && s.Close() == nil && other.Close() == nil
}
