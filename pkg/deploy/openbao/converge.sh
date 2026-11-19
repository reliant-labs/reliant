#!/usr/bin/env bash
# converge.sh - converge a running OpenBao to the access document forge rendered.
#
# Run by the component's two content-addressed Jobs, in the pinned kubectl
# image (kubectl, curl, jq; no `bao`, no openssl). OPENBAO_PHASE says which:
#
#   widen     pre-rollout. Apply the UNION of the access document the last
#             converge applied and the new one, so a name a release adds is
#             already granted when its workloads first sync. A first install has
#             nothing applied yet and nothing to widen: it exits 0 at once. So
#             does a store that still answers in plaintext (the previous
#             incarnation, before an adoption or after a rollback): it has no
#             TLS listener to widen over, and the rollout that would give it
#             one waits on this Job. Converge applies the whole document later.
#   converge  post-rollout. Initialise the store if it has never been, then
#             apply the new document, narrow (delete what it no longer
#             contains), retire the auth mounts the document lists as retired,
#             and record it as applied.
#
# The document (/converge/access.json) is the whole contract: the KV mount, the
# JWT auth mounts, the ACL policies and the login roles. Every step is an
# idempotent overwrite, except enabling a mount, which OpenBao answers with 400
# when it exists, so `sys/auth` is LISTED first and only missing mounts are
# enabled.
#
# CREDENTIALS. Nothing is stored for this Job. It logs in with its own
# projected service-account token (audience-pinned) against the self mount's
# converge role. The one exception is the very first run: the self mount does
# not exist yet, so it converges as the root token `operator init` just minted,
# read back from the recovery Secret, and revokes it. A crash between init and
# revoke is therefore recoverable by simply running the Job again.
#
# THE ORDER IS THE SAFETY PROPERTY. `operator init` is the one step that cannot
# be repeated, and its output the one thing that cannot be recovered. So:
#   1. init, writing the output to a file in this pod;
#   2. persist it to the in-cluster recovery Secret, in ONE create;
#   3. hand it to the escrow hook (outside the cluster), if one is mounted;
#   4. only then converge anything.
# A store that is initialised while its recovery Secret is absent is REFUSED,
# never re-initialised: the store itself is the single-flight lock.
#
# Exit status 64 is a verdict a retry cannot change (the Job's podFailurePolicy
# fails the Job on it at once); anything else is retried.

set -euo pipefail
umask 077

export HOME=/tmp

PHASE="${OPENBAO_PHASE:?OPENBAO_PHASE}"
ADDR="${OPENBAO_ADDR:?OPENBAO_ADDR}"
INIT_ADDR="${OPENBAO_INIT_ADDR:-$ADDR}"
CA_FILE="${OPENBAO_CA_FILE:?OPENBAO_CA_FILE}"
NS="${OPENBAO_NAMESPACE:?OPENBAO_NAMESPACE}"
RECOVERY="${OPENBAO_RECOVERY_SECRET:?OPENBAO_RECOVERY_SECRET}"
APPLIED_CM="${OPENBAO_APPLIED_CONFIGMAP:?OPENBAO_APPLIED_CONFIGMAP}"
CA_CM="${OPENBAO_CA_CONFIGMAP:-}"
CA_NAMESPACES="${OPENBAO_CA_NAMESPACES:-$NS}"
PUBLISH_CA="${OPENBAO_PUBLISH_CA:-true}"
SELF_MOUNT="${OPENBAO_SELF_MOUNT:?OPENBAO_SELF_MOUNT}"
CONVERGE_ROLE="${OPENBAO_CONVERGE_ROLE:?OPENBAO_CONVERGE_ROLE}"
KV_MOUNT="${OPENBAO_KV_MOUNT:?OPENBAO_KV_MOUNT}"
KV_MAX_VERSIONS="${OPENBAO_KV_MAX_VERSIONS:-10}"
DOC_FILE="${OPENBAO_DOC_FILE:-/converge/access.json}"
TOKEN_FILE="${OPENBAO_TOKEN_FILE:-/var/run/openbao/token}"
ESCROW_HOOK="${OPENBAO_ESCROW_HOOK:-/escrow/escrow.sh}"
GENERATE_HOOK="${OPENBAO_GENERATE_HOOK:-/generate/generate.sh}"
BREAK_GLASS_TOKEN="${OPENBAO_BREAK_GLASS_TOKEN:-}"
EXIT_REFUSED="${OPENBAO_EXIT_REFUSED:-64}"
# Connection-level failures (no HTTP answer at all) are retried this many times
# with doubling delays (RETRY_BASE_SECONDS, capped at 15s): a pod restarting
# behind the Service, a node drain or a Cloud SQL proxy hiccup is not a verdict.
API_RETRIES="${OPENBAO_API_RETRIES:-6}"
RETRY_BASE_SECONDS="${OPENBAO_RETRY_BASE_SECONDS:-1}"
if [ "$PHASE" = widen ]; then
  WAIT_SECONDS="${OPENBAO_WAIT_SECONDS:-180}"
