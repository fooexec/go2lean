// Package udp answers datagrams on a UDP socket. Its properties run the
// server and two clients over loopback sockets (net.ListenPacket and
// net.Dial("udp", ...)), which the model has as a queue of datagrams per
// port: whether a reply reaches the client that asked depends on the
// addresses the socket reports, so the model has to route them.
package udp

import (
	"errors"
	"net"
)

var errNoSocket = errors.New("udp: no socket")

// StickyServer answers each datagram with "r" and its first byte. Bug: it
// remembers the first client's address and answers every later request
// there, so a second client never hears back.
type StickyServer struct {
	pc   net.PacketConn
	peer net.Addr
}

// NewStickyServer serves on pc.
func NewStickyServer(pc net.PacketConn) *StickyServer { return &StickyServer{pc: pc} }

// Step reads one datagram and answers it.
func (s *StickyServer) Step() error {
	if s.pc == nil {
		return errNoSocket
	}
	b := make([]byte, 16)
	n, from, err := s.pc.ReadFrom(b)
	if err != nil {
		return err
	}
	if s.peer == nil {
		s.peer = from
	}
	_, err = s.pc.WriteTo(reply(b[:n]), s.peer)
	return err
}

// EchoServer answers each datagram the same way, to its sender.
type EchoServer struct {
	pc net.PacketConn
}

// NewEchoServer serves on pc.
func NewEchoServer(pc net.PacketConn) *EchoServer { return &EchoServer{pc: pc} }

// Step reads one datagram and answers it.
func (s *EchoServer) Step() error {
	if s.pc == nil {
		return errNoSocket
	}
	b := make([]byte, 16)
	n, from, err := s.pc.ReadFrom(b)
	if err != nil {
		return err
	}
	_, err = s.pc.WriteTo(reply(b[:n]), from)
	return err
}

func reply(req []byte) []byte {
	if len(req) == 0 {
		return []byte{'r', 0}
	}
	return []byte{'r', req[0]}
}

// recorder is the server's socket, noting where each reply goes.
type recorder struct {
	net.PacketConn
	to net.Addr
}

func (r *recorder) ReadFrom(b []byte) (int, net.Addr, error) {
	if r == nil || r.PacketConn == nil {
		return 0, nil, errNoSocket
	}
	return r.PacketConn.ReadFrom(b)
}

func (r *recorder) WriteTo(b []byte, a net.Addr) (int, error) {
	if r == nil || r.PacketConn == nil {
		return 0, errNoSocket
	}
	r.to = a
	return r.PacketConn.WriteTo(b, a)
}

type stepper interface {
	Step() error
}

// repliesToSender sends one datagram per event (up to four) from one of
// two clients (the event says which), holding the event's index, and lets
// the server answer: the reply must go to the client that sent it, which
// then reads it.
func repliesToSender(rec *recorder, srv stepper, events []bool) bool {
	if rec == nil || rec.PacketConn == nil || srv == nil {
		return false
	}
	if len(events) > 4 {
		events = events[:4] // two clients alternating show the bug in two
	}
	var clients [2]net.Conn
	for i := range clients {
		c, err := net.Dial("udp", rec.LocalAddr().String())
		if err != nil {
			return false
		}
		defer c.Close()
		clients[i] = c
	}
	for i, second := range events {
		c := clients[0]
		if second {
			c = clients[1]
		}
		e := byte(i)
		if _, err := c.Write([]byte{e}); err != nil {
			return false
		}
		rec.to = nil
		if err := srv.Step(); err != nil || rec.to == nil {
			return false
		}
		to, ok1 := rec.to.(*net.UDPAddr)
		me, ok2 := c.LocalAddr().(*net.UDPAddr)
		if !ok1 || !ok2 || to.Port != me.Port {
			return false
		}
		b := make([]byte, 4)
		n, err := c.Read(b)
		if err != nil || n != 2 || b[0] != 'r' || b[1] != e {
			return false
		}
	}
	return true
}

// SeqStickyRepliesToSender: every reply goes to the client that asked, and
// it reads it. Fails: once a second client asks, its reply goes to the
// first.
//
// go2lean:sequence StickyServer.Step
func SeqStickyRepliesToSender(events []bool) bool {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	defer pc.Close()
	rec := &recorder{PacketConn: pc}
	return repliesToSender(rec, NewStickyServer(rec), events)
}

// SeqEchoRepliesToSender: the same for EchoServer. Holds.
//
// go2lean:sequence EchoServer.Step
func SeqEchoRepliesToSender(events []bool) bool {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	defer pc.Close()
	rec := &recorder{PacketConn: pc}
	return repliesToSender(rec, NewEchoServer(rec), events)
}
