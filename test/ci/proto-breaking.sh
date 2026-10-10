#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Gate 6 (part): buf breaking against the last release tag (v*).
# Before the first release there is nothing to compare. Run from the repository root.
set -eu

base=$(git describe --tags --abbrev=0 --match 'v*' 2>/dev/null || true)
if [ -z "$base" ]; then
  echo "No release tag yet: buf breaking has nothing to compare."
  exit 0
fi
go run github.com/bufbuild/buf/cmd/buf@v1.47.2 breaking proto --against ".git#tag=$base,subdir=proto"
