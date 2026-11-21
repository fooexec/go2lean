package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

// RFC 9000 section 8.1: until an address is validated, an endpoint MUST NOT
// send more than three times the bytes it has received from that address.
// The attacker's socket counts what it sends and what arrives, and drops
// everything that arrives, so the server never sees a reply that validates it.

type deafConn struct {
	net.PacketConn // an interface, so the UDP-specific batch methods are hidden and ReadFrom/WriteTo are used
	rx, tx         atomic.Int64
}

func (c *deafConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, _, err := c.PacketConn.ReadFrom(b)
		if err != nil {
			return 0, nil, err
		}
		c.rx.Add(int64(n))
	}
}

func (c *deafConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(b, addr)
	c.tx.Add(int64(n))
	return n, err
}

func newDeaf() *deafConn {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		panic(err)
	}
	return &deafConn{PacketConn: pc}
}

func tlsConf() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"conf"}}
}

func runAmplification(addr string) int {
	raddr, _ := net.ResolveUDPAddr("udp", addr)
	bad := 0
	report := func(id, rfc, text string, sent, got int64) {
		status := "ok"
		if got >= 3*sent+1 { // 3 times what was received is the limit
			status = "DIFF(MUST)"
			bad++
		}
		fmt.Printf("%s  %-11s RFC %-8s %s -- sent %d bytes, received %d bytes (ratio %.2f)\n", id, status, rfc, text, sent, got, float64(got)/float64(max(sent, 1)))
	}

	// A1: a client that sends Initial packets and never receives.
	d := newDeaf()
	tr := &quic.Transport{Conn: d, ConnectionIDLength: 8}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	tr.Dial(ctx, raddr, tlsConf(), &quic.Config{HandshakeIdleTimeout: 4 * time.Second})
	cancel()
	time.Sleep(200 * time.Millisecond)
	report("A1", "9000 8.1", "handshake with a client that never answers", d.tx.Load(), d.rx.Load())
	tr.Close()

	// A2: a path the server cannot validate (the new socket receives, but drops).
	c, err := dial(addr)
	if err != nil {
		fmt.Println("A2 ERROR dial:", err)
		return 1
	}
	d2 := newDeaf()
	tr2 := &quic.Transport{Conn: d2, ConnectionIDLength: 8}
	p, err := c.AddPath(tr2)
	if err != nil {
		fmt.Println("A2 ERROR AddPath:", err)
		return 1
	}
	pctx, pcancel := context.WithTimeout(context.Background(), 3*time.Second)
	p.Probe(pctx) // never validates: the replies are dropped
	pcancel()
	time.Sleep(200 * time.Millisecond)
	report("A2", "9000 8.2", "server's bytes to a new path it cannot validate", d2.tx.Load(), d2.rx.Load())
	c.CloseWithError(0, "")

	// A3: same, while the server has data to send (a bidirectional stream with a large response is not set up; the victim only reads).
	if bad > 0 {
		return 1
	}
	return 0
}
