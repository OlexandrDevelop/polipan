#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Gate 8 (part, REQ-OSS-002): no GPL, AGPL, SSPL or BUSL license in the production dependencies of the web app.
# The web app needs "npm ci" first. Run from the repository root.
set -eu

out=$(mktemp)
trap 'rm -f "$out"' EXIT
(cd web && npx --yes license-checker-rseidelsohn@4.4.2 --production --excludePrivatePackages --json) > "$out"
count=$(jq 'length' "$out")
bad=$(jq -r 'to_entries[] | select((.value.licenses | tostring) | test("(^|[^A-Za-z])(GPL|AGPL|SSPL|BUSL)")) | "\(.key): \(.value.licenses)"' "$out")
if [ -n "$bad" ]; then
  echo "forbidden licenses:"
  echo "$bad"
  exit 1
fi
echo "web licenses ok ($count packages)"
