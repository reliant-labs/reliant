#!/usr/bin/env bash
# stack.sh — where the running dev stack is, read from forge, never guessed.
#
# Sourced by port-info.sh, api-call.sh, inspect-state.sh and log-search.sh.
# Every port, URL and database comes from `forge env config dev --json`: the
# values `forge env up dev` passes to each process, with the ports the running
# stack was LAUNCHED with (each worktree gets its own 100-port block). There is
# no ports file to read and none to go stale.
#
# Two stacks, one layout. Pick one:
#
#   (default)    this checkout's own `forge env up dev` (deploy/kcl/dev): api,
#                worker, gateway and web on the host, no control plane
#   --cp         control-plane's full-platform stack (`forge env up dev` in
#                ../control-plane, or CONTROL_PLANE_DIR): billing, the LLM
#                gateway, managed workspaces; needs k3d
#
# RELIANT_STACK=cp does the same as --cp. Either way the logs are that
# checkout's .forge/logs/dev/.
#
# After `stack_load`, these are set (empty when the stack does not declare one):
#   STACK_NAME      reliant | control-plane
#   STACK_DIR       the checkout whose stack this is
#   STACK_LOG_DIR   $STACK_DIR/.forge/logs/dev
#   API_URL         reliant-api-server's public URL (Connect RPC)
#   API_HEALTH_URL  its health listener (/health, /ready)
#   WORKER_HEALTH_URL, GATEWAY_URL, GATEWAY_HEALTH_URL
#   WEB_URL         the reliant web app
#   DATABASE_URL    the reliant database the stack's processes use
#   TEMPORAL_ADDR   host:port of the Temporal frontend

STACK_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# stack_args: consume a leading --cp from "$@" into STACK_SELECT; the caller
# re-reads the rest from STACK_REST.
stack_args() {
  STACK_SELECT=${RELIANT_STACK:-reliant}
  STACK_REST=()
  local a
  for a in "$@"; do
    case "$a" in
      --cp|--control-plane) STACK_SELECT=cp ;;
      *) STACK_REST+=("$a") ;;
    esac
  done
}

# stack_forge: the forge to ask. FORGE_BIN, else forge on PATH, else the one
# Reliant bundles. Any of them runs the forge forge.yaml pins (it re-execs
# the pin when it is not it).
stack_forge() {
  if [ -n "${FORGE_BIN:-}" ]; then echo "$FORGE_BIN"
  elif command -v forge >/dev/null 2>&1; then echo forge
  elif command -v reliant >/dev/null 2>&1; then echo "reliant forge"
  else
    echo "stack.sh: no forge found (set FORGE_BIN, or install forge)" >&2
    return 1
  fi
}

stack_load() {
  local select=${1:-${STACK_SELECT:-reliant}} f json
  case "$select" in
    cp|control-plane)
      STACK_NAME=control-plane
      STACK_DIR="$(cd "${CONTROL_PLANE_DIR:-$STACK_ROOT/../control-plane}" 2>/dev/null && pwd)" || {
        echo "stack.sh: no control-plane checkout at ${CONTROL_PLANE_DIR:-$STACK_ROOT/../control-plane} (set CONTROL_PLANE_DIR)" >&2
        return 1
      } ;;
    *) STACK_NAME=reliant; STACK_DIR=$STACK_ROOT ;;
  esac
  STACK_LOG_DIR="$STACK_DIR/.forge/logs/dev"
  command -v jq >/dev/null 2>&1 || { echo "stack.sh: jq is required" >&2; return 1; }
  f=$(stack_forge) || return 1
  # A missing secret is reported on stderr and is not fatal: the ports and
  # URLs are still right.
  json=$($f env config dev --json -C "$STACK_DIR" 2>/dev/null) || {
    echo "stack.sh: \`$f env config dev -C $STACK_DIR\` failed; run it by hand to see why" >&2
    return 1
  }
  STACK_JSON=$json
  _env() { jq -r --arg w "$1" --arg k "$2" '.workloads[] | select(.name == $w) | .env[$k] // empty' <<<"$STACK_JSON" | head -1; }
  local api_health worker_health gw_port gw_health temporal
  API_URL=$(_env reliant-api-server PUBLIC_URL)
  api_health=$(_env reliant-api-server HEALTH_PORT)
  worker_health=$(_env reliant-temporal-worker HEALTH_PORT)
  gw_port=$(_env daemon-gateway TOOLS_DAEMON_PORT)
  gw_health=$(_env daemon-gateway HEALTH_PORT)
  temporal=$(_env reliant-api-server TEMPORAL_PORT)
  DATABASE_URL=$(_env reliant-api-server DATABASE_URL)
  WEB_URL=$(_env reliant-api-server APP_URL)
  [ -n "$WEB_URL" ] || WEB_URL=$(_env web VITE_APP_URL)
  [ -n "$WEB_URL" ] || WEB_URL=$(_env reliant-web APP_URL)
  API_HEALTH_URL=${api_health:+http://localhost:$api_health}
  WORKER_HEALTH_URL=${worker_health:+http://localhost:$worker_health}
  GATEWAY_URL=${gw_port:+http://localhost:$gw_port}
  GATEWAY_HEALTH_URL=${gw_health:+http://localhost:$gw_health}
  TEMPORAL_ADDR=${temporal:+localhost:$temporal}
  unset -f _env
}

# stack_port <url>: the port of a URL, or nothing.
stack_port() { sed -n 's#^[a-z]*://[^/:]*:\([0-9][0-9]*\).*#\1#p' <<<"$1"; }
