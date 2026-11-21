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
cp "$HERE/hook/attack_hook.go" "$W/attack/zz_attack_hook.go"
echo "revision $(git -C "$QG" rev-parse --short "$REV")"
(cd "$HERE/victim" && go mod tidy -e >/dev/null 2>&1; go build -o "$W/victim" .)
(cd "$HERE/attacker" && go mod tidy -e >/dev/null 2>&1; go build -o "$W/attacker" .)
PORT=${PORT:-4433}
if [ "${ROLE:-client}" = server ]; then
  "$W/attacker" -role server -addr 127.0.0.1:$PORT ${ONLY:+-only $ONLY} > "$W/attacker.log" 2>&1 &
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
"$W/attacker" -addr 127.0.0.1:$PORT ${ONLY:+-only $ONLY} || true
if ! kill -0 $VP 2>/dev/null; then echo "THE SERVER DIED:"; tail -n 30 "$W/victim.log"; fi
