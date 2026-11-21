// Command attacker is a QUIC client built against a quic-go checkout with
// one extra method (hook/attack_hook.go) that sends raw frame bytes. It
// completes a handshake with the server under test, sends the frames of one
// RFC 9000 rule, and reports how the server reacted. The table in rules()
// is written from the RFC text, independent of quic-go's code.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"flag"
	"fmt"
	"math/big"
	mrand "math/rand"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/quicvarint"
)

// RFC 9000 transport error codes.
const (
	alive              = -1
	noError            = 0x0
	flowControlError   = 0x3
	streamLimitError   = 0x4
	streamStateError   = 0x5
	finalSizeError     = 0x6
	frameEncodingError = 0x7
	protocolViolation  = 0xa
	connIDLimitError   = 0x9
)

var names = map[int]string{alive: "alive", 0: "NO_ERROR", 1: "INTERNAL_ERROR", 2: "CONNECTION_REFUSED", 3: "FLOW_CONTROL_ERROR", 4: "STREAM_LIMIT_ERROR", 5: "STREAM_STATE_ERROR", 6: "FINAL_SIZE_ERROR", 7: "FRAME_ENCODING_ERROR", 8: "TRANSPORT_PARAMETER_ERROR", 9: "CONNECTION_ID_LIMIT_ERROR", 10: "PROTOCOL_VIOLATION", 11: "INVALID_TOKEN"}

func name(c int) string {
	if n, ok := names[c]; ok {
		return n
	}
	return fmt.Sprintf("%#x", c)
}

func vi(vs ...uint64) []byte {
	var b []byte
	for _, v := range vs {
		b = quicvarint.Append(b, v)
	}
	return b
}

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func pad(n int, c byte) []byte { return []byte(strings.Repeat(string([]byte{c}), n)) }

// Frame builders (RFC 9000 section 19).
func retireCID(seq uint64) []byte { return cat(vi(0x19), vi(seq)) }
func newCID(seq, retirePrior uint64, cid []byte) []byte {
	return cat(vi(0x18, seq, retirePrior, uint64(len(cid))), cid, pad(16, 0xaa))
}
func maxStreams(bidi bool, n uint64) []byte {
	if bidi {
		return vi(0x12, n)
	}
	return vi(0x13, n)
}
func streamsBlocked(bidi bool, n uint64) []byte {
	if bidi {
		return vi(0x16, n)
	}
	return vi(0x17, n)
}
func resetStream(id, code, final uint64) []byte { return vi(0x04, id, code, final) }
func stopSending(id, code uint64) []byte        { return vi(0x05, id, code) }
func maxStreamData(id, n uint64) []byte         { return vi(0x11, id, n) }
func maxData(n uint64) []byte                   { return vi(0x10, n) }
func stream(id, off uint64, data []byte, fin bool) []byte {
	t := uint64(0x08 | 0x04 | 0x02)
	if fin {
		t |= 0x01
	}
	return cat(vi(t, id, off, uint64(len(data))), data)
}
func ack(largest, delay, ranges, first uint64) []byte { return vi(0x02, largest, delay, ranges, first) }
func crypto(off uint64, data []byte) []byte {
	return cat(vi(0x06, off, uint64(len(data))), data)
}

type rule struct {
	id     string
	rfc    string
	text   string
	expect int    // transport error code, or alive
	note   string // known and accepted deviation
	run    func(c *quic.Conn, send func(...[]byte))
}

const maxVarint = 1<<62 - 1

