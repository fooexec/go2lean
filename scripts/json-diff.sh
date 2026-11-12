#!/bin/bash
# Differential check of go2lean's encoding/json model against Go: hunt the
# always-failing specs of testdata/jsondiff with several seeds, run every
# counterexample for real (M3), and compare each model probe with the real
# value. PKG=testdata/rediff checks the regexp model the same way.
# DEPS=1 also models dependencies (and the standard-library packages go2lean
# translates from source, such as net/url).
# usage: [PKG=testdata/...] [DEPS=1] scripts/json-diff.sh <workspace> [seeds]
set -euo pipefail
cd "$(dirname "$0")/.."
WS=${1:?workspace}; SEEDS=${2:-12}; PKG=${PKG:-testdata/jsondiff}
[[ "$SEEDS" =~ ^[1-9][0-9]*$ ]] || { echo 'seeds must be a positive integer' >&2; exit 1; }
DEPS=${DEPS:-}
rm -rf "$WS"; mkdir -p "$WS"
go build -o "$WS/g2l" ./cmd/go2lean
"$WS/g2l" compile ${DEPS:+-deps} -workspace "$WS" "./$PKG" > "$WS/compile.log" 2>&1
"$WS/g2l" props -workspace "$WS" -package "github.com/fooexec/go2lean/$PKG" > /dev/null
for p in $(ls "$WS/properties" | sed 's/\.json$//' | grep 'diff$'); do
  for seed in $(seq 1 "$SEEDS"); do
    "$WS/g2l" hunt -workspace "$WS" -property "$p" -seed "$seed" -engine random >> "$WS/hunt.log" 2>&1
  done
done
for cx in $(ls "$WS/counterexamples" | sed 's/\.json$//'); do
  "$WS/g2l" poc -workspace "$WS" -counterexample "$cx" >> "$WS/poc.log" 2>&1
done
python3 scripts/json-diff-compare.py "$WS" "$SEEDS"