else
  WAIT_SECONDS="${OPENBAO_WAIT_SECONDS:-900}"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
TOKEN=""
TOKEN_KIND=""   # jwt | root

log() { echo "[openbao-converge:$PHASE] $*" >&2; }
die() { log "FATAL: $*"; exit 1; }
refuse() { log "REFUSED: $*"; exit "$EXIT_REFUSED"; }

# ---- the API ---------------------------------------------------------------

# api METHOD PATH [BODY]: sets API_CODE and API_CURL_EXIT, leaves the body in
# $TMP/resp. The token travels in a header FILE, never on a command line.
#
# API_CODE is the HTTP status, or 000 when the store gave no HTTP answer at all.
# A 000 is a connection-level failure (curl exit 6, 7, 28, 35, 52, 55, 56) and
# is retried with bounded backoff; any HTTP status, 4xx and 5xx included, is the
# store's answer and returns at once. $TMP/resp is emptied before every attempt
# so a failure can never quote the previous call's response. A failure curl
# cannot recover from (60: the CA does not verify; 77: no CA file) is not
# retried either.
#
# A request that may have reached the store (curl exit 28, 52, 55, 56) is
# repeated only when repeating it is harmless. Every call here is an idempotent
# overwrite or a read except three, which are NOT replayed after an ambiguous
# failure: sys/init (its output cannot be recovered), and enabling a mount
# (sys/mounts, sys/auth: a repeat answers 400, hiding whether the first took).
# Those exit 1 instead, and the Job's own retry starts over from a listing. A
# failure before any byte was sent (6, 7, 35) is always safe to repeat.
api() {
  local method=$1 path=$2 body=${3-}
  local args=(-sS -o "$TMP/resp" -w '%{http_code}' --cacert "$CA_FILE" -X "$method")
  if [ -n "$TOKEN" ]; then
    printf 'X-Vault-Token: %s\n' "$TOKEN" >"$TMP/hdr"
    args+=(-H "@$TMP/hdr")
  fi
  if [ -n "$body" ]; then
    printf '%s' "$body" >"$TMP/body"
    args+=(-H 'Content-Type: application/json' --data-binary "@$TMP/body")
  fi
  local attempt=0 delay=$RETRY_BASE_SECONDS
  while :; do
    : >"$TMP/resp"
    API_CURL_EXIT=0
    API_CODE=$(curl "${args[@]}" "${API_BASE:-$ADDR}/v1/$path" 2>"$TMP/curl.err") || API_CURL_EXIT=$?
    [ "$API_CURL_EXIT" -eq 0 ] || API_CODE=000
    [ "$API_CODE" = 000 ] || return 0
    case "$API_CURL_EXIT" in
      6|7|35) ;;
      28|52|55|56) case "$method $path" in "POST sys/init"|"POST sys/mounts/"*|"POST sys/auth/"*) return 0 ;; esac ;;
      *) return 0 ;;
    esac
    attempt=$((attempt + 1))
    if [ "$attempt" -gt "$API_RETRIES" ]; then return 0; fi
    log "$method $path: no answer from the store (curl exit $API_CURL_EXIT: $(head -c 200 "$TMP/curl.err" | tr '\n' ' ')); retry $attempt/$API_RETRIES in ${delay}s"
    sleep "$delay"
    delay=$((delay * 2))
    [ "$delay" -le 15 ] || delay=15
  done
}

# api_failure: what to say about the call that just failed. A 000 has no
# response to quote, only curl's own account of why.
api_failure() {
  if [ "$API_CODE" = 000 ]; then
    echo "no answer from the store (curl exit $API_CURL_EXIT: $(head -c 300 "$TMP/curl.err" 2>/dev/null | tr '\n' ' '))"
  else
    echo "answered $API_CODE: $(head -c 400 "$TMP/resp" 2>/dev/null | tr '\n' ' ')"
  fi
}

