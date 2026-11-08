#!/usr/bin/env bash
# Regenerates gen/ — the sample frontend's generated TypeScript — from proto/.
#
#   PROTOC_GEN_ES=<a frontend>/node_modules/.bin/protoc-gen-es ./regen.sh
#
# buf compiles in a scratch copy, beside forge's vendored validate.proto, and
# only the sample's own modules are copied back: gen/buf/validate/validate_pb.ts
# is a hand-written stub (the real module is ~230KB, and the sample uses
# validate.proto only for options, which the embedded descriptors carry).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../../.." && pwd)
plugin=${PROTOC_GEN_ES:-$(command -v protoc-gen-es)}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cp -r "$here/proto" "$tmp/"
mkdir -p "$tmp/proto/buf/validate"
cp "$repo/internal/assets/proto/buf/validate/validate.proto" "$tmp/proto/buf/validate/"
printf 'version: v2\nmodules:\n  - path: proto\n' >"$tmp/buf.yaml"
sed "s#local: protoc-gen-es#local: $plugin#" "$here/buf.gen.yaml" >"$tmp/buf.gen.yaml"
(cd "$tmp" && buf generate)

for f in sample/v1/telemetry_pb.ts acme/v1/billing_pb.ts; do
  mkdir -p "$here/gen/$(dirname "$f")"
  cp "$tmp/gen/$f" "$here/gen/$f"
done
