#!/bin/bash
# Evaluation 7, second amendment: both arms on the same concurrent harness
# (harness/<id>/go2lean_conc_test.go) at the case's parent. B fuzzes the
# properties A recorded, so both share one workspace.
#   A: go2lean run -deps -specs-only -budget $BUDGET
#   B: go2lean fuzz -fuzztime $FUZZTIME, built with -race
# Results in $OUT/<id>/. usage: run21.sh [ids...]
# Rerun of arm A only at main 15e8aaf (#139): after M3 (fuel raise, run order,
# memory limit, net loopback, regexp, DeepEqual), on the same harnesses, with
# the same flags and run8b.sh's adjustments.
# B is not rerun: the fuzzer did not change; its results8 scores stand.
H=/tmp/claude-0/-home-user/2603d40b-be47-5ac1-abd7-f2d549bc2e2a/scratchpad/bench7
G=$H/tool-139/go2lean
BUDGET=${BUDGET:-300}; FUZZTIME=${FUZZTIME:-10m}; OUT=${OUT:-$H/results21}
export PATH=$HOME/.elan/bin:$PATH
ids=("$@")
[ ${#ids[@]} -eq 0 ] && ids=($(ls $H/harness | grep '^k'))
for id in "${ids[@]}"; do
  [ -f $H/harness/$id/go2lean_conc_test.go ] || { echo "$id no-harness" | tee -a $OUT/run8.log; continue; }
  read -r repo parent moddir pkgdir <<< "$(python3 -c "
import json
for l in open('$H/cases.jsonl'):
    c=json.loads(l)
    if c['id']=='$id': print(c['repo_dir'], c['parent'], c['module_dir'] or '.', c['package_dir'] or '.')")"
  out=$OUT/$id; wt=$H/w/$id; wsA=$H/ws/$id-A; wsB=$H/ws/$id-B
  rm -rf "$out" "$wsA" "$wsB"; mkdir -p "$out"
  git -C "$repo" worktree remove --force "$wt" 2>/dev/null
  git -C "$repo" worktree add -q --detach "$wt" "$parent" || { echo "$id worktree-failed" | tee -a $OUT/run8.log; continue; }
  cp $H/harness/$id/go2lean_conc_test.go "$wt/$pkgdir/"
  pre=""; aflags=""; tc=""
  case $id in
    k043) pre="go get github.com/nats-io/nats-server/v2@v2.15.2"; tc=auto ;;  # as the harness was written
    k044) aflags="-fuzz-specs=false" ;;  # #64: the package's own fuzz targets break loading
  esac
  mod=$wt/$moddir; rel=$(realpath --relative-to="$mod" "$wt/$pkgdir")
  gf=-mod=mod; [ -f "$wt/go.work" ] && gf=
  # Modules the screening needed (goleak) are fetched by -mod=mod.
  [ -n "$tc" ] && export GOTOOLCHAIN=$tc || unset GOTOOLCHAIN
  [ -n "$pre" ] && (cd "$mod" && GOFLAGS=$gf $pre) > "$out/pre.log" 2>&1
  s=$(date +%s)
  (cd "$mod" && GOFLAGS=$gf timeout 2400 $G run $aflags -deps -specs-only -budget $BUDGET -workspace "$wsA" -module . ./$rel) > "$out/A.log" 2>&1
  echo "rc=$? secs=$(( $(date +%s)-s ))" > "$out/A.time"
  mkdir -p "$out/wsA" && cp -r "$wsA"/{counterexamples,verdicts,hunts,proofs,properties,targets,poc} "$out/wsA/" 2>/dev/null
  (cd "$wsA" && find models -name '*.json' -exec cp --parents {} "$out/wsA/" \;) 2>/dev/null
  rm -rf "$wsA"
  git -C "$repo" worktree remove --force "$wt"
  echo "$id A:$(cat $out/A.time)" | tee -a $OUT/run8.log
done
echo done | tee -a $OUT/run8.log