# must METHOD PATH [BODY]: an api call that has to succeed.
must() {
  api "$@"
  case "$API_CODE" in
    2??) ;;
    *) die "$1 $2 $(api_failure)" ;;
  esac
}

# health ADDR: the node's status document, whatever its state.
health() {
  local code
  : >"$TMP/health"
  code=$(curl -sS -o "$TMP/health" -w '%{http_code}' --cacert "$CA_FILE" \
    "$1/v1/sys/health?standbyok=true&sealedcode=200&uninitcode=200&drsecondarycode=200&performancestandbycode=200" \
    2>"$TMP/curl.err") || return 1
  [ "$code" = 200 ] && jq -e . "$TMP/health" >/dev/null 2>&1
}

# serves_plaintext ADDR: 0 when the https:// ADDR answers an unauthenticated
# health check in PLAINTEXT on the same host and port, i.e. the listener there
# is not TLS. It sends no token, so it is safe against a store that is. A TLS
# listener answers a plaintext request with an error, so it is never mistaken
# for one.
serves_plaintext() {
  case $1 in https://*) ;; *) return 1 ;; esac
  local code
  code=$(curl -sS -o "$TMP/plain-health" -w '%{http_code}' --connect-timeout 5 --max-time 10 \
    "http://${1#https://}/v1/sys/health?standbyok=true&sealedcode=200&uninitcode=200&drsecondarycode=200&performancestandbycode=200" \
    2>/dev/null) || return 1
  [ "$code" = 200 ] && jq -e 'has("initialized")' "$TMP/plain-health" >/dev/null 2>&1
}

# wait_for ADDR PREDICATE: poll the node's health until the jq predicate holds.
wait_for() {
  local addr=$1 pred=$2 soft=${3-} deadline=$((SECONDS + WAIT_SECONDS))
  while :; do
    if health "$addr" && jq -e "$pred" "$TMP/health" >/dev/null 2>&1; then return 0; fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      [ -z "$soft" ] || return 1
      die "$addr was not ready within ${WAIT_SECONDS}s ($pred); last: $(head -c 200 "$TMP/health" 2>/dev/null | tr '\n' ' ') $(head -c 200 "$TMP/curl.err" 2>/dev/null)"
    fi
    sleep 3
  done
}

# ---- Kubernetes ------------------------------------------------------------

# k8s_has KIND NAME: 0 present, 1 absent; anything else (forbidden, API down)
# is a retryable failure, not an answer.
k8s_has() {
  local err
  if err=$(kubectl -n "$NS" get "$1" "$2" -o name 2>&1 >/dev/null); then return 0; fi
  case "$err" in
    *NotFound*|*"not found"*) return 1 ;;
  esac
  die "cannot tell whether $1/$2 exists: $err"
}

recovery_field() { kubectl -n "$NS" get secret "$RECOVERY" -o "jsonpath={.data.$1}" | base64 -d; }

# applied_doc: the document the last converge applied, empty when there was none.
applied_doc() {
  if k8s_has configmap "$APPLIED_CM"; then
    kubectl -n "$NS" get configmap "$APPLIED_CM" -o 'jsonpath={.data.access\.json}'
  fi
}

# ---- credentials -----------------------------------------------------------

# unreachable WHAT: dies (exit 1, which the Job retries) when the last call got
# no answer. An unreachable store says nothing about the credential, so it must
# never fall through to "no usable credential", a verdict (exit 64) that fails
# the Job at once.
unreachable() {
  [ "$API_CODE" != 000 ] || die "$1: $(api_failure)"
}

jwt_login() {
  [ -r "$TOKEN_FILE" ] || return 1
  local body
  body=$(jq -n --rawfile jwt "$TOKEN_FILE" --arg role "$CONVERGE_ROLE" '{jwt: ($jwt | rtrimstr("\n")), role: $role}')
  TOKEN=""
  api POST "auth/$SELF_MOUNT/login" "$body"
  unreachable "logging in on $SELF_MOUNT"
  [ "$API_CODE" = 200 ] || return 1
  TOKEN=$(jq -r '.auth.client_token // empty' "$TMP/resp")
  [ -n "$TOKEN" ]
}

