package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/quicvarint"
)

// RFC 9000 sections 7.4 and 18.2: how a server treats the transport
// parameters of a client. The attacker rewrites its encoded parameters.

const (
	transportParameterError = 0x8
	tpOrDefault             = -3 // TRANSPORT_PARAMETER_ERROR or PROTOCOL_VIOLATION
)

type tpRule struct {
	id, rfc, text string
	expect        int
	edit          func(params []tpParam) []tpParam
}

type tpParam struct {
	id    uint64
	value []byte
}

func parseTPs(b []byte) []tpParam {
	var out []tpParam
	for len(b) > 0 {
		id, n, err := quicvarint.Parse(b)
		if err != nil {
			break
		}
		b = b[n:]
		l, n, err := quicvarint.Parse(b)
		if err != nil || int(l) > len(b)-n {
			break
		}
		out = append(out, tpParam{id, append([]byte(nil), b[n:n+int(l)]...)})
		b = b[n+int(l):]
	}
	return out
}

func encodeTPs(ps []tpParam) []byte {
	var b []byte
	for _, p := range ps {
		b = quicvarint.Append(b, p.id)
		b = quicvarint.Append(b, uint64(len(p.value)))
		b = append(b, p.value...)
	}
	return b
}

func without(ps []tpParam, id uint64) []tpParam {
	var out []tpParam
	for _, p := range ps {
		if p.id != id {
			out = append(out, p)
		}
	}
	return out
}

func with(ps []tpParam, id uint64, v []byte) []tpParam {
	return append(without(ps, id), tpParam{id, v})
}

func tpRuleList() []tpRule {
	return []tpRule{
		{"T01", "9000 7.4", "a transport parameter sent twice", transportParameterError, func(ps []tpParam) []tpParam { return append(ps, tpParam{0x04, vi(1000)}, tpParam{0x04, vi(2000)}) }},
		{"T02", "9000 18.2", "stateless_reset_token sent by a client", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x02, pad(16, 1)) }},
		{"T03", "9000 18.2", "original_destination_connection_id sent by a client", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x00, pad(8, 1)) }},
		{"T04", "9000 18.2", "preferred_address sent by a client", transportParameterError, func(ps []tpParam) []tpParam {
			return with(ps, 0x0d, cat(pad(4, 1), []byte{0x1f, 0x90}, pad(16, 0), []byte{0x1f, 0x90, 4}, pad(4, 2), pad(16, 3)))
		}},
		{"T05", "9000 18.2", "retry_source_connection_id sent by a client", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x10, pad(8, 1)) }},
		{"T06", "9000 18.2", "max_udp_payload_size below 1200", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x03, vi(1199)) }},
		{"T07", "9000 18.2", "ack_delay_exponent above 20", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x0a, vi(21)) }},
		{"T08", "9000 18.2", "max_ack_delay of 2^14 or more", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x0b, vi(1<<14)) }},
		{"T09", "9000 18.2", "active_connection_id_limit below 2", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x0e, vi(1)) }},
		{"T10", "9000 4.6", "initial_max_streams_bidi above 2^60", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x08, vi(1<<60+1)) }},
		{"T11", "9000 7.3", "initial_source_connection_id missing", tpOrDefault, func(ps []tpParam) []tpParam { return without(ps, 0x0f) }},
		{"T12", "9000 7.3", "initial_source_connection_id not the Source Connection ID of the Initial", tpOrDefault, func(ps []tpParam) []tpParam { return with(ps, 0x0f, pad(8, 9)) }},
		{"T13", "9000 18.2", "disable_active_migration with a value", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x0c, []byte{1}) }},
		{"T14", "9000 7.4", "a transport parameter block whose last length runs past the end", transportParameterError, func(ps []tpParam) []tpParam {
			return append(ps, tpParam{0x20, nil}, tpParam{0x21, nil}) // replaced below by a truncated tail
		}},
		{"T15", "9000 7.4.2", "an unknown transport parameter is ignored", alive, func(ps []tpParam) []tpParam { return with(ps, 0x7f01, []byte("whatever")) }},
		{"T16", "9000 18.2", "max_ack_delay at its limit, 2^14-1, is valid", alive, func(ps []tpParam) []tpParam { return with(ps, 0x0b, vi(1<<14-1)) }},
		{"T17", "9000 18.2", "active_connection_id_limit of 2 is valid", alive, func(ps []tpParam) []tpParam { return with(ps, 0x0e, vi(2)) }},
	}
}

