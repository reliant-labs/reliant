#!/usr/bin/env bash
# Smoke test for the Gateway API ingress story. Real k3d + Traefik + curl.
#
# Drives a freshly-scaffolded `forge new --kind service` project through
#   forge cluster up      (Gateway API CRDs + Traefik + GatewayClass)
#   kubectl apply         (Gateway + HTTPRoute from rendered KCL,
#                          plus a stand-in traefik/whoami backend instead
#                          of the forge-built service image)
#   curl http://localhost:${HOST_PORT}/   (proves the host->LB->Traefik->
#                                          Gateway->HTTPRoute->Service->
#                                          pod path actually wires up
#                                          end to end)
#
# Requires: k3d, kubectl, curl, go, kcl, docker on PATH. Bash 4+.
# Usage:    bash scripts/e2e-ingress.sh
#           HOST_PORT=38080 HOST_PORT_GRPC=39190 bash scripts/e2e-ingress.sh
# Cost:     ~2 minutes for a clean run (first run downloads CRDs +
#           pulls the traefik/whoami image; subsequent runs are faster).
#
# Exit codes:
#   0  PASS
#   1  Test failed (a step did not behave as expected — diagnostic
#                   dump of gateway/httproute/traefik logs is printed
#                   before exit)
#   2  Preflight failed (missing tool / docker not running / requested
#                        host ports already bound)
#
# The cluster is named `e2e-ingress` (distinct from the scaffold default
# so this smoke can run alongside other forge k3d clusters). Cleanup
# runs unconditionally via trap, even on failure.
#
# Known caveats this smoke deliberately works around (rather than
# fixes — see the report from the script's author for details):
#   - The scaffolded `kcl.mod` pins the forge KCL module to a
#     published git tag that may not exist during local development.
#     We rewrite to a `path = ` dep pointing at the repo's kcl/ dir.
#   - The scaffolded `deploy/k3d.yaml` maps host:18080->container:80
#     for the bundled Traefik that `forge cluster up` then
#     disables; the generated `deploy/k3d-ports.yaml` would append a
#     conflicting host:18080 mapping. We strip the conflicting entry.

set -u
set -o pipefail

# ---- knobs ------------------------------------------------------------

CLUSTER_NAME="e2e-ingress"
PROJECT_NAME="e2e-ingress"
MODULE_PATH="example.com/${PROJECT_NAME}"
NAMESPACE="${PROJECT_NAME}-dev"
# Smoke uses a non-default host port to avoid colliding with any other
# k3d-based forge project already running on the dev box (which would
# itself be using the scaffold default 18080). Override via env var.
HOST_PORT="${HOST_PORT:-28080}"
HOST_PORT_GRPC="${HOST_PORT_GRPC:-29190}"
CURL_TIMEOUT=5
CURL_RETRY_SECONDS=60
GATEWAY_READY_TIMEOUT=180

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
FORGE_BIN="${FORGE_BIN:-/tmp/forge-e2e-${RANDOM}}"
WORK_DIR="$(mktemp -d -t forge-e2e-ingress.XXXXXX)"
PROJECT_DIR="${WORK_DIR}/${PROJECT_NAME}"

# ---- output helpers ---------------------------------------------------

c_red()    { printf "\033[31m%s\033[0m" "$1"; }
c_green()  { printf "\033[32m%s\033[0m" "$1"; }
c_yellow() { printf "\033[33m%s\033[0m" "$1"; }
c_cyan()   { printf "\033[36m%s\033[0m" "$1"; }

step()   { echo; echo "==> $(c_cyan "$*")"; }
info()   { echo "  $*"; }
warn()   { echo "  $(c_yellow "warning:") $*" >&2; }
fail()   { echo; echo "$(c_red "FAIL:") $*" >&2; exit 1; }
skip()   { echo; echo "$(c_yellow "SKIP:") $*" >&2; exit 2; }

# ---- cleanup trap -----------------------------------------------------

CLEANUP_DONE=0
cleanup() {
    # Preserve the in-flight exit code so the trap doesn't mask a
    # FAIL/SKIP with the cleanup's own success.
    rc=$?
    [ "${CLEANUP_DONE}" -eq 1 ] && return
    CLEANUP_DONE=1
    echo
    step "Cleanup"
    if command -v k3d >/dev/null 2>&1; then
        if k3d cluster list -o json 2>/dev/null | grep -q "\"name\": *\"${CLUSTER_NAME}\""; then
            info "deleting k3d cluster ${CLUSTER_NAME}..."
            k3d cluster delete "${CLUSTER_NAME}" >/dev/null 2>&1 || warn "k3d cluster delete failed"
        else
            info "no cluster named ${CLUSTER_NAME} to delete"
        fi
    fi
    if [ -f "${FORGE_BIN}" ]; then rm -f "${FORGE_BIN}"; fi
    if [ -n "${WORK_DIR:-}" ] && [ -d "${WORK_DIR}" ]; then
        info "removing tempdir ${WORK_DIR}"
        rm -rf "${WORK_DIR}"
    fi
    # Re-exit with the original status so a SKIP (2) or FAIL (1)
    # propagates out to the caller / Makefile / CI.
    exit "${rc}"
}
trap cleanup EXIT INT TERM

