"""
Positive test — opting out of ALL observability (`enabled = False`).

Every runtime that runs a process gets OTEL_SDK_DISABLED=true and no endpoint;
a frontend's browser ingest is forced off; the release shape's policy lists
every category.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {enabled = False}
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {name = "hosted", image = "ghcr.io/acme/hosted", runtime = forge.OnHosted {}}
    ]
    control_plane = forge.ControlPlane {}
    frontends = [forge.Frontend {
        name = "site", image = "ghcr.io/acme/site", path = "site", type = "vite", public_dir = "dist"
        runtime = forge.OnHosted {}
        runtime_config = {OTEL_ENDPOINT = "/_otel", APP_NAME = "acme"}
    }]
})

_env = lambda o: any, n: str -> {str:str} {
    {e.name: e.value for w in o.workloads if w.name == n for e in w.spec.env if e.value}
}

assert_cluster_sdk_disabled = _env(output, "api").OTEL_SDK_DISABLED == "true"
assert_hosted_sdk_disabled = _env(output, "hosted").OTEL_SDK_DISABLED == "true"
assert_no_endpoint = "OTEL_EXPORTER_OTLP_ENDPOINT" not in _env(output, "api")
assert_policy_disabled = output.observability.policy.enabled == False
assert_policy_lists_every_category = output.observability.policy.disabled == ["browser", "logs", "metadata", "metrics", "profiles", "traces"]
assert_browser_off_over_author = output.frontends[0].runtime_config_spec.OTEL_ENDPOINT == {"value": "off"}
assert_other_runtime_config_kept = output.frontends[0].runtime_config_spec.APP_NAME == {"value": "acme"}
