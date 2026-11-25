#!/usr/bin/env bash
# signin-e2e.sh — prove the managed identity's native sign-in end to end
# against THIS checkout's running dev stack (`forge env up dev`):
#
#   1. register a fresh person on reliant-api's login broker (POST /auth/register,
#      open in dev), which signs them in: an HttpOnly cookie holding a
#      server-side session id;
#   2. GET /auth/session with the cookie reports them signed in;
#   3. the cookie authenticates a Connect RPC (the browser session);
#   4. the cookie passes the approval gate (Approve on an unknown id answers
#      not_found, not permission_denied), and the same session id presented as
#      a bearer is refused (person_required): approvals need a browser session;
#   5. a token replayed as the cookie is refused (unauthenticated);
#   6. POST /auth/logout ends the session: the cookie no longer authenticates.
#
# Usage: .reliant/tools/signin-e2e.sh [api-base-url]
# The default base is the api port `forge env config dev` resolves.
set -euo pipefail

base=${1:-}
if [ -z "$base" ]; then
  port=$(reliant forge env config dev --workload reliant-api-server 2>/dev/null | awk '$1 == "GRPC_PORT" {print $2}')
  base="http://localhost:${port:?could not resolve the api port; pass the base URL}"
fi
jar=$(mktemp)
trap 'rm -f "$jar"' EXIT
email="signin-e2e-$(date +%s)-$RANDOM@example.com"
password="Sign-in-e2e-$RANDOM-Aa1!"
fail() { echo "FAIL: $*" >&2; exit 1; }
cookie_value() { awk '$6 ~ /_session$/ {print $7}' "$jar" | tail -1; }
rpc() { # rpc <procedure> <json> [curl args...]; prints "<status> <body>"
  local p=$1 d=$2; shift 2
  curl -s -o /tmp/signin-e2e-body.$$ -w '%{http_code}' -X POST "$base/$p" \
    -H 'Content-Type: application/json' -d "$d" "$@"
  printf ' %s\n' "$(cat /tmp/signin-e2e-body.$$)"; rm -f /tmp/signin-e2e-body.$$
}

echo "== options"; curl -sf "$base/auth/options"; echo

echo "== register $email"
status=$(curl -s -o /dev/null -w '%{http_code}' -c "$jar" -X POST "$base/auth/register" \
  -H 'Content-Type: application/json' -d "{\"email\":\"$email\",\"password\":\"$password\",\"givenName\":\"Sign\",\"familyName\":\"In\"}")
[ "$status" = 200 ] || [ "$status" = 201 ] || fail "register answered $status"
sid=$(cookie_value); [ -n "$sid" ] || fail "no session cookie set"
case "$sid" in fs_*) ;; *) fail "the cookie holds no server-side session id: ${sid:0:6}…";; esac
echo "   cookie holds a server-side session id (fs_…)"

echo "== session"
session=$(curl -sf -b "$jar" "$base/auth/session"); echo "   $session"
echo "$session" | grep -q '"authenticated":true' || fail "/auth/session does not report the person signed in"

echo "== the cookie authenticates an RPC"
out=$(rpc reliant.v1.ChatService/ListChats '{}' -b "$jar"); echo "   $out" | cut -c1-160
[ "${out%% *}" = 200 ] || fail "ListChats with the cookie answered ${out%% *}"

echo "== the cookie passes the approval gate"
out=$(rpc reliant.v1.ApprovalService/Approve '{"requestId":"no-such-approval"}' -b "$jar"); echo "   $out"
echo "$out" | grep -q '"code":"not_found"' || fail "a browser session was refused at the approval gate"

echo "== the session id as a bearer may not approve"
out=$(rpc reliant.v1.ApprovalService/Approve '{"requestId":"no-such-approval"}' -H "Authorization: Bearer $sid"); echo "   $out"
echo "$out" | grep -q '"code":"permission_denied"' || fail "a session id presented as a bearer passed the approval gate"

echo "== a token replayed as the cookie is refused"
name=$(awk '$6 ~ /_session$/ {print $6}' "$jar" | tail -1)
out=$(rpc reliant.v1.ChatService/ListChats '{}' -H "Cookie: $name=eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"); echo "   $out"
echo "$out" | grep -q '"code":"unauthenticated"' || fail "a token in the cookie was not refused"

echo "== sign out"
curl -sf -o /dev/null -b "$jar" -c "$jar" -X POST "$base/auth/logout" -H 'Content-Type: application/json' -d '{}'
out=$(rpc reliant.v1.ChatService/ListChats '{}' -H "Cookie: $name=$sid"); echo "   $out"
echo "$out" | grep -q '"code":"unauthenticated"' || fail "the session still authenticates after sign-out"

echo "PASS: native sign-in, browser session, approval gate and sign-out"