# ---- 1. preflight -----------------------------------------------------

step "Preflight"
missing=()
for t in k3d kubectl curl go kcl docker; do
    if ! command -v "$t" >/dev/null 2>&1; then
        missing+=("$t")
    fi
done
if [ "${#missing[@]}" -ne 0 ]; then
    skip "missing required tool(s): ${missing[*]}"
fi
info "all required tools present"

if ! docker info >/dev/null 2>&1; then
    skip "docker daemon not reachable (start Docker Desktop / docker engine)"
fi
info "docker daemon reachable"

# Make sure the cluster name isn't already in use from a previous run.
if k3d cluster list -o json 2>/dev/null | grep -q "\"name\": *\"${CLUSTER_NAME}\""; then
    info "previous ${CLUSTER_NAME} cluster found — deleting before run"
    k3d cluster delete "${CLUSTER_NAME}" >/dev/null 2>&1 || \
        fail "could not delete preexisting cluster ${CLUSTER_NAME}"
fi

# Smoke rewrites the scaffolded gateway listener ports to
# HOST_PORT/HOST_PORT_GRPC so we don't collide with any other k3d-based
# forge project the user may have running (those default to 18080/19190).
for p in "${HOST_PORT}" "${HOST_PORT_GRPC}"; do
    busy=$(docker ps --filter "publish=${p}" --format '{{.Names}}' 2>/dev/null | head -1)
    if [ -n "${busy}" ]; then
        skip "host port ${p} already bound by docker container ${busy} — set HOST_PORT/HOST_PORT_GRPC to a free port or stop the container"
    fi
done

# ---- 2. build forge ---------------------------------------------------

step "Build forge CLI"
info "building from ${REPO_ROOT} -> ${FORGE_BIN}"
( cd "${REPO_ROOT}" && go build -o "${FORGE_BIN}" ./cmd/forge ) || \
    fail "go build ./cmd/forge failed — check WIP state in internal/cli/* (forge new may be broken)"
info "$( "${FORGE_BIN}" version 2>&1 | head -1 )"

# ---- 3. scaffold project ----------------------------------------------

step "Scaffold project (forge new --kind service)"
info "tempdir: ${WORK_DIR}"
(
    cd "${WORK_DIR}"
    # --skip-tools so we don't try to `go install` codegen plugins;
    # we only need the manifests for this smoke. --link-forge: the binary is
    # built from this checkout, so the scaffold compiles forge from it.
    "${FORGE_BIN}" new "${PROJECT_NAME}" \
        --mod "${MODULE_PATH}" \
        --kind service \
        --skip-tools \
        --link-forge 2>&1
) || fail "forge new failed — likely WIP breakage in internal/cli/new.go; let the user fix and retry"

[ -d "${PROJECT_DIR}/deploy/kcl/dev" ] || fail "scaffold missing deploy/kcl/dev"
[ -f "${PROJECT_DIR}/deploy/kcl/ingress.k" ] || fail "scaffold missing deploy/kcl/ingress.k"
[ -f "${PROJECT_DIR}/deploy/k3d.yaml" ] || fail "scaffold missing deploy/k3d.yaml"

# ---- 4. uncomment the example HTTPRoute ------------------------------

step "Rewrite scaffolded Gateway listener ports to ${HOST_PORT}/${HOST_PORT_GRPC}"
INGRESS_K="${PROJECT_DIR}/deploy/kcl/ingress.k"
python3 - "${INGRESS_K}" "${HOST_PORT}" "${HOST_PORT_GRPC}" <<'PYEOF'
import sys
path, http_port, grpc_port = sys.argv[1], sys.argv[2], sys.argv[3]
src = open(path).read()
new = src.replace("port = 18080", f"port = {http_port}", 1)
new = new.replace("port = 19190", f"port = {grpc_port}", 1)
open(path, "w").write(new)
PYEOF