func rules() []rule {
	return []rule{
		{"R01", "9000 19.16", "RETIRE_CONNECTION_ID for a sequence number never issued", protocolViolation, "", func(c *quic.Conn, s func(...[]byte)) { s(retireCID(100)) }},
		{"R02", "9000 19.15", "NEW_CONNECTION_ID with Retire Prior To greater than Sequence Number", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(newCID(1, 2, pad(8, 1))) }},
		{"R03", "9000 19.15", "NEW_CONNECTION_ID with a zero-length connection ID", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(newCID(1, 0, nil)) }},
		{"R04", "9000 19.15", "NEW_CONNECTION_ID with a 21-byte connection ID", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(newCID(1, 0, pad(21, 1))) }},
		{"R05", "9000 19.15", "NEW_CONNECTION_ID repeating a connection ID with a different sequence number", protocolViolation, "", func(c *quic.Conn, s func(...[]byte)) {
			s(newCID(50, 50, pad(8, 1)))
			time.Sleep(50 * time.Millisecond)
			s(newCID(51, 50, pad(8, 1)))
		}},
		{"R05b", "9000 19.15", "NEW_CONNECTION_ID using one sequence number for two different connection IDs", protocolViolation, "", func(c *quic.Conn, s func(...[]byte)) {
			s(newCID(50, 50, pad(8, 1)))
			time.Sleep(50 * time.Millisecond)
			s(newCID(50, 50, pad(8, 2)))
		}},
		{"R05c", "9000 19.15", "NEW_CONNECTION_ID using a sequence number the endpoint already holds (issued by this client's own handshake) for a different connection ID", protocolViolation, "", func(c *quic.Conn, s func(...[]byte)) { s(newCID(2, 0, pad(8, 3))) }},
		{"R06", "9000 5.1.1", "more active connection IDs than the advertised limit", connIDLimitError, "", func(c *quic.Conn, s func(...[]byte)) {
			for i := uint64(50); i < 62; i++ {
				s(newCID(i, 0, []byte{byte(i), 9, 9, 9, 9, 9, 9, 9}))
			}
		}},
		{"R07", "9000 19.11", "MAX_STREAMS above 2^60", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(maxStreams(true, 1<<60+1)) }},
		{"R08", "9000 19.14", "STREAMS_BLOCKED above 2^60", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(streamsBlocked(true, 1<<60+1)) }},
		{"R09", "9000 19.4", "RESET_STREAM for a stream the receiver can only send on (server-initiated unidirectional stream 3)", streamStateError, "", func(c *quic.Conn, s func(...[]byte)) { s(resetStream(3, 0, 0)) }},
		{"R10", "9000 19.5", "STOP_SENDING for a stream the receiver can only receive on (client-initiated unidirectional stream 2)", streamStateError, "", func(c *quic.Conn, s func(...[]byte)) { s(stopSending(2, 0)) }},
		{"R11", "9000 19.10", "MAX_STREAM_DATA for a stream the receiver can only receive on (client-initiated unidirectional stream 2)", streamStateError, "", func(c *quic.Conn, s func(...[]byte)) { s(maxStreamData(2, 1000)) }},
		{"R12", "9000 19.10", "MAX_STREAM_DATA for a locally initiated stream that does not exist yet (server-initiated bidirectional stream 5)", streamStateError, "", func(c *quic.Conn, s func(...[]byte)) { s(maxStreamData(5, 1000)) }},
		{"R13", "9000 19.5", "STOP_SENDING for a locally initiated stream that does not exist yet (server-initiated bidirectional stream 5)", streamStateError, "", func(c *quic.Conn, s func(...[]byte)) { s(stopSending(5, 0)) }},
		{"R14", "9000 4.6", "STREAM frame for a stream beyond the advertised stream limit", streamLimitError, "", func(c *quic.Conn, s func(...[]byte)) { s(stream(4*1000, 0, []byte("x"), false)) }},
		{"R15", "9000 19.8", "STREAM frame whose offset plus length exceeds 2^62-1", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(stream(0, maxVarint, []byte("x"), false)) }},
		{"R16", "9000 4.5", "STREAM data beyond the final size set by an earlier FIN", finalSizeError, "", func(c *quic.Conn, s func(...[]byte)) {
			s(stream(0, 0, []byte("hello"), true))
			time.Sleep(50 * time.Millisecond)
			s(stream(0, 10, []byte("x"), false))
		}},
		{"R17", "9000 4.5", "RESET_STREAM with a final size smaller than the data already received", finalSizeError, "", func(c *quic.Conn, s func(...[]byte)) {
			s(stream(0, 0, pad(10, 'a'), false))
			time.Sleep(50 * time.Millisecond)
			s(resetStream(0, 0, 5))
		}},
		{"R18", "9000 4.5", "RESET_STREAM whose final size differs from the one set by a FIN", finalSizeError, "", func(c *quic.Conn, s func(...[]byte)) {
			s(stream(0, 0, []byte("hello"), true))
			time.Sleep(50 * time.Millisecond)
			s(resetStream(0, 0, 6))
		}},
		{"R19", "9000 4.1", "STREAM data far beyond the stream's flow-control limit", flowControlError, "", func(c *quic.Conn, s func(...[]byte)) { s(stream(0, 1<<40, []byte("x"), false)) }},
		{"R20", "9000 4.1", "STREAM data on two streams that together exceed the connection's flow-control limit", flowControlError, "", func(c *quic.Conn, s func(...[]byte)) {
			s(stream(0, 500000, []byte("x"), false))
			s(stream(4, 500000, []byte("x"), false))
		}},
		{"R21", "9000 13.1", "ACK of a packet number never sent", protocolViolation, "SHOULD", func(c *quic.Conn, s func(...[]byte)) { s(ack(1000000, 0, 0, 0)) }},
		{"R22", "9000 19.3.1", "ACK whose first range is larger than the largest acknowledged", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(ack(5, 0, 0, 6)) }},
		{"R23", "9000 19.7", "NEW_TOKEN sent by a client", protocolViolation, "", func(c *quic.Conn, s func(...[]byte)) { s(cat(vi(0x07, 4), []byte("tokn"))) }},
		{"R24", "9000 19.20", "HANDSHAKE_DONE sent by a client", protocolViolation, "", func(c *quic.Conn, s func(...[]byte)) { s(vi(0x1e)) }},
		{"R25", "9000 19.6", "CRYPTO frame whose offset plus length exceeds 2^62-1", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(crypto(maxVarint, []byte("x"))) }},
		{"R26", "9000 12.4", "a frame type that is not defined", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(vi(0x21)) }},
		{"R27", "9000 12.4", "a frame type with a longer encoding than necessary", protocolViolation, "", func(c *quic.Conn, s func(...[]byte)) { s([]byte{0x40, 0x01}) }},
		{"R28", "9221 4", "DATAGRAM frame when the extension was not negotiated", protocolViolation, "quic-go treats it as an unknown frame type (FRAME_ENCODING_ERROR); commented in frame_parser.go", func(c *quic.Conn, s func(...[]byte)) { s(cat(vi(0x31), []byte("x"))) }},
		{"R29", "9000 19", "PING, MAX_DATA decreasing, DATA_BLOCKED, MAX_STREAMS at 2^60, STREAMS_BLOCKED, padding: all valid", alive, "", func(c *quic.Conn, s func(...[]byte)) {
			s(vi(0x01), maxData(1), vi(0x14, 0), maxStreams(true, 1<<60), streamsBlocked(true, 1<<60), []byte{0, 0, 0})
		}},
		{"R30", "9000 4.1", "STREAM frame with the maximum offset and no data exceeds the stream's flow-control limit", flowControlError, "", func(c *quic.Conn, s func(...[]byte)) { s(stream(0, maxVarint, nil, false)) }},
		{"R32", "9000 19.8", "STREAM frame for a stream the receiver can only send on (server-initiated unidirectional stream 3)", streamStateError, "", func(c *quic.Conn, s func(...[]byte)) { s(stream(3, 0, []byte("x"), false)) }},
		{"R33", "9000 19.13", "STREAM_DATA_BLOCKED for a stream the receiver can only send on (server-initiated unidirectional stream 3)", streamStateError, "", func(c *quic.Conn, s func(...[]byte)) { s(vi(0x15, 3, 100)) }},
		{"R34", "9000 4.6", "RESET_STREAM for a stream beyond the advertised stream limit", streamLimitError, "", func(c *quic.Conn, s func(...[]byte)) { s(resetStream(4*1000, 0, 0)) }},
		{"R35", "9000 4.6", "STOP_SENDING for a stream beyond the advertised stream limit", streamLimitError, "", func(c *quic.Conn, s func(...[]byte)) { s(stopSending(4*1000, 0)) }},
		{"R36", "9000 4.6", "MAX_STREAM_DATA for a stream beyond the advertised stream limit", streamLimitError, "", func(c *quic.Conn, s func(...[]byte)) { s(maxStreamData(4*1000, 100)) }},
		{"R37", "9000 4.6", "STREAM_DATA_BLOCKED for a stream beyond the advertised stream limit", streamLimitError, "", func(c *quic.Conn, s func(...[]byte)) { s(vi(0x15, 4*1000, 100)) }},
		{"R38", "9000 19.3.1", "ACK with more ranges than the frame holds", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(ack(5, 0, 1000, 0)) }},
		{"R39", "9000 19.3.1", "ACK whose gap and range would go below packet number 0", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(cat(vi(0x02, 5, 0, 1, 0), vi(10, 0))) }},
		{"R40", "9000 19.8", "STREAM frame whose Length runs past the end of the packet", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(cat(vi(0x0a, 0, 1000), []byte("x"))) }},
		{"R42", "9000 19.17", "PATH_CHALLENGE is answered and the connection stays open", alive, "", func(c *quic.Conn, s func(...[]byte)) { s(cat(vi(0x1a), pad(8, 7))) }},
		{"R43", "9000 19.9", "MAX_STREAM_DATA for a client-initiated stream the server has not seen is valid (it opens the stream)", alive, "", func(c *quic.Conn, s func(...[]byte)) { s(maxStreamData(8, 1000)) }},
		{"R44", "9000 4.5", "RESET_STREAM for a new stream with no data is valid", alive, "", func(c *quic.Conn, s func(...[]byte)) { s(resetStream(8, 0, 0)) }},
		{"R45", "9000 19.6", "CRYPTO frame in a 1-RTT packet carrying bytes that are not a TLS message", 0x100, "CRYPTO_ERROR is 0x100 plus a TLS alert; any code in that range is accepted", func(c *quic.Conn, s func(...[]byte)) { s(crypto(0, []byte("notatlsmessage"))) }},
	}
}

