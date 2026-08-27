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
# ── Invoke it through npm, not directly ────────────────────────────────
#
#   cd electron && npm run dist:mac:prod-local
#
# That target wraps this script in electron/scripts/with-release-config.mjs,
# which expands electron/release.config.json into the environment — the same
# expansion release.yml does with jq, and the same one every other packaging
# target now goes through. This script deliberately does NOT read that file
# itself: a second expansion is a second place the config can drift, which is
# the defect this whole path exists to prevent.
#
#   .vite  -> VITE_* read directly by `vite build` (no .env file is written)
#   .main  -> consumed by generate-build-config.mjs, which writes
#             electron/src/build-config.js (the main-process endpoints)
#
# Running this file directly is rejected below rather than silently building
# against whatever the calling shell had — in a dev shell that is localhost.
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

BUILDER_CONFIG="electron/electron-builder.local.js"
[[ -f "$BUILDER_CONFIG" ]] || { echo "missing $BUILDER_CONFIG" >&2; exit 1; }

# The environment must already carry the release config. with-release-config.mjs
# is the only thing that sets it, so an empty value here means this script was
# run directly — in which case the safe move is to stop, not to guess. A bare
# `./scripts/build-electron-prod-local.sh` from a dev shell would otherwise
# inherit localhost endpoints and package them into a build labelled "prod".
for var in VITE_API_URL VITE_CONTROL_PLANE_API_URL RELIANT_API_URL RELIANT_GATEWAY_URL; do
  case "${!var:-}" in
    ""|*localhost*|*127.0.0.1*)
      echo "$var is '${!var:-}' — refusing to build a 'prod' app against a local endpoint." >&2
      echo "Run this through the npm target, which supplies the KCL-derived config:" >&2
      echo "    cd electron && npm run dist:mac:prod-local" >&2
      exit 1
      ;;
  esac
done

echo "==> Building with prod config"
echo "    VITE_API_URL               = $VITE_API_URL"
echo "    VITE_CONTROL_PLANE_API_URL = $VITE_CONTROL_PLANE_API_URL"
echo "    RELIANT_API_URL            = $RELIANT_API_URL"
echo "    RELIANT_GATEWAY_URL        = $RELIANT_GATEWAY_URL"

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

# Drop the stale keychain grant BEFORE re-signing.
#
# macOS binds a keychain ACL to the app's CODE SIGNATURE. A Developer ID build
# has a stable identity, so "Always Allow" sticks forever. This build is
# ad-hoc signed (no cert on the machine), so the ACL is pinned to the exact
# code hash — and every rebuild produces a new hash. macOS then sees an
# unknown app asking for an existing item and prompts again, no matter how
# many times you clicked "Always Allow": the grant was saved, just for a
# binary that no longer exists.
#
# Deleting the item lets the fresh build recreate it on first use, so you get
# ONE prompt per rebuild instead of one per launch. Scoped to this build's own
# service name (electron-builder.local.js sets extraMetadata.name), so the
# installed Reliant's credentials are never touched.
if security find-generic-password -s "reliant-local Safe Storage" >/dev/null 2>&1; then
  security delete-generic-password -s "reliant-local Safe Storage" >/dev/null 2>&1 || true
  echo "==> Cleared the stale 'reliant-local Safe Storage' keychain grant"
fi

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

# ── Launcher ──────────────────────────────────────────────────────────
#
# Emitted because launching this app from a DEV SHELL silently breaks it, and
# the failure does not look like an environment problem.
#
# BackendManager decides which daemon binary to run from
# `process.env.NODE_ENV === 'development'` (backend-manager.js) — the ambient
# environment, NOT whether the app is packaged. A dev shell exports
# NODE_ENV=development and RELIANT_BACKEND_BIN=<worktree>/dist/reliant, and
# `open` passes both to the app. The packaged prod build then runs the DEV
# binary against http://localhost:8090, the local stack is not there, and the
# UI reports:
#
#   Daemon failed to become ready within 30000ms — no runtime record written
#   [gRPC Client] gRPC client not ready - no backend URL available
#
# — with no backend call ever attempted. Launching from Finder or the Dock
# works, because those inherit a clean environment; only a terminal launch
# fails, which makes it look intermittent.
#
# This wrapper strips the dev vars so the app behaves the same either way.
LAUNCHER="$PROJECT_ROOT/electron/dist/run-reliant-local.sh"
cat > "$LAUNCHER" <<LAUNCH
#!/usr/bin/env bash
# Launch the local prod build with dev environment variables stripped.
# See scripts/build-electron-prod-local.sh for why this is necessary.
exec env -u NODE_ENV -u RELIANT_BACKEND_BIN -u RELIANT_API_URL \\
         -u RELIANT_SERVER_URL -u RELIANT_GATEWAY_URL -u DISABLE_TLS \\
     open -a "$APP" "\$@"
LAUNCH
chmod +x "$LAUNCHER"

cat <<EOF

==> Done.

  $APP

Configured against PRODUCTION (${RELIANT_API_URL:-?}).
Runs alongside your installed Reliant: separate appId, separate userData
(~/Library/Application Support/reliant-local), separate logs.

Launch it with:

  $LAUNCHER

Do NOT use a bare \`open\` from a dev shell: BackendManager keys off
NODE_ENV, so it would run the DEV binary against localhost and fail with
"Daemon failed to become ready". Finder/Dock are fine.
EOF
