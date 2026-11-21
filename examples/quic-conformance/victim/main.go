// Command victim is an ordinary quic-go server: it accepts connections,
// opens one unidirectional and one bidirectional stream towards the peer,
// and reads and discards whatever the peer sends. It is built against an
// unmodified checkout.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

func selfSigned() tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), DNSNames: []string{"localhost"}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4433", "listen address")
	dialAddr := flag.String("dial", "", "act as a client: connect to this address, repeatedly, one connection at a time")
	flag.Parse()
	if *dialAddr != "" {
		client(*dialAddr)
		return
	}
	udpAddr, err := net.ResolveUDPAddr("udp", *addr)
	if err != nil {
		panic(err)
	}
	pc, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		panic(err)
	}
	// A stateless reset key makes the server answer unattributable packets with Stateless Resets.
	var key quic.StatelessResetKey
	rand.Read(key[:])
	tr := &quic.Transport{Conn: pc, StatelessResetKey: &key}
	ln, err := tr.Listen(&tls.Config{Certificates: []tls.Certificate{selfSigned()}, NextProtos: []string{"conf"}}, &quic.Config{MaxIdleTimeout: 10 * time.Second})
	if err != nil {
		panic(err)
	}
	fmt.Println("listening", *addr)
	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			panic(err)
		}
		go serve(conn)
	}
}

func serve(conn *quic.Conn) {
	if s, err := conn.OpenUniStream(); err == nil {
		s.Write([]byte("hello")) // makes the stream exist on the peer
	}
	if s, err := conn.OpenStream(); err == nil {
		s.Write([]byte("hello"))
		go io.Copy(io.Discard, s)
	}
	go func() {
		for {
			s, err := conn.AcceptStream(context.Background())
			if err != nil {
				return
			}
			go io.Copy(io.Discard, s)
		}
	}()
	go func() {
		for {
			s, err := conn.AcceptUniStream(context.Background())
			if err != nil {
				return
			}
			go io.Copy(io.Discard, s)
		}
	}()
	<-conn.Context().Done()
	fmt.Printf("closed %v\n", context.Cause(conn.Context()))
}

// client connects to the attacker, opens one bidirectional and one
// unidirectional stream towards it, reads whatever it sends, and
// reconnects when the connection ends.
func client(addr string) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := quic.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"conf"}}, &quic.Config{MaxIdleTimeout: 10 * time.Second})
		cancel()
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if s, err := conn.OpenStream(); err == nil {
			s.Write([]byte("hello"))
			go io.Copy(io.Discard, s)
		}
		if s, err := conn.OpenUniStream(); err == nil {
			s.Write([]byte("hello"))
		}
		go func() {
			for {
				s, err := conn.AcceptStream(context.Background())
				if err != nil {
					return
				}
				go io.Copy(io.Discard, s)
			}
		}()
		go func() {
			for {
				s, err := conn.AcceptUniStream(context.Background())
				if err != nil {
					return
				}
				go io.Copy(io.Discard, s)
			}
		}()
		<-conn.Context().Done()
		fmt.Printf("closed %v\n", context.Cause(conn.Context()))
	}
}
