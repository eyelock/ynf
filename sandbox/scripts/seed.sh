#!/bin/sh
# Push the seed as a single commit on main. Called by Terraform; needs REPO and SEED_DIR, and
# takes MESSAGE. The seed names eyelock's sandbox; with OWNER, SANDBOX_REPO and FACTORY_REPO it is
# pushed with those names instead, by the same replacements terraform/main.tf hashes.
set -eu
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$SEED_DIR"/. "$work"/
cd "$work"
if [ -n "${SANDBOX_REPO:-}" ]; then
  grep -rlF -e 'eyelock/ynf-sandbox' -e '* @eyelock' . | while IFS= read -r f; do
    sed -e "s|eyelock/ynf-sandbox-factory|$FACTORY_REPO|g" \
      -e "s|eyelock/ynf-sandbox|$SANDBOX_REPO|g" \
      -e "s|\* @eyelock|* @$OWNER|g" "$f" >"$f.rendered"
    cat "$f.rendered" >"$f"
    rm "$f.rendered"
  done
fi
git init -q -b main
git add -A
git commit -q -m "${MESSAGE:-seed: ynf sandbox fixtures}"
git -c credential.helper= -c 'credential.helper=!gh auth git-credential' \
  push -q --force "https://github.com/$REPO.git" HEAD:main
