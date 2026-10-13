#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Reproducible build of the three binaries of one platform, packed in one archive (REQ-OSS-004).
#
#   sh deploy/release/build.sh VERSION GOOS GOARCH OUTDIR
#
# Same commit, same Go version, same web build: the archive has the same bytes. For that the build
#   - uses -trimpath and -buildvcs=false and no cgo,
#   - sets the version and the commit with -ldflags (the commit has 12 characters),
#   - sorts the files and sets owner, group and times in the archive (SOURCE_DATE_EPOCH is the commit time),
#   - compresses with gzip -n (no name and time in the header).
# The web app must be built before (make web). Without it the server binary has an empty web folder.
# Optional: AGENT_IMAGE_DIGEST (sha256:...) is put in the install script that the server serves (stamp.sh).
set -eu

[ $# -eq 4 ] || { echo "usage: build.sh VERSION GOOS GOARCH OUTDIR" >&2; exit 2; }
version=$1
goos=$2
goarch=$3
out=$4

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
mkdir -p "$out"
out=$(cd "$out" && pwd)

commit=$(git rev-parse --short=12 HEAD)
sde=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct HEAD)}
name="polipan_${version}_${goos}_${goarch}"
stage=$(mktemp -d)
script=server/internal/api/install-agent.sh
cp "$script" "$stage/install-agent.sh.orig"
cleanup() {
  cp "$stage/install-agent.sh.orig" "$root/$script"
  rm -rf "$stage"
}
trap cleanup EXIT

if [ -n "${AGENT_IMAGE_DIGEST:-}" ]; then
  sh deploy/release/stamp.sh "$script" AGENT_IMAGE_DIGEST="$AGENT_IMAGE_DIGEST" AGENT_IMAGE_REPO="${AGENT_IMAGE_REPO:-ghcr.io/olexandrdevelop/polipan-agent}"
fi

mod=github.com/polipan/polipan
mkdir -p "$stage/$name"
build() { # build PACKAGE-DIR VERSION-PACKAGE OUTPUT-NAME
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X $mod/$2.Version=$version -X $mod/$2.Commit=$commit" \
    -o "$stage/$name/$3" "$1"
}
build ./server/cmd/polipan-server server/internal/version polipan-server
build ./agent/cmd/polipan-agent agent/internal/version polipan-agent
build ./cli/cmd/polipan cli/internal/version polipan
cp LICENSE "$stage/$name/LICENSE"
cp README.md "$stage/$name/README.md"

# Files and folders in sorted order, with fixed owner and time, so the archive does not depend on this machine.
(cd "$stage" && tar --sort=name --mtime="@$sde" --owner=0 --group=0 --numeric-owner --format=gnu -cf - "$name") \
  | gzip -n -9 > "$out/$name.tar.gz"
echo "$out/$name.tar.gz"
