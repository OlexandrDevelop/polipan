#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Tests of the release scripts (AC-OSS-03, REQ-OSS-004). They need no network, no cosign and no Docker.
# With --build and Go installed, it also builds the binaries twice and compares them (reproducible build).
#
#   sh test/release/test.sh [--build]
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
pass=0
ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
# expect_fail DESCRIPTION COMMAND...: the command must fail.
expect_fail() { d=$1; shift; if "$@" >/dev/null 2>&1; then fail "$d: the command passed but must fail"; else ok "$d"; fi; }

digest="sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

# ---- stamp.sh ----
printf 'a="@NAME@"\nb="@NAME@"\n' > "$tmp/f"
sh deploy/release/stamp.sh "$tmp/f" NAME="$digest"
[ "$(grep -c "$digest" "$tmp/f")" -eq 2 ] && ok "stamp replaces every placeholder" || fail "stamp did not replace both placeholders"
printf 'a="@NAME@"\n' > "$tmp/f"
expect_fail "stamp refuses a value with a quote" sh deploy/release/stamp.sh "$tmp/f" NAME='x";rm -rf /;"'
expect_fail "stamp refuses a value with a space" sh deploy/release/stamp.sh "$tmp/f" NAME='a b'
expect_fail "stamp refuses a missing placeholder" sh deploy/release/stamp.sh "$tmp/f" OTHER=x
expect_fail "stamp refuses a bad name" sh deploy/release/stamp.sh "$tmp/f" 'na me=x'

# ---- the install scripts ----
# The placeholders must be in the sources, and a stamped script must still be valid shell.
for f in server/internal/api/install-agent.sh deploy/install.sh; do
  grep -q '@AGENT_IMAGE_DIGEST@' "$f" || fail "$f has no placeholder for the agent image digest"
done
cp server/internal/api/install-agent.sh "$tmp/install-agent.sh"
sh deploy/release/stamp.sh "$tmp/install-agent.sh" AGENT_IMAGE_DIGEST="$digest" AGENT_IMAGE_REPO=ghcr.io/polipan/polipan-agent
sh -n "$tmp/install-agent.sh" && ok "the stamped agent install script is valid shell" || fail "bad shell after stamping"
grep -q "stamped_digest=\"$digest\"" "$tmp/install-agent.sh" && ok "the agent install script holds the pinned digest" || fail "digest not in the script"
! grep -q '@AGENT_IMAGE_[A-Z]*@' "$tmp/install-agent.sh" && ok "no placeholder is left in the stamped agent script" || fail "placeholder left"
cp deploy/install.sh "$tmp/install.sh"
sh deploy/release/stamp.sh "$tmp/install.sh" VERSION=v1.2.3 SERVER_IMAGE_DIGEST="$digest" AGENT_IMAGE_DIGEST="$digest" \
  COMPOSE_SHA256=aa FIREWALL_SHA256=bb
sh -n "$tmp/install.sh" && ok "the stamped install script is valid shell" || fail "bad shell after stamping"
# The unstamped scripts run in a development build: they pin nothing, and say so.
grep -q 'is not a release build' server/internal/api/install-agent.sh && ok "the agent script warns in a development build" || fail "no warning"
# The default agent image of a release script is a digest reference, never a tag.
grep -q 'agent_image="$stamped_repo@$stamped_digest"' server/internal/api/install-agent.sh && ok "the release agent image is a digest reference" || fail "agent image is not pinned"
grep -q 'cosign verify' deploy/install.sh && grep -q 'cosign verify' server/internal/api/install-agent.sh && ok "both install scripts verify the signature" || fail "no cosign verify"
grep -q 'check_sha256 "$dir/docker-compose.yml"' deploy/install.sh && ok "install.sh checks the downloaded files" || fail "no checksum check"

