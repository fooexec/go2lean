// Package netloop is the model of loopback networking (#126): the
// frontend type-checks this file into the program it translates and makes
// the program's calls of net.Listen, net.Dial and net.ListenPacket call
// Listen, Dial and ListenPacket here. A stream connection is a net.Pipe
// pair, so both ends are the program's own code over channels, which the
// model covers; only the socket boundary is replaced. A UDP socket is a
// queue of datagrams in a table of ports (ListenPacket, and Dial of a
// "udp" network). It is not used by go2lean itself.
package netloop

import (
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

// backlog is how many dialled connections wait for Accept, as a socket's
// listen queue does: Dial returns before the server accepts.
const backlog = 16

type addr struct{ network, s string }

func (a addr) Network() string { return a.network }
func (a addr) String() string  { return a.s }

type listener struct {
	key   string
	addr  addr
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

var (
	mu        sync.Mutex
	listeners = map[string]*listener{}
	ports     int // ports handed out for port 0
)

var errRefused = errors.New("connect: connection refused")

// Listen opens a listener at address; a TCP or UDP port 0 is a fresh port
// from 40000 on. A unix socket's address is its path.
func Listen(network, address string) (net.Listener, error) {
	mu.Lock()
	defer mu.Unlock()
	a := address
	if network != "unix" && network != "unixpacket" {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, &net.OpError{Op: "listen", Net: network, Err: err}
		}
		if port == "0" || port == "" {
			ports++
			port = strconv.Itoa(40000 + ports)
		}
		if host == "" {
			host = "127.0.0.1"
		}
		a = net.JoinHostPort(host, port)
	}
	key := network + " " + a
	if listeners[key] != nil {
		return nil, &net.OpError{Op: "listen", Net: network, Err: errors.New("bind: address already in use")}
	}
	l := &listener{key: key, addr: addr{network, a}, conns: make(chan net.Conn, backlog), done: make(chan struct{})}
	listeners[key] = l
	return l, nil
}

// Dial connects to the listener at address: a net.Pipe pair, one end in
// the listener's backlog, the other returned. With no listener there, the
// connection is refused.
func Dial(network, address string) (net.Conn, error) {
	if isUDP(network) {
		return dialUDP(network, address)
	}
	mu.Lock()
	l := listeners[network+" "+address]
	mu.Unlock()
	if l == nil {
		return nil, &net.OpError{Op: "dial", Net: network, Err: errRefused}
	}
	c, s := net.Pipe()
	select {
	case l.conns <- s:
		return c, nil
	case <-l.done:
		return nil, &net.OpError{Op: "dial", Net: network, Err: errRefused}
	}
}

func (l *listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, &net.OpError{Op: "accept", Net: l.addr.network, Addr: l.addr, Err: net.ErrClosed}
	}
}

func (l *listener) Close() error {
	closed := false
	l.once.Do(func() {
		close(l.done)
		mu.Lock()
		delete(listeners, l.key)
		mu.Unlock()
		closed = true
	})
	if !closed {
		return &net.OpError{Op: "close", Net: l.addr.network, Addr: l.addr, Err: net.ErrClosed}
	}
	return nil
}

func (l *listener) Addr() net.Addr { return l.addr }

// UDP on loopback. Every address is the local machine: a datagram goes to
// the socket bound to its destination port, whatever the IP. A socket keeps
// up to udpQueue datagrams; one sent to a full queue, or to a port no socket
// is bound to, is lost and the send still succeeds, as on a real socket.
// Datagrams keep their boundaries and arrive in the order sent; a read into
// a shorter buffer gets the start of the datagram and the rest is
// discarded.

// udpQueue is how many datagrams wait to be read at one socket.
const udpQueue = 64

// maxUDP is the largest UDP payload over IPv4.
const maxUDP = 65507

type datagram struct {
	from *net.UDPAddr
	b    []byte
}

// cancel is a channel closed once, by a deadline's timer or by a deadline
// set in the past.
type cancel struct {
	ch   chan struct{}
	once sync.Once
}

func newCancel() *cancel { return &cancel{ch: make(chan struct{})} }

func (c *cancel) fire() { c.once.Do(func() { close(c.ch) }) }

// deadline is a read deadline on the model's clock. Every set makes a new
// channel, so a timer left over from an earlier deadline cannot end a
// later one (it closes a channel nothing waits on; there is no need to stop
// it), and wakes the reads that wait (changed), which then wait on the new
// one.
type deadline struct {
	mu      sync.Mutex
	cur     *cancel
	changed chan struct{}
}

