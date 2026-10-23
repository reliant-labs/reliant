#!/usr/bin/env bash
# pre-commit hook: a commit that edits proto/forge/v1/forge.proto must carry
# what is derived from it, so Verify Generated Code does not go red after the
# merge.
#
#   1. internal/assets/proto/forge/v1/forge.proto, the copy embedded in the
#      binary and vendored into every scaffolded project, is byte-identical.
#   2. pkg/forgepb/forge.pb.go is what buf.gen.yaml generates from it now.
#
# WHY. On 2026-10-20 four pull requests (#957, #961, #965, #971) edited
# forge.proto without regenerating, and Verify Generated stayed red on main,
# and so on every open pull request, for most of a day. That check runs the
# whole `forge generate` in CI, minutes after the push; this one regenerates
# the single file a proto edit can stale, in about a second, before the
# commit exists. gen/forge_descriptor.json is not checked: since #984 forge's
# own descriptor stamps no source (`"sources": {}`), so a proto edit cannot
# stale it.
#
# The protoc-gen-go and protoc version lines of the header are compared out:
# a plugin a patch away from go.mod's pin would fail every commit here over a
# line Verify Generated judges on its own (it installs the pinned plugin).
#
# CI skips this hook (ci.yml checks, SKIP): it needs protoc-gen-go, and
# its verify-generated step already runs the complete check there.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

src=proto/forge/v1/forge.proto
asset=internal/assets/proto/forge/v1/forge.proto
pb=pkg/forgepb/forge.pb.go
remedy="run \`forge generate\` and \`cp $src $asset\`, then stage the result"
fail=0

if ! cmp -s "$src" "$asset"; then
  echo "$asset differs from $src: the binary would embed, and scaffold, the old grammar." >&2
  fail=1
fi

for tool in buf protoc-gen-go; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "$tool is not on PATH, so $pb cannot be checked against $src. Install it with \`forge tools install\`." >&2
    exit 1
  fi
done

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
buf generate --template buf.gen.yaml -o "$tmp"

strip_versions() { grep -vE '^//[[:space:]]+protoc(-gen-go)?[[:space:]]' "$1"; }
if ! diff -q <(strip_versions "$tmp/$pb") <(strip_versions "$pb") >/dev/null; then
  echo "$pb is stale: it is not what buf.gen.yaml generates from $src." >&2
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo "To fix: $remedy." >&2
fi
exit "$fail"
