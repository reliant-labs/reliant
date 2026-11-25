"""
Positive test — OTEL_RESOURCE_ATTRIBUTES, derived beside OTEL_SERVICE_NAME.

Every runtime that runs a process (host, cluster, hosted) gets

    deployment.environment.name=<Bundle.env>,service.namespace=<Bundle.project>,
    service.version=<Bundle.release>

with each key omitted when its source is empty: forge reports only what it
knows (never `latest`, never an empty value). Values are percent-encoded, so
a `,` or `=` cannot split an attribute.

ALL-OR-NOTHING: an author's non-empty value is kept whole, never spliced
into. An EMPTY literal is "no value" to the process (the same rule as
OTEL_SERVICE_NAME) and takes the derived one, in place. A tool, a
build-only artifact and a compose service run no forge process and get none.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_pinned = "ghcr.io/acme/hosted-api@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
_go = forge.GoBuild {cmd = "./cmd/acme"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "staging"
    release = "v1.2.3"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [
        fw.Workload {name = "host-api", build = _go, args = ["server"], runtime = forge.OnHost {}}
        fw.Workload {name = "cluster-worker", kind = "worker", image = "localhost:5050/acme", runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {name = "hosted-api", image = _pinned, runtime = forge.OnHosted {}}
        fw.Workload {name = "compose-db", image = "postgres:16", runtime = forge.OnCompose {service = "db"}}
        fw.Workload {name = "a-tool", kind = "tool", build = _go, args = ["x"], runtime = forge.OnHost {}}
        fw.Workload {name = "built-only", build = _go, runtime = forge.BuildOnly {}}
        # The author's value wins whole; the service name is still derived.
        fw.Workload {name = "author-attrs", build = _go, args = ["server"], env = {OTEL_RESOURCE_ATTRIBUTES = "team=payments"}, runtime = forge.OnHost {}}
        # A blank value is no value: derived, in the author's position.
        fw.Workload {name = "blank-attrs", build = _go, args = ["server"], env = {OTEL_RESOURCE_ATTRIBUTES = "", LOG_LEVEL = "debug"}, runtime = forge.OnHost {}}
    ]
})

_w = {w.name: w for w in output.workloads}
_env = lambda name: str -> {str:str} {
    {e.name: e.value for e in (_w[name].spec.env or []) if "value" in e}
}

_ATTRS = "deployment.environment.name=staging,service.namespace=acme,service.version=v1.2.3"

assert_host_attrs = _env("host-api").OTEL_RESOURCE_ATTRIBUTES == _ATTRS
assert_cluster_attrs = _env("cluster-worker").OTEL_RESOURCE_ATTRIBUTES == _ATTRS
assert_hosted_attrs = _env("hosted-api").OTEL_RESOURCE_ATTRIBUTES == _ATTRS
# Derived after the service name, and both after the author's own keys.
assert_derived_order = [e.name for e in _w["host-api"].spec.env] == ["OTEL_SERVICE_NAME", "OTEL_RESOURCE_ATTRIBUTES"]

# The cluster record (what kubectl applies) carries the same value.
_rec = [m for m in output.manifests if m.kind == "Workload" and m.metadata.name == "cluster-worker"][0]
assert_record_attrs = {e.name: e.value for e in _rec.spec.env if "value" in e}.OTEL_RESOURCE_ATTRIBUTES == _ATTRS

assert_compose_none = "OTEL_RESOURCE_ATTRIBUTES" not in _env("compose-db")
assert_tool_none = _env("a-tool") == {}
assert_build_only_none = _env("built-only") == {}

assert_author_value_whole = _env("author-attrs").OTEL_RESOURCE_ATTRIBUTES == "team=payments"
assert_author_name_still_derived = _env("author-attrs").OTEL_SERVICE_NAME == "author-attrs"
assert_blank_derived = _env("blank-attrs").OTEL_RESOURCE_ATTRIBUTES == _ATTRS
assert_blank_keeps_position = [e.name for e in _w["blank-attrs"].spec.env] == ["OTEL_RESOURCE_ATTRIBUTES", "LOG_LEVEL", "OTEL_SERVICE_NAME"]

# Forge reports only what it knows.
_wl = fw.Workload {name = "api", build = _go, args = ["server"], runtime = forge.OnHost {}}
_attrs = lambda o: any -> str {
    {e.name: e.value for e in o.workloads[0].spec.env}.OTEL_RESOURCE_ATTRIBUTES
}
_no_release = forge.render(forge.Bundle {project = "acme", env = "dev", workloads = [_wl]})
_no_env = forge.render(forge.Bundle {project = "acme", env = "", release = "v2", workloads = [_wl]})
_neither = forge.render(forge.Bundle {project = "acme", env = "", workloads = [_wl]})
assert_no_release_omits_version = _attrs(_no_release) == "deployment.environment.name=dev,service.namespace=acme"
assert_no_env_omits_environment = _attrs(_no_env) == "service.namespace=acme,service.version=v2"
assert_neither = _attrs(_neither) == "service.namespace=acme"

# Separators in a value cannot split an attribute.
_odd = forge.render(forge.Bundle {project = "a,b=c d", env = "dev", workloads = [_wl]})
assert_values_are_encoded = _attrs(_odd) == "deployment.environment.name=dev,service.namespace=a%2Cb%3Dc%20d"
