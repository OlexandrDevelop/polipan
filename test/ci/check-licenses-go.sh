#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Gate 8 (part, REQ-OSS-002): only allowed licenses in the Go dependencies of the three binaries.
# GPL, AGPL, SSPL and BUSL are not in the list, so go-licenses fails on them. Run from the repository root.
set -eu

go run github.com/google/go-licenses@v1.6.0 check \
  ./server/cmd/polipan-server ./agent/cmd/polipan-agent ./cli/cmd/polipan \
  --allowed_licenses=Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MIT,MPL-2.0
