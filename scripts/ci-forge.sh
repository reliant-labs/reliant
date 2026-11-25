#!/usr/bin/env bash
# Run the forge of THIS checkout: the CI scripts that record their test lanes
# (`forge test capture`, `forge test flakes gate|confirm`) dogfood the forge
# they test, not one installed on the machine.
#
#   scripts/ci-forge.sh <forge args...>
#
# The binary is built once into the checkout's git directory (never the work
# tree) and relinked only when its inputs change: `go build -o` leaves an
# up-to-date binary alone (~1s), where a fresh link is ~10-20s. Not `go run`:
# it reports every non-zero exit as 1, and the callers branch on forge's exit
# codes. A lock keeps concurrent callers (ci-premerge.sh runs the shard and
# the flake gate side by side) from writing the binary at once.
#
# CI_FORGE names a forge to run instead (a release binary, or a fixture's
# stub in internal/ciguard).
set -euo pipefail

if [[ -n "${CI_FORGE:-}" ]]; then
  exec "$CI_FORGE" "$@"
fi
root=$(git rev-parse --show-toplevel)
dir="$(git rev-parse --absolute-git-dir)/ci-forge"
mkdir -p "$dir"
build() { (cd "$root" && go build -o "$dir/forge" ./cmd/forge); }
if command -v flock >/dev/null 2>&1; then
  exec 9>"$dir/lock"
  flock 9
  build
  flock -u 9
  exec 9>&-
else
  build
fi
exec "$dir/forge" "$@"
