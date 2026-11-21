"""
Positive test — the 20%: a consumer remaps outputs onto non-standard config
fields (an app that validates two issuers), and a workload reads an output
through an explicit forge.IdentityRef.
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {
        project_name = "acme legacy"
        applications = [forge.IdentityApplication {name = "console", workload = "admin-api", kind = "native_broker"}]
        consumers = [forge.IdentityConsumer {
            workload = "admin-api"
            fields = forge.IdentityFields {issuer = "zitadel_issuer", audience = "zitadel_audience", client_id = "zitadel_broker_client_id"}
        }]
    }
    workloads = [
        fw.Workload {name = "admin-api", image = "ghcr.io/acme/admin", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {
            name = "reporter"
            image = "ghcr.io/acme/reporter"
            env = {ISSUER_FOR_REPORTS = forge.IdentityRef {output = "issuer"}}
            runtime = forge.OnCluster {target = _prod}
        }
    ]
})

_by = {w.name: w for w in output.workloads}
_admin = {e.name: e for e in _by["admin-api"].spec.env}
_rep = {e.name: e for e in _by["reporter"].spec.env}

assert_remapped_issuer = _admin["ZITADEL_ISSUER"].secretRef.key == "ISSUER" and "JWT_ISSUER" not in _admin
assert_remapped_audience = _admin["ZITADEL_AUDIENCE"].secretRef.key == "AUDIENCE"
assert_remapped_client_id = _admin["ZITADEL_BROKER_CLIENT_ID"].secretRef.key == "CONSOLE_CLIENT_ID"
assert_unmapped_outputs_keep_the_standard_field = _admin["OIDC_REDIRECT_URI"].secretRef.key == "CONSOLE_REDIRECT_URI"
assert_explicit_ref_lowers = _rep["ISSUER_FOR_REPORTS"].secretRef.name == "forge-identity-identity" and _rep["ISSUER_FOR_REPORTS"].secretRef.key == "ISSUER"
assert_legacy_project_name = output.identity.project_name == "acme legacy"
assert_fields_recorded_in_the_shape = output.identity.consumers[0].fields == {issuer = "zitadel_issuer", audience = "zitadel_audience", client_id = "zitadel_broker_client_id"}
assert_reporter_is_not_a_consumer_of_an_application = [c.workload for c in output.identity.consumers] == ["admin-api"]
