"""
Positive test — two lowering rules whose failure would be silent.

1. A `tool` bound to a CLUSTER renders NO Workload record. A tool is built
   into the image and never scheduled; a record would make Go render a
   Deployment for something nothing should run (and an expected-rollout
   that never converges). It still appears in output.workloads, so the
   build ships it.

2. A config-projected SecretRef (`store_key` set by config codegen) bound to
   a HOSTED workload lowers to `managedSecret {name = store_key, optional}`
   — the same declaration reads the same stored value on every runtime —
   while on a CLUSTER workload it stays a `secretRef` to the rendered
   Secret. A hosted `secretRef` would be refused by the Restricted profile,
   so without this lowering the one generated config map could not bind to
   a hosted workload at all.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_pinned = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

_env = {
    STRIPE_KEY = forge.SecretRef {name = "demo-secrets", key = "stripe_key", store_key = "STRIPE_KEY"}
    SENTRY_DSN = forge.SecretRef {name = "demo-secrets", key = "sentry_dsn", store_key = "SENTRY_DSN", optional = True}
}

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [
        fw.Workload {name = "cli", kind = "tool", build = forge.GoBuild {cmd = "./cmd/cli"}, image = "localhost:5050/demo", args = ["admin"], runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {name = "api", image = "localhost:5050/demo", args = ["api"], env = _env, runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {name = "hosted-api", image = _pinned, ports = [fw.Port {name = "http", port = 8080, expose = True}], env = _env, runtime = forge.OnHosted {}}
    ]
})

_records = [m.metadata.name for m in output.manifests if m.kind == "Workload"]
assert_tool_has_no_record = "cli" not in _records
assert_tool_still_built = [w for w in output.workloads if w.name == "cli"][0].build.type == "go"
assert_scheduled_sibling_has_record = _records == ["api"]

_envof = lambda n: str -> {str:} {
    {e.name: e for e in [w for w in output.workloads if w.name == n][0].spec.env}
}
_h = _envof("hosted-api")
_c = _envof("api")
assert_hosted_managed_secret = _h["STRIPE_KEY"] == {name = "STRIPE_KEY", managedSecret = {name = "STRIPE_KEY"}}
assert_hosted_optional_kept = _h["SENTRY_DSN"].managedSecret == {name = "SENTRY_DSN", optional = True}
assert_hosted_no_secret_ref = all e in [v for _, v in _h] { "secretRef" not in e }
assert_cluster_keeps_secret_ref = _c["STRIPE_KEY"].secretRef == {name = "demo-secrets", key = "stripe_key"}
assert_cluster_optional_kept = _c["SENTRY_DSN"].secretRef.optional == True
