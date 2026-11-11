#!/bin/bash
# Coverage audit of the compile stage on the standard library: compile
# every public, non-runtime package of std under -deps, then group the
# functions the model does not translate completely by cause
# (scripts/audit-std.py). The report says how far the model is from
# translating the language completely, and which gaps to close first.
# usage: scripts/audit-std.sh <workspace> [jobs]
set -euo pipefail
cd "$(dirname "$0")/.."
WS=${1:?workspace}
JOBS=${2:-2}
mkdir -p "$WS"
go build -o "$WS/g2l" ./cmd/go2lean
go list std | grep -vE '^(cmd/|vendor/)|(^|/)internal(/|$)|^runtime|^syscall|^unsafe$|^plugin$' > "$WS/packages.txt"
compile() {
  d="$WS/pkgs/${1//\//_}"
  if [ -f "$d/done" ]; then return; fi
  rm -rf "$d"; mkdir -p "$d"
  s=$(date +%s)
  rc=0
  timeout 600 "$WS/g2l" compile -deps -workspace "$d/ws" "$1" > "$d/log" 2>&1 || rc=$?
  echo "$1 $rc $(( $(date +%s) - s ))" > "$d/done"
  rm -rf "$d/ws/lean"
}
export -f compile
export WS
xargs -P "$JOBS" -I{} bash -c 'compile {}' < "$WS/packages.txt"
python3 scripts/audit-std.py "$WS" > "$WS/report.md"
echo "audit-std: $WS/report.md"
head -30 "$WS/report.md"
