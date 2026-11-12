#!/bin/bash
# Semantic mutants of the proved functions (testdata/mutants.txt): each
# mutant's package is copied under testdata/, mutated, and must (1) fail
# the package's Go property test (proved_test.go: the property tells the
# mutant apart) and (2) leave at least one goal of the mutated function
# unproved by go2lean prove with the package's saved proofs (the proof is
# about this function, not any). docs/eval/proof-trust.md.
# usage: scripts/mutants.sh <workspace> [jobs]
set -e
cd "$(dirname "$0")/.."
WS=${1:?workspace}
JOBS=${2:-4}
rm -rf "$WS"; mkdir -p "$WS"
go build -o "$WS/g2l" ./cmd/go2lean
trap 'rm -rf testdata/mutant-*' EXIT
mutant() {
  IFS='|' read -r pkg fn from to <<< "$1"
  name=mutant-$pkg-$fn
  dir=testdata/$name
  rm -rf "$dir"; mkdir -p "$dir"
  cp examples/$pkg/*.go "$dir/"
  goals=examples/$pkg/proofs.json; [ -f "$goals" ] || goals=examples/$pkg/goals.json
  src=$(grep -l "^func $fn(" "$dir"/*.go | head -1)
  # Replace the first occurrence inside the function's body only.
  python3 -I - "$src" "$fn" "$from" "$to" <<'PY' || { echo "ERROR    $name: mutation not applied"; return; }
import sys
path, fn, old, new = sys.argv[1:]
s = open(path).read()
i = s.index("\nfunc " + fn + "(")
j = s.find("\nfunc ", i + 1)
j = len(s) if j < 0 else j
body = s[i:j]
if old not in body:
    sys.exit(1)
open(path, "w").write(s[:i] + body.replace(old, new, 1) + s[j:])
PY
  sed -i "s|github.com/fooexec/go2lean/examples/$pkg\"|github.com/fooexec/go2lean/$dir\"|" "$dir/proved_test.go"
  if go test "./$dir" > "$WS/$name.test.log" 2>&1; then gotest="passes (not told apart)"; else gotest="fails"; fi
  "$WS/g2l" prove -workspace "$WS/$name" -paranoid=false -goals "$goals" "./$dir" > "$WS/$name.log" 2>&1 || true
  open=$(grep -c "^  open     $fn " "$WS/$name.log" || true)
  proved=$(grep -c "^  proved   $fn " "$WS/$name.log" || true)
  if [ "$gotest" = fails ] && [ "$open" -gt 0 ]; then v=ok; else v=BAD; fi
  echo "$v $name: go test $gotest; $fn goals: $proved proved, $open open"
}
export -f mutant; export WS
grep -v '^#' testdata/mutants.txt | grep . | xargs -P "$JOBS" -d '\n' -I{} bash -c 'mutant "$@"' _ {} | sort > "$WS/verdicts.txt"
cat "$WS/verdicts.txt"
if grep -q '^BAD\|^ERROR' "$WS/verdicts.txt"; then echo "mutants: a mutant was not told apart"; exit 1; fi
echo "mutants: $(grep -c '^ok' "$WS/verdicts.txt") mutants told apart by Go and not proved"
