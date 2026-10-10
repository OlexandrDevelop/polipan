#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Generate the Go code (models, chi server, client) from api/openapi.yaml.
# The TypeScript types are made by "npm run generate:api" in /web.
# Run from the repository root.
set -eu

OAPI_CODEGEN="github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

go run ./api/tools/downgrade api/openapi.yaml "$TMP/openapi-3.0.yaml"
go run "$OAPI_CODEGEN" -config api/oapi-codegen.yaml "$TMP/openapi-3.0.yaml"
gofmt -w api/gen
