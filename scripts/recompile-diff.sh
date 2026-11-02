#!/bin/bash
# Differential check of the model's regexp compiler (reCompile, for patterns
# built at run time, #46) against Go's regexp: for each pattern of
# testdata/recompile, Go's verdict (accepted or rejected) and, on accepted
# ones, FindStringSubmatchIndex on each string, against the model's. A
# pattern the model does not take (code 2, havoc) is listed, not counted.
# usage: scripts/recompile-diff.sh <workdir>
set -euo pipefail
cd "$(dirname "$0")/.."
WS=${1:?workdir}
mkdir -p "$WS"
WS=$(cd "$WS" && pwd)
go run testdata/recompile/gen.go "$WS"
(cd lean && lake build Go2Lean.Dialect.GoSSA > /dev/null && lake env lean --run "$WS/Check.lean" > "$WS/got.txt")
python3 - "$WS" <<'PY'
import sys
ws = sys.argv[1]
def parse(f):
    out, cur = {}, None
    for l in open(f):
        l = l.rstrip('\n')
        if l.startswith('  '):
            out[cur][1].append(l.strip())
        else:
            k, c = l.rsplit(' ', 1)
            cur = k
            out[k] = (c, [])
    return out
want, got = parse(ws + '/expected.txt'), parse(ws + '/got.txt')
bad, havoc = 0, []
for k, (c, rows) in want.items():
    gc, grows = got.get(k, ('missing', []))
    if gc == '2':
        havoc.append(k)
    elif gc != c:
        print(f'{k}: Go {"rejects" if c == "1" else "accepts"}, model code {gc}')
        bad += 1
    elif rows != grows:
        for i, (a, b) in enumerate(zip(rows, grows)):
            if a != b:
                print(f'{k} on string {i}: Go {a}, model {b}')
        bad += 1
print(f'recompile-diff: {bad} of {len(want)} patterns differ; {len(havoc)} not taken (havoc): {" ".join(havoc)}')
sys.exit(1 if bad else 0)
PY