func dial(addr string) (*quic.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Non-zero connection IDs, so that the server may be sent NEW_CONNECTION_ID.
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	tr := &quic.Transport{Conn: pc, ConnectionIDLength: 8}
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	c, err := tr.Dial(ctx, raddr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"conf"}}, &quic.Config{MaxIdleTimeout: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	// Make streams exist: client bidi 0 and uni 2 at the server, server uni 3 and bidi 1 at the client.
	bs, err := c.OpenStream()
	if err != nil {
		return nil, err
	}
	bs.Write([]byte("x"))
	us, err := c.OpenUniStream()
	if err != nil {
		return nil, err
	}
	us.Write([]byte("x"))
	bs2, err := c.OpenStream() // client bidi 4
	if err != nil {
		return nil, err
	}
	bs2.Write([]byte("x"))
	rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer rcancel()
	if _, err := c.AcceptUniStream(rctx); err != nil {
		return nil, err
	}
	if _, err := c.AcceptStream(rctx); err != nil {
		return nil, err
	}
	time.Sleep(100 * time.Millisecond)
	return c, nil
}

func outcome(c *quic.Conn) (code int, detail string) {
	select {
	case <-c.Context().Done():
	case <-time.After(1500 * time.Millisecond):
		return alive, ""
	}
	err := context.Cause(c.Context())
	var te *quic.TransportError
	if errors.As(err, &te) {
		return int(te.ErrorCode), fmt.Sprintf("remote=%v frame=%#x %q", te.Remote, te.FrameType, te.ErrorMessage)
	}
	var ae *quic.ApplicationError
	if errors.As(err, &ae) {
		return 1000 + int(ae.ErrorCode), "application error"
	}
	return -2, err.Error()
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4433", "server address")
	only := flag.String("only", "", "comma-separated rule ids (default: all)")
	fuzzN := flag.Int("fuzz", 0, "instead of the rule table: this many connections, each with a random sequence of frames; reports INTERNAL_ERROR and unknown error codes")
	seed := flag.Int64("seed", 1, "seed of -fuzz")
	role := flag.String("role", "client", "client: attack a server at -addr; server: listen on -addr and attack the clients that connect")
	flag.Parse()
	want := map[string]bool{}
	for _, id := range strings.Split(*only, ",") {
		if id != "" {
			want[id] = true
		}
	}
	if *fuzzN > 0 {
		os.Exit(fuzz(*addr, *fuzzN, *seed))
	}
	rs := rules()
	var ln *quic.Listener
	if *role == "server" {
		rs = serverRules()
		var err error
		ln, err = quic.ListenAddr(*addr, &tls.Config{Certificates: []tls.Certificate{selfSigned()}, NextProtos: []string{"conf"}}, &quic.Config{MaxIdleTimeout: 10 * time.Second})
		if err != nil {
			fmt.Println("listen:", err)
			os.Exit(2)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].id < rs[j].id })
	levels := map[string]string{"R05": "MAY", "R05b": "MAY", "R05c": "MAY", "R21": "SHOULD", "R28": "MUST (RFC 9221)", "S08": "SHOULD"}
	bad := 0
	for _, r := range rs {
		if len(want) > 0 && !want[r.id] {
			continue
		}
		var c *quic.Conn
		var err error
		if ln != nil {
			c, err = accept(ln)
		} else {
			c, err = dial(*addr)
		}
		if err != nil {
			fmt.Printf("%s  ERROR  dial: %v\n", r.id, err)
			bad++
			continue
		}
		r.run(c, func(frames ...[]byte) {
			for _, f := range frames {
				c.AttackSendRaw(f)
			}
		})
		got, detail := outcome(c)
		status := "ok"
		if !matches(r.expect, got) {
			status = "DIFF"
			bad++
			if l := levels[r.id]; l != "" {
				status = "DIFF(" + l + ")"
			}
		}
		fmt.Printf("%s  %-12s  expected %-26s got %-26s RFC %-11s %s %s\n", r.id, status, name(r.expect), name(got), r.rfc, r.text, detail)
		if r.note != "" && status == "DIFF" {
			fmt.Printf("      note: %s\n", r.note)
		}
		c.CloseWithError(0, "")
	}
	if bad > 0 {
		os.Exit(1)
	}
}