token_valid() {
  [ -n "$TOKEN" ] || return 1
  api GET auth/token/lookup-self
  unreachable "checking the token"
  [ "$API_CODE" = 200 ]
}

authenticate() {
  if [ -n "$BREAK_GLASS_TOKEN" ]; then
    TOKEN=$BREAK_GLASS_TOKEN
    if token_valid; then TOKEN_KIND=root; log "converging as a BREAK-GLASS token (revoked at the end of this run)"; return 0; fi
    log "WARNING: OPENBAO_BREAK_GLASS_TOKEN was supplied but is not a valid token"
  fi
  if jwt_login; then TOKEN_KIND=jwt; log "logged in as $CONVERGE_ROLE on $SELF_MOUNT"; return 0; fi
  if k8s_has secret "$RECOVERY"; then
    TOKEN=$(recovery_field root-token 2>/dev/null || true)
    if token_valid; then TOKEN_KIND=root; log "converging as the initial root token (revoked at the end of this run)"; return 0; fi
  fi
  TOKEN=""
  refuse "no usable credential. The converge role $CONVERGE_ROLE on $SELF_MOUNT does not accept this Job's token, and the root token in Secret $RECOVERY is absent or already revoked. If the self mount's config was lost, mint a root token from the recovery keys and supply it as OPENBAO_BREAK_GLASS_TOKEN (a Secret named by the Job's optional secretKeyRef), then rerun. Do not wipe storage based on this message"
}

revoke_if_root() {
  [ "$TOKEN_KIND" = root ] || return 0
  api POST auth/token/revoke-self
  log "revoked this run's $TOKEN_KIND token (HTTP $API_CODE)"
  TOKEN=""
}

# ---- init ------------------------------------------------------------------

initialise() {
  log "store is not initialised: initialising"
  API_BASE=$INIT_ADDR TOKEN="" api POST sys/init '{"recovery_shares":5,"recovery_threshold":3}'
  [ "$API_CODE" = 200 ] || die "operator init $(api_failure)"
  cp "$TMP/resp" "$TMP/init.json"
  jq -r '.root_token' "$TMP/init.json" | tr -d '\n' >"$TMP/root-token"
  [ -s "$TMP/root-token" ] || die "operator init returned no root token"

  # ONE create, immediately: the window in which a crash strands the output is
  # a single API call wide.
  local manifest
  manifest=$(jq -n --arg name "$RECOVERY" --arg ns "$NS" --rawfile init "$TMP/init.json" --rawfile root "$TMP/root-token" '{
      apiVersion: "v1", kind: "Secret", type: "Opaque",
      metadata: {name: $name, namespace: $ns, labels: {"app.kubernetes.io/managed-by": "openbao-converge"}},
      data: {"init.json": ($init | @base64), "root-token": ($root | @base64)}}')
  if ! printf '%s' "$manifest" | kubectl create -f - >"$TMP/kubectl.out" 2>&1; then
    log "FATAL: could not persist the init output to Secret $RECOVERY: $(tr '\n' ' ' <"$TMP/kubectl.out")"
    log "FATAL: the store IS initialised and this material exists nowhere else."
    log "FATAL: printing it ONCE, now, because losing it silently is worse. Save it,"
    log "FATAL: create the Secret by hand, then rotate: this log is not a safe home."
    cat "$TMP/init.json"
    exit "$EXIT_REFUSED"
  fi
  log "initialised; recovery keys and initial root token persisted to Secret $RECOVERY"
}

# escrow: hand the init output to the escrow hook, every run, so an escrow that
# failed once is retried. The hook (mounted by the bootstrap component) must be
# idempotent.
escrow() {
  [ -f "$ESCROW_HOOK" ] || return 0
  k8s_has secret "$RECOVERY" || return 0
  recovery_field 'init\.json' >"$TMP/escrow.json"
  bash "$ESCROW_HOOK" "$TMP/escrow.json" || die "the escrow hook failed; the store is initialised but its recovery output is not yet escrowed. Rerun after fixing the escrow destination"
  rm -f "$TMP/escrow.json"
}

# ---- JWKS -> PEM (no openssl in the image) ---------------------------------

