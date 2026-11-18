#!/bin/bash
# Ground-truth regression for the LLVM line (#268): runs `go2lean llvm` on
# every C example in examples/llvm and compares each function's outcome
# with testdata/regress/llvm.golden. An outcome is the replay's verdict
# (confirmed, spurious, ...) for a function where the search found
# undefined behaviour, "exhausted" when the exhaustive engine found none
# on its whole domain, and "held" when only the random search ran.
# Needs LLVM 18's development files and sanitizer runtime (llvm-18-dev,
# libclang-rt-18-dev) and the Lean toolchain.
# usage: scripts/llvm-regress.sh <workspace> [-update]
set -euo pipefail
cd "$(dirname "$0")/.."
WS=${1:?workspace}
rm -rf "$WS"; mkdir -p "$WS"
go build -tags llvm18 -o "$WS/g2l" ./cmd/go2lean
for src in examples/llvm/*.c; do
  name=$(basename "$src" .c)
  # An example may ask for flags on its first line (the fuzzer's budget).
  flags=$(sed -n '1s|^// go2lean-llvm-flags: ||p' "$src")
  # shellcheck disable=SC2086
  "$WS/g2l" llvm $flags -workspace "$WS/ws-$name" "$src" > "$WS/$name.log" 2>&1 || {
    echo "llvm-regress: go2lean llvm failed on $src; log: $WS/$name.log"
    tail -20 "$WS/$name.log"
    exit 1
  }
done
python3 - "$WS" > "$WS/outcomes.txt" <<'PY'
import glob, os, re, sys
ws = sys.argv[1]
out = []
for log in sorted(glob.glob(os.path.join(ws, "*.log"))):
    name = os.path.basename(log)[:-4]
    fn, seen = None, None
    def flush():
        if fn is not None:
            out.append(f"{seen or 'none'} {name}.{fn}")
    for line in open(log):
        m = re.match(r"^  (\S+)$", line.rstrip("\n"))
        if m:
            flush()
            fn, seen = m.group(1), None
            continue
        m = re.match(r"^    -> (\S+)", line)
        if m and fn is not None:
            seen = m.group(1)
            continue
        m = re.match(r"^    libfuzzer: no crash", line)
        if m and fn is not None and seen in (None, "held"):
            seen = "fuzzed"
            continue
        if line.startswith("    no UB in ") and fn is not None and seen is None:
            seen = "exhausted" if "exhaustive: no UB on all" in line else "held"
    flush()
print("\n".join(out))
PY
golden=testdata/regress/llvm.golden
if [ "${2:-}" = -update ]; then
  cp "$WS/outcomes.txt" "$golden"
  echo "llvm-regress: $golden updated ($(grep -c . "$golden") functions)"
  exit 0
fi
if diff -u "$golden" "$WS/outcomes.txt"; then
  echo "llvm-regress: $(grep -c . "$golden") functions as expected"
else
  echo "llvm-regress: outcomes differ from $golden (- expected, + now)"
  exit 1
fi
