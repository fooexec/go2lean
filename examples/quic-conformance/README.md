# QUIC rule table against a live connection

A harness that checks what a QUIC implementation does with frames RFC 9000
forbids, on a real connection. The attacker is a client (or a server) built
from a scratch copy of the implementation with one extra method that queues
raw frame bytes (`hook/*.go.txt`, kept as text so the root module does not build it; `run.sh` also adds one call, `AttackPreSeal`, to the attacker copy's packet packer so that header bits can be changed before encryption); the endpoint under test is an
unmodified checkout of the same revision, built separately
(`victim/main.go`). Each rule in `attacker/main.go` is one RFC sentence:
the frames to send and the connection error the receiver must close with.
The table is written from the RFC text, not from quic-go's code.

```bash
examples/quic-conformance/run.sh <quic-go checkout> [revision]      # attacker is a client
ROLE=server examples/quic-conformance/run.sh <quic-go checkout>     # attacker is a server
ONLY=R05c,R25 PORT=4500 examples/quic-conformance/run.sh <checkout> # a subset
FUZZ=400 SEED=3 examples/quic-conformance/run.sh <checkout>         # random frame sequences
PACKETS=1 examples/quic-conformance/run.sh <checkout>               # raw datagrams: version negotiation, stateless reset, dropped packets
HOOKRULES=1 examples/quic-conformance/run.sh <checkout>             # packet header bits changed before encryption
AMP=1 examples/quic-conformance/run.sh <checkout>                   # the 3x anti-amplification limit, handshake and new path
TPRULES=1 examples/quic-conformance/run.sh <checkout>               # transport parameters a client sends (add ROLE=server for the ones a server sends)
```

Output is one line per rule: `ok`, or `DIFF` with the RFC's strength
(`MAY`, `SHOULD`) where it is not a MUST, the expected and the received
error, and the receiver's message. The checkout needs Go 1.26 and git
worktrees; nothing is changed in it. Results for quic-go are in
[docs/eval/quic-go.md](../../docs/eval/quic-go.md).

`FUZZ=n` replaces the table by n connections, each sent a random sequence of
frames (half of them valid, half with edge values). The oracle: the receiver
never answers with INTERNAL_ERROR or a code RFC 9000 does not define, never
crashes, and its memory is printed before and after.

To add a rule, add an entry to `rules()` (client attacks server) or
`serverRules()` (server attacks client); build frames with the helpers
(`stream`, `ack`, `newCID`, ...) or raw bytes.
