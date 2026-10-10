#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# The copies in /api and /proto must equal the spec files (docs/spec is the source).
# Run from the repository root.
set -eu

status=0
check() {
  if ! cmp -s "$1" "$2"; then
    echo "$2 differs from $1. Run: make api-sync"
    status=1
  fi
}
check docs/spec/openapi.yaml api/openapi.yaml
check docs/spec/schemas/agent.proto proto/agent.proto
exit "$status"
