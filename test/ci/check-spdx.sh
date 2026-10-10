#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Gate 8 (part): every source file has an SPDX license line in its first 5 lines.
# Generated files are skipped (*.gen.go, *.pb.go, *.d.ts). Run from the repository root.
set -eu

missing=0
files=$(git ls-files '*.go' '*.ts' '*.tsx' '*.js' '*.sh' '*.proto' '*.sql' 'Dockerfile*' '*.Dockerfile' ':!:docs/spec' ':!:*.gen.go' ':!:*.pb.go' ':!:*.d.ts')
for f in $files; do
  [ -f "$f" ] || continue
  if ! head -n 5 "$f" | grep -q 'SPDX-License-Identifier: Apache-2.0'; then
    echo "missing SPDX header: $f"
    missing=1
  fi
done
if [ "$missing" -ne 0 ]; then
  exit 1
fi
echo "SPDX headers ok"
