#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Checks the files of a release you downloaded (AC-OSS-03).
#
#   sh deploy/release/verify.sh [--repo OWNER/NAME] [--no-signature] [--images] TAG DIR
#
# DIR has the release files: SHA256SUMS, SHA256SUMS.sigstore.json and the files in the list.
# Steps:
#   1. cosign checks that the signature of SHA256SUMS was made by the release workflow of this repository
#      for this tag (keyless signing: the certificate names the workflow, no key is kept).
#   2. sha256sum checks every file against SHA256SUMS.
#   3. With --images: cosign checks the signature and the SBOM of every image in images.lock.
# --no-signature skips steps 1 and 3 (checksums only, no cosign needed). Use it only for a quick check.
set -eu

repo="polipan/polipan"
signature=1
images=""
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) repo=$2; shift 2 ;;
    --no-signature) signature=""; shift ;;
    --images) images=1; shift ;;
    -h|--help) sed -n '3,15p' "$0"; exit 0 ;;
    -*) echo "verify: unknown option: $1" >&2; exit 2 ;;
    *) break ;;
  esac
done
[ $# -eq 2 ] || { echo "usage: verify.sh [--repo OWNER/NAME] [--no-signature] [--images] TAG DIR" >&2; exit 2; }
tag=$1
dir=$2
[ -d "$dir" ] || { echo "verify: no such folder: $dir" >&2; exit 2; }
case "$tag" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "verify: the tag must look like v1.2.3: $tag" >&2; exit 2 ;;
esac

issuer="https://token.actions.githubusercontent.com"
identity="https://github.com/$repo/.github/workflows/release.yml@refs/tags/$tag"

if [ -n "$signature" ]; then
  command -v cosign >/dev/null 2>&1 || { echo "verify: cosign is needed (or use --no-signature)" >&2; exit 1; }
  echo "==> Checking the signature of SHA256SUMS"
  cosign verify-blob --bundle "$dir/SHA256SUMS.sigstore.json" \
    --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$dir/SHA256SUMS"
fi

echo "==> Checking the checksums"
(cd "$dir" && sha256sum -c SHA256SUMS)

if [ -n "$images" ]; then
  command -v cosign >/dev/null 2>&1 || { echo "verify: cosign is needed for --images" >&2; exit 1; }
  [ -f "$dir/images.lock" ] || { echo "verify: images.lock is not in $dir" >&2; exit 1; }
  while IFS='=' read -r name ref; do
    [ -n "$name" ] || continue
    case "$ref" in
      *@sha256:*) ;;
      *) echo "verify: $name in images.lock is not pinned by digest" >&2; exit 1 ;;
    esac
    echo "==> Checking the signature and the SBOM of $ref"
    cosign verify --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$ref" >/dev/null
    cosign verify-attestation --type spdxjson --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$ref" >/dev/null
  done < "$dir/images.lock"
fi
echo "==> OK: the files of $tag are the ones the release workflow made"
