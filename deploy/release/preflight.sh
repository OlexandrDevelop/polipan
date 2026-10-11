#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Checks that the repository is ready for a release tag. It changes nothing: it does not tag, push or publish.
#
#   sh deploy/release/preflight.sh vX.Y.Z[-rc.N]
#
# It prints the two commands that start the release when every check passes. See docs/operations/release.md.
set -eu

[ $# -eq 1 ] || { echo "usage: preflight.sh vX.Y.Z[-rc.N]" >&2; exit 2; }
tag=$1
bad=0
fail() { echo "FAIL: $*"; bad=1; }
ok() { echo "ok:   $*"; }

# Semantic version with a v in front. A name with a dash is a pre-release.
if printf '%s' "$tag" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$'; then
  ok "$tag is a valid version"
else
  fail "$tag is not a version like v1.2.3 or v1.2.3-rc.1"
fi

branch=$(git rev-parse --abbrev-ref HEAD)
[ "$branch" = "main" ] && ok "on the branch main" || fail "the branch is $branch, a release comes from main"

[ -z "$(git status --porcelain)" ] && ok "the working tree is clean" || fail "the working tree has changes"

if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  fail "the tag $tag exists already"
else
  ok "the tag $tag is new"
fi

if git remote get-url origin >/dev/null 2>&1; then
  git fetch -q origin main --tags 2>/dev/null || echo "note: could not fetch origin (offline?), the next two checks use what is local"
  if git rev-parse -q --verify refs/remotes/origin/main >/dev/null; then
    [ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] && ok "HEAD is origin/main" || fail "HEAD is not origin/main: pull or push first"
  fi
  if git rev-parse -q --verify "refs/tags/$tag" >/dev/null 2>&1 || git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1; then
    fail "the tag $tag exists on origin"
  fi
fi

for f in LICENSE docs/operations/release.md deploy/release/build.sh .github/workflows/release.yml; do
  [ -f "$f" ] && ok "$f exists" || fail "$f is missing"
done
# Every action in the workflows must be pinned by a full commit SHA.
unpinned=$(grep -rhE '^\s*-?\s*uses:' .github/workflows | grep -vE 'uses:\s*\./' | grep -vE '@[0-9a-f]{40}(\s|$)' || true)
[ -z "$unpinned" ] && ok "every action is pinned by a commit SHA" || fail "actions without a commit SHA: $unpinned"

if [ "$bad" -ne 0 ]; then
  echo "Not ready. Fix the failed checks and run this again."
  exit 1
fi
echo
echo "Ready. These two commands start the release (the release workflow does the rest):"
echo "  git tag -s $tag -m \"$tag\"    # or: git tag -a $tag -m \"$tag\" without a GPG key"
echo "  git push origin $tag"
