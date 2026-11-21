package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

// Rules about packets the receiver cannot see from raw frames: the attacker
// changes the header bits of packets it builds, before they are encrypted.

type hookRule struct {
	id, rfc, text string
	expect        int
	arm           func() // sets quic.AttackPreSeal for the packets that follow
	longHeader    bool   // the rule applies while the handshake runs
	send          func(c *quic.Conn)
}

func hookRuleList() []hookRule {
	setReserved := func(mask byte, long bool) func() {
		return func() {
			quic.AttackPreSeal = func(raw []byte, po, pl int) {
				if (raw[0]&0x80 != 0) == long {
					raw[0] |= mask
				}
			}
		}
	}
	return []hookRule{
		{"Q01", "9000 17.3.1", "short header packet with the reserved bits set", protocolViolation, setReserved(0x18, false), false,
			func(c *quic.Conn) { c.AttackSendRaw(vi(0x01)) }},
		{"Q02", "9000 17.2", "Initial packet with the reserved bits set", protocolViolation, setReserved(0x0c, true), true, nil},
		{"Q03", "9001 6", "short header packet with the Key Phase bit flipped but sealed with the current keys: discarded, the connection goes on", alive,
			func() {
				quic.AttackPreSeal = func(raw []byte, po, pl int) {
					if raw[0]&0x80 == 0 {
						raw[0] ^= 0x04
					}
				}
			}, false,
			func(c *quic.Conn) {
				c.AttackSendRaw(vi(0x01))
				time.Sleep(100 * time.Millisecond)
				quic.AttackPreSeal = nil
				c.AttackSendRaw(vi(0x01))
			}},
	}
}

func runHookRules(addr string) int {
	bad := 0
	for _, r := range hookRuleList() {
		var got int
		var detail string
		if r.longHeader {
			r.arm()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			pc, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			tr := &quic.Transport{Conn: pc, ConnectionIDLength: 8}
			raddr, _ := net.ResolveUDPAddr("udp", addr)
			_, err := tr.Dial(ctx, raddr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"conf"}}, &quic.Config{HandshakeIdleTimeout: 3 * time.Second})
			cancel()
			quic.AttackPreSeal = nil
			var te *quic.TransportError
			switch {
			case err == nil:
				got = alive
			case errors.As(err, &te):
				got, detail = int(te.ErrorCode), fmt.Sprintf("remote=%v %q", te.Remote, te.ErrorMessage)
			default:
				got, detail = -2, err.Error()
			}
		} else {
			c, err := dial(addr)
			if err != nil {
				fmt.Printf("%s  ERROR dial: %v\n", r.id, err)
				bad++
				continue
			}
			r.arm()
			r.send(c)
			got, detail = outcome(c)
			quic.AttackPreSeal = nil
			c.CloseWithError(0, "")
		}
		status := "ok"
		if !matches(r.expect, got) {
			status = "DIFF"
			bad++
		}
		fmt.Printf("%s  %-5s expected %-20s got %-20s RFC %-12s %s %s\n", r.id, status, name(r.expect), name(got), r.rfc, r.text, detail)
	}
	if bad > 0 {
		return 1
	}
	return 0
}
