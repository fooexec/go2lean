package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// Packet-level rules: raw datagrams sent to a server, no connection. The
// expected outcome is a datagram of a given shape or silence.

type pktRule struct {
	id, rfc, text string
	level         string // MUST, SHOULD, MAY
	run           func(conn *net.UDPConn) (ok bool, detail string)
}

func exchange(conn *net.UDPConn, dgram []byte, wait time.Duration) [][]byte {
	conn.Write(dgram)
	var out [][]byte
	conn.SetReadDeadline(time.Now().Add(wait))
	for {
		buf := make([]byte, 2000)
		n, err := conn.Read(buf)
		if err != nil {
			return out
		}
		out = append(out, buf[:n])
	}
}

func rnd(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// longHeader builds first byte, version, DCID, SCID, then fills with random bytes up to size.
func longHeader(first byte, version uint32, dcid, scid []byte, size int) []byte {
	b := []byte{first}
	b = binary.BigEndian.AppendUint32(b, version)
	b = append(b, byte(len(dcid)))
	b = append(b, dcid...)
	b = append(b, byte(len(scid)))
	b = append(b, scid...)
	if len(b) < size {
		b = append(b, rnd(size-len(b))...)
	}
	return b
}

func silent(r [][]byte) (bool, string) {
	if len(r) == 0 {
		return true, "no response"
	}
	return false, fmt.Sprintf("%d datagram(s), first %d bytes: %x", len(r), len(r[0]), r[0][:min(len(r[0]), 24)])
}

func packetRules() []pktRule {
	return []pktRule{
		{"P01", "9000 5.2.2, 17.2.1", "unsupported version, datagram of 1200 bytes: Version Negotiation with the connection IDs swapped, listing versions but not the offered one", "SHOULD", func(c *net.UDPConn) (bool, string) {
			dcid, scid := rnd(8), rnd(8)
			r := exchange(c, longHeader(0xc0|byte(rnd(1)[0]&0x0f), 0x0a1a2a3a, dcid, scid, 1200), time.Second)
			if len(r) != 1 {
				return false, fmt.Sprintf("%d datagrams", len(r))
			}
			p := r[0]
			if p[0]&0x80 == 0 || binary.BigEndian.Uint32(p[1:5]) != 0 {
				return false, "not a Version Negotiation packet"
			}
			dl := int(p[5])
			if !bytes.Equal(p[6:6+dl], scid) {
				return false, "Destination Connection ID is not the client's Source Connection ID"
			}
			sl := int(p[6+dl])
			if !bytes.Equal(p[7+dl:7+dl+sl], dcid) {
				return false, "Source Connection ID is not the client's Destination Connection ID"
			}
			vs := p[7+dl+sl:]
			if len(vs) == 0 || len(vs)%4 != 0 {
				return false, fmt.Sprintf("version list of %d bytes", len(vs))
			}
			for i := 0; i < len(vs); i += 4 {
				if binary.BigEndian.Uint32(vs[i:]) == 0x0a1a2a3a {
					return false, "lists the version the client offered"
				}
			}
			return true, fmt.Sprintf("versions %x", vs)
		}},
		{"P02", "9000 5.2.2", "unsupported version, datagram of 1100 bytes: dropped", "MUST", func(c *net.UDPConn) (bool, string) {
			return silent(exchange(c, longHeader(0xc0, 0x0a1a2a3a, rnd(8), rnd(8), 1100), 700*time.Millisecond))
		}},
		{"P03", "9000 17.2", "version 1 long header with a 21-byte Destination Connection ID: dropped", "MUST", func(c *net.UDPConn) (bool, string) {
			return silent(exchange(c, longHeader(0xc0, 1, rnd(21), rnd(8), 1200), 700*time.Millisecond))
		}},
		{"P04", "9000 17.2", "version 1 long header with a 21-byte Source Connection ID: dropped", "MUST", func(c *net.UDPConn) (bool, string) {
			return silent(exchange(c, longHeader(0xc0, 1, rnd(8), rnd(21), 1200), 700*time.Millisecond))
		}},
		{"P05", "9000 6.1", "a Version Negotiation packet is never answered", "MUST", func(c *net.UDPConn) (bool, string) {
			vn := longHeader(0x80, 0, rnd(8), rnd(8), 0)
			vn = binary.BigEndian.AppendUint32(vn, 0x0a1a2a3a)
			return silent(exchange(c, vn, 700*time.Millisecond))
		}},
		{"P06", "9000 10.3", "short header packet of 20 bytes is too small to be valid: dropped", "MUST", func(c *net.UDPConn) (bool, string) {
			return silent(exchange(c, append([]byte{0x40 | byte(rnd(1)[0]&0x3f)}, rnd(19)...), 700*time.Millisecond))
		}},
		{"P07", "9000 17.2", "a packet with the Fixed Bit clear is dropped (short header, 100 bytes)", "MUST", func(c *net.UDPConn) (bool, string) {
			return silent(exchange(c, append([]byte{byte(rnd(1)[0] & 0x3f)}, rnd(99)...), 700*time.Millisecond))
		}},
		{"P08", "9000 10.3", "an unattributable short header packet: any reply is a Stateless Reset, a short header datagram of at least 21 bytes and less than three times the trigger", "MUST", func(c *net.UDPConn) (bool, string) {
			var seen []string
			for _, size := range []int{21, 22, 43, 44, 100, 1200} {
				trig := append([]byte{0x40 | byte(rnd(1)[0]&0x3f)}, rnd(size-1)...)
				r := exchange(c, trig, 700*time.Millisecond)
				if len(r) == 0 {
					seen = append(seen, fmt.Sprintf("%d:none", size))
					continue
				}
				p := r[0]
				if len(p) < 21 || p[0]&0xc0 != 0x40 || len(p) >= 3*size {
					return false, fmt.Sprintf("trigger of %d bytes answered with %d bytes, first byte %#x", size, len(p), p[0])
				}
				seen = append(seen, fmt.Sprintf("%d:%d", size, len(p)))
			}
			return true, "trigger:reply sizes " + fmt.Sprint(seen)
		}},
		{"P09", "9000 10.3", "a Stateless Reset answering a trigger of 43 bytes or fewer is one byte shorter than it", "SHOULD", func(c *net.UDPConn) (bool, string) {
			for _, size := range []int{22, 30, 43} {
				trig := append([]byte{0x40 | byte(rnd(1)[0]&0x3f)}, rnd(size-1)...)
				r := exchange(c, trig, 700*time.Millisecond)
				if len(r) == 1 && len(r[0]) != size-1 {
					return false, fmt.Sprintf("trigger of %d bytes answered with %d bytes", size, len(r[0]))
				}
			}
			return true, ""
		}},
		{"P10", "9000 10.3.3", "Stateless Resets in reply to Stateless Resets do not loop: replying to the reply is not an answer of the same size or larger", "MUST", func(c *net.UDPConn) (bool, string) {
			trig := append([]byte{0x40 | byte(rnd(1)[0]&0x3f)}, rnd(99)...)
			r := exchange(c, trig, 700*time.Millisecond)
			if len(r) != 1 {
				return true, "no reset to loop on"
			}
			r2 := exchange(c, r[0], 700*time.Millisecond)
			for _, p := range r2 {
				if len(p) >= len(r[0]) {
					return false, fmt.Sprintf("the reset of %d bytes was answered with %d bytes", len(r[0]), len(p))
				}
			}
			return true, fmt.Sprintf("reset of %d bytes, answer: %d datagrams", len(r[0]), len(r2))
		}},
	}
}

func runPackets(addr string) int {
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	bad := 0
	for _, r := range packetRules() {
		c, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			fmt.Println(err)
			return 2
		}
		ok, detail := r.run(c)
		c.Close()
		status := "ok"
		if !ok {
			status = "DIFF(" + r.level + ")"
			bad++
		}
		fmt.Printf("%s  %-12s RFC %-18s %s -- %s\n", r.id, status, r.rfc, r.text, detail)
	}
	if bad > 0 {
		return 1
	}
	return 0
}
