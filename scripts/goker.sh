#!/bin/bash
# Run go2lean on the GoKer kernels of GoBench (docs/concurrency.md): every
# kernel's test function is a target (-tests), one workspace per kernel,
# and a table of outcomes at the end.
# usage: scripts/goker.sh <gobench checkout> <output dir> [jobs] [kernel dir glob]
#   git clone https://github.com/timmyyuan/gobench <gobench checkout>
set -e
cd "$(dirname "$0")/.."
ROOT=$(pwd)
GB=$(cd "${1:?gobench checkout}" && pwd)
OUT=${2:?output dir}
JOBS=${3:-4}
GLOB=${4:-"gobench/goker/*/*/*"}
mkdir -p "$OUT/logs" "$OUT/ws"
OUT=$(cd "$OUT" && pwd)
go build -o "$OUT/g2l" ./cmd/go2lean
run_one() {
  k=$1; name=$(echo "$k" | sed 's|gobench/goker/||; s|/|_|g')
  ws=$OUT/ws/$name; rm -rf "${ws:?}"
  (cd "$ROOT" && timeout 900 "$OUT/g2l" run -tests -unexported -module "$GB" -workspace "$ws" "./$k") > "$OUT/logs/$name.log" 2>&1
  echo "exit $?" >> "$OUT/logs/$name.log"
  rm -rf "${ws:?}/lean" # the Lean build: ~100 MB per kernel
}
export -f run_one; export OUT ROOT GB
(cd "$GB" && ls -d $GLOB) | xargs -P "$JOBS" -I{} bash -c 'run_one {}'
python3 - "$OUT" <<'EOF' | tee "$OUT/results.tsv"
import collections, glob, json, os, re, sys
out = sys.argv[1]
rows = []
for log in sorted(glob.glob(f"{out}/logs/*.log")):
    name = os.path.basename(log)[:-4]
    txt = open(log).read()
    ws = f"{out}/ws/{name}"
    status, kind, note = "no_target", "", ""
    m = re.search(r"_test\w*_nopanic random: [^\n]*", txt)
    if m:
        line = m.group(0)
        mm = re.search(r", (\d+) moot/stuck\)", line)
        if "holds on" in line:
            status = "holds"
        elif mm and int(mm.group(1)) > 0:
            status = "stuck"
            sm = re.search(r"stuck mostly at ([^;(]*)", line)
            note = sm.group(1).strip()[:80] if sm else ""
        else:
            status = "not_found"
    for f in glob.glob(f"{ws}/verdicts/*.json"):
        v = json.load(open(f))
        cx = json.load(open(f"{ws}/counterexamples/{v['counterexample']}.json"))
        if "_test" not in cx["property"]:
            continue
        status = v["status"] + (f" ({v['diagnosis']['reason']})" if v.get("diagnosis") else "")
        kind = (cx.get("model_output") or {}).get("panic", "")
    rows.append((name.split("_")[0], name, status, kind, note))
for r in rows:
    print("\t".join(r))
c = collections.Counter((r[0], r[2].split(" ")[0]) for r in rows)
print()
for k in sorted(c):
    print(f"# {k[0]:12} {k[1]:14} {c[k]}")
EOF
