#!/bin/sh
# Push the seed as a single commit on main. Called by Terraform; needs REPO and SEED_DIR.
set -eu
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$SEED_DIR"/. "$work"/
cd "$work"
git init -q -b main
git add -A
git commit -q -m "seed: ynf sandbox fixtures"
git -c credential.helper= -c 'credential.helper=!gh auth git-credential' \
  push -q --force "https://github.com/$REPO.git" HEAD:main
