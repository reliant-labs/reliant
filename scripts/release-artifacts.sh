#!/usr/bin/env bash
# release-artifacts.sh — build and publish a reliant release's artifacts from a
# maintainer's machine.
#
# THIS IS THE ONLY PUBLISH PATH. .github/workflows/release.yml (the Electron
# app → Cloudflare R2, the GitHub Release, the Homebrew dispatch) and
# .github/workflows/build-images.yml (the service image → GAR) are DELETED, not
# disabled: releases are local-only and CI runs checks only. There is nothing
# to dispatch and nothing to wait for.
#
# ── The two-phase release is unchanged ──────────────────────────────────────
#
#   1. ./scripts/release.sh [patch|minor|major|prerelease]   # open the bump PR
#   2. (merge it)
#   3. ./scripts/release-tag.sh                              # tag main
#   4. THIS SCRIPT                                           # build + publish
#
# Step 4 used to happen by itself, because the tag push triggered the
# workflows. It no longer does. A tag now records what a release IS; publishing
# is a separate, deliberate act.
#
# ── Usage ───────────────────────────────────────────────────────────────────
#
#   scripts/release-artifacts.sh desktop  [vX.Y.Z]   # electron-builder → R2
#   scripts/release-artifacts.sh image    [vX.Y.Z]   # service image → GAR
#   scripts/release-artifacts.sh github   [vX.Y.Z]   # the GitHub Release page
#   scripts/release-artifacts.sh homebrew [vX.Y.Z]   # nudge the tap
#
# With no version it reads the tag from electron/package.json, so the common
# case is a bare `scripts/release-artifacts.sh desktop`.
#
# DRY_RUN=1 builds everything and publishes nothing.
#
# ── Why `desktop` is per-platform and not one command ───────────────────────
#
# Because the signing identities are physical. macOS needs a Mac holding the
# Developer ID certificate AND an App Store Connect key to notarize; Windows
# needs the Azure Trusted Signing service principal. Neither cross-compiles,
# and a script that pretended otherwise would produce unsigned artifacts that
# Gatekeeper and SmartScreen reject — which looks like a successful release
# right up until a user downloads it.
#
# So run `desktop` once on each platform you are shipping. It builds for the
# host platform only, and it says which one it did. Shipping a subset is a
# legitimate outcome (v1.7.14 shipped Windows and Linux without macOS); the R2
# channel feeds encode per-platform availability, so a missing platform
# degrades rather than breaks. Say so in the release notes.
#
# ── Credentials, by phase ───────────────────────────────────────────────────
#
#   desktop   R2:      AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, R2_ENDPOINT
#             macOS:   APPLE_API_KEY (the .p8 CONTENTS), APPLE_API_KEY_ID,
#                      APPLE_API_ISSUER, APPLE_TEAM_ID, CSC_LINK,
#                      CSC_KEY_PASSWORD
#                      (APPLE_ID + APPLE_APP_SPECIFIC_PASSWORD are the
#                      fallback; the API key is preferred because it belongs
#                      to the TEAM, not a person — v1.7.14 lost macOS when the
#                      account holder changed and notarization began 403ing
#                      with "a required agreement is missing or has expired")
#             Windows: AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET,
#                      AZURE_TRUSTED_SIGNING_* (three)
#             telemetry (optional): SENTRY_DSN, STATSIG_CLIENT_KEY. Absent is
#                      fine and leaves telemetry inert.
#   image     a gcloud session with push access to prod GAR
#   github    gh, authenticated
#   homebrew  HOMEBREW_TAP_TOKEN
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

DRY_RUN="${DRY_RUN:-}"

# prod GAR. The same registry control-plane's deploy/kcl/{prod,preprod}/main.k
# pulls the reliant services from. GHCR was retired; do not revive it.
GAR_PROD="us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod"

log()  { printf '\n== %s\n' "$*"; }
die()  { printf 'release-artifacts: %s\n' "$*" >&2; exit 1; }

phase="${1:-}"
version="${2:-}"

case "$phase" in
  desktop|image|github|homebrew) ;;
  *) die "usage: $0 desktop|image|github|homebrew [vX.Y.Z]" ;;
