#!/usr/bin/env bash
# Regenerate config/endpoints.<env>.env from control-plane's KCL.
#
# control-plane's deploy/kcl/lib/env.k (`reliant_web_vite_env`) is the source of
# truth for what the DEPLOYED frontend is configured with. This projects those
# same values into a file the desktop release can read, so the two cannot drift.
#
# They had drifted: the desktop release read a GitHub secret pinned to
# `https://api.reliant.so/api`, a domain with no DNS record, while the hosted
# frontend used `https://api.reliantapi.com` from KCL. Because the value was a
# secret nobody could read it back, so every release baked in a dead endpoint.
#
# Usage:
#   scripts/sync-endpoints.sh [prod|preprod]     (default: prod)
#   CONTROL_PLANE_DIR=/path/to/control-plane scripts/sync-endpoints.sh prod
#
# Verifying rather than writing (what CI does):
#   CHECK=1 scripts/sync-endpoints.sh prod
set -euo pipefail

ENVIRONMENT="${1:-prod}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONTROL_PLANE_DIR="${CONTROL_PLANE_DIR:-$ROOT_DIR/../control-plane}"
OUT="$ROOT_DIR/config/endpoints.${ENVIRONMENT}.env"

case "$ENVIRONMENT" in
  prod|preprod) ;;
  *) echo "error: unknown environment '$ENVIRONMENT' (want prod|preprod)" >&2; exit 1 ;;
esac

if [ ! -d "$CONTROL_PLANE_DIR" ]; then
  echo "error: control-plane not found at $CONTROL_PLANE_DIR" >&2
  echo "       clone it beside this repo, or set CONTROL_PLANE_DIR=..." >&2
  exit 1
fi

if ! command -v forge >/dev/null 2>&1; then
  echo "error: forge not on PATH — needed to resolve the KCL config" >&2
  exit 1
fi

echo "==> reading VITE_* from control-plane KCL (env=$ENVIRONMENT)"
VALUES="$(cd "$CONTROL_PLANE_DIR" && forge env config "$ENVIRONMENT" --json 2>/dev/null | python3 -c "
import json,sys
d=json.load(sys.stdin)
found={}
for w in (d.get('workloads') or []):
    for k,v in (w.get('env') or {}).items():
        if k.startswith('VITE_'):
            found.setdefault(k,v)
if not found:
    sys.exit('no VITE_* variables found in the rendered config')
for k in sorted(found):
    print(f'{k}={found[k]}')
")"

if [ -z "$VALUES" ]; then
  echo "error: resolved no VITE_* values — refusing to write an empty config" >&2
  exit 1
fi

# The API URL is load-bearing; a packaged app that points at nothing is the
# exact failure this file exists to prevent.
API_URL="$(printf '%s\n' "$VALUES" | sed -n 's/^VITE_API_URL=//p')"
if [ -z "$API_URL" ]; then
  echo "error: KCL produced no VITE_API_URL" >&2
  exit 1
fi
case "$API_URL" in
  *localhost*|*127.0.0.1*)
    echo "error: VITE_API_URL is $API_URL — a packaged build must not target localhost" >&2
    exit 1 ;;
esac

HEADER="$(sed -n '1,/^$/p' "$OUT" 2>/dev/null || true)"
TMP="$(mktemp)"
if [ -n "$HEADER" ]; then
  # Preserve the explanatory header block already in the file.
  sed -n '/^#/p' "$OUT" > "$TMP"
  echo "" >> "$TMP"
else
  {
    echo "# Public endpoint configuration for ${ENVIRONMENT} builds."
    echo "# GENERATED FROM control-plane KCL — regenerate with scripts/sync-endpoints.sh"
    echo ""
  } > "$TMP"
fi
printf '%s\n' "$VALUES" >> "$TMP"

if [ "${CHECK:-}" = "1" ]; then
  if diff -u "$OUT" "$TMP" >/dev/null 2>&1; then
    echo "==> $OUT is in sync with KCL"
    rm -f "$TMP"
    exit 0
  fi
  echo "error: $OUT is STALE relative to control-plane's KCL:" >&2
  diff -u "$OUT" "$TMP" >&2 || true
  echo "" >&2
  echo "Run: scripts/sync-endpoints.sh $ENVIRONMENT" >&2
  rm -f "$TMP"
  exit 1
fi

mv "$TMP" "$OUT"
echo "==> wrote $OUT"
printf '%s\n' "$VALUES" | sed 's/^/    /'
