#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# REQ-UI-023: every operationId named in a Data or Actions line of docs/spec/ui/screens.md
# must exist in api/openapi.yaml. The "Known API gaps" table is not read.
# Run from the repository root.
set -eu

screens=docs/spec/ui/screens.md
openapi=api/openapi.yaml

# Words in backticks on those lines that are not operations.
ignore='cron inputs localStorage preview'

known=$(grep -oE 'operationId: [A-Za-z0-9_]+' "$openapi" | awk '{print $2}' | sort -u)

used=$(grep -E '^- \*\*(Data|Actions):\*\*' "$screens" |
  grep -oE '`[^`]+`' | tr -d '`' |
  grep -E '^[a-z][A-Za-z0-9]*$' | sort -u || true)

if [ -z "$used" ]; then
  echo "No operationId found in $screens. The check is broken."
  exit 1
fi

status=0
for name in $used; do
  case " $ignore " in *" $name "*) continue ;; esac
  if ! printf '%s\n' "$known" | grep -qx "$name"; then
    echo "$screens uses operationId $name, which is not in $openapi."
    status=1
  fi
done
exit "$status"