esac

if [ -z "$version" ]; then
  version="v$(node -p "require('./electron/package.json').version")"
fi
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$ ]] ||
  die "version '$version' is not a vX.Y.Z tag"
bare="${version#v}"

# The tag must already exist and be the thing being published. A release built
# from an untagged tree cannot be reproduced, and a tag created afterwards
# would describe a different commit.
git rev-parse -q --verify "refs/tags/$version" >/dev/null ||
  die "tag $version does not exist — run ./scripts/release-tag.sh first"

# A prerelease tag ships on the alpha channel with the alpha electron-builder
# config; a stable tag ships on latest. Derived from the tag exactly as
# release.yml's prepare job derived it, so the channel cannot disagree with
# the version.
if [[ "$bare" == *-* ]]; then
  config=electron-builder.alpha.js
  channel=alpha
else
  config=electron-builder.js
  channel=latest
fi

log "release $version (channel: $channel, config: $config)${DRY_RUN:+ [DRY RUN]}"

# ── desktop ─────────────────────────────────────────────────────────────────
do_desktop() {
  local os platform
  os="$(uname -s)"
  case "$os" in
    Darwin) platform=mac   ;;
    Linux)  platform=linux ;;
    MINGW*|MSYS*|CYGWIN*) platform=win ;;
    *) die "unsupported build host '$os' — run this on macOS, Linux or Windows" ;;
  esac
  log "building the $platform desktop artifacts on this host"

  for var in AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY R2_ENDPOINT; do
    [ -n "${!var:-}" ] || die "$var is required to publish to R2"
  done
  export AWS_REGION="${AWS_REGION:-auto}"

  if [ "$platform" = mac ]; then
    [ -n "${CSC_LINK:-}" ] || die "CSC_LINK is required to SIGN the macOS app"
    if [ -z "${APPLE_API_KEY:-}" ] && [ -z "${APPLE_ID:-}" ]; then
      die "no notarization identity: set APPLE_API_KEY/_ID/_ISSUER (preferred) or APPLE_ID + APPLE_APP_SPECIFIC_PASSWORD"
    fi
  fi
  if [ "$platform" = win ] && [ -z "${AZURE_CLIENT_ID:-}" ]; then
    die "AZURE_CLIENT_ID (Trusted Signing) is required to sign the Windows app"
  fi

  log "building the backend binaries for $platform"
  VERSION="$bare" ./scripts/build-electron.sh "$platform"

  ( cd electron && npm ci --legacy-peer-deps --prefer-offline )

  # The endpoints the packaged app talks to come from electron/release.config.json
  # (generated from control-plane's KCL) via with-release-config.mjs, which is
  # the same one file CI read with jq. Nothing here restates a URL: a hardcoded
  # endpoint is a second declaration that drifts, and that is exactly how v1.7.5
  # shipped with no control-plane URL.
  local publish_flag=always
  [ -n "$DRY_RUN" ] && publish_flag=never

  # ONE wrapper around BOTH steps. with-release-config.mjs expands
  # release.config.json's .main into the environment that
  # generate-build-config.mjs reads, and its .vite into the environment
  # `vite build` reads — so the main-process endpoints and the renderer's must
  # be produced under the same expansion or they can disagree.
  log "electron-builder --$platform --publish $publish_flag"
  ( cd electron && node scripts/with-release-config.mjs sh -c \
      "npm run build:config && npx electron-builder --config '$config' '--$platform' --publish '$publish_flag'" )

  [ -n "$DRY_RUN" ] && { log "DRY RUN — built into electron/dist, published nothing"; return; }

  # Verify the artifacts are actually FETCHABLE rather than trusting a clean
  # exit from electron-builder. The channel feed is what the auto-updater
  # reads, so it is the one file whose absence silently breaks every existing
  # install.
  log "verifying the published channel feed"
  local feed="https://downloads.reliantlabs.io/${channel}-${platform}.yml"
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 "$feed")"
  printf '  %s -> %s\n' "$feed" "$code"
  [ "$code" = 200 ] || die "channel feed $feed is not being served (HTTP $code)"
}