b64url_hex() {
  local s=${1//-/+}
  s=${s//_//}
  while [ $((${#s} % 4)) -ne 0 ]; do s+="="; done
  printf '%s' "$s" | base64 -d | xxd -p | tr -d '\n'
}
der_len() {
  local n=$1
  if [ "$n" -lt 128 ]; then printf '%02x' "$n"
  elif [ "$n" -lt 256 ]; then printf '81%02x' "$n"
  else printf '82%04x' "$n"; fi
}
der_int() {
  local h=$1
  case ${h:0:1} in [89a-f]) h="00$h" ;; esac
  printf '02%s%s' "$(der_len $((${#h} / 2)))" "$h"
}
der_seq() { printf '30%s%s' "$(der_len $((${#1} / 2)))" "$1"; }
pem_of_spki() {
  echo '-----BEGIN PUBLIC KEY-----'
  printf '%s' "$1" | xxd -r -p | base64 | tr -d '\n' | sed 's/.\{64\}/&\n/g'
  echo
  echo '-----END PUBLIC KEY-----'
}
rsa_pem() {
  local n e body
  n=$(b64url_hex "$1")
  e=$(b64url_hex "$2")
  body=$(der_seq "$(der_int "$n")$(der_int "$e")")
  pem_of_spki "$(der_seq "300d06092a864886f70d0101010500""03$(der_len $((${#body} / 2 + 1)))00$body")"
}
ec_pem() {
  local x y
  x=$(b64url_hex "$1")
  y=$(b64url_hex "$2")
  while [ ${#x} -lt 64 ]; do x="0$x"; done
  while [ ${#y} -lt 64 ]; do y="0$y"; done
  pem_of_spki "3059301306072a8648ce3d020106082a8648ce3d030107034200""04$x$y"
}

# self_mount_config: the verification config of the mount for THIS cluster,
# built from the cluster's own issuer and JWKS, read with this Job's token.
self_mount_config() {
  local issuer jwks pems='[]' algs='[]' key kty pem
  issuer=$(kubectl get --raw /.well-known/openid-configuration | jq -r .issuer)
  jwks=$(kubectl get --raw /openid/v1/jwks)
  [ -n "$issuer" ] && [ "$issuer" != null ] || die "the cluster publishes no OIDC issuer"
  while IFS= read -r key; do
    [ -n "$key" ] || continue
    kty=$(jq -r .kty <<<"$key")
    case "$kty" in
      RSA) pem=$(rsa_pem "$(jq -r .n <<<"$key")" "$(jq -r .e <<<"$key")"); algs=$(jq -c '. + ["RS256"] | unique' <<<"$algs") ;;
      EC)
        [ "$(jq -r .crv <<<"$key")" = P-256 ] || continue
        pem=$(ec_pem "$(jq -r .x <<<"$key")" "$(jq -r .y <<<"$key")"); algs=$(jq -c '. + ["ES256"] | unique' <<<"$algs") ;;
      *) continue ;;
    esac
    pems=$(jq -c --arg p "$pem" '. + [$p]' <<<"$pems")
  done <<<"$(jq -c '.keys[] | select((.use // "sig") == "sig")' <<<"$jwks")"
  [ "$pems" != '[]' ] || die "the cluster's JWKS holds no RSA or P-256 signing key"
  jq -n --arg iss "$issuer" --argjson pems "$pems" --argjson algs "$algs" \
    '{bound_issuer: $iss, jwt_validation_pubkeys: $pems, jwt_supported_algs: $algs}'
}

# ---- the document ----------------------------------------------------------

# HCL of one policy, rule by rule, in the document's order (sorted by path).
# The same text pkg/secretaccess renders.
JQ_HCL='[ .rules[] | ( (if (.reason // "") != "" then "# wildcard read: " + (.reason | gsub("\\s+"; " ")) + "\n" else "" end)
  + "path \"" + .path + "\" {\n  capabilities = [" + (.capabilities | map("\"" + . + "\"") | join(", ")) + "]\n}\n" ) ] | join("\n")'

JQ_WIDEN='
def rules_union: group_by(.path) | map(.[0] + {capabilities: (map(.capabilities) | add | unique), reason: (map(.reason // "") | map(select(. != "")) | (first // ""))});
{self: $new.self, kv: $new.kv,
 mounts:   (($old.mounts + $new.mounts) | group_by(.name) | map(last)),
 roles:    (($old.roles + $new.roles) | group_by(.mount + "/" + .name) | map(last)),
 policies: (($old.policies + $new.policies) | group_by(.name) | map({name: .[0].name, rules: (map(.rules) | add | rules_union)}))}'

JQ_REMOVED='
{mounts:   ($old.mounts   | map(select(.cluster != $new.self and ((.name) as $n | ($new.mounts   | map(.name))               | index($n) | not)))),
 roles:    ($old.roles    | map(select(((.mount + "/" + .name) as $k | ($new.roles | map(.mount + "/" + .name)) | index($k) | not)))),
 policies: ($old.policies | map(select(((.name) as $n | ($new.policies | map(.name)) | index($n) | not))))}'

ensure_kv() {
  api GET sys/mounts
  [ "$API_CODE" = 200 ] || die "cannot list mounts: $(api_failure)"
  if jq -e --arg k "$KV_MOUNT/" '(.data // .)[$k]' "$TMP/resp" >/dev/null; then
    log "kv-v2 already mounted at $KV_MOUNT/"
  else
    log "mounting kv-v2 at $KV_MOUNT/"
    must POST "sys/mounts/$KV_MOUNT" '{"type":"kv","options":{"version":"2"}}'
  fi
  must POST "$KV_MOUNT/config" "$(jq -n --argjson v "$KV_MAX_VERSIONS" '{max_versions: $v}')"
}

# ensure_transit: enable each transit mount the document lists, if missing.
# Never disables one (that would destroy its keys) and never touches a key.
ensure_transit() {
  local doc=$1 t
  [ "$(jq '(.transit // []) | length' <<<"$doc")" -gt 0 ] || return 0
  api GET sys/mounts
  [ "$API_CODE" = 200 ] || die "cannot list mounts: $(api_failure)"
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    if jq -e --arg k "$t/" '(.data // .)[$k]' "$TMP/resp" >/dev/null; then
      log "transit already mounted at $t/"
    else
      log "mounting transit at $t/"
      must POST "sys/mounts/$t" '{"type":"transit","description":"forge.OpenBaoTransit"}'
    fi
  done <<<"$(jq -r '(.transit // [])[].mount' <<<"$doc")"
}

apply_doc() {
  local doc=$1 p m r name existing
  ensure_kv

  while IFS= read -r p; do
    [ -n "$p" ] || continue
    name=$(jq -r .name <<<"$p")
    must PUT "sys/policies/acl/$name" "$(jq -n --arg policy "$(jq -r "$JQ_HCL" <<<"$p")" '{policy: $policy}')"
  done <<<"$(jq -c '.policies[]' <<<"$doc")"
  log "applied $(jq '.policies | length' <<<"$doc") policies"
  # AFTER the policies: converge-admin's grant on each transit mount is
  # written above, and a token's policies are read at request time. The
  # widened document carries no transit list, so mounts are enabled by the
  # pass over the new document.
  ensure_transit "$doc"

  api GET sys/auth
  [ "$API_CODE" = 200 ] || die "cannot list auth mounts: $(api_failure)"
  existing=$(jq -c '(.data // .) | keys' "$TMP/resp")
  while IFS= read -r m; do
    [ -n "$m" ] || continue
    name=$(jq -r .name <<<"$m")
    if jq -e --arg k "$name/" 'index($k)' <<<"$existing" >/dev/null; then
      log "auth mount $name already enabled"
    else
      log "enabling auth mount $name"
      must POST "sys/auth/$name" '{"type":"jwt","description":"Kubernetes service-account tokens"}'
    fi
    if [ "$(jq -r .trust <<<"$m")" = self ]; then
      must POST "auth/$name/config" "$(self_mount_config)"
    else
      must POST "auth/$name/config" "$(jq -c .config <<<"$m")"
    fi
  done <<<"$(jq -c '.mounts[]' <<<"$doc")"

  while IFS= read -r r; do
    [ -n "$r" ] || continue
    must PUT "auth/$(jq -r .mount <<<"$r")/role/$(jq -r .name <<<"$r")" "$(jq -c .payload <<<"$r")"
  done <<<"$(jq -c '.roles[]' <<<"$doc")"
  log "applied $(jq '.mounts | length' <<<"$doc") mounts and $(jq '.roles | length' <<<"$doc") roles"
}

narrow() {
  local old=$1 new=$2 gone r
  gone=$(jq -n --argjson old "$old" --argjson new "$new" "$JQ_REMOVED")
  while IFS= read -r r; do
    [ -n "$r" ] || continue
    log "removing role $(jq -r '.mount + "/" + .name' <<<"$r")"
    must DELETE "auth/$(jq -r .mount <<<"$r")/role/$(jq -r .name <<<"$r")"
  done <<<"$(jq -c '.roles[]' <<<"$gone")"
  while IFS= read -r r; do
    [ -n "$r" ] || continue
    log "removing policy $(jq -r .name <<<"$r")"
    must DELETE "sys/policies/acl/$(jq -r .name <<<"$r")"
  done <<<"$(jq -c '.policies[]' <<<"$gone")"
  while IFS= read -r r; do
    [ -n "$r" ] || continue
    log "disabling auth mount $(jq -r .name <<<"$r")"
    must DELETE "sys/auth/$(jq -r .name <<<"$r")"
  done <<<"$(jq -c '.mounts[]' <<<"$gone")"
}

# retire: disable each auth mount the document lists under `retired` (a mount a
# previous install left behind, which the component never wrote). Idempotent: a
# mount that is already gone is logged and skipped. Disabling a mount revokes
# every token it issued and deletes its roles, so a mount that still HAS roles is
# refused (exit 64, a verdict a retry cannot change) unless its entry says
# `force`. A mount whose type is not known to keep logins as roles (anything but
# kubernetes, approle, jwt, oidc) cannot be checked, and is refused the same way.
# The mount this component logs in on, and any mount the document itself manages,
# are never retired, whatever the document says.
#
# Two passes, so a refusal never leaves the store half-retired: every listed
# mount is verified first, and only then is any of them disabled.
retire() {
  local doc=$1 m name reason force type existing roles todo=()
  [ "$(jq '(.retired // []) | length' <<<"$doc")" -gt 0 ] || return 0
  api GET sys/auth
  [ "$API_CODE" = 200 ] || die "cannot list auth mounts: $(api_failure)"
  existing=$(jq -c '(.data // .)' "$TMP/resp")
  while IFS= read -r m; do
    [ -n "$m" ] || continue
    name=$(jq -r .name <<<"$m")
    reason=$(jq -r '.reason // ""' <<<"$m")
    force=$(jq -r '.force // false' <<<"$m")
    if [ "$name" = "$SELF_MOUNT" ] || jq -e --arg n "$name" '.mounts | map(.name) | index($n)' <<<"$doc" >/dev/null; then
      refuse "auth mount $name is listed under retired, but it is a mount this component manages. Remove it from retiredAuthMounts"
    fi
    type=$(jq -r --arg k "$name/" '.[$k].type // empty' <<<"$existing")
    if [ -z "$type" ]; then
      log "retired auth mount $name is already gone"
      continue
    fi
    case "$type" in
      kubernetes|approle|jwt|oidc)
        api GET "auth/$name/role?list=true"
        case "$API_CODE" in
          404) roles="" ;;
          200) roles=$(jq -r '.data.keys // [] | join(", ")' "$TMP/resp") ;;
          *) die "cannot list the roles of auth mount $name: $(api_failure)" ;;
        esac
        if [ -n "$roles" ] && [ "$force" != true ]; then
          refuse "auth mount $name is listed as retired but still has roles ($roles). Disabling it revokes every token it issued. Reason given: $reason. Delete the roles, or set force on this retiredAuthMounts entry once they are known to be dead"
        fi
        [ -z "$roles" ] || log "FORCED: auth mount $name still has roles ($roles)"
        ;;
      *)
        [ "$force" = true ] || refuse "auth mount $name is a $type mount: its logins are not roles, so this Job cannot tell whether it is in use. Set force on this retiredAuthMounts entry to disable it anyway. Reason given: $reason"
        log "FORCED: auth mount $name is a $type mount; not checked for use"
        ;;
    esac
    todo+=("$name")
  done <<<"$(jq -c '(.retired // [])[]' <<<"$doc")"

  for name in ${todo[@]+"${todo[@]}"}; do
    type=$(jq -r --arg k "$name/" '.[$k].type' <<<"$existing")
    reason=$(jq -r --arg n "$name" '(.retired[] | select(.name == $n) | .reason) // ""' <<<"$doc")
    log "disabling retired auth mount $name ($type): $reason"
    must DELETE "sys/auth/$name"
  done
}

record_applied() {
  jq -n --arg name "$APPLIED_CM" --arg ns "$NS" --rawfile doc "$DOC_FILE" '{
      apiVersion: "v1", kind: "ConfigMap",
      metadata: {name: $name, namespace: $ns, labels: {"app.kubernetes.io/managed-by": "openbao-converge"}},
      data: {"access.json": $doc}}' | kubectl apply -f - >/dev/null
  log "recorded the applied access document in ConfigMap $APPLIED_CM"
}

publish_ca() {
  [ "$PUBLISH_CA" = true ] && [ -n "$CA_CM" ] || return 0
  local ns
  for ns in $CA_NAMESPACES; do
    jq -n --arg name "$CA_CM" --arg ns "$ns" --rawfile ca "$CA_FILE" '{
        apiVersion: "v1", kind: "ConfigMap",
        metadata: {name: $name, namespace: $ns, labels: {"app.kubernetes.io/managed-by": "openbao-converge"}},
        data: {"ca.crt": $ca}}' | kubectl apply -f - >/dev/null
    log "published the CA to ConfigMap $ns/$CA_CM"
  done
}

run_hook() {
  [ -f "$1" ] || return 0
  log "running $2 hook"
  bash "$1" || die "the $2 hook failed"
}

# ---- phases ----------------------------------------------------------------

[ -r "$DOC_FILE" ] || die "no access document at $DOC_FILE"
NEW=$(jq -c . "$DOC_FILE") || refuse "$DOC_FILE is not valid JSON"

case "$PHASE" in
widen)
  OLD=$(applied_doc)
  if [ -z "$OLD" ]; then
    log "no access document has been applied yet (first install): nothing to widen"
    exit 0
  fi
  # The store running NOW may not be this component yet: a first adoption, or a
  # roll-forward after a rollback, leaves the previous plaintext store serving
  # until the rollout this Job precedes replaces it. That store has no TLS
  # listener to dial, and the rollout that would give it one waits on this Job,
  # so failing here is a deadlock, not a verdict. Nothing of the component's is
  # in force to widen (the first-install case), so skip: the converge phase
  # applies the whole document once the store has rolled. Only a store that
  # positively answers in plaintext is skipped; an unreachable store or a CA
  # that does not verify still fails below.
  if ! health "$ADDR" && serves_plaintext "$ADDR"; then
    log "the store at $ADDR answers in plaintext, not on this component's TLS listener: it is the previous incarnation, still serving until the rollout. Nothing of this component is in force to widen (as on a first install); the converge phase applies the whole document after the rollout. Skipping. No credential was sent"
    exit 0
  fi
  wait_for "$ADDR" '.initialized == true and .sealed == false'
  authenticate
  WIDE=$(jq -n --argjson old "$OLD" --argjson new "$NEW" "$JQ_WIDEN")
  apply_doc "$WIDE"
  revoke_if_root
  log "widened: both the previous and the new grants are in force until the converge phase narrows them"
  ;;
converge)
  publish_ca
  # Ask the Service first: an initialised store answers there from any ready
  # node. Only an uninitialised one is asked at INIT_ADDR, which names the one
  # node that may be initialised (Raft: pod 0).
  if health "$ADDR" && jq -e '.initialized == true' "$TMP/health" >/dev/null 2>&1; then
    :
  else
    wait_for "$INIT_ADDR" '.initialized != null'
  fi
  if [ "$(jq -r .initialized "$TMP/health")" = false ]; then
    if k8s_has secret "$RECOVERY"; then
      # A Raft node that lost its volume looks uninitialised until it has
      # rejoined its peers; give it the chance before calling it a wipe.
      WAIT_SECONDS=${OPENBAO_REJOIN_SECONDS:-90} wait_for "$INIT_ADDR" '.initialized == true' soft ||
        refuse "the store reports it is NOT initialised, but Secret $RECOVERY already exists. Initialising would strand the recovery material it holds. Storage may have been wiped or pointed at an empty database: restore it, or delete the Secret yourself once you are sure the old store is gone"
    else
      initialise
    fi
  fi
  escrow
  wait_for "$ADDR" '.initialized == true and .sealed == false'
  authenticate
  OLD=$(applied_doc)
  apply_doc "$NEW"
  if [ -n "$OLD" ]; then narrow "$(jq -c . <<<"$OLD")" "$NEW"; fi
  retire "$NEW"
  run_hook "$GENERATE_HOOK" generate
  record_applied
  revoke_if_root
  log "done"
  ;;
*) refuse "OPENBAO_PHASE '$PHASE' must be widen or converge" ;;
esac
