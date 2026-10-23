#!/usr/bin/env bash
# escrow.sh - copy the `bao operator init` output to the recovery escrow in
# Google Secret Manager.
#
# Mounted at /escrow/escrow.sh by the bootstrap component and run by converge.sh
# with the init output file as $1, on EVERY converge run, after the in-cluster
# recovery Secret exists and before anything is converged. A non-zero exit
# fails the run: a store whose recovery output is not escrowed outside the
# cluster is not converged.
#
# WRITE-ONLY BY DESIGN. The Job's Google service account holds
# secretmanager.versions.add on the one escrow secret and nothing else, so this
# script cannot read the container, list its versions or learn whether it is
# empty. "Copy if empty" is therefore decided in the cluster: a ledger
# ConfigMap records the uid of the recovery Secret that was escrowed. The
# recovery Secret is created once and never rewritten, so its uid names one
# `operator init` output:
#
#   ledger holds this uid   -> already escrowed: nothing to do, no Google call
#   anything else           -> add ONE version, then write the ledger
#
# A crash between the add and the ledger write adds a second, identical version
# on the next run. That is the whole cost of not being able to read.
#
# Credentials: the Job's own Workload Identity, through the GKE metadata
# server. Nothing is stored and nothing is passed on a command line: the access
# token and the payload travel in files. This script never prints either.
set -euo pipefail
umask 077

INIT_FILE="${1:?usage: escrow.sh <init-output-file>}"
NS="${OPENBAO_NAMESPACE:?OPENBAO_NAMESPACE}"
RECOVERY="${OPENBAO_RECOVERY_SECRET:?OPENBAO_RECOVERY_SECRET}"
PROJECT="${OPENBAO_ESCROW_PROJECT:?OPENBAO_ESCROW_PROJECT}"
SECRET="${OPENBAO_ESCROW_SECRET:?OPENBAO_ESCROW_SECRET}"
LEDGER="${OPENBAO_ESCROW_LEDGER:?OPENBAO_ESCROW_LEDGER}"
IDENTITY="${OPENBAO_ESCROW_IDENTITY:-the converge Job service account}"
GSM="${OPENBAO_ESCROW_GSM_BASE:-https://secretmanager.googleapis.com}"
META="${OPENBAO_ESCROW_METADATA_URL:-http://metadata.google.internal}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

log() { echo "[openbao-escrow] $*" >&2; }
die() { log "FATAL: $*"; exit 1; }

# remote_error: the status and message of a Google error response. They name
# resources and never echo a request payload.
remote_error() {
  jq -r '[.error.status // empty, .error.message // empty] | map(select(. != "")) | join(": ")' "$WORK/resp" 2>/dev/null | head -c 300 || true
}

[ -r "$INIT_FILE" ] && [ -s "$INIT_FILE" ] || die "no init output at $INIT_FILE"

uid="$(kubectl -n "$NS" get secret "$RECOVERY" -o 'jsonpath={.metadata.uid}')" ||
  die "cannot read the uid of Secret $NS/$RECOVERY"
[ -n "$uid" ] || die "Secret $NS/$RECOVERY has no uid"

recorded="$(kubectl -n "$NS" get configmap "$LEDGER" -o 'jsonpath={.data.escrowed-secret-uid}' 2>/dev/null)" || recorded=""
if [ -n "$recorded" ] && [ "$recorded" = "$uid" ]; then
  log "this init output is already escrowed in $SECRET (ledger ConfigMap $LEDGER): nothing to do"
  exit 0
fi

code="$(curl -sS -o "$WORK/tok" -w '%{http_code}' -H 'Metadata-Flavor: Google' \
  "$META/computeMetadata/v1/instance/service-accounts/default/token" 2>"$WORK/curl.err")" || code=000
[ "$code" = 200 ] ||
  die "cannot get a Google access token from the metadata server (HTTP $code). The escrow runs as $IDENTITY: its Kubernetes service account must be bound to the Google service account by Workload Identity, and the node pool must run the GKE metadata server. $(head -c 200 "$WORK/curl.err")"
token="$(jq -r '.access_token // empty' "$WORK/tok")"
[ -n "$token" ] || die "the metadata server answered 200 with no access_token"
printf 'Authorization: Bearer %s\n' "$token" >"$WORK/hdr"
rm -f "$WORK/tok"

jq -n --rawfile data "$INIT_FILE" '{payload: {data: ($data | @base64)}}' >"$WORK/body"

code="$(curl -sS -o "$WORK/resp" -w '%{http_code}' -X POST -H "@$WORK/hdr" -H 'Content-Type: application/json' \
  --data-binary "@$WORK/body" "$GSM/v1/projects/$PROJECT/secrets/$SECRET:addVersion" 2>"$WORK/curl.err")" || code=000
rm -f "$WORK/body"
case "$code" in
200) ;;
403) die "Secret Manager refused: $(remote_error). $IDENTITY needs roles/secretmanager.secretVersionAdder on projects/$PROJECT/secrets/$SECRET and nothing else" ;;
404) die "projects/$PROJECT/secrets/$SECRET does not exist: create the container first (it holds no value until this script adds one). $(remote_error)" ;;
*) die "Secret Manager answered HTTP $code adding a version to $SECRET: $(remote_error) $(head -c 200 "$WORK/curl.err")" ;;
esac

version="$(jq -r '(.name // "") | split("/") | last' "$WORK/resp")"
case "$version" in
1) log "the escrow was empty: the init output is now version 1 of projects/$PROJECT/secrets/$SECRET" ;;
*) log "added version $version of projects/$PROJECT/secrets/$SECRET (an earlier copy already existed; an identical copy is harmless)" ;;
esac

if ! jq -n --arg name "$LEDGER" --arg ns "$NS" --arg uid "$uid" --arg project "$PROJECT" --arg secret "$SECRET" --arg version "$version" '{
    apiVersion: "v1", kind: "ConfigMap",
    metadata: {name: $name, namespace: $ns, labels: {"app.kubernetes.io/managed-by": "openbao-converge"}},
    data: {"escrowed-secret-uid": $uid, "escrow": ("projects/" + $project + "/secrets/" + $secret), "version": $version}}' |
  kubectl apply -f - >/dev/null 2>"$WORK/kubectl.err"; then
  log "WARNING: the init output IS escrowed (version $version) but ledger ConfigMap $LEDGER could not be written ($(head -c 200 "$WORK/kubectl.err")). The next run will add another identical version"
fi
log "done"