# ── image ───────────────────────────────────────────────────────────────────
do_image() {
  command -v gcloud >/dev/null 2>&1 || die "gcloud is required to push to prod GAR"
  log "configuring docker for prod GAR"
  gcloud auth configure-docker us-central1-docker.pkg.dev --quiet

  # linux/amd64,linux/arm64 — both, because prod nodes and the daemon cluster
  # are not the same architecture. A single-arch push produces pods that
  # CrashLoopBackOff with "exec format error", which reads like an application
  # fault rather than a build one.
  local tags=(-t "$GAR_PROD/reliant:$version" -t "$GAR_PROD/reliant:latest")
  if [ -n "$DRY_RUN" ]; then
    log "DRY RUN — would build linux/amd64,linux/arm64 and push ${tags[*]}"
    docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile "${tags[@]}" .
    return
  fi

  log "building and pushing the reliant image ($version)"
  docker buildx build \
    --platform linux/amd64,linux/arm64 \
    -f Dockerfile \
    --build-arg "VERSION=$bare" \
    "${tags[@]}" \
    --push \
    .

  # The interpreter check build-images.yml ran. A dynamically-linked binary in
  # a distroless-ish image fails at exec with a message that names neither the
  # image nor the missing loader.
  log "verifying image interpreters"
  ./scripts/check-image-interp.sh "$GAR_PROD/reliant:$version"
}

# ── github ──────────────────────────────────────────────────────────────────
do_github() {
  command -v gh >/dev/null 2>&1 || die "gh is required to publish the Release"

  if gh release view "$version" >/dev/null 2>&1; then
    log "Release $version already exists — leaving it alone."
    return
  fi
  if [ -n "$DRY_RUN" ]; then
    log "DRY RUN — would create the GitHub Release for $version"
    python3 scripts/release-notes-body.py "$version" || true
    return
  fi

  # --generate-notes produces the commit/PR list by diffing against the
  # previous release; --notes-file prepends the human summary from
  # docs/data/releases/<tag>.yaml — the same source the Mintlify changelog
  # renders — so the prose leads and the mechanical list follows. If that file
  # is absent the Release is still created with generated notes only, which is
  # strictly better than no Release.
  python3 scripts/release-notes-body.py "$version" > /tmp/release-body.md || true

  # --verify-tag refuses to invent a tag that does not exist, so a mistyped
  # version fails here rather than creating a Release pointing at nothing.
  local args=("$version" --title "$version" --verify-tag --generate-notes)
  if [ -s /tmp/release-body.md ]; then
    args+=(--notes-file /tmp/release-body.md)
  else
    log "docs/data/releases/$version.yaml not found — publishing with generated notes only."
  fi
  gh release create "${args[@]}"
  log "published https://github.com/reliant-labs/reliant/releases/tag/$version"
}

# ── homebrew ────────────────────────────────────────────────────────────────
do_homebrew() {
  [ -n "${HOMEBREW_TAP_TOKEN:-}" ] || die "HOMEBREW_TAP_TOKEN is required to nudge the tap"
  if [ -n "$DRY_RUN" ]; then
    log "DRY RUN — would dispatch release-published to homebrew-reliant for $bare"
    return
  fi
  log "dispatching release-published to reliant-labs/homebrew-reliant ($bare)"
  local code
  code="$(curl -sS -o /tmp/hb.json -w '%{http_code}' -X POST \
    -H "Accept: application/vnd.github.v3+json" \
    -H "Authorization: token ${HOMEBREW_TAP_TOKEN}" \
    -H "Content-Type: application/json" \
    https://api.github.com/repos/reliant-labs/homebrew-reliant/dispatches \
    -d "{\"event_type\":\"release-published\",\"client_payload\":{\"version\":\"$bare\"}}")"
  [ "$code" = 204 ] || die "Homebrew dispatch failed (HTTP $code): $(cat /tmp/hb.json)"
  log "Homebrew dispatch succeeded"
}

case "$phase" in
  desktop)  do_desktop ;;
  image)    do_image ;;
  github)   do_github ;;
  homebrew) do_homebrew ;;
esac
