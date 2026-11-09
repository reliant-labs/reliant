#!/usr/bin/env bash
# Installs the system packages Playwright's browsers need (what
# `playwright install --with-deps` installs) from a cache of the .deb files a
# previous run downloaded, and fills that cache when it is empty.
#
#   scripts/playwright-os-deps.sh <cache-dir> <browser>...
#
# Why: CI's E2E jobs spent their whole 15-minute budget inside
# `playwright install --with-deps` when the runner's Ubuntu mirror served
# ~100 KB/s (reliant main 74127457b, 2026-11-09; control-plane main-red #2372).
# WebKit alone pulls ~114 MB of GStreamer/GTK packages. The browsers were
# already cached (ci.yml "Restore Playwright browsers"); the packages were not.
#
# With a filled cache the install never touches the mirror, not even for
# `apt-get update`: `dpkg -i` installs the cached files in dependency order.
# That is sound only on the exact runner image whose missing packages the
# cache recorded, so ci.yml keys the cache on ImageOS + ImageVersion (and the
# Playwright lock). If the cached set does not install cleanly anyway, it
# falls back to the slow path, which repairs anything dpkg left half-done.
set -euo pipefail

[[ $# -ge 2 ]] || { echo "usage: $0 <cache-dir> <browser>..." >&2; exit 2; }
cache=$1
shift
debs="$cache/debs"
web="$(cd "$(dirname "${BASH_SOURCE[0]}")/../frontends/web" && pwd)"

sudo=()
[[ $(id -u) == 0 ]] || sudo=(sudo)

from_mirror() {
  # apt-get downloads into an archive dir of its own for this one install,
  # and keeps what it downloaded: an image may turn keeping off, or clean
  # /var/cache/apt/archives after every install (Docker images do).
  # Both paths can be overridden for tests (tools/test-affected), which run
  # the script against stub package managers without root.
  local conf=${PLAYWRIGHT_OS_DEPS_APT_CONF:-/etc/apt/apt.conf.d/99playwright-os-deps}
  local archives=${PLAYWRIGHT_OS_DEPS_ARCHIVES:-/var/cache/apt/playwright-os-deps}
  "${sudo[@]}" rm -rf "$archives"
  "${sudo[@]}" mkdir -p "$archives/partial"
  printf '%s\n' "Dir::Cache::Archives \"$archives/\";" \
    'APT::Keep-Downloaded-Packages "true";' \
    'Binary::apt-get::APT::Keep-Downloaded-Packages "true";' |
    "${sudo[@]}" tee "$conf" >/dev/null
  local status=0
  (cd "$web" && npx playwright install-deps "$@") || status=$?
  "${sudo[@]}" rm -f "$conf"
  [[ $status == 0 ]] || exit "$status"
  all_installed "$@" || exit 1
  # Recorded only after the install succeeded, so a cache never holds a
  # partial set. An empty set (the image already had everything) still
  # writes the marker, so the next run skips the mirror too.
  rm -rf "$debs"
  mkdir -p "$debs"
  if compgen -G "$archives/*.deb" >/dev/null; then
    cp "$archives"/*.deb "$debs/"
  fi
  "${sudo[@]}" rm -rf "$archives"
  printf '%s\n' "$@" >"$cache/browsers"
  echo "playwright-os-deps: installed from the mirror; cached $(find "$debs" -name '*.deb' | wc -l) packages in $debs"
}

# The packages Playwright installs for these browsers, from its own dry run
# (`... apt-get install -y --no-install-recommends <pkg>..."`).
required() {
  (cd "$web" && npx playwright install-deps --dry-run "$@") |
    sed -n 's/.*--no-install-recommends \([^"]*\)".*/\1/p' | tr ' ' '\n' | sed '/^$/d'
}

# Every required package is installed: the proof a cached set was complete,
# so a cache that recorded too little cannot pass for a hit.
all_installed() {
  local pkgs missing
  pkgs=$(required "$@")
  [[ -n $pkgs ]] || { echo "playwright-os-deps: could not read Playwright's package list" >&2; return 1; }
  # shellcheck disable=SC2086 # one word per package
  missing=$(dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' $pkgs 2>&1 | grep -v '^ii ' || true)
  [[ -z $missing ]] || { echo "playwright-os-deps: not installed: $missing" >&2; return 1; }
}

if [[ -f $cache/browsers && $(cat "$cache/browsers") == "$(printf '%s\n' "$@")" ]]; then
  shopt -s nullglob
  files=("$debs"/*.deb)
  shopt -u nullglob
  if [[ ${#files[@]} == 0 ]] || "${sudo[@]}" dpkg -i --skip-same-version "${files[@]}"; then
    if all_installed "$@"; then
      echo "playwright-os-deps: cache hit, ${#files[@]} cached packages installed without the mirror"
      exit 0
    fi
  fi
  # Repair (finish anything dpkg left half-configured) and install from the
  # mirror, without re-recording: this run downloads only what the cache
  # lacked, and ci.yml saves only on a cache miss anyway.
  echo "playwright-os-deps: the cache did not install every package; repairing from the mirror" >&2
  "${sudo[@]}" apt-get update
  "${sudo[@]}" apt-get -f install -y --no-install-recommends
  (cd "$web" && npx playwright install-deps "$@")
  all_installed "$@"
  exit
fi
from_mirror "$@"
