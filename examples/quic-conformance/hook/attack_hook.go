package quic

import (
	"github.com/quic-go/quic-go/internal/protocol"
)

// This file is copied into a scratch checkout of quic-go by run.sh. It gives
// the attacking endpoint a way to put arbitrary bytes into the next 1-RTT
// packet as one frame. The endpoint under test is an unmodified checkout.

type attackRaw []byte

func (r attackRaw) Append(b []byte, _ protocol.Version) ([]byte, error) { return append(b, r...), nil }
func (r attackRaw) Length(protocol.Version) protocol.ByteCount          { return protocol.ByteCount(len(r)) }

// AttackSendRaw queues b as one frame, bytes exactly as given.
func (c *Conn) AttackSendRaw(b []byte) {
	c.framer.QueueControlFrame(attackRaw(append([]byte(nil), b...)))
	c.scheduleSending()
}