# ---- images and actions are pinned ----
unpinned=$(grep -hE '^FROM ' deploy/docker/*.Dockerfile | grep -v '@sha256:' | grep -v 'FROM scratch' || true)
[ -z "$unpinned" ] && ok "every base image of the Dockerfiles is pinned by digest" || fail "base images without digest: $unpinned"
unpinned=$(grep -hE '^\s+image:' deploy/compose/docker-compose.yml | grep -v '@sha256:' | grep -v 'POLIPAN_SERVER_IMAGE' || true)
[ -z "$unpinned" ] && ok "every image of the compose file is pinned by digest" || fail "compose images without digest: $unpinned"
unpinned=$(grep -rhE '^\s*-?\s*uses:' .github | grep -vE 'uses:\s*\./' | grep -vE '@[0-9a-f]{40}(\s|$)' || true)
[ -z "$unpinned" ] && ok "every GitHub action is pinned by a commit SHA" || fail "actions without a SHA: $unpinned"

# ---- checksums.sh and verify.sh (checksums only: cosign is not needed) ----
mkdir "$tmp/rel"
printf 'one\n' > "$tmp/rel/polipan_v1.0.0_linux_amd64.tar.gz"
printf 'two\n' > "$tmp/rel/install.sh"
sh deploy/release/checksums.sh "$tmp/rel" >/dev/null
[ "$(wc -l < "$tmp/rel/SHA256SUMS")" -eq 2 ] && ok "SHA256SUMS lists the files" || fail "SHA256SUMS has the wrong lines"
sort -c -k2 "$tmp/rel/SHA256SUMS" && ok "SHA256SUMS is sorted by name" || fail "not sorted"
sh deploy/release/verify.sh --no-signature v1.0.0 "$tmp/rel" >/dev/null && ok "verify passes for the right files" || fail "verify failed for good files"
printf 'evil\n' > "$tmp/rel/install.sh"
expect_fail "verify catches a changed file" sh deploy/release/verify.sh --no-signature v1.0.0 "$tmp/rel"
expect_fail "verify refuses a tag that is not a version" sh deploy/release/verify.sh --no-signature main "$tmp/rel"
# Without --no-signature the check needs the signature file and cosign: it must not pass without them.
expect_fail "verify does not pass without a signature" env PATH=/nonexistent /bin/sh deploy/release/verify.sh v1.0.0 "$tmp/rel"

# ---- preflight.sh ----
expect_fail "preflight refuses a bad tag" sh deploy/release/preflight.sh 1.0
expect_fail "preflight refuses a tag with a build suffix" sh deploy/release/preflight.sh v1.0.0+build

# ---- the release workflow ----
w=.github/workflows/release.yml
[ -f "$w" ] || fail "$w is missing"
grep -q 'id-token: write' "$w" && ok "the release workflow can sign keyless" || fail "no id-token permission"
a=.github/actions/image/action.yml
grep -q 'cosign sign-blob' "$w" && grep -q 'cosign sign --yes' "$a" && grep -q 'cosign attest' "$a" && ok "the workflow signs the checksums, and signs and attests every image" || fail "workflow does not sign"
grep -q 'verify.sh' "$w" && ok "the workflow verifies its own release before it publishes" || fail "no self check"
! grep -qE 'git push|docker push|gh release delete' "$w" && ok "the workflow only publishes what the tag push started" || fail "unexpected push in the workflow"

# ---- reproducible build (needs Go and the repository as a git checkout) ----
if [ "${1:-}" = "--build" ]; then
  command -v go >/dev/null 2>&1 || fail "--build needs go"
  sh deploy/release/build.sh v0.0.0-test linux amd64 "$tmp/b1" >/dev/null
  sh deploy/release/build.sh v0.0.0-test linux amd64 "$tmp/b2" >/dev/null
  a=$(sha256sum "$tmp/b1"/*.tar.gz | cut -d ' ' -f 1)
  b=$(sha256sum "$tmp/b2"/*.tar.gz | cut -d ' ' -f 1)
  [ "$a" = "$b" ] && ok "two builds of the same commit give the same archive ($a)" || fail "the builds differ: $a $b"
  git diff --quiet -- server/internal/api/install-agent.sh && ok "the build leaves the install script as it was" || fail "build changed the script"
fi

printf '%d checks passed\n' "$pass"
