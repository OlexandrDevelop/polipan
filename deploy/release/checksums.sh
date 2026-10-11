#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Writes the file SHA256SUMS for the release files of a folder: one line per file, sorted by name,
# in the format of "sha256sum -c". SHA256SUMS and signature files are not listed.
#
#   sh deploy/release/checksums.sh DIR
set -eu

[ $# -eq 1 ] && [ -d "$1" ] || { echo "usage: checksums.sh DIR" >&2; exit 2; }
cd "$1"
rm -f SHA256SUMS
files=$(ls -1 | LC_ALL=C sort | grep -v -E '^(SHA256SUMS.*)$' || true)
[ -n "$files" ] || { echo "checksums: no files in $1" >&2; exit 1; }
for f in $files; do
  [ -f "$f" ] || { echo "checksums: $f is not a file" >&2; exit 1; }
  sha256sum "$f"
done > SHA256SUMS
cat SHA256SUMS
