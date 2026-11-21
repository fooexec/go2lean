#!/bin/bash
# usage: run.sh <quic-go checkout> [rev]
# Builds an unmodified server and an attacker (a copy with hook/attack_hook.go)
# from the same revision, runs the RFC 9000 rules of attacker/main.go against it.
set -e
QG=$(realpath "$1"); REV=${2:-HEAD}
HERE=$(cd "$(dirname "$0")" && pwd)
W=$HERE/.work
rm -rf "$W"; mkdir -p "$W"
git -C "$QG" worktree prune
for d in clean attack; do
  git -C "$QG" worktree add -q --detach "$W/$d" "$REV"
done
cp "$HERE/hook/attack_hook.go.txt" "$W/attack/zz_attack_hook.go"
# One call at the start of encryptPacket (packet_packer.go) runs AttackPreSeal.
sed -i '/^func (p \*packetPacker) encryptPacket(/a\	if AttackPreSeal != nil {\n\t\tAttackPreSeal(raw, int(payloadOffset), int(pnLen))\n\t}' "$W/attack/packet_packer.go"
# Transport parameters pass through attackTP before they go to TLS.
cp "$HERE/hook/attack_handshake.go.txt" "$W/attack/internal/handshake/zz_attack.go"
sed -i 's/h\.conn\.SetTransportParameters(h\.ourParams\.Marshal(h\.perspective))/h.conn.SetTransportParameters(attackTP(h.ourParams.Marshal(h.perspective)))/; s/cs\.conn\.SetTransportParameters(cs\.ourParams\.Marshal(protocol\.PerspectiveClient))/cs.conn.SetTransportParameters(attackTP(cs.ourParams.Marshal(protocol.PerspectiveClient)))/' "$W/attack/internal/handshake/crypto_setup.go"
grep -c attackTP "$W/attack/internal/handshake/crypto_setup.go" | grep -q 2 || { echo "could not patch crypto_setup.go"; exit 1; }
grep -q AttackPreSeal "$W/attack/packet_packer.go" || { echo "could not patch packet_packer.go"; exit 1; }
echo "revision $(git -C "$QG" rev-parse --short "$REV")"
(cd "$HERE/victim" && go mod tidy -e >/dev/null 2>&1; go build -o "$W/victim" .)
(cd "$HERE/attacker" && go mod tidy -e >/dev/null 2>&1; go build -o "$W/attacker" .)
PORT=${PORT:-4433}
if [ "${ROLE:-client}" = server ]; then
  "$W/attacker" -role server -addr 127.0.0.1:$PORT ${ONLY:+-only $ONLY} ${TPRULES:+-tprules} > "$W/attacker.log" 2>&1 &
  AP=$!
  sleep 1
  "$W/victim" -dial 127.0.0.1:$PORT > "$W/victim.log" 2>&1 &
  VP=$!
  wait $AP || true
  cat "$W/attacker.log"
  kill $VP 2>/dev/null
  git -C "$QG" worktree remove --force "$W/clean"; git -C "$QG" worktree remove --force "$W/attack"
  exit 0
fi
"$W/victim" -addr 127.0.0.1:$PORT > "$W/victim.log" 2>&1 &
VP=$!
trap 'kill $VP 2>/dev/null; git -C "$QG" worktree remove --force "$W/clean"; git -C "$QG" worktree remove --force "$W/attack"' EXIT
sleep 1
RSS0=$(ps -o rss= -p $VP | tr -d ' ')
"$W/attacker" -addr 127.0.0.1:$PORT ${ONLY:+-only $ONLY} ${FUZZ:+-fuzz $FUZZ -seed ${SEED:-1}} ${PACKETS:+-packets} ${HOOKRULES:+-hookrules} ${AMP:+-amp} ${TPRULES:+-tprules} || true
echo "server memory: ${RSS0} KiB before, $(ps -o rss= -p $VP 2>/dev/null | tr -d ' ') KiB after"
if ! kill -0 $VP 2>/dev/null; then echo "THE SERVER DIED:"; tail -n 30 "$W/victim.log"; fi
