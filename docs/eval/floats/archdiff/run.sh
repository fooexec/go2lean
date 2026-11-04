#!/bin/bash
# Go's math on this machine (amd64: assembly for Exp, Log, Floor, Ceil,
# Trunc, Modf, Hypot, Max, Min) against the portable Go code the model
# translates (#148): copies that code from GOROOT next to main.go and
# compares the bits of every result on 3 million inputs.
# usage: docs/eval/floats/archdiff/run.sh
set -e
D=$(mktemp -d)
trap 'rm -rf "$D"' EXIT
here=$(cd "$(dirname "$0")" && pwd)
R=$(go env GOROOT)/src/math
for f in exp.go log.go hypot.go floor.go dim.go modf.go; do
  sed -e 's/^package math/package main/' "$R/$f" > "$D/p_$f"
done
for f in main.go stubs.go; do sed "/^\/\/go:build ignore$/d" "$here/$f" > "$D/$f"; done
(cd "$D" && go mod init archdiff >/dev/null 2>&1 && go run .)
