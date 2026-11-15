"""
Positive test for the forge.TestJWKS manifest builder.

forge.test_jwks_manifests(jwks, namespace, public_jwk) emits a ConfigMap
(the JWKS JSON) + a static-file Deployment + Service serving it at
`/.well-known/jwks.json`. The public JWK is supplied by the caller (in
real use, derived via kcl_plugin.forge.derive_jwk; here a literal so the
plugin-less `kcl run` can exercise the manifest shape).

Invariants:
  * Exactly three manifests: ConfigMap, Deployment, Service.
  * The ConfigMap data carries the JWKS doc (a {"keys":[...]} JSON
    string) under jwks.json.
  * The Service exposes port 80 -> 8080 and selects the Deployment.
  * The Deployment mounts the ConfigMap at /www/.well-known so httpd
    serves /.well-known/jwks.json.
  * Every object carries the forge managed-by label.
"""

import forge

_jwks = forge.TestJWKS {
    name = "auth-jwks"
    kid = "k1"
    private_key = forge.StoreRef { key = "JWKS_PEM" }
}

# A literal public JWK (in real use: plugin.derive_jwk(pem, kid, alg)).
_public_jwk = {
    kty = "EC"
    crv = "P-256"
    x = "abc"
    y = "def"
    kid = "k1"
    alg = "ES256"
    use = "sig"
}

manifests = forge.test_jwks_manifests(_jwks, "auth", _public_jwk)

_cm = [m for m in manifests if m.kind == "ConfigMap"][0]
_deploy = [m for m in manifests if m.kind == "Deployment"][0]
_svc = [m for m in manifests if m.kind == "Service"][0]

# ── Invariants ─────────────────────────────────────────────────────

assert_count = len(manifests) == 3
assert_kinds = _cm.kind == "ConfigMap" and _deploy.kind == "Deployment" and _svc.kind == "Service"

# ConfigMap holds the JWKS doc under jwks.json, in the auth namespace.
assert_cm_ns = _cm.metadata.namespace == "auth"
assert_cm_name = _cm.metadata.name == "auth-jwks-jwks"
assert_cm_has_doc = "jwks.json" in _cm.data
assert_cm_doc_keys = "\"keys\"" in _cm.data["jwks.json"]
assert_cm_doc_kid = "\"k1\"" in _cm.data["jwks.json"]

# Service: 80 -> 8080, selects the Deployment pods.
assert_svc_port = _svc.spec.ports[0].port == 80 and _svc.spec.ports[0].targetPort == 8080
assert_svc_selector = _svc.spec.selector["app.kubernetes.io/name"] == "auth-jwks"

# Deployment mounts the ConfigMap at the well-known dir so the static
# server serves /.well-known/jwks.json.
assert_mount = _deploy.spec.template.spec.containers[0].volumeMounts[0].mountPath == "/www/.well-known"
assert_volume_cm = _deploy.spec.template.spec.volumes[0].configMap.name == "auth-jwks-jwks"

# Deployability: this pod gates every sign-in in the environment, so it
# must carry a readiness probe (or the Service takes endpoints before the
# ConfigMap has mounted, and auth fails with unexplained 401s) and
# resources (or it is BestEffort and the first thing evicted under node
# pressure). `forge doctor` fails a project on both, and a container
# rendered entirely by forge must not be the thing that fails it — the
# project has no knob to fix it with.
_jwks_container = _deploy.spec.template.spec.containers[0]
assert_readiness_probe = _jwks_container.readinessProbe.httpGet.path == "/.well-known/jwks.json" and \
    _jwks_container.readinessProbe.httpGet.port == 8080
# Both probes, because `forge doctor` treats exactly one as a
# half-finished thought — and it is right: readiness alone never restarts
# a wedged process, liveness alone never gates a rollout.
assert_liveness_probe = _jwks_container.livenessProbe.httpGet.path == "/.well-known/jwks.json" and \
    _jwks_container.livenessProbe.httpGet.port == 8080
# Liveness must be slacker than readiness, or a momentary slow response
# restarts a pod that readiness would merely have taken out of rotation.
assert_liveness_slacker = _jwks_container.livenessProbe.periodSeconds > _jwks_container.readinessProbe.periodSeconds
assert_resources = _jwks_container.resources.requests.cpu != Undefined and \
    _jwks_container.resources.requests.memory != Undefined and \
    _jwks_container.resources.limits.cpu != Undefined and \
    _jwks_container.resources.limits.memory != Undefined

# Managed-by label on every object.
assert_labels = _cm.metadata.labels["app.kubernetes.io/managed-by"] == "forge" and \
    _deploy.metadata.labels["app.kubernetes.io/managed-by"] == "forge" and \
    _svc.metadata.labels["app.kubernetes.io/managed-by"] == "forge"
