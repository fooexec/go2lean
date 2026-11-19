#!/bin/bash
# Prepare the weekly release (every Sunday; docs/releases/README.md).
#
# usage: scripts/release.sh [check|notes]
#   check   the release gates: a clean tree on main at origin/main's head,
#           CI green on that commit, the counts in README.md and
#           docs/findings.md agreeing, and the next version number
#   notes   print a draft of the release notes from the pull requests
#           merged since the last tag (to edit into docs/releases/vX.Y.Z.md)
#
# It never pushes a tag and never publishes a release: that is a person's
# step (docs/releases/README.md). A pushed tag is public and cannot be
# taken back.
set -euo pipefail
cd "$(dirname "$0")/.."

last=$(git tag --list 'v*' --sort=-version:refname | head -1)
[ -n "$last" ] || { echo "release: no earlier tag"; exit 1; }
IFS=. read -r major minor patch <<<"${last#v}"
next="v$major.$minor.$((patch + 1))"

case "${1:-check}" in
check)
  git fetch -q origin main --tags
  fail=0
  note() { echo "release: $*"; fail=1; }
  [ -z "$(git status --porcelain)" ] || note "the working tree is not clean"
  head=$(git rev-parse origin/main)
  echo "last tag:  $last ($(git log -1 --format=%cd --date=short "$last"))"
  echo "next:      $next, on $(git rev-parse --short "$head") (origin/main)"
  # CI on that commit (REST: GitHub's GraphQL is not available here).
  if conclusion=$(gh api "repos/fooexec/go2lean/commits/$head/check-runs" \
      --jq '[.check_runs[] | select(.name != "changes")] | if length == 0 then "none" elif any(.status != "completed") then "pending" elif all(.conclusion == "success" or .conclusion == "skipped") then "green" else "red" end' 2>/dev/null); then
    echo "ci:        $conclusion"
    [ "$conclusion" = green ] || note "CI on origin/main is $conclusion (wait for green; a red run blocks the release)"
  else
    note "could not read CI for $head"
  fi
  # The defect counts a reader sees must agree.
  readme=$(grep -oE 'found [0-9]+ confirmed defects across [0-9]+ projects, [0-9]+ of them new' README.md | head -1 || true)
  findings=$(grep -oE '\*\*[0-9]+ defects in [0-9]+ projects\*\*' docs/findings.md | head -1 || true)
  rn=$(echo "$readme" | grep -oE '[0-9]+' | head -2 | tr '\n' ' ')
  fn=$(echo "$findings" | grep -oE '[0-9]+' | head -2 | tr '\n' ' ')
  echo "counts:    README [$rn] findings.md [$fn]"
  [ "$rn" = "$fn" ] || note "README.md and docs/findings.md disagree on the counts"
  [ -f "docs/releases/$next.md" ] || note "docs/releases/$next.md is missing (scripts/release.sh notes)"
  [ -f CHANGELOG.md ] && grep -q "^## $next\b" CHANGELOG.md || note "CHANGELOG.md has no $next entry"
  if [ "$fail" = 0 ]; then echo "release: ready for $next"; else exit 1; fi
  ;;
notes)
  echo "# go2lean $next"
  echo
  echo "<!-- One paragraph: what this week's release is about. -->"
  echo
  echo "## Install"
  echo
  echo "    go install github.com/fooexec/go2lean/cmd/go2lean@$next"
  echo "    cd your/module && go2lean run ./..."
  echo
  echo "The LLVM line (C) needs LLVM's development files and a build"
  echo "with the tag: go build -tags llvm18 ./cmd/go2lean (docs/usage.md)."
  echo
  echo "## Merged since $last"
  echo
  git log --format='%s' "$last"..origin/main --first-parent 2>/dev/null \
    | grep -E '\(#[0-9]+\)$' | sed 's/^/- /' | head -60
  echo
  echo "## Findings"
  echo
  echo "<!-- The counts as of the release date, and what is new this week. -->"
  echo
  echo "## Known limits"
  echo
  echo "<!-- What is not modelled; what changed since the last release. -->"
  ;;
*)
  echo "usage: scripts/release.sh [check|notes]"
  exit 2
  ;;
esac
