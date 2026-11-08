#!/usr/bin/env bash
# Mutation check for one change: swap a snippet in a source file, run the
# package tests, and say whether they notice.
#
#   tools/mutant.sh <label> <file> <old> <new>
#
# <old> must occur exactly once in <file>. The file is restored on exit, also
# on an interrupt. Results:
#   CAUGHT  a test failed (the change is covered)
#   MISSED  every test passed (a gap in the tests, or an equivalent change)
#   BUILD   the changed code does not compile (no result, try another change)
#   NOOP    <old> was not found exactly once (nothing was changed)
#
# Environment:
#   PKG    package to vet and test, default ./...
#   EXTRA  extra `go test` arguments, for example "-run TestName"
#   T      wall-clock limit in seconds for the test run, default 150
#   GT     go test -timeout, default 100s
#
# The backup lives in dist/mutant/ (ignored by git), never in a system
# temporary directory.
set -u

if [ $# -ne 4 ]; then
  sed -n '2,/^set -u/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//' >&2
  exit 2
fi
label=$1 file=$2 old=$3 new=$4

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root" || exit 2
[ -f "$file" ] || { echo "NOOP    $label  [no such file: $file]"; exit 0; }

mkdir -p dist/mutant
work=$(mktemp -d dist/mutant/run.XXXXXX) || exit 2
backup=$work/original
cp -p "$file" "$backup"

restore() {
  cp -p "$backup" "$file"
  if ! cmp -s "$backup" "$file"; then echo "RESTORE FAILED for $file (original kept in $backup)" >&2; return; fi
  rm -rf "$work"
}
trap restore EXIT
trap 'exit 130' INT TERM

count=$(python3 -I - "$file" "$old" "$new" <<'PY'
import sys
path, old, new = sys.argv[1:4]
text = open(path, encoding="utf-8").read()
n = text.count(old)
if n == 1:
    open(path, "w", encoding="utf-8").write(text.replace(old, new))
print(n)
PY
)
if [ "$count" != "1" ]; then
  echo "NOOP    $label  [pattern matched $count times]"
  exit 0
fi

pkg=${PKG:-./...}
if ! GOTOOLCHAIN=local go vet -tags nomsgpack "$pkg" >"$work/vet" 2>&1; then
  echo "BUILD   $label  [$(sed -n 3p "$work/vet" | cut -c1-90)]"
  exit 0
fi

# EXTRA is split on purpose: it holds several words such as "-run X".
# shellcheck disable=SC2086
out=$(GOTOOLCHAIN=local timeout "${T:-150}" go test -tags nomsgpack -count=1 -timeout "${GT:-100s}" ${EXTRA:-} "$pkg" 2>&1)
code=$?
if [ $code -ne 0 ]; then
  why=$(echo "$out" | grep -aE "panic: test timed out|^\s*--- FAIL" | head -2 | tr '\n' ' ')
  echo "CAUGHT  $label  [$why]"
else
  echo "MISSED  $label"
fi
