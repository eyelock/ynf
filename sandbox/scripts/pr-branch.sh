#!/bin/sh
# Cut BRANCH from main and commit FILES_DIR on top. Called by Terraform; needs REPO, BRANCH,
# FILES_DIR, TITLE, BODY_FILE and LABELS (comma-separated).
set -eu
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
url="https://github.com/$REPO.git"
git -c credential.helper= -c 'credential.helper=!gh auth git-credential' \
  clone -q --depth 1 --branch main "$url" "$work"
cd "$work"
git checkout -q -b "$BRANCH"
cp -R "$FILES_DIR"/. .
git add -A
git commit -q -m "$TITLE"
git -c credential.helper= -c 'credential.helper=!gh auth git-credential' \
  push -q --force origin "HEAD:$BRANCH"

set --
old_ifs=$IFS; IFS=,
for l in $LABELS; do set -- "$@" --label "$l"; done
IFS=$old_ifs
gh pr create --repo "$REPO" --base main --head "$BRANCH" --title "$TITLE" --body-file "$BODY_FILE" "$@" >/dev/null
