// Package netloop is the model of loopback networking (#126): the
// frontend type-checks this file into the program it translates and makes
// the program's calls of net.Listen and net.Dial call Listen and Dial
// here. A connection is a net.Pipe pair, so both ends are the program's
// own code over channels, which the model covers; only the socket
// boundary is replaced. It is not used by go2lean itself.
package netloop

import (
	"errors"
	"net"
	"strconv"
	"sync"
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
