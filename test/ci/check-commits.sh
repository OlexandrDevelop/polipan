#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Pull request commits: Conventional Commits subject and a DCO sign-off (REQ-OSS-003, REQ-OSS-009).
# Usage: check-commits.sh <base-sha> <head-sha>
set -eu

base=$1
head=$2
status=0
pattern='^(feat|fix|refactor|docs|test|chore|perf|style|build|ci)(\([a-z0-9._/-]+\))?!?: .+'
for sha in $(git rev-list --no-merges "$base..$head"); do
  subject=$(git log -1 --format=%s "$sha")
  if ! printf '%s\n' "$subject" | grep -Eq "$pattern"; then
    echo "$sha: subject is not a Conventional Commit: $subject"
    status=1
  fi
  if ! git log -1 --format=%B "$sha" | grep -Eq '^Signed-off-by: .+ <.+>$'; then
    echo "$sha: missing Signed-off-by line (use git commit -s)"
    status=1
  fi
done
exit "$status"
