#!/bin/bash
# Eval 5 arm A rerun on the cases #46 lists, rebuilt from this directory
# (cases.jsonl, truth.jsonl, specs/): clone, worktrees at the parent (with
# the spec) and the fix, go2lean run, then score A's spec counterexample at
# the fix as score.sh does. Env: G (tool), OUT, RUNBUDGET (-run-budget),
# EXTRA (more run flags, e.g. -specs-only).
# usage: run46.sh ids...
S=/tmp/claude-0/-home-user/2603d40b-be47-5ac1-abd7-f2d549bc2e2a/scratchpad
H=$S/bench5; D=$S/g2l-iso/docs/eval/search-vs-fuzz
G=${G:-$H/tool/go2lean}; OUT=${OUT:-$H/results}
export PATH=$HOME/.elan/bin:$PATH
mkdir -p $OUT $H/repos $H/scorews
for id in "$@"; do
  read -r repo pkgdir module <<< "$(python3 -c "
import json
for l in open('$D/cases.jsonl'):
    c=json.loads(l)
    if c['id']=='$id': print(c['repo'], c['package_dir'] or '.', c['module'])")"
  parent=$(python3 -c "
import json
for l in open('$D/cases.jsonl'):
    c=json.loads(l)
    if c['id']=='$id': print(c['parent'])")
  fix=$(python3 -c "
import json
for l in open('$D/truth.jsonl'):
    t=json.loads(l)
    if t['id']=='$id': print(t['fix_sha'])")
  rd=$H/repos/$repo
  [ -d $rd ] || git clone -q --filter=blob:none https://github.com/${repo/_//}.git $rd || { echo "$id clone-failed" | tee -a $OUT/run.log; continue; }
  loc=$(awk -v i=$id '$1==i{print $2}' $D/specs/locations.txt)
  out=$OUT/$id; ws=$H/ws/$id; rm -rf $out $ws; mkdir -p $out
  for side in parent fix; do
    wt=$H/w/$id-$side; sha=$parent; [ $side = fix ] && sha=$fix
    git -C $rd worktree remove --force $wt 2>/dev/null
    git -C $rd worktree add -q --detach $wt $sha || { echo "$id worktree-failed" | tee -a $OUT/run.log; continue 2; }
    cp $D/specs/$id.go.txt $wt/$loc
    mod=$wt/$pkgdir; while [ "$mod" != "$wt" ] && [ ! -f "$mod/go.mod" ]; do mod=$(dirname "$mod"); done
    if [ ! -f "$mod/go.mod" ]; then mod=$wt; (cd $wt && go mod init "$module" >/dev/null 2>&1 && GOFLAGS=-mod=mod go mod tidy >/dev/null 2>&1); fi
    eval "mod_$side=$mod"
  done
  gf="-mod=mod -vet=off"; [ -f "$H/w/$id-parent/go.work" ] && gf="-vet=off"
  rel=$(realpath --relative-to="$mod_parent" "$H/w/$id-parent/$pkgdir")
  s=$(date +%s)
  (cd $mod_parent && GOFLAGS="$gf" timeout 1800 $G run -deps -budget 300 ${RUNBUDGET:+-run-budget $RUNBUDGET} $EXTRA -prove=false -workspace $ws -module . ./$rel) > $out/A.log 2>&1
  echo "rc=$? secs=$(( $(date +%s)-s ))" > $out/A.time
  mkdir -p $out/ws && cp -r $ws/{counterexamples,verdicts,hunts,properties,targets} $out/ws/ 2>/dev/null
  a="-"
  for v in $out/ws/verdicts/*.json; do
    [ -f "$v" ] || continue
    read -r cx status kind <<< "$(python3 -c "
import json
v=json.load(open('$v')); cx=v['counterexample']
c=json.load(open('$out/ws/counterexamples/'+cx+'.json'))
print(cx, v['status'], 'tmpl' if c['property'].endswith('_nopanic') or c['property'].endswith('_nohang') else 'spec')")"
    [ "$kind" = spec ] || continue
    if [ "$status" != confirmed ]; then a="not-confirmed($status)"; continue; fi
    w=$H/scorews/$id; rm -rf $w; cp -r $ws $w; rm -rf $w/verdicts $w/poc
    (cd $mod_fix && GOFLAGS="$gf" timeout 600 $G poc -workspace $w -module . -counterexample $cx) > $out/score-A.log 2>&1
    st=$(python3 -c "
import json,glob
for f in glob.glob('$w/verdicts/*.json'): v=json.load(open(f)); print(v['status'], (v.get('diagnosis') or {}).get('reason',''))" 2>/dev/null)
    case "$st" in
      spurious*oracle_holds*) a="FOUND" ;;
      confirmed*) a="spec-fails-on-fix" ;;
      *) a="replay-error($st)" ;;
    esac
    rm -rf $w
  done
  rm -rf $ws
  for side in parent fix; do git -C $rd worktree remove --force $H/w/$id-$side; done
  echo "$id A:$a $(cat $out/A.time)" | tee -a $OUT/run.log
done
echo done | tee -a $OUT/run.log
