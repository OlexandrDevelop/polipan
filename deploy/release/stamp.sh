#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Puts release values into a file. Every @NAME@ in the file is replaced by the value.
#
#   sh deploy/release/stamp.sh FILE NAME=VALUE [NAME=VALUE ...]
#
# The release build uses it for the install scripts (image digests, version, checksums), so a script
# that comes from a release installs exactly the files of that release. A value may only have
# letters, digits and the characters : . _ / - + (nothing a shell would run).
set -eu

[ $# -ge 2 ] || { echo "usage: stamp.sh FILE NAME=VALUE ..." >&2; exit 2; }
file=$1
shift
[ -f "$file" ] || { echo "stamp: no such file: $file" >&2; exit 1; }

for pair in "$@"; do
  name=${pair%%=*}
  value=${pair#*=}
  case "$name" in
    ''|*[!A-Z0-9_]*) echo "stamp: bad name: $name" >&2; exit 2 ;;
  esac
  case "$value" in
    ''|*[!A-Za-z0-9:._/+-]*) echo "stamp: bad value for $name" >&2; exit 2 ;;
  esac
  grep -q "@$name@" "$file" || { echo "stamp: @$name@ is not in $file" >&2; exit 1; }
  tmp="$file.stamp.$$"
  sed "s|@$name@|$value|g" "$file" > "$tmp"
  cat "$tmp" > "$file"
  rm -f "$tmp"
done