// matches compares a transport error code with the expected one; the TLS
// alert range (0x100 to 0x1ff) matches any code in it.
func matches(want, got int) bool {
	if want == 0x100 {
		return got >= 0x100 && got <= 0x1ff
	}
	return want == got
}

func selfSigned() tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), DNSNames: []string{"localhost"}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// accept takes the next client; the streams it opens exist at this end afterwards.
func accept(ln *quic.Listener) (*quic.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := ln.Accept(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.AcceptStream(ctx); err != nil {
		return nil, err
	}
	if _, err := c.AcceptUniStream(ctx); err != nil {
		return nil, err
	}
	// our own streams towards the client: server bidi 1 and uni 3
	bs, err := c.OpenStream()
	if err != nil {
		return nil, err
	}
	bs.Write([]byte("x"))
	us, err := c.OpenUniStream()
	if err != nil {
		return nil, err
	}
	us.Write([]byte("x"))
	time.Sleep(200 * time.Millisecond)
	return c, nil
}

// serverRules: the rules a client must apply to what a server sends.
func serverRules() []rule {
	return []rule{
		{"S01", "9000 19.7", "NEW_TOKEN with an empty token", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(vi(0x07, 0)) }},
		{"S02", "9000 19.7", "NEW_TOKEN with a token is valid", alive, "", func(c *quic.Conn, s func(...[]byte)) { s(cat(vi(0x07, 4), []byte("tokn"))) }},
		{"S03", "9000 19.20", "HANDSHAKE_DONE after the handshake is confirmed is valid", alive, "", func(c *quic.Conn, s func(...[]byte)) { s(vi(0x1e)) }},
		{"S04", "9000 19.16", "RETIRE_CONNECTION_ID for a sequence number never issued", protocolViolation, "", func(c *quic.Conn, s func(...[]byte)) { s(retireCID(100)) }},
		{"S05", "9000 19.8", "STREAM frame for a stream the receiver can only send on (client-initiated unidirectional stream 2)", streamStateError, "", func(c *quic.Conn, s func(...[]byte)) { s(stream(2, 0, []byte("x"), false)) }},
		{"S06", "9000 4.6", "STREAM frame for a client-initiated stream beyond the limit", streamLimitError, "", func(c *quic.Conn, s func(...[]byte)) { s(stream(4*1000+1, 0, []byte("x"), false)) }},
		{"S07", "9000 19.15", "NEW_CONNECTION_ID with a zero-length connection ID", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(newCID(60, 0, nil)) }},
		{"S08", "9000 13.1", "ACK of a packet number never sent", protocolViolation, "SHOULD", func(c *quic.Conn, s func(...[]byte)) { s(ack(1000000, 0, 0, 0)) }},
		{"S09", "9000 19.4", "RESET_STREAM for a stream the receiver can only send on (client-initiated unidirectional stream 2)", streamStateError, "", func(c *quic.Conn, s func(...[]byte)) { s(resetStream(2, 0, 0)) }},
		{"S10", "9000 12.4", "a frame type that is not defined", frameEncodingError, "", func(c *quic.Conn, s func(...[]byte)) { s(vi(0x21)) }},
	}
}

