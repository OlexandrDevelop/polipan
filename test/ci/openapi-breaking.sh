#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Gate 3 (part, REQ-API-008): no breaking change inside /api/v1.
# The base is the last release tag (v*). Before the first release there is nothing to compare.
# Run from the repository root.
set -eu

base=$(git describe --tags --abbrev=0 --match 'v*' 2>/dev/null || true)
if [ -z "$base" ]; then
  echo "No release tag yet: the OpenAPI diff check has nothing to compare."
  exit 0
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git show "$base:api/openapi.yaml" > "$tmp/base.yaml"
go run github.com/tufin/oasdiff@v1.10.25 breaking "$tmp/base.yaml" api/openapi.yaml --fail-on ERR
