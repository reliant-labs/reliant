"""
Forge KCL — in-cluster test-JWKS builders.

Emits the raw manifests for a forge.TestJWKS: a ConfigMap holding the
JWKS JSON, plus a tiny static-file Deployment + Service that serves it at
`/.well-known/jwks.json`. A service that validates JWTs against it gets a
normal in-cluster dependency (an `http://<name>.<ns>/.well-known/jwks.json`
URL) and exercises the REAL ES256 validation path — no host-port boot
race, no dev auth-bypass.

The PUBLIC JWK is derived from the ES256 PRIVATE key PEM at render time
via the `forge.derive_jwk` plugin (kcl_plugin.forge), so the served JWKS
can never drift from the signer. The private PEM is passed IN by the
caller (the CLI reads it from `.env.<env>` and injects it) — it is read
to derive the public JWK and never appears in the rendered output.
"""

import json

# Build the JWKS JSON document ({"keys":[<public-jwk>]}) for a TestJWKS
# from the ES256 private-key PEM. derive_jwk returns ONLY the public half
# (kty/crv/x/y/kid/alg/use) — the private key never enters the output.
# Build the JWKS JSON document ({"keys":[<public-jwk>]}) from the derived
# PUBLIC JWK dict. The JWK is derived by the CALLER via the
# kcl_plugin.forge.derive_jwk plugin (same pattern as resolve_port — the
# plugin is imported in the USER's file, never in the forge module, so the
# module renders fine under a plugin-less `kcl run`). `public_jwk` carries
# only the public half (kty/crv/x/y/kid/alg/use) — no private key.
_test_jwks_document = lambda public_jwk: any -> str {
    json.encode({"keys" = [public_jwk]})
}

# The busybox httpd image serving the static JWKS file. Pinned digest-less
# tag is fine for a test fixture; the deploy layer's _pull_policy handles
# the cache behavior.
_jwks_image = "busybox:1.36"

# Standard forge labels for the rendered objects, so
# `kubectl get -l app.kubernetes.io/managed-by=forge` finds them and the
# manifest-driven prune can sweep ones no longer declared.
_jwks_labels = lambda name: str -> {str:str} {
    {
        "app.kubernetes.io/name" = name
        "app.kubernetes.io/managed-by" = "forge"
        "app.kubernetes.io/component" = "test-jwks"
    }
}

# Render the full TestJWKS manifest set: ConfigMap + Deployment + Service.
# `public_jwk` is the PUBLIC JWK the caller derived (via
# kcl_plugin.forge.derive_jwk) from the ES256 private PEM it read from
# `.env.<env>`. Only the public half flows in — the private key is never
# seen here, so it can't leak into the rendered output.
test_jwks_manifests = lambda j: any, namespace: str, public_jwk: any -> [any] {
    _name = j.name
    _labels = _jwks_labels(_name)
    _doc = _test_jwks_document(public_jwk)
    _cm_name = "${_name}-jwks"
    # busybox httpd serves from /www; mount the ConfigMap there so
    # /.well-known/jwks.json resolves. httpd default doc root is the CWD
    # passed via -h.
    [
        {
            apiVersion = "v1"
            kind = "ConfigMap"
            metadata = {name = _cm_name, namespace = namespace, labels = _labels}
            # The single file the static server serves, under the
            # well-known path. A ConfigMap key can't contain "/", so we
            # key it by the basename and mount with a subPath-free
            # projection into the well-known dir below.
            data = {"jwks.json" = _doc}
        }
        {
            apiVersion = "apps/v1"
            kind = "Deployment"
            metadata = {name = _name, namespace = namespace, labels = _labels}
            spec = {
                replicas = 1
                selector = {matchLabels = {"app.kubernetes.io/name" = _name}}
                template = {
                    metadata = {labels = _labels}
                    spec = {
                        enableServiceLinks = False
                        securityContext = {
                            runAsNonRoot = True
                            runAsUser = 65532
                            runAsGroup = 65532
                            seccompProfile = {type = "RuntimeDefault"}
                        }
                        containers = [
                            {
                                name = "jwks"
                                image = _jwks_image
                                # Serve /www (where the ConfigMap mounts the
                                # well-known dir) in the foreground.
                                command = ["httpd", "-f", "-p", "8080", "-h", "/www"]
                                ports = [{containerPort = 8080}]
                                # This pod is a dependency of every sign-in in
                                # the environment: a verifier that cannot fetch
                                # the JWKS rejects every token. Without a
                                # readiness gate the Service takes endpoints the
                                # moment the container starts, so a rollout can
                                # point auth at a pod whose ConfigMap has not
                                # mounted yet — which surfaces as unexplained
                                # 401s rather than as a pod that is not ready.
                                # The probe fetches the actual document, so it
                                # gates on serving the JWKS rather than on httpd
                                # merely being alive.
                                readinessProbe = {
                                    httpGet = {path = "/.well-known/jwks.json", port = 8080}
                                    initialDelaySeconds = 1
                                    periodSeconds = 5
                                    timeoutSeconds = 2
                                    failureThreshold = 3
                                }
                                # Liveness restarts an httpd that has stopped
                                # answering at all. It is deliberately far
                                # slacker than readiness — a static server has
                                # no dependency that can make it briefly slow,
                                # so the only thing this should ever catch is a
                                # genuinely dead process, and a tight liveness
                                # probe on a healthy pod is an outage mode of
                                # its own. Both probes are declared because one
                                # without the other is a half-finished thought:
                                # readiness alone never restarts a wedged
                                # process, liveness alone never gates a rollout.
                                livenessProbe = {
                                    httpGet = {path = "/.well-known/jwks.json", port = 8080}
                                    initialDelaySeconds = 10
                                    periodSeconds = 30
                                    timeoutSeconds = 5
                                    failureThreshold = 6
                                }
                                # Requests and limits are deliberately tiny —
                                # busybox httpd serving one small document — but
                                # present, so the pod is Burstable rather than
                                # BestEffort and is not the first thing evicted
                                # under node pressure, taking sign-in with it.
                                resources = {
                                    requests = {cpu = "10m", memory = "16Mi"}
                                    limits = {cpu = "100m", memory = "64Mi"}
                                }
                                securityContext = {
                                    runAsNonRoot = True
                                    runAsUser = 65532
                                    runAsGroup = 65532
                                    readOnlyRootFilesystem = True
                                    allowPrivilegeEscalation = False
                                    capabilities = {drop = ["ALL"]}
                                }
                                volumeMounts = [
                                    {name = "jwks", mountPath = "/www/.well-known", readOnly = True}
                                ]
                            }
                        ]
                        volumes = [
                            {name = "jwks", configMap = {name = _cm_name}}
                        ]
                    }
                }
            }
        }
        {
            apiVersion = "v1"
            kind = "Service"
            metadata = {name = _name, namespace = namespace, labels = _labels}
            spec = {
                selector = {"app.kubernetes.io/name" = _name}
                ports = [{name = "http", port = 80, targetPort = 8080}]
            }
        }
    ]
}
