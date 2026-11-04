#!/bin/bash
# Scores each arm's failing input of the harness at the fix commit. A: the
# counterexample replayed there with go2lean poc (repeated -race runs and
# the model's schedule); B: the fuzzer's failing input run 20 times under
# -race. A find counts when the input does not fail at the fix; its kind
# is then checked by reading the parent's report.
# Arm A only, replayed with the tool that found it (G). usage: score22.sh <results dir> [ids...]
# As score9.sh, with the pinned toolchain (GOTOOLCHAIN=go1.26.6+auto) for k043 too.
H=/tmp/claude-0/-home-user/2603d40b-be47-5ac1-abd7-f2d549bc2e2a/scratchpad/bench7
G=${G:-$H/tool-154/go2lean}
export PATH=$HOME/.elan/bin:$PATH
R=$(realpath "$1"); shift
ids=("$@")
[ ${#ids[@]} -eq 0 ] && ids=($(ls $R | grep '^k'))
for id in "${ids[@]}"; do
  [ -d "$R/$id" ] || continue
  read -r repo fix moddir pkgdir <<< "$(python3 -c "
import json
for l in open('$H/cases.jsonl'):
    c=json.loads(l)
    if c['id']=='$id': print(c['repo_dir'], c['fix'], c['module_dir'] or '.', c['package_dir'] or '.')")"
  wt=$H/fw/$id
  git -C "$repo" worktree remove --force "$wt" 2>/dev/null
  git -C "$repo" worktree add -q --detach "$wt" "$fix" || { echo "$id fix-worktree-failed" | tee -a $R/score8.log; continue; }
  h=$H/harness/$id/go2lean_conc_test.go
  [ -f $H/harness/$id/go2lean_conc_test.fix.go.txt ] && h=$H/harness/$id/go2lean_conc_test.fix.go.txt
  cp "$h" "$wt/$pkgdir/go2lean_conc_test.go"
  mod=$wt/$moddir
  gf=-mod=mod; [ -f "$wt/go.work" ] && gf=
  # The per-case adjustments of run8b.sh, at the fix as at the parent.
  unset GOTOOLCHAIN
  case $id in
    k043) (cd "$mod" && GOFLAGS=$gf go get github.com/nats-io/nats-server/v2@v2.15.2) > "$R/$id/score-pre.log" 2>&1 ;;
  esac
  # A: replay each confirmed counterexample of the harness at the fix.
  a="-"
  for v in "$R/$id"/wsA/verdicts/*.json; do
    [ -f "$v" ] || continue
    read -r cx status <<< "$(python3 -c "
import json; v=json.load(open('$v')); print(v['counterexample'], v['status'])")"
    if [ "$status" != confirmed ]; then [ "$a" = "-" ] && a="not-confirmed($status)"; continue; fi
    w=$H/scorews/$id; rm -rf "$w"; mkdir -p "$w"; cp -r "$R/$id/wsA/"* "$w/"; rm -rf "$w/verdicts" "$w/poc"
    [ -f "$w/models.tgz" ] && tar xzf "$w/models.tgz" -C "$w" && rm "$w/models.tgz"
    (cd "$mod" && GOFLAGS="$gf" timeout 900 $G poc -workspace "$w" -module . -counterexample "$cx") > "$R/$id/score-A.log" 2>&1
    st=$(python3 -c "
import json,glob
for f in glob.glob('$w/verdicts/*.json'): v=json.load(open(f)); print(v['status'], (v.get('diagnosis') or {}).get('reason',''))" 2>/dev/null)
    case "$st" in
      spurious*|*race_not_observed*) a="FOUND" ;;
      confirmed*) a="fails-on-fix" ;;
      *) a="replay-error($st)" ;;
    esac
    rm -rf "$w"
  done
  git -C "$repo" worktree remove --force "$wt"
  echo "$id A:$a" | tee -a $R/score8.log
done
