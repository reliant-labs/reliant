#!/usr/bin/env bash
# Call the running dev stack's API (Connect RPC: POST /<package>.<Service>/<Method>, JSON).
#
#   ./.reliant/tools/api-call.sh /reliant.v1.ProjectService/ListProjects '{}'
#   ./.reliant/tools/api-call.sh --cp /reliant.v1.ChatService/ListChats '{}'
#   ./.reliant/tools/api-call.sh GET /health        the API's health listener
#   ./.reliant/tools/api-call.sh ... -v             curl -v
#
# The URL comes from `forge env config dev` (stack.sh). The bearer token is
# RELIANT_API_TOKEN when set; otherwise, on this checkout's own stack
# (AUTH_MODE=apikey), the AUTH_API_KEY forge generated into secrets/dev.yaml.
# control-plane's stack signs in through its IdP: pass RELIANT_API_TOKEN.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/stack.sh"
stack_args "$@"; set -- "${STACK_REST[@]+"${STACK_REST[@]}"}"

verbose=() args=()
for a in "$@"; do case "$a" in -v|--verbose) verbose=(-v) ;; *) args+=("$a") ;; esac; done
set -- "${args[@]+"${args[@]}"}"

method=POST
case "${1:-}" in GET|POST|PUT|DELETE) method=$1; shift ;; esac
path=${1:-}; body=${2:-}
[ -n "$path" ] || { sed -n '2,12p' "$0"; exit 2; }

stack_load
base=$API_URL
case "$path" in /health|/ready) base=$API_HEALTH_URL; method=GET ;; esac
[ -n "$base" ] || { echo "api-call: the $STACK_NAME stack declares no reliant-api-server URL" >&2; exit 1; }

token=${RELIANT_API_TOKEN:-}
if [ -z "$token" ] && [ "$STACK_NAME" = reliant ] && [ -f "$STACK_DIR/secrets/dev.yaml" ]; then
  token=$(sed -n 's/^AUTH_API_KEY:[[:space:]]*//p' "$STACK_DIR/secrets/dev.yaml" | tr -d '"' | head -1)
fi

echo "→ $method $base$path" >&2
curl_args=(-s "${verbose[@]+"${verbose[@]}"}" -X "$method" -H "Content-Type: application/json")
[ -n "$token" ] && curl_args+=(-H "Authorization: Bearer $token")
[ "$method" = POST ] && curl_args+=(-d "${body:-{\}}")
out=$(curl "${curl_args[@]}" "$base$path")
jq . <<<"$out" 2>/dev/null || printf '%s\n' "$out"
