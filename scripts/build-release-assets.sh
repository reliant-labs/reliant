#!/usr/bin/env bash
# build-release-assets.sh — cross-build the forge CLI for every release target
# and write the archives plus checksums.txt that install.sh, install.ps1, the npm
# shim and the Homebrew formula download.
#
#   scripts/build-release-assets.sh vX.Y.Z <outdir>
#
# Output, in <outdir>:
#   forge_<os>_<arch>.tar.gz   one per target; holds `forge` (`forge.exe` on
#                              windows), LICENSE and NOTICE at the archive root
#   checksums.txt              `<sha256>  <file>` for every archive (sha256sum format)
#
# The names carry no version on purpose: GitHub's /releases/latest/download/<name>
# then resolves without an API call, and every installer derives a name from the
# OS and CPU alone. install.sh, install.ps1, npm/ and the Homebrew formula all
# depend on this naming — change them together.
#
# Windows ships a .tar.gz as well (not a .zip): one archive format means one
# extractor in the npm shim, and tar.exe has shipped with Windows 10 since 1803.
#
# forge is cgo-free (README, "Install"), so every target cross-compiles from any
# host with a plain Go toolchain.
#
# Environment:
#   FORGE_RELEASE_TARGETS  space-separated GOOS/GOARCH list to build
#                          (default: the six below). Tests use it to build one.
#   FORGE_RELEASE_COMMIT   commit to stamp (default: git rev-parse HEAD)
#   FORGE_RELEASE_DATE     build date to stamp (default: today, UTC)
set -euo pipefail

usage() {
  echo "usage: $0 vX.Y.Z <outdir>" >&2
  exit 2
}

[ $# -eq 2 ] || usage
VERSION="$1"
OUT="$2"
if ! echo "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "error: version must look like vX.Y.Z (got: $VERSION)" >&2
  exit 1
fi

TARGETS="${FORGE_RELEASE_TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}"
COMMIT="${FORGE_RELEASE_COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
DATE="${FORGE_RELEASE_DATE:-$(date -u +%Y-%m-%d)}"

# Build the checkout the caller is in (release-forge.sh --repo cd's into its
# target first), not the checkout this script lives in.
cd "$(git rev-parse --show-toplevel)"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
rm -f "$OUT"/forge_*.tar.gz "$OUT/checksums.txt"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

# macOS tar would add AppleDouble ._* members for files with extended attributes.
export COPYFILE_DISABLE=1

# build_one <goos> <goarch>: one archive, its log in $STAGE/<goos>-<goarch>.log.
# Every step propagates its own failure (`|| return`): build_one runs as the left
# operand of `&&` below, where bash disables `set -e` for the whole function, so
# without them a failed go build, cp or chmod is ignored whenever tar succeeds.
build_one() {
  local goos=$1 goarch=$2 bin=forge name dir
  if [ "$goos" = "windows" ]; then bin="forge.exe"; fi
  name="forge_${goos}_${goarch}.tar.gz"
  dir="$STAGE/$goos-$goarch"
  mkdir -p "$dir" || return
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath \
    -ldflags "-s -w -X main.Version=$VERSION -X main.GitCommit=$COMMIT -X main.BuildDate=$DATE" \
    -o "$dir/$bin" ./cmd/forge || return
  cp LICENSE NOTICE "$dir/" || return
  chmod 755 "$dir/$bin" || return
  tar -C "$dir" -czf "$OUT/$name" "$bin" LICENSE NOTICE || return
}

# The targets build CONCURRENTLY, at most $FORGE_RELEASE_JOBS at once (default:
# all of them). Each target is a separate compile of the whole program (a
# different GOOS/GOARCH shares no build-cache entry with another), so one after
# another the six took ~25 minutes on a loaded shared host (v1.0.5) while the
# release waited; side by side the wall time is the slowest target's. A target
# that fails fails the script after every build has finished, naming it, with
# its log; checksums.txt is written only when all succeeded, in $TARGETS order.
jobs_max="${FORGE_RELEASE_JOBS:-0}"
pids=()
for target in $TARGETS; do
  goos="${target%/*}"
  goarch="${target#*/}"
  if [ "$jobs_max" -gt 0 ] && [ "${#pids[@]}" -ge "$jobs_max" ]; then
    # Bounded: wait for the oldest before starting another.
    wait "${pids[0]}" || true
    pids=("${pids[@]:1}")
  fi
  echo "→ building forge_${goos}_${goarch}.tar.gz"
  (build_one "$goos" "$goarch" && touch "$STAGE/$goos-$goarch.ok") >"$STAGE/$goos-$goarch.log" 2>&1 &
  pids+=($!)
done
wait || true

: >"$OUT/checksums.txt"
failed=0
for target in $TARGETS; do
  goos="${target%/*}"
  goarch="${target#*/}"
  name="forge_${goos}_${goarch}.tar.gz"
  if [ ! -f "$STAGE/$goos-$goarch.ok" ]; then
    echo "error: building $name failed:" >&2
    sed 's/^/  /' "$STAGE/$goos-$goarch.log" >&2
    failed=1
    continue
  fi
  printf '%s  %s\n' "$(sha256_of "$OUT/$name")" "$name" >>"$OUT/checksums.txt"
done
if [ "$failed" = 1 ]; then
  rm -f "$OUT"/forge_*.tar.gz "$OUT/checksums.txt"
  exit 1
fi

echo "→ wrote $(wc -l <"$OUT/checksums.txt" | tr -d ' ') archives and checksums.txt to $OUT"