var edge = []uint64{0, 1, 2, 3, 4, 5, 7, 8, 9, 16, 100, 255, 256, 1000, 4000, 1 << 14, 1 << 20, 1 << 30, 1 << 40, 1<<60 - 1, 1 << 60, 1<<60 + 1, maxVarint - 1, maxVarint}

// fuzz sends a random sequence of frames on each of n connections and reports
// the ones the receiver answers with INTERNAL_ERROR or a code RFC 9000 does
// not define: input from a peer is never an internal fault.
func fuzz(addr string, n int, seed int64) int {
	rng := mrand.New(mrand.NewSource(seed))
	pick := func() uint64 { return edge[rng.Intn(len(edge))] }
	sid := func() uint64 {
		if rng.Intn(3) == 0 {
			return pick()
		}
		return uint64(rng.Intn(14)) // streams around the ones that exist
	}
	cidb := func() []byte {
		l := []int{0, 1, 4, 8, 20, 21}[rng.Intn(6)]
		b := make([]byte, l)
		rng.Read(b)
		return b
	}
	nextSeq := uint64(100)
	safe := func() []byte {
		switch rng.Intn(9) {
		case 0:
			nextSeq++
			return newCID(nextSeq, 0, []byte{byte(nextSeq), byte(rng.Intn(256)), 1, 2, 3, 4, 5, 6})
		case 1:
			return retireCID(uint64(rng.Intn(3)))
		case 2:
			return stream(uint64(4*rng.Intn(3)), uint64(rng.Intn(200)), []byte("some stream data"), rng.Intn(8) == 0)
		case 3:
			return maxData(uint64(1<<20 + rng.Intn(1<<20)))
		case 4:
			return maxStreamData(uint64(4*rng.Intn(3)), uint64(1<<20+rng.Intn(1<<20)))
		case 5:
			return cat(vi(0x1a), pad(8, byte(rng.Intn(256))))
		case 6:
			return stopSending(uint64(4*rng.Intn(3)), uint64(rng.Intn(5)))
		case 7:
			return resetStream(uint64(4*rng.Intn(3)+4), 0, uint64(rng.Intn(10)))
		default:
			return maxStreams(rng.Intn(2) == 0, uint64(100+rng.Intn(100)))
		}
	}
	gen := func() []byte {
		if rng.Intn(2) == 0 {
			return safe()
		}
		switch rng.Intn(24) {
		case 0:
			return retireCID(pick())
		case 1:
			return newCID(pick(), pick(), cidb())
		case 2:
			return maxStreams(rng.Intn(2) == 0, pick())
		case 3:
			return streamsBlocked(rng.Intn(2) == 0, pick())
		case 4:
			return resetStream(sid(), pick(), pick())
		case 5:
			return stopSending(sid(), pick())
		case 6:
			return maxStreamData(sid(), pick())
		case 7:
			return maxData(pick())
		case 8:
			d := make([]byte, rng.Intn(40))
			rng.Read(d)
			return stream(sid(), pick(), d, rng.Intn(3) == 0)
		case 9:
			return ack(pick(), pick(), pick()%8, pick())
		case 10:
			return crypto(pick(), []byte("x"))
		case 11:
			return vi(0x01)
		case 12:
			return vi(0x14, pick())
		case 13:
			return vi(0x15, sid(), pick())
		case 14:
			return cat(vi(0x1a), pad(8, byte(rng.Intn(256))))
		case 15:
			return cat(vi(0x1b), pad(8, byte(rng.Intn(256))))
		case 16:
			return cat(vi(0x07, 3), []byte("tok"))
		case 17:
			return vi(0x1e)
		case 18:
			return cat(vi(0x1c, pick(), pick(), 0))
		case 19:
			return vi(pick() % 0x40)
		case 20:
			return cat(vi(0x30), []byte("dg"))
		case 21:
			b := make([]byte, 1+rng.Intn(12))
			rng.Read(b)
			return b
		case 22:
			return cat(vi(0x2, pick(), pick(), 1, pick()), vi(pick(), pick()))
		default:
			return stream(sid(), pick()%64, []byte("data"), false)
		}
	}
	bad := 0
	hist := map[string]int{}
	for i := 0; i < n; i++ {
		c, err := dial(addr)
		if err != nil {
			fmt.Printf("fuzz %d: dial failed: %v (is the server still running?)\n", i, err)
			return 1
		}
		var frames [][]byte
		for k, m := 0, 1+rng.Intn(10); k < m; k++ {
			f := gen()
			frames = append(frames, f)
			c.AttackSendRaw(f)
			time.Sleep(time.Duration(rng.Intn(15)) * time.Millisecond)
		}
		code, detail := outcome(c)
		hist[name(code)]++
		known := code == alive || (code >= 0 && code <= 0x11) || (code >= 0x100 && code <= 0x1ff) || code >= 1000
		if code == 1 || !known {
			bad++
			hex := ""
			for _, f := range frames {
				hex += fmt.Sprintf(" %x", f)
			}
			fmt.Printf("fuzz %d: %s %s; frames:%s\n", i, name(code), detail, hex)
		}
		c.CloseWithError(0, "")
	}
	fmt.Printf("fuzz: %d connections, %d reported; outcomes %v\n", n, bad, hist)
	if bad > 0 {
		return 1
	}
	return 0
}