func runTPRules(addr string) int {
	raddr, _ := net.ResolveUDPAddr("udp", addr)
	bad := 0
	for _, r := range tpRuleList() {
		quic.AttackSetTransportParams(func(b []byte) []byte {
			out := encodeTPs(r.edit(parseTPs(b)))
			if r.id == "T14" {
				out = append(b, 0x20, 0x08, 0x01) // claims 8 bytes, has 1
			}
			return out
		})
		pc, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		tr := &quic.Transport{Conn: pc, ConnectionIDLength: 8}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		c, err := tr.Dial(ctx, raddr, tlsConf(), &quic.Config{HandshakeIdleTimeout: 3 * time.Second})
		cancel()
		quic.AttackSetTransportParams(nil)
		var got int
		var detail string
		var te *quic.TransportError
		switch {
		case err == nil:
			time.Sleep(300 * time.Millisecond) // the server may close right after its handshake completes
			got, detail = outcome(c)
			c.CloseWithError(0, "")
		case errors.As(err, &te):
			got, detail = int(te.ErrorCode), fmt.Sprintf("remote=%v %q", te.Remote, te.ErrorMessage)
		default:
			got, detail = -2, err.Error()
		}
		ok := matches(r.expect, got)
		if r.expect == tpOrDefault {
			ok = got == transportParameterError || got == protocolViolation
		}
		status := "ok"
		if !ok {
			status = "DIFF"
			bad++
		}
		exp := name(r.expect)
		if r.expect == tpOrDefault {
			exp = "TPE or PV"
		}
		fmt.Printf("%s  %-5s expected %-22s got %-22s RFC %-9s %s %s\n", r.id, status, exp, name(got), r.rfc, r.text, detail)
		tr.Close()
	}
	if bad > 0 {
		return 1
	}
	return 0
}

func serverTPRuleList() []tpRule {
	return []tpRule{
		{"U01", "9000 7.3", "original_destination_connection_id missing", tpOrDefault, func(ps []tpParam) []tpParam { return without(ps, 0x00) }},
		{"U02", "9000 7.3", "original_destination_connection_id not the Destination Connection ID of the first Initial", tpOrDefault, func(ps []tpParam) []tpParam { return with(ps, 0x00, pad(8, 9)) }},
		{"U03", "9000 7.3", "initial_source_connection_id missing", tpOrDefault, func(ps []tpParam) []tpParam { return without(ps, 0x0f) }},
		{"U04", "9000 7.3", "retry_source_connection_id present although no Retry was sent", tpOrDefault, func(ps []tpParam) []tpParam { return with(ps, 0x10, pad(8, 1)) }},
		{"U05", "9000 18.2", "stateless_reset_token of 15 bytes", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x02, pad(15, 1)) }},
		{"U06", "9000 18.2", "max_udp_payload_size below 1200", transportParameterError, func(ps []tpParam) []tpParam { return with(ps, 0x03, vi(1199)) }},
		{"U07", "9000 18.2", "preferred_address with a zero-length connection ID", transportParameterError, func(ps []tpParam) []tpParam {
			return with(ps, 0x0d, cat(pad(4, 1), []byte{0x1f, 0x90}, pad(16, 0), []byte{0x1f, 0x90, 0}, pad(16, 3)))
		}},
		{"U08", "9000 7.4.2", "an unknown transport parameter is ignored", alive, func(ps []tpParam) []tpParam { return with(ps, 0x7f01, []byte("whatever")) }},
	}
}

// runServerTPRules: the attacker is a server whose transport parameters are
// rewritten; the client under test reacts.
func runServerTPRules(addr string) int {
	ln, err := quic.ListenAddrEarly(addr, serverTLS(), &quic.Config{MaxIdleTimeout: 10 * time.Second})
	if err != nil {
		fmt.Println("listen:", err)
		return 2
	}
	bad := 0
	for _, r := range serverTPRuleList() {
		quic.AttackSetTransportParams(func(b []byte) []byte { return encodeTPs(r.edit(parseTPs(b))) })
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		c, err := ln.Accept(ctx)
		cancel()
		quic.AttackSetTransportParams(nil)
		if err != nil {
			fmt.Printf("%s  ERROR accept: %v\n", r.id, err)
			bad++
			continue
		}
		got, detail := outcome(c)
		ok := matches(r.expect, got)
		exp := name(r.expect)
		if r.expect == tpOrDefault {
			ok = got == transportParameterError || got == protocolViolation
			exp = "TPE or PV"
		}
		status := "ok"
		if !ok {
			status = "DIFF"
			bad++
		}
		fmt.Printf("%s  %-5s expected %-22s got %-22s RFC %-9s %s %s\n", r.id, status, exp, name(got), r.rfc, r.text, detail)
		c.CloseWithError(0, "")
	}
	if bad > 0 {
		return 1
	}
	return 0
}

func serverTLS() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{selfSigned()}, NextProtos: []string{"conf"}}
}
