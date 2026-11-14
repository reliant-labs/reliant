#!/usr/bin/env bash
# release-sourcemaps.sh — file the desktop renderer's source maps so its browser
# errors symbolicate (reliant#2535).
#
#   release-sourcemaps.sh <version> <web-dist>
#
# The packaged app reports its renderer errors as forge frontend `desktop`, with
# service.version = the release version, into tenant zero's prod env (project
# control-plane). control-plane's issues worker resolves a stack through the maps
# filed for exactly that (project, frontend, version). With none filed it falls
# back to keys made of the content-hashed bundle name and minified function names,
# which change every build, so each desktop release re-opened every browser issue.
#
# This runs `forge frontend sourcemaps push` against deploy/kcl/desktop, which names
# the control plane and the credential (RELIANT_DESKTOP_SOURCEMAPS_TOKEN). The maps
# are filed as <registry>/<org>/control-plane/desktop-sourcemaps:<version>. <web-dist>
# is not modified.
#
# THE CREDENTIAL is registry-only: an org automation token carrying sourcemaps:write
# and nothing else (forge#2059, control-plane#2493). The registry grants it push on
# `*-sourcemaps` repositories only, and the control plane refuses it on every RPC,
# so it cannot ask which org it belongs to: RELIANT_DESKTOP_SOURCEMAPS_ORG names the
# org (forge#2060's --org), and must be the token's own org (any other org's
# repositories are refused by the registry, so a wrong value files nothing).
#
# Exits 0 without filing anything when <version> is not a release (vX.Y.Z[-pre]), or
# the credential or the org is unset; each says why. Fails when the renderer does not report
# <version> (maps filed under any other id are never looked up), when the push
# fails, or when the maps land anywhere but control-plane's subtree (a forge that
# ignores --project: forge#2055).
#
# FORGE overrides the forge command (the self-test's fake). The default is the
# forge this project pins (forge.yaml's forge_version), never one from PATH.
set -euo pipefail

die() { echo "::error::$*" >&2; exit 1; }

[ $# -eq 2 ] || die "usage: release-sourcemaps.sh <version> <web-dist>"
version="$1" dist="$2"
frontend=desktop project=control-plane env=desktop

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
  echo "Not a release version ('$version'): no source maps filed."
  exit 0
fi
id="${version#v}"
reported="$(jq -r '.version // empty' "$dist/version.json" 2>/dev/null)" || true
[ "$reported" = "$id" ] || die "the renderer in $dist reports version '${reported:-<none>}', not $id: its maps would be filed where nothing looks"

if [ -z "${RELIANT_DESKTOP_SOURCEMAPS_TOKEN:-}" ]; then
  echo "::warning::RELIANT_DESKTOP_SOURCEMAPS_TOKEN is not set: the desktop renderer's source maps for $id are NOT filed, and its errors will not symbolicate"
  exit 0
fi
org="${RELIANT_DESKTOP_SOURCEMAPS_ORG:-}"
if [ -z "$org" ]; then
  echo "::warning::RELIANT_DESKTOP_SOURCEMAPS_ORG is not set: the desktop renderer's source maps for $id are NOT filed (a registry-only token cannot look its org up)"
  exit 0
fi

if [ -z "${FORGE:-}" ]; then
  pin="$(sed -n 's/^forge_version:[[:space:]]*//p' forge.yaml | tr -d "\"' \t")"
  [ -n "$pin" ] || die "forge.yaml names no forge_version"
  FORGE="go run github.com/reliant-labs/forge/cmd/forge@$pin"
fi

# shellcheck disable=SC2086 # FORGE is a command line
out="$($FORGE frontend sourcemaps push "$dist" --frontend "$frontend" --build-id "$id" \
  --env "$env" --project "$project" --org "$org" 2>&1)" || { printf '%s\n' "$out"; die "filing the desktop renderer's source maps for $id failed"; }
printf '%s\n' "$out"
grep -Eq "filed as [^ ]+/$org/$project/$frontend-sourcemaps:$id " <<< "$out" ||
  die "the maps for $id were not filed under $org/$project/$frontend-sourcemaps, where the issues worker looks (a forge without forge#2055 ignores --project, one without forge#2060 ignores --org)"
echo "Filed the desktop renderer's source maps for $id."
