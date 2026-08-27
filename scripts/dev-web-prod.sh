#!/usr/bin/env bash
# Run the normal Vite dev server (HMR, source maps, instant reload) against the
# PRODUCTION backends instead of localhost.
#
# This is `npm run dev`, not a static preview of `web/dist`. You get the full
# dev loop — edit a file, see it — while every RPC goes to prod.
#
# ── How it works ──────────────────────────────────────────────────────
#
# vite.config.ts's proxy targets already read the environment and fall back to
# localhost:
#
#   "/reliant.v1."      -> process.env.VITE_API_URL              || 127.0.0.1:3090
#   "/controlplane.v1." -> process.env.VITE_CONTROL_PLANE_API_URL || 127.0.0.1:8090
#
# So pointing dev at prod is purely a matter of exporting the right values.
# They come from electron/release.config.json — the SAME file the release
# workflow sources — so this cannot drift from what actually ships.
#
# ── Read this before you sign in ──────────────────────────────────────
#
# This talks to REAL PRODUCTION. Chats, projects and daemons you create here
# are real user data in the prod database. There is no sandbox.
#
# Google sign-in needs `http://localhost:<port>/auth/callback` in the prod
# Supabase project's redirect allow-list, or consent will fail — a browser has
# no loopback escape hatch the way the desktop app does.
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

RELEASE_CONFIG="electron/release.config.json"
[[ -f "$RELEASE_CONFIG" ]] || { echo "missing $RELEASE_CONFIG" >&2; exit 1; }
command -v jq >/dev/null || { echo "jq is required (brew install jq)" >&2; exit 1; }

PORT="${FRONTEND_PORT:-5173}"

# Assign explicitly rather than sourcing, so a dev shell that already exports
# VITE_API_URL=localhost cannot win. Same trap as build-electron-prod-local.sh.
while IFS='=' read -r k v; do
  export "$k=$v"
done < <(jq -er '.vite | to_entries[] | "\(.key)=\(.value)"' "$RELEASE_CONFIG")

export FRONTEND_PORT="$PORT"

# Refuse to start rather than serve a "prod" UI wired to a dev backend.
for var in VITE_API_URL VITE_CONTROL_PLANE_API_URL; do
  case "${!var:-}" in
    ""|*localhost*|*127.0.0.1*)
      echo "$var is '${!var:-}' — refusing to start a 'prod' dev server against a local endpoint" >&2
      exit 1
      ;;
  esac
done

cat <<EOF

==> Vite dev server, PRODUCTION backends

  app          http://localhost:${PORT}
  reliant api  ${VITE_API_URL}
  control      ${VITE_CONTROL_PLANE_API_URL}
  supabase     ${VITE_SUPABASE_URL}

THIS IS REAL PRODUCTION DATA. Anything you create here is real.

If Google sign-in fails, add http://localhost:${PORT}/auth/callback to the
prod Supabase redirect allow-list.

EOF

cd web
exec npm run dev
