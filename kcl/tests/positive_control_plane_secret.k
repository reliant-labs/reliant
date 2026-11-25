"""
Positive test — forge.ControlPlaneSecret: a Secret the declared control plane
supplies and renews.

It renders into the `control_plane_secrets` bucket as a RECORD (name,
namespace, cluster, connected cluster, kind, the keys the control plane writes,
never a value), and it is NOT a required_secrets entry, so a deploy preflight
never waits on it.

A cluster workload in the same cluster and namespace reads it with no further
declaration: an OPTIONAL Secret volume (the pod starts before the first
delivery) plus OTEL_EXPORTER_OTLP_HEADERS_FILE. A workload on another
namespace, a workload that set its own headers, and a tool get neither.
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    connected_cluster = "acme-prod"
    platform = "amd64"
}

_other_ns = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-batch"
    connected_cluster = "acme-prod"
    platform = "amd64"
}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    control_plane_secrets = [
        forge.ControlPlaneSecret {
            name = "reliant-otel-auth"
            cluster = _prod
            reason = "OTLP ingest token"
        }
    ]
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {
            name = "owns-auth"
            image = "ghcr.io/acme/owns-auth"
            env = {OTEL_EXPORTER_OTLP_HEADERS = "authorization=Bearer x"}
            runtime = forge.OnCluster {target = _prod}
        }
        fw.Workload {name = "batch", image = "ghcr.io/acme/batch", runtime = forge.OnCluster {target = _other_ns}}
    ]
})

_cps = output.control_plane_secrets
assert_one_record = len(_cps) == 1
assert_record_name = _cps[0].name == "reliant-otel-auth"
assert_record_namespace_defaults_to_the_clusters = _cps[0].namespace == "acme-prod"
assert_record_cluster = _cps[0].cluster == "gke_acme_prod"
assert_record_connected = _cps[0].connected_cluster == "acme-prod"
assert_record_kind = _cps[0].kind == "telemetry_token"
assert_record_keys_not_values = _cps[0].keys == ["OTEL_EXPORTER_OTLP_HEADERS", "token"]
assert_record_reason = _cps[0].reason == "OTLP ingest token"

# It is a control-plane Secret, not an out-of-band promise: the preflight's
# required_secrets bucket stays empty, which is what lets a first deploy through.
assert_not_a_required_secret = len(output.required_secrets or []) == 0

_by_name = {w.name: w for w in output.workloads}

_api_vols = _by_name["api"].spec.volumes
assert_api_mounts_one_volume = len(_api_vols) == 1
assert_volume_is_the_fixed_name = _api_vols[0].name == "forge-otlp-auth"
assert_volume_is_a_directory = _api_vols[0].mountPath == "/var/run/secrets/forge/otlp"
assert_volume_is_read_only = _api_vols[0].readOnly == True
assert_volume_sources_the_secret = _api_vols[0].source.secret.name == "reliant-otel-auth"
assert_volume_is_optional = _api_vols[0].source.secret.optional == True
assert_volume_projects_the_headers_key = _api_vols[0].source.secret.items == [{key = "OTEL_EXPORTER_OTLP_HEADERS", path = "headers"}]

_api_env = {e.name: e for e in _by_name["api"].spec.env}
assert_api_reads_the_headers_file = _api_env["OTEL_EXPORTER_OTLP_HEADERS_FILE"].value == "/var/run/secrets/forge/otlp/headers"

# An author who set the headers owns the auth: neither the env var nor the volume.
assert_authored_headers_get_no_volume = "volumes" not in _by_name["owns-auth"].spec
assert_authored_headers_get_no_file_var = "OTEL_EXPORTER_OTLP_HEADERS_FILE" not in {e.name: e for e in _by_name["owns-auth"].spec.env}

# A workload in another namespace is not given a Secret that is not there.
assert_other_namespace_gets_no_volume = "volumes" not in _by_name["batch"].spec
