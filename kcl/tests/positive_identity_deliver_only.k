"""
Positive test — a deliver_only consumer: the control plane still delivers the
identity Secrets for the workload's cluster, but nothing is projected into the
workload (no env, no volumes), and a hand-set value is not refused.
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
        applications = [forge.IdentityApplication {name = "console", workload = "admin-api", kind = "native_broker"}]
        consumers = [forge.IdentityConsumer {workload = "admin-api", deliver_only = True}]
    }
    workloads = [
        fw.Workload {
            name = "admin-api"
            image = "ghcr.io/acme/admin"
            env = {JWT_ISSUER = "http://localhost:8098"}
            runtime = forge.OnCluster {target = _prod}
        }
    ]
})

_by = {w.name: w for w in output.workloads}
_env = {e.name: e for e in _by["admin-api"].spec.env}
_names = [s.name for s in output.control_plane_secrets]

assert_hand_set_issuer_is_kept = _env["JWT_ISSUER"].value == "http://localhost:8098"
assert_no_identity_env = "JWT_AUDIENCE" not in _env and "IDP_BROKER_TOKEN_FILE" not in _env and "OIDC_CLIENT_ID" not in _env
assert_secrets_are_still_delivered = "forge-identity-identity" in _names and "forge-identity-identity-jwks" in _names
assert_shape_records_deliver_only = output.identity.consumers[0].deliver_only == True