func (d *deadline) init() {
	d.cur = newCancel()
	d.changed = make(chan struct{})
}

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cur = newCancel()
	close(d.changed)
	d.changed = make(chan struct{})
	if t.IsZero() {
		return
	}
	if dur := time.Until(t); dur > 0 {
		time.AfterFunc(dur, d.cur.fire)
		return
	}
	d.cur.fire()
}

func (d *deadline) wait() (*cancel, chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cur, d.changed
}

type udpSock struct {
	network string
	laddr   *net.UDPAddr
	peer    *net.UDPAddr // the peer of a connected socket (Dial)
	q       chan datagram
	done    chan struct{}
	once    sync.Once
	rd      deadline
}

var udpSocks = map[int]*udpSock{}

func isUDP(network string) bool {
	return network == "udp" || network == "udp4" || network == "udp6"
}

var (
	ipLoop4  = net.IP{127, 0, 0, 1}
	ipLoop6  = net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	ipZero4  = net.IP{0, 0, 0, 0}
	ipZero6  = net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	errNoDst = errors.New("destination address required")
)

// udpIP resolves the host part of a UDP address: an IP literal of the
// loopback or unspecified address, or localhost. Other hosts are not
// modelled.
func udpIP(network, host string) (net.IP, bool) {
	switch host {
	case "":
		if network == "udp4" {
			return ipZero4, true
		}
		return ipZero6, true
	case "0.0.0.0":
		return ipZero4, true
	case "::":
		return ipZero6, true
	case "127.0.0.1", "localhost":
		if network == "udp6" {
			return ipLoop6, true
		}
		return ipLoop4, true
	case "::1":
		return ipLoop6, true
	}
	return nil, false
}

func isV4(ip net.IP) bool { return len(ip) == 4 }

func isUnspecified(ip net.IP) bool {
	for _, b := range ip {
		if b != 0 {
			return false
		}
	}
	return true
}

func splitUDP(network, address string) (net.IP, int, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, 0, err
	}
	ip, ok := udpIP(network, host)
	if !ok {
		return nil, 0, errors.New("lookup " + host + ": no such host")
	}
	p := 0
	if port != "" {
		if p, err = strconv.Atoi(port); err != nil || p < 0 || p > 65535 {
			return nil, 0, errors.New("invalid port " + port)
		}
	}
	return ip, p, nil
}

// bindUDP binds a socket to ip and port (0: a fresh port from 40000 on).
func bindUDP(network string, ip net.IP, port int) (*udpSock, error) {
	mu.Lock()
	defer mu.Unlock()
	if port == 0 {
		ports++
		port = 40000 + ports
	}
	if udpSocks[port] != nil {
		return nil, errors.New("bind: address already in use")
	}
	s := &udpSock{network: network, laddr: &net.UDPAddr{IP: ip, Port: port},
		q: make(chan datagram, udpQueue), done: make(chan struct{})}
	s.rd.init()
	udpSocks[port] = s
	return s, nil
}

// ListenPacket binds a UDP socket at address. Other networks (ip, unixgram)
// are not modelled.
func ListenPacket(network, address string) (net.PacketConn, error) {
	if !isUDP(network) {
		return nil, &net.OpError{Op: "listen", Net: network, Err: errors.New("network not modelled")}
	}
	ip, port, err := splitUDP(network, address)
	if err == nil {
		var s *udpSock
		if s, err = bindUDP(network, ip, port); err == nil {
			return s, nil
		}
	}
	return nil, &net.OpError{Op: "listen", Net: network, Err: err}
}

// dialUDP makes a connected socket: bound to a fresh port, it sends to the
// peer and reads only what the peer sends. A UDP connect does not check that
// anything listens.
func dialUDP(network, address string) (net.Conn, error) {
	ip, port, err := splitUDP(network, address)
	if err == nil && port == 0 {
		err = errors.New("missing port in address")
	}
	if err == nil {
		local := ipLoop4
		if !isV4(ip) {
			local = ipLoop6
		}
		var s *udpSock
		if s, err = bindUDP(network, local, 0); err == nil {
			s.peer = &net.UDPAddr{IP: ip, Port: port}
			return s, nil
		}
	}
	return nil, &net.OpError{Op: "dial", Net: network, Err: err}
}

func (s *udpSock) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *udpSock) opErr(op string, a net.Addr, err error) error {
	return &net.OpError{Op: op, Net: s.network, Source: s.laddr, Addr: a, Err: err}
}