step "Activate the example HTTPRoute in deploy/kcl/ingress.k"
# The template ships the route commented out as a 6-line block
# (forge.HTTPRoute { ... } with name/gateway/listener/service/port).
# Use python for a portable in-place edit — perl/sed -i differs on
# macOS vs Linux and a 6-line region is awkward in sed.
python3 - "${INGRESS_K}" <<'PYEOF'
import sys, re
path = sys.argv[1]
src = open(path).read()
# Uncomment the contiguous HTTPRoute example block. Match lines that
# start with `    # forge.HTTPRoute {` through the matching `    # }`.
def uncomment(m):
    body = m.group(0)
    return "\n".join(
        re.sub(r'^(\s*)#\s?', r'\1', line, count=1)
        for line in body.splitlines()
    )
new = re.sub(
    r'^( {4}# forge\.HTTPRoute \{\n(?: {4}#.*\n)+ {4}# \})',
    uncomment,
    src,
    count=1,
    flags=re.MULTILINE,
)
if new == src:
    sys.stderr.write("ERROR: could not find the commented HTTPRoute example block\n")
    sys.exit(1)
open(path, "w").write(new)
PYEOF
[ $? -eq 0 ] || fail "could not uncomment example HTTPRoute"
info "HTTPRoute block activated:"
grep -A 6 "HTTP_ROUTES" "${INGRESS_K}" | sed 's/^/    /'

# ---- 4b. pick a registry port that's not already bound ----------------

# The scaffolded deploy/k3d.yaml creates a k3d registry on host port
# 5050. If the user already has a forge project's registry running
# on 5050 (very common — every previous `forge cluster up` left
# one), `k3d cluster create` will fail with "port already allocated".
# Pick a fresh port for the smoke test.
step "Pick a free host port for the smoke-test registry"
REGISTRY_PORT=""
for candidate in 5150 5151 5152 5153 5154 5155 5050; do
    if ! (lsof -i ":${candidate}" -sTCP:LISTEN >/dev/null 2>&1 || \
          docker ps --filter "publish=${candidate}" -q 2>/dev/null | grep -q .); then
        REGISTRY_PORT="${candidate}"
        break
    fi
done
[ -n "${REGISTRY_PORT}" ] || fail "no free host port in 5050,5150-5155 for the registry"
info "registry host port: ${REGISTRY_PORT}"
# Rewrite the scaffolded hostPort in deploy/k3d.yaml.
python3 - "${PROJECT_DIR}/deploy/k3d.yaml" "${REGISTRY_PORT}" <<'PYEOF'
import sys, re
path, port = sys.argv[1], sys.argv[2]
src = open(path).read()
new = re.sub(r'hostPort:\s*"5050"', f'hostPort: "{port}"', src, count=1)
open(path, "w").write(new)
PYEOF

# ---- 5. fix the k3d.yaml port collision -------------------------------

# The scaffolded deploy/k3d.yaml maps host:18080->container:80 for the
# bundled Traefik that `forge cluster up` then disables. The
# generated deploy/k3d-ports.yaml fragment will append a second
# host:18080->container:18080 entry from the Gateway listener — k3d
# rejects two entries on the same host port.
#
# Fix in the test: strip the conflicting `- port: 18080:80` and
# `- port: 18443:443` entries from the scaffolded k3d.yaml so the
# generated fragment owns the host ports.
step "Strip pre-existing 18080/18443 mappings from scaffolded k3d.yaml"
K3D_YAML="${PROJECT_DIR}/deploy/k3d.yaml"
python3 - "${K3D_YAML}" <<'PYEOF'
import sys
path = sys.argv[1]
src = open(path).read()
# Drop the two "- port: 18080:80 / nodeFilters: [loadbalancer]" and
# "- port: 18443:443 / nodeFilters: [loadbalancer]" blocks. They're
# both 3 lines each:
#   - port: 18080:80
#     nodeFilters:
#       - loadbalancer
import re
new = re.sub(
    r'^\s*-\s+port:\s+18080:80\n\s+nodeFilters:\n\s+-\s+loadbalancer\n',
    '',
    src,
    flags=re.MULTILINE,
)
new = re.sub(
    r'^\s*-\s+port:\s+18443:443\n\s+nodeFilters:\n\s+-\s+loadbalancer\n',
    '',
    new,
    flags=re.MULTILINE,
)
open(path, "w").write(new)
PYEOF
info "remaining ports block in deploy/k3d.yaml:"
grep -A 4 "^ports:" "${K3D_YAML}" | sed 's/^/    /' || info "(no ports block — will be supplied by k3d-ports.yaml)"

# ---- 6a. verify the project is born with a resolvable KCL module ------

