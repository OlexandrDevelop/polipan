#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Every acceptance criterion (AC-xxx-nn) of the feature files in docs/spec/04-features must have a Go test whose
# name contains the id, for example TestAC_DOM_02_03_Domains covers AC-DOM-02 and AC-DOM-03.
# A name may list more numbers after the id (_02_03_) or a range (_01_to_04).
# Some criteria can only be proven on a live stack (real Docker, Caddy, ACME, nodes). Their test is a check of the
# end-to-end drivers in test/e2e: ok("AC-xxx-nn: ...") with the id at the start of the check name. These ids are
# counted as "live". The script prints the ids that have no test at all, and fails when there are any.
# Run from the repository root.
set -eu

ids=$(grep -rhoE '\*\*AC-[A-Z]+-[0-9]+\*\*' docs/spec/04-features | tr -d '*' | sort -u)
covered=$(grep -rhoE --include='*_test.go' --exclude-dir=.claude --exclude-dir=node_modules '^func Test[A-Za-z0-9_]*' . | sed 's/^func //' |
  awk '{
    n = split($0, t, "_")
    for (i = 1; i < n; i++) {
      if ((t[i] != "AC" && t[i] != "TestAC") || t[i+1] !~ /^[A-Z]+$/) continue
      cat = t[i+1]; j = i + 2; prev = ""
      while (j <= n && t[j] ~ /^[0-9][0-9]$/) {
        print "AC-" cat "-" t[j]; prev = t[j]; j++
        if (j + 1 <= n && t[j] == "to" && t[j+1] ~ /^[0-9][0-9]$/) {
          for (k = prev + 1; k <= t[j+1] + 0; k++) printf "AC-%s-%02d\n", cat, k
          j += 2
        }
      }
    }
  }' | sort -u)

live=$(grep -rhoE 'ok\(f?"AC-[A-Z]+-[0-9]+' test/e2e --include='*.py' | grep -oE 'AC-[A-Z]+-[0-9]+' | sort -u)

total=0
missing=""
nlive=0
for id in $ids; do
  total=$((total + 1))
  if echo "$covered" | grep -qx "$id"; then continue; fi
  if echo "$live" | grep -qx "$id"; then nlive=$((nlive + 1)); echo "  live only: $id"; continue; fi
  missing="$missing $id"
done
count=$(echo $missing | wc -w)
echo "acceptance criteria: $total, Go test: $((total - count - nlive)), live e2e check only: $nlive, missing: $count"
if [ "$count" -gt 0 ]; then
  for id in $missing; do echo "  missing: $id"; done
  exit 1
fi
