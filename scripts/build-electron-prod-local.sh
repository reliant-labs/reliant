#!/usr/bin/env bash
# Build a LOCAL macOS Electron app configured exactly like a production
# release, without Developer ID signing or notarization.
#
# WHAT THIS IS FOR. Testing the packaged-app code paths — protocol
# registration, the baked-in endpoints, BackendManager spawning the real
# daemon, the single-instance lock — against the REAL production backend, on a
# machine that does not hold the release certificate.
#
# WHAT IT IS NOT. Not a distributable artifact: unsigned and un-notarized, so
# Gatekeeper will reject it anywhere but the machine that built it. Releases
# still go through .github/workflows/release.yml.
#
# ── The config comes from the SAME source CI uses ──────────────────────
#
# electron/release.config.json is generated from control-plane's KCL
# (.github/scripts/sync-release-config.mjs) and is the single source of truth
# for prod endpoints. The release workflow jq's its two blocks into the
# environment; this script does the identical thing, so a local build cannot
# drift from what ships:
#
#   .vite  -> VITE_* read directly by `vite build` (no .env file is written)
#   .main  -> consumed by generate-build-config.mjs, which writes
#             electron/src/build-config.js (the main-process endpoints)
#
# Secrets (Sentry DSN, Statsig key) are NOT baked in: they come from GitHub
# Actions secrets in CI and are simply absent here. Telemetry is inert in a
# local build, which is what you want anyway.
#
# ── Why the app is re-signed at the end ────────────────────────────────
#
# THIS STEP IS LOAD-BEARING; without it the app is SIGKILLed at launch.
#
# electron-builder.local.js sets identity:null so no Developer ID signing is
# attempted. But @electron/fuses runs AFTER packaging and REWRITES BYTES INSIDE
# the Electron Framework binary to flip the fuse flags. That invalidates
# whatever signature the framework shipped with, and because signing is off,
# nothing re-signs it. The kernel then kills the process on the first fuse read
# — before any JS executes — with:
#
#   Termination Reason: Namespace CODESIGNING, Code 2 Invalid Page
#   Thread 0: electron::fuses::IsRunAsNodeEnabled()
#
# which looks like a silent startup hang and is not one. Re-signing ad-hoc
# (inside-out: nested frameworks and helper .apps first, then the outer bundle)
# makes the modified pages valid again.
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

RELEASE_CONFIG="electron/release.config.json"
BUILDER_CONFIG="electron/electron-builder.local.js"

for f in "$RELEASE_CONFIG" "$BUILDER_CONFIG"; do
  [[ -f "$f" ]] || { echo "missing $f" >&2; exit 1; }
done
command -v jq >/dev/null || { echo "jq is required (brew install jq)" >&2; exit 1; }

echo "==> Exporting prod config from $RELEASE_CONFIG"
# Same two jq expressions the release workflow uses.
#
# THESE MUST OVERRIDE THE CALLING SHELL, not defer to it. A dev shell here
# commonly already exports RELIANT_API_URL / RELIANT_SERVER_URL /
# RELIANT_GATEWAY_URL (and sometimes VITE_*) pointing at a LOCAL stack —
# scripts/dev.sh and .dev-ports.sh both do it. `vite build` reads VITE_*
# straight from the environment, so an inherited value would silently bake
# localhost into a build labelled "prod" and the mistake would only surface as
# a packaged app talking to a port on the tester's laptop.
#
# CI never hits this because a fresh runner has none of them set. Locally it is
# the default case, so each key is assigned explicitly with `export k=v` rather
# than sourced into whatever the shell already had.
while IFS='=' read -r k v; do
  export "$k=$v"
done < <(jq -er '(.vite + .main) | to_entries[] | "\(.key)=\(.value)"' "$RELEASE_CONFIG")

echo "    VITE_API_URL               = ${VITE_API_URL:-(unset)}"
echo "    VITE_CONTROL_PLANE_API_URL = ${VITE_CONTROL_PLANE_API_URL:-(unset)}"
echo "    RELIANT_API_URL            = ${RELIANT_API_URL:-(unset)}"
echo "    RELIANT_GATEWAY_URL        = ${RELIANT_GATEWAY_URL:-(unset)}"

# Fail loudly rather than ship a mislabelled build: every endpoint that
# reaches the packaged app must be the one release.config.json declares.
for var in VITE_API_URL VITE_CONTROL_PLANE_API_URL RELIANT_API_URL RELIANT_GATEWAY_URL; do
  case "${!var:-}" in
    ""|*localhost*|*127.0.0.1*)
      echo "$var is '${!var:-}' — refusing to build a 'prod' app against a local endpoint" >&2
      exit 1
      ;;
  esac
done

echo "==> Writing electron/src/build-config.js"
( cd electron && node ../.github/scripts/generate-build-config.mjs )

# `npm run build`, NOT build:alpha. The difference is `tsc -b`: build:alpha is
# a bare `vite build` that type-checks nothing, and because electron's
# build:web called it, the desktop path silently accumulated 20 type errors
# while builds kept "succeeding". Release runs the real gate; so does this.
echo "==> Building the renderer (tsc -b && vite build)"
( cd web && npm run build )

echo "==> Building the Go backend binaries (mac)"
./scripts/build-electron.sh mac

echo "==> Packaging with $BUILDER_CONFIG"
( cd electron && npx electron-builder --mac --config electron-builder.local.js )

APP="$PROJECT_ROOT/electron/dist/mac-arm64/Reliant Local.app"
[[ -d "$APP" ]] || APP="$(find "$PROJECT_ROOT/electron/dist" -maxdepth 2 -name 'Reliant Local.app' -print -quit)"
[[ -n "$APP" && -d "$APP" ]] || { echo "packaged app not found under electron/dist" >&2; exit 1; }

# Re-sign ad-hoc, inside-out. See the header comment — this is what keeps the
# fuse-rewritten Electron Framework from being SIGKILLed at launch.
echo "==> Re-signing ad-hoc (repairs the fuse-rewritten framework)"
while IFS= read -r -d '' fw; do
  codesign --force --sign - --timestamp=none "$fw" >/dev/null 2>&1
done < <(find "$APP/Contents/Frameworks" -maxdepth 1 -name '*.framework' -print0 2>/dev/null)

while IFS= read -r -d '' helper; do
  codesign --force --deep --sign - --timestamp=none "$helper" >/dev/null 2>&1
done < <(find "$APP/Contents/Frameworks" -maxdepth 1 -name '*.app' -print0 2>/dev/null)

codesign --force --deep --sign - --timestamp=none "$APP" >/dev/null 2>&1

# Verify rather than assume: a bad signature here presents as a silent crash
# at launch, so it is worth failing loudly at build time instead.
if ! codesign --verify --deep "$APP" 2>/dev/null; then
  echo "ad-hoc signature did not verify — the app would be SIGKILLed at launch" >&2
  codesign --verify --deep --verbose=2 "$APP" || true
  exit 1
fi

cat <<EOF

==> Done.

  $APP

Configured against PRODUCTION (${RELIANT_API_URL:-?}).
Runs alongside your installed Reliant: separate appId, separate userData
(~/Library/Application Support/reliant-local), separate logs.

  open "$APP"
EOF