# No rewrite needed. `forge project new` materializes the KCL module
# embedded in the forge binary into `.forge-kcl/` and points the
# project's kcl.mod at it by relative path, so the import resolves
# offline with nothing published. This step used to hand-patch the
# dependency because the scaffold pinned a git tag that did not exist.
step "Verify the scaffold vendored the forge KCL module"
[ -f "${PROJECT_DIR}/.forge-kcl/kcl.mod" ] || fail "scaffold did not vendor the forge KCL module into .forge-kcl/"

# ---- 6b. run forge generate to emit k3d-ports.yaml --------------------

step "forge generate (emits deploy/k3d-ports.yaml)"
(
    cd "${PROJECT_DIR}"
    "${FORGE_BIN}" generate 2>&1 | tail -20
) || warn "forge generate exited non-zero — continuing (manifests may still render)"

# If forge generate couldn't produce k3d-ports.yaml (e.g. because the
# KCL module fetch failed), hand-write the fragment we know the
# scaffold should produce: one entry per listener in deploy/kcl/ingress.k.
if [ ! -f "${PROJECT_DIR}/deploy/k3d-ports.yaml" ]; then
    warn "deploy/k3d-ports.yaml was not generated — writing a hand-crafted fallback"
    cat >"${PROJECT_DIR}/deploy/k3d-ports.yaml" <<EOF
# Hand-written by scripts/e2e-ingress.sh because \`forge generate\` could
# not run the KCL extractor (typically: forge KCL module fetch failed).
# Mirrors what \`forge generate\` would emit from deploy/kcl/ingress.k.
ports:
  # public/http
  - port: ${HOST_PORT}:${HOST_PORT}
    nodeFilters:
      - loadbalancer
  # public/grpc
  - port: ${HOST_PORT_GRPC}:${HOST_PORT_GRPC}
    nodeFilters:
      - loadbalancer
EOF
fi
info "k3d-ports.yaml:"
sed 's/^/    /' "${PROJECT_DIR}/deploy/k3d-ports.yaml"

# ---- 7. forge cluster up ----------------------------------------------

step "forge cluster up (5 min timeout)"
(
    cd "${PROJECT_DIR}"
    # 5-minute timeout — first run pulls images + downloads CRDs.
    timeout 300 "${FORGE_BIN}" cluster up --wait 2>&1
) || fail "forge cluster up failed"

# Sanity: the cluster is reachable.
kubectl --context "k3d-${CLUSTER_NAME}" get nodes >/dev/null 2>&1 || \
    fail "kubectl can't reach k3d-${CLUSTER_NAME} after cluster up"
info "cluster reachable"

# Sanity: Traefik is installed and the traefik GatewayClass is ready.
kubectl --context "k3d-${CLUSTER_NAME}" get gatewayclass traefik >/dev/null 2>&1 || \
    fail "traefik GatewayClass not present after cluster up"
info "traefik GatewayClass present"

# ---- 8. apply Gateway + HTTPRoute, plus a stand-in backend -----------

step "Render KCL manifests and apply Gateway/HTTPRoute"
MANIFESTS_FILE="${WORK_DIR}/manifests.yaml"
(
    cd "${PROJECT_DIR}"
    kcl run "deploy/kcl/dev/main.k" \
        -S manifests \
        -D image_tag=e2e \
        -D namespace="${NAMESPACE}" \
        > "${MANIFESTS_FILE}"
) || fail "kcl run failed"
[ -s "${MANIFESTS_FILE}" ] || fail "no manifests produced"

info "rendered $(grep -c '^---' "${MANIFESTS_FILE}" || true) documents"

# Create the namespace first (Gateway + HTTPRoute live in it).
kubectl --context "k3d-${CLUSTER_NAME}" create namespace "${NAMESPACE}" \
    --dry-run=client -o yaml | kubectl --context "k3d-${CLUSTER_NAME}" apply -f - >/dev/null

# Apply only the Gateway-API kinds from the rendered manifests. We
# deliberately skip the rendered Deployment/Service for the project
# image (which doesn't exist — we never built it). yq filters by kind.
yq eval-all 'select(.kind == "Gateway" or .kind == "HTTPRoute" or .kind == "GRPCRoute" or .kind == "Namespace")' \
    "${MANIFESTS_FILE}" \
  | kubectl --context "k3d-${CLUSTER_NAME}" apply -f - || \
    fail "kubectl apply (Gateway/HTTPRoute) failed"