func addrPort(a net.Addr) (net.IP, int, bool) {
	if u, ok := a.(*net.UDPAddr); ok && u != nil {
		return u.IP, u.Port, true
	}
	if a == nil {
		return nil, 0, false
	}
	host, port, err := net.SplitHostPort(a.String())
	if err != nil {
		return nil, 0, false
	}
	ip, ok := udpIP("udp", host)
	p, err := strconv.Atoi(port)
	return ip, p, ok && err == nil
}

// send delivers a copy of b to the socket bound to port, from s.
func (s *udpSock) send(b []byte, ip net.IP, port int) {
	src := s.laddr.IP
	if isUnspecified(src) {
		// The kernel sends from the loopback address of the destination's
		// family.
		src = ipLoop4
		if !isV4(ip) && !isUnspecified(ip) {
			src = ipLoop6
		}
	}
	mu.Lock()
	r := udpSocks[port]
	mu.Unlock()
	if r == nil || r.closed() {
		return
	}
	if r.peer != nil && r.peer.Port != s.laddr.Port {
		return // a connected socket takes datagrams from its peer only
	}
	c := make([]byte, len(b))
	copy(c, b)
	select {
	case r.q <- datagram{from: &net.UDPAddr{IP: src, Port: s.laddr.Port}, b: c}:
	default: // the receive queue is full: the datagram is lost
	}
}

func (s *udpSock) WriteTo(b []byte, a net.Addr) (int, error) {
	if s.closed() {
		return 0, s.opErr("write", a, net.ErrClosed)
	}
	if s.peer != nil {
		return 0, s.opErr("write", a, net.ErrWriteToConnected)
	}
	ip, port, ok := addrPort(a)
	if !ok {
		return 0, s.opErr("write", a, errors.New("invalid address"))
	}
	if len(b) > maxUDP {
		return 0, s.opErr("write", a, errors.New("sendto: message too long"))
	}
	s.send(b, ip, port)
	return len(b), nil
}

func (s *udpSock) Write(b []byte) (int, error) {
	if s.closed() {
		return 0, s.opErr("write", s.peer, net.ErrClosed)
	}
	if s.peer == nil {
		return 0, s.opErr("write", nil, errNoDst)
	}
	if len(b) > maxUDP {
		return 0, s.opErr("write", s.peer, errors.New("write: message too long"))
	}
	s.send(b, s.peer.IP, s.peer.Port)
	return len(b), nil
}

func (s *udpSock) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		if s.closed() {
			return 0, nil, s.opErr("read", nil, net.ErrClosed)
		}
		c, changed := s.rd.wait()
		select {
		case <-c.ch:
			return 0, nil, s.opErr("read", nil, os.ErrDeadlineExceeded)
		default:
		}
		select {
		case d := <-s.q:
			return copy(b, d.b), d.from, nil
		case <-s.done:
			return 0, nil, s.opErr("read", nil, net.ErrClosed)
		case <-c.ch:
			return 0, nil, s.opErr("read", nil, os.ErrDeadlineExceeded)
		case <-changed:
		}
	}
}

func (s *udpSock) Read(b []byte) (int, error) {
	n, _, err := s.ReadFrom(b)
	return n, err
}

func (s *udpSock) Close() error {
	closed := false
	s.once.Do(func() {
		close(s.done)
		mu.Lock()
		delete(udpSocks, s.laddr.Port)
		mu.Unlock()
		closed = true
	})
	if !closed {
		return s.opErr("close", nil, net.ErrClosed)
	}
	return nil
}

func (s *udpSock) LocalAddr() net.Addr { return s.laddr }

// RemoteAddr is the peer of a connected socket, nil otherwise.
func (s *udpSock) RemoteAddr() net.Addr {
	if s.peer == nil {
		return nil
	}
	return s.peer
}

func (s *udpSock) SetDeadline(t time.Time) error {
	if s.closed() {
		return s.opErr("set", nil, net.ErrClosed)
	}
	s.rd.set(t)
	return nil
}

func (s *udpSock) SetReadDeadline(t time.Time) error { return s.SetDeadline(t) }

// SetWriteDeadline: a send never blocks, so a write deadline changes
// nothing.
func (s *udpSock) SetWriteDeadline(t time.Time) error {
	if s.closed() {
		return s.opErr("set", nil, net.ErrClosed)
	}
	return nil
}
