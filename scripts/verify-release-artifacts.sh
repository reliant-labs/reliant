#!/usr/bin/env bash
# verify-release-artifacts.sh — prove a desktop release is downloadable, as a
# client sees it, before anyone calls it released.
#
# WHY. A green "Release Electron App" run has twice not meant "released":
# v1.8.1 failed late and the feeds kept serving 1.7.16 for a week, and v1.8.6's
# CDN purge died, so every feed served 1.8.5 from cache while R2 already held
# 1.8.6. latest-linux-arm64.yml was never purged at all. This script reads what
# downloads.reliantlabs.io actually serves, through the CDN, and fails on
# anything a client would trip over:
#
#   1. every installer and archive of every variant returns 200
#      (mac arm64/x64 dmg+zip, win x64/arm64 setup + x64 portable,
#      linux x86_64/arm64 AppImage + amd64/arm64 deb)
#   2. every update feed of the channel reads `version: <v>`
#      (<ch>-mac.yml, <ch>.yml (Windows), <ch>-linux.yml, <ch>-linux-arm64.yml)
#   3. every file a feed names returns 200 (what the updater downloads)
#
# Feeds are retried until --wait seconds pass, because a purge takes a moment
# to reach every edge. Installers are not: a missing one is missing.
#
# Usage: scripts/verify-release-artifacts.sh <vX.Y.Z> [--channel latest|alpha]
#                                            [--wait SECONDS] [--base URL]
# Exit:  0 all present · 1 something a client needs is missing or stale ·
#        2 usage error
#
# Run by release.yml's "Verify published release" job after Finalize, and by
# control-plane's scripts/verify-prod-release.sh after a prod promotion.
set -uo pipefail

usage() { sed -n '2,/^set -uo/p' "$0" | sed 's/^# \{0,1\}//; /^set -uo/d' >&2; exit 2; }

VERSION="${1:-}"
[ -n "$VERSION" ] || usage
shift
CHANNEL=latest
WAIT=600
BASE_URL="https://downloads.reliantlabs.io"
while [ $# -gt 0 ]; do
  case "$1" in
    --channel) CHANNEL="${2:?--channel needs a value}"; shift 2 ;;
    --wait) WAIT="${2:?--wait needs seconds}"; shift 2 ;;
    --base) BASE_URL="${2:?--base needs a URL}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "verify-release-artifacts: unknown argument: $1" >&2; exit 2 ;;
  esac
done
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || { echo "verify-release-artifacts: bad version '$VERSION' (want vX.Y.Z)" >&2; exit 2; }
case "$CHANNEL" in latest|alpha) ;; *) echo "verify-release-artifacts: --channel must be latest or alpha" >&2; exit 2 ;; esac
V="${VERSION#v}"
BASE_URL="${BASE_URL%/}"

fail=0
bad() { echo "FAIL  $*"; fail=1; }
ok() { echo "ok    $*"; }

# HEAD, never GET: an installer is ~300 MB. A transport error reads as 000.
code() { curl -sI -o /dev/null -w '%{http_code}' --max-time 30 "$1" 2>/dev/null || echo 000; }

echo "== installers and archives ($VERSION)"
for f in mac-arm64.dmg mac-arm64.zip mac-x64.dmg mac-x64.zip \
         win-x64.exe win-arm64.exe win-x64-portable.exe \
         linux-x86_64.AppImage linux-arm64.AppImage linux-amd64.deb linux-arm64.deb; do
  url="$BASE_URL/Reliant-$V-$f"
  c="$(code "$url")"
  if [ "$c" = 200 ]; then ok "$c $url"; else bad "$c $url"; fi
done

echo "== update feeds ($CHANNEL channel, waiting up to ${WAIT}s for the CDN)"
for feed in "$CHANNEL-mac.yml" "$CHANNEL.yml" "$CHANNEL-linux.yml" "$CHANNEL-linux-arm64.yml"; do
  url="$BASE_URL/$feed"
  deadline=$(( $(date +%s) + WAIT ))
  got=""
  while :; do
    body="$(curl -fsS --max-time 30 "$url" 2>/dev/null || true)"
    got="$(printf '%s\n' "$body" | sed -n 's/^version:[[:space:]]*//p' | head -1 | tr -d "'\"\r")"
    [ "$got" = "$V" ] && break
    [ "$(date +%s)" -ge "$deadline" ] && break
    sleep 20
  done
  if [ "$got" != "$V" ]; then
    bad "$feed reads version '${got:-<unreadable>}', want $V ($url)"
    continue
  fi
  ok "$feed version $V"
  # What the updater will actually download: every url/path the feed names.
  for name in $(printf '%s\n' "$body" | sed -n 's/^[[:space:]-]*\(url\|path\):[[:space:]]*//p' | tr -d "'\"\r" | sort -u); do
    case "$name" in http*) furl="$name" ;; *) furl="$BASE_URL/$name" ;; esac
    c="$(code "$furl")"
    if [ "$c" = 200 ]; then ok "  $c $furl"; else bad "  $feed names $furl -> $c"; fi
  done
done

if [ "$fail" -ne 0 ]; then
  echo "verify-release-artifacts: $VERSION is NOT fully published (see FAIL lines)."
  exit 1
fi
echo "verify-release-artifacts: $VERSION published: every variant downloadable, every $CHANNEL feed at $V."
