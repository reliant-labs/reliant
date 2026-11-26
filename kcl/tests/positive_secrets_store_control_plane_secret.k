"""
Positive test — forge.ControlPlaneSecret with delivery = "store": the control
plane writes the value into the env's hosted store and ESO delivers it, as a
FLOATING ExternalSecret.

  * the ExternalSecret has no `version` (a platform-issued token is renewed in
    place, so it must read the latest), refreshes on the declared interval, and
    syncs a Secret named like the ControlPlaneSecret, so the workloads' optional
    volume finds it;
  * one store name (RELIANT_OTEL_HEADERS) carries the value; the Secret's
    `token` key is derived from it, so the two keys cannot disagree;
  * the record in `control_plane_secrets` says `delivery: store` and carries the
    source, and the Secret is still NOT a required_secrets entry (a deploy never
    waits on it);
  * a push Secret in the same Bundle renders exactly as before.
"""

# kcl-args: env=prod

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    connected_cluster = "acme-prod"
    platform = "amd64"
}

_out = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _prod}]
    control_plane_secrets = [
        forge.ControlPlaneSecret {
            name = "reliant-otel-auth"
            cluster = _prod
            delivery = "store"
            store = forge.HostedStore {org = "55949942-3ee9-4cfc-bb7c-817bdba75186", env_id = "13b10699-40f1-4b19-b273-4b36a9977cc9"}
        }
        forge.ControlPlaneSecret {
            name = "pushed-otel-auth"
            cluster = _prod
            namespace = "acme-batch"
        }
    ]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})

_es = [x for x in _out.manifests if x.kind == "ExternalSecret"]
_ss = [x for x in _out.manifests if x.kind == "SecretStore"]
_e = _es[0]
_vault = _ss[0].spec.provider.vault
_cps = {s.name: s for s in _out.control_plane_secrets}

assert_one_external_secret = len(_es) == 1 and _e.metadata.name == "reliant-otel-auth" and _e.metadata.namespace == "acme-prod"
assert_floats = all d in _e.spec.data { "version" not in d.remoteRef }
assert_one_store_name = len(_e.spec.data) == 1 and _e.spec.data[0].secretKey == "OTEL_EXPORTER_OTLP_HEADERS" and _e.spec.data[0].remoteRef.key == "tenants/55949942-3ee9-4cfc-bb7c-817bdba75186/13b10699-40f1-4b19-b273-4b36a9977cc9/RELIANT_OTEL_HEADERS" and _e.spec.data[0].remoteRef.property == "value"
assert_token_key_is_derived = _e.spec.target.template.data == {token = "{{ .OTEL_EXPORTER_OTLP_HEADERS | trimPrefix \"authorization=Bearer \" }}"}
assert_refreshes = _e.spec.refreshInterval == "15m"
assert_outage_safe = _e.spec.target.creationPolicy == "Orphan" and _e.spec.target.deletionPolicy == "Retain" and _e.spec.target.name == "reliant-otel-auth"
assert_hosted_login = _vault.auth.jwt.path == "jwt-acme-prod" and _vault.server == "https://secrets.reliantapi.com"
assert_record_says_store = _cps["reliant-otel-auth"].delivery == "store" and _cps["reliant-otel-auth"].source.data == {OTEL_EXPORTER_OTLP_HEADERS = "RELIANT_OTEL_HEADERS"}
assert_record_keys_unchanged = _cps["reliant-otel-auth"].keys == ["OTEL_EXPORTER_OTLP_HEADERS", "token"]
assert_push_record_unchanged = "delivery" not in _cps["pushed-otel-auth"] and "source" not in _cps["pushed-otel-auth"]
assert_not_a_deploy_prerequisite = len([s for s in _out.required_secrets if s.name == "reliant-otel-auth"]) == 0
