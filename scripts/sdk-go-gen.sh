#!/usr/bin/env bash
# sdk-go-gen.sh — the `sdk-go-gen` generate hook: regenerate sdk/go/gen from
# buf.gen.sdk-go.yaml, then drop the one thing that would tie the SDK module to
# forge.
#
# The public protos carry (forge.v1.method) / (forge.v1.service) options, so
# protoc-gen-go emits `_ ".../forge/pkg/forgepb"` into every file that has
# one. That blank import only registers forge/v1/forge.proto so the options
# decode as extensions; it forces every consumer of the SDK onto a forge
# module. Without it the descriptors are byte-identical: the options stay in
# the raw descriptor, an unlinked forge.proto import resolves to a placeholder,
# and a binary that links forge (the app) decodes the options exactly as
# before, because options are unmarshalled lazily against the global registry.
# A consumer that does not link forge simply never decodes them.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

scripts/proto-generate-atomic.sh buf.gen.sdk-go.yaml sdk/go/gen

for f in sdk/go/gen/reliant/v1/*.pb.go; do
  grep -v '^[[:space:]]*_ "github.com/reliant-labs/forge/pkg/forgepb"$' "$f" > "$f.tmp"
  mv "$f.tmp" "$f"
done

if grep -rn 'reliant-labs/forge' sdk/go/gen >&2; then
  echo "sdk-go-gen: sdk/go/gen still imports forge" >&2
  exit 1
fi
