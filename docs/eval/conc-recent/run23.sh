#!/bin/bash
# Evaluation 7, rerun 6 of arm A on k009, k016, k023 and k042, after large
# heap arrays updated in place and the fuel raise up to 256x (#71). Same
# harnesses, flags and budget as run22.sh, on a fresh machine: the bench
# ($H) is rebuilt from pool.jsonl (the parent is the fix's first parent)
# and the committed harnesses, the repositories cloned with
# --filter=blob:none. Every case runs on go1.26.6 (GOTOOLCHAIN, as run22.sh
# from k023 on). The go2lean binary is built once from the branch (frozen).
# Results in $OUT/<id>/. usage: run23.sh [ids...]
H=${H:?set H to the bench directory}
G=${G:-$H/tool/go2lean}
BUDGET=${BUDGET:-300}; OUT=${OUT:-$H/results23}
export PATH=$HOME/.elan/bin:$PATH GOTOOLCHAIN=go1.26.6+auto
mkdir -p "$OUT"
ids=("$@")
[ ${#ids[@]} -eq 0 ] && ids=(k016 k042 k009 k023)
for id in "${ids[@]}"; do
  read -r repo parent moddir pkgdir <<< "$(python3 -c "
import json
for l in open('$H/cases.jsonl'):
    c=json.loads(l)
    if c['id']=='$id': print('$H/'+c['repo_dir'], c['parent'], c['module_dir'] or '.', c['package_dir'] or '.')")"
  out=$OUT/$id; wt=$H/w/$id; wsA=$H/ws/$id-A
  rm -rf "$out" "$wsA"; mkdir -p "$out"
  git -C "$repo" worktree remove --force "$wt" 2>/dev/null
  git -C "$repo" worktree add -q --detach "$wt" "$parent" || { echo "$id worktree-failed" | tee -a $OUT/run.log; continue; }
  cp $H/harness/$id/go2lean_conc_test.go "$wt/$pkgdir/"
  mod=$wt/$moddir; rel=$(realpath --relative-to="$mod" "$wt/$pkgdir")
  gf=-mod=mod; [ -f "$wt/go.work" ] && gf=
  s=$(date +%s)
  (cd "$mod" && GOFLAGS=$gf timeout 2400 $G run -deps -specs-only -budget $BUDGET -workspace "$wsA" -module . ./$rel) > "$out/A.log" 2>&1
  echo "rc=$? secs=$(( $(date +%s)-s ))" > "$out/A.time"
  mkdir -p "$out/wsA" && cp -r "$wsA"/{counterexamples,verdicts,hunts,proofs,properties,targets,poc} "$out/wsA/" 2>/dev/null
  (cd "$wsA" && find models -name '*.json' -exec cp --parents {} "$out/wsA/" \;) 2>/dev/null
  rm -rf "$wsA"
  git -C "$repo" worktree remove --force "$wt"
  echo "$id A:$(cat $out/A.time)" | tee -a $OUT/run.log
done
echo done | tee -a $OUT/run.log