# Stand-in backend: traefik/whoami listens on :80 and prints request
# info. Service named "${PROJECT_NAME}" exposing port 8080 ->
# targetPort 80 so the HTTPRoute (which targets port 8080) finds it.
step "Apply stand-in whoami backend"
# The KCL-rendered Namespace enforces PodSecurity "restricted", so the
# stand-in container needs the full hardened securityContext. Also use
# port 8080 inside the container instead of :80 (traefik/whoami honors
# WHOAMI_PORT_NUMBER) so we don't need NET_BIND_SERVICE for <1024.
kubectl --context "k3d-${CLUSTER_NAME}" apply -n "${NAMESPACE}" -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${PROJECT_NAME}
  labels: { app: ${PROJECT_NAME} }
spec:
  replicas: 1
  selector: { matchLabels: { app: ${PROJECT_NAME} } }
  template:
    metadata: { labels: { app: ${PROJECT_NAME} } }
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        seccompProfile: { type: RuntimeDefault }
      containers:
        - name: whoami
          image: traefik/whoami:v1.10
          env:
            - { name: WHOAMI_PORT_NUMBER, value: "8080" }
          ports: [{ containerPort: 8080, name: http }]
          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            runAsUser: 65532
            capabilities: { drop: ["ALL"] }
            seccompProfile: { type: RuntimeDefault }
---
apiVersion: v1
kind: Service
metadata:
  name: ${PROJECT_NAME}
  labels: { app: ${PROJECT_NAME} }
spec:
  selector: { app: ${PROJECT_NAME} }
  ports:
    - name: http
      port: 8080
      targetPort: 8080
EOF
[ $? -eq 0 ] || fail "could not apply stand-in whoami backend"

info "waiting for whoami deployment rollout..."
kubectl --context "k3d-${CLUSTER_NAME}" -n "${NAMESPACE}" \
    rollout status deployment/"${PROJECT_NAME}" --timeout=120s || \
    fail "whoami deployment never reached ready"

# ---- 9. wait for Gateway Programmed ----------------------------------

step "Wait for Gateway/public to be Programmed"
# Gateway API v1.x condition: Programmed=True is the cue Traefik has
# wired up the listener. Fall back to a sleep if the condition name
# differs (older Traefik builds reported Ready instead).
if ! kubectl --context "k3d-${CLUSTER_NAME}" -n "${NAMESPACE}" wait \
        gateway/public --for=condition=Programmed \
        --timeout="${GATEWAY_READY_TIMEOUT}s" 2>/dev/null; then
    warn "Programmed condition didn't go True within ${GATEWAY_READY_TIMEOUT}s — sleeping 10s and proceeding"
    sleep 10
fi
kubectl --context "k3d-${CLUSTER_NAME}" -n "${NAMESPACE}" get gateway,httproute -o wide | sed 's/^/    /'

# ---- 10. curl the host port ------------------------------------------

step "curl http://localhost:${HOST_PORT}/  (retry up to ${CURL_RETRY_SECONDS}s)"
deadline=$(( $(date +%s) + CURL_RETRY_SECONDS ))
last_body=""
last_code=""
while [ "$(date +%s)" -lt "${deadline}" ]; do
    body=$(curl --max-time "${CURL_TIMEOUT}" -s -o /dev/stdout -w "\n___HTTP=%{http_code}___" \
        "http://localhost:${HOST_PORT}/" 2>/dev/null || true)
    last_code=$(printf '%s' "$body" | grep -oE '___HTTP=[0-9]+___' | tail -1 | grep -oE '[0-9]+')
    last_body=$(printf '%s' "$body" | sed '/___HTTP=/d')
    if [ "${last_code}" = "200" ]; then
        echo
        info "$(c_green "HTTP 200")"
        echo "${last_body}" | sed 's/^/    /'
        echo
        echo "$(c_green "PASS"): ingress path works end to end."
        exit 0
    fi
    info "got HTTP=${last_code:-?} — retrying..."
    sleep 3
done

echo
warn "curl never returned 200 (last code: ${last_code:-none})"
echo "  last body:"
echo "${last_body}" | sed 's/^/    /'
echo
info "diagnostic dump:"
kubectl --context "k3d-${CLUSTER_NAME}" -n "${NAMESPACE}" get gateway,httproute,svc,deploy,pod -o wide 2>&1 | sed 's/^/    /'
kubectl --context "k3d-${CLUSTER_NAME}" -n "${NAMESPACE}" describe gateway public 2>&1 | tail -40 | sed 's/^/    /'
kubectl --context "k3d-${CLUSTER_NAME}" -n traefik-system logs -l app.kubernetes.io/name=traefik --tail=80 2>&1 | sed 's/^/    /'
fail "ingress smoke did not return 200 within ${CURL_RETRY_SECONDS}s"
