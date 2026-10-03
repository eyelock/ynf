#!/bin/sh
# Per-package coverage gate: every package with statements must reach MIN percent.
# storetest is a conformance suite exercised through the providers; cmd/ynf is main(); the root
# package only embeds the schemas.
set -eu
MIN=${1:-80}
out=$(go test -race -count=1 -cover ./... 2>&1) || { printf '%s\n' "$out"; exit 1; }
printf '%s\n' "$out" | awk -v min="$MIN" '
  /coverage: / {
    pkg = ($1 == "ok") ? $2 : $1
    if (pkg == "github.com/eyelock/ynf" || pkg ~ /internal\/store\/storetest$/ || pkg ~ /cmd\/ynf$/) next
    for (i = 1; i <= NF; i++) if ($i == "coverage:") { pct = $(i+1); sub("%", "", pct) }
    printf "%-48s %6s%%\n", pkg, pct
    if (pct + 0 < min) { bad = bad "\n  " pkg " " pct "%" }
  }
  END {
    if (bad != "") { printf "\ncoverage below %s%%:%s\n", min, bad; exit 1 }
    printf "\nevery package at or above %s%%\n", min
  }'
