#!/bin/sh
# Fails when README.md and greet's real flags disagree, in either direction.
# An optional package path scopes the check: README.md documents cmd/greet only, so scoped to any
# other package there is nothing to compare.
set -eu
cd "$(dirname "$0")/.."
scope=${1:-}
case "$scope" in
  ""|.|./...|cmd/greet|./cmd/greet|cmd/greet/...|./cmd/greet/...) ;;
  *) echo "nothing documented for $scope"; exit 0 ;;
esac
help=$(go run ./cmd/greet -h 2>&1 || true)
code_flags=$(printf '%s\n' "$help" | sed -n 's/^  -\([a-z][a-z-]*\).*/\1/p' | sort -u)
doc_flags=$(sed -n 's/.*`--\([a-z][a-z-]*\)`.*/\1/p' README.md | sort -u)
status=0
for f in $code_flags; do
  printf '%s\n' "$doc_flags" | grep -qx "$f" || { echo "README.md: flag --$f is not documented"; status=1; }
done
for f in $doc_flags; do
  printf '%s\n' "$code_flags" | grep -qx "$f" || { echo "README.md: documents --$f, which greet does not have"; status=1; }
done
exit $status
