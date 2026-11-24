"""
Positive test — the CLUSTER lowering: a fw.Workload bound to OnCluster
becomes ONE forge.dev/v1alpha1 Workload record in output.manifests (map §2.3),
which Go expands with pkg/deploy.RenderWorkloads. KCL renders no Deployment.

Pins the record shape the extractor decodes (strictly) and groups by
(forge.dev/cluster, namespace):

  * apiVersion/kind, name, namespace from the target;
  * labels: managed labels, part-of = the project, forge.dev/cluster = the
    target's kubectl context, forge.dev/env when the env is known;
  * spec is WorkloadSpec JSON (camelCase), with every env channel in its
    wire shape, and `image` resolved to <registry>/<image>:<tag>.
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_us-central1_prod"
    namespace = "acme-prod"
    platform = "amd64"
}

_proxy = fw.Workload {
    name = "workspace-proxy"
    image = "ghcr.io/reliant-labs/control-plane"
    command = ["/app/control-plane"]
    args = ["workspace-proxy"]
    replicas = 3
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
    resources = fw.Resources {cpuRequestMillicores = 500, memoryRequestBytes = 536870912}
    env = {
        LOG_LEVEL = "info"
        DATABASE_URL = forge.SecretRef {name = "db", key = "url"}
        SENTRY_DSN = forge.SecretRef {name = "obs", key = "dsn", optional = True}
        MODE = forge.ConfigMapRef {name = "app-config", key = "mode"}
        POD_IP = forge.FieldRef {path = "status.podIP"}
        EMPTY = ""
    }
    runtime = forge.OnCluster {target = _prod}
}

output = forge.render(forge.Bundle {project = "acme", env = "prod", workloads = [_proxy]})
_records = [m for m in output.manifests if m.kind == "Workload"]
_r = _records[0]

assert_one_record = len(_records) == 1
assert_api_version = _r.apiVersion == "forge.dev/v1alpha1"
assert_name = _r.metadata.name == "workspace-proxy"
assert_namespace = _r.metadata.namespace == "acme-prod"
assert_cluster_label = _r.metadata.labels["forge.dev/cluster"] == "gke_acme_us-central1_prod"
assert_part_of = _r.metadata.labels["app.kubernetes.io/part-of"] == "acme"
assert_app_name = _r.metadata.labels["app.kubernetes.io/name"] == "workspace-proxy"
assert_env_stamp = _r.metadata.labels["forge.dev/env"] == "prod"

# KCL expands nothing: the Go renderer owns every Kubernetes object.
assert_no_kcl_k8s_objects = len([m for m in output.manifests if m.kind in ["Deployment", "Service", "ServiceAccount", "Role", "RoleBinding", "PodDisruptionBudget", "NetworkPolicy"]]) == 0

_s = _r.spec
assert_kind = _s.kind == "service"
assert_command_args = _s.command == ["/app/control-plane"] and _s.args == ["workspace-proxy"]
assert_replicas = _s.replicas == 3
assert_ports = _s.ports == [{name = "http", port = 8080, protocol = "tcp", expose = True}]
assert_resources = _s.resources.cpuRequestMillicores == 500 and _s.resources.memoryRequestBytes == 536870912
# Request-only resources: the limits stay UNSET in the record, and Go
# defaults each to its request (Resources.WithDefaults).
assert_limits_unset = "cpuLimitMillicores" not in _s.resources and "memoryLimitBytes" not in _s.resources
# The registry-less artifact name, resolved for the cluster's registry.
assert_image_resolved = _s.image.startswith("ghcr.io/reliant-labs/control-plane:")

_env = {e.name: e for e in _s.env}
assert_env_value = _env["LOG_LEVEL"] == {name = "LOG_LEVEL", value = "info"}
assert_env_secret = _env["DATABASE_URL"].secretRef == {name = "db", key = "url"}
# `optional` survives (config codegen emits optional secret refs).
assert_env_secret_optional = _env["SENTRY_DSN"].secretRef.optional == True
assert_env_config_map = _env["MODE"].configMapRef == {name = "app-config", key = "mode"}
assert_env_field_ref = _env["POD_IP"].fieldRef == {fieldPath = "status.podIP"}
# An empty string is a real value (a disable sentinel), not "unset".
assert_env_empty_value = _env["EMPTY"].value == ""
# Exactly one channel per entry.
assert_one_channel_each = all e in _s.env {
    len([k for k in ["value", "secretRef", "configMapRef", "fieldRef", "managedSecret", "databaseRef", "workloadURL"] if k in e]) == 1
}
# Env order is the author's order (k8s $(VAR) expansion depends on it); the
# keys forge derives (render.k _derived_env) follow everything declared.
assert_env_order = [e.name for e in _s.env] == ["LOG_LEVEL", "DATABASE_URL", "SENTRY_DSN", "MODE", "POD_IP", "EMPTY", "OTEL_SERVICE_NAME", "OTEL_RESOURCE_ATTRIBUTES"]
