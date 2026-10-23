"""
Positive test — forge.ManagedIdentity on a HOSTED env. A hosted consumer reads
reserved managed secrets (RELIANT_IDENTITY_<NAME>_<KEY>), the control plane is
the only writer, and no Secret is delivered to a cluster for it. A machine user
is the exception: its key is delivered to a Secret on a connected cluster.
"""

import forge
import forge.workloads as fw

_ops = forge.ClusterTarget {cluster = "gke_acme_ops", namespace = "acme-ops", connected_cluster = "acme-ops", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "admin", members_from = "declared", members = ["boss@acme.test"]}]
        machine_users = [forge.IdentityMachineUser {name = "ops", roles = ["admin"], key_secret = "acme-ops-key", cluster = _ops}]
        smtp = forge.IdentitySMTP {host = "smtp.acme.test", sender = "no-reply@acme.test", password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}
    }
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}
    ]
})

_env = {e.name: e for e in {w.name: w for w in output.workloads}["api"].spec.env}
_id = output.identity

assert_issuer_is_a_managed_secret = _env["JWT_ISSUER"].managedSecret.name == "RELIANT_IDENTITY_IDENTITY_ISSUER"
assert_audience = _env["JWT_AUDIENCE"].managedSecret.name == "RELIANT_IDENTITY_IDENTITY_AUDIENCE"
assert_client_id_is_per_application = _env["OIDC_CLIENT_ID"].managedSecret.name == "RELIANT_IDENTITY_IDENTITY_PROD_API_CLIENT_ID"
assert_broker_token = _env["IDP_BROKER_TOKEN"].managedSecret.name == "RELIANT_IDENTITY_IDENTITY_PROD_API_BROKER_TOKEN"
assert_jwks_is_optional = _env["JWT_JWKS_SNAPSHOT"].managedSecret.name == "RELIANT_IDENTITY_IDENTITY_JWKS" and _env["JWT_JWKS_SNAPSHOT"].managedSecret.optional == True
assert_no_secret_refs = all k, e in _env { not e.secretRef }
assert_no_volumes_on_hosted = "volumes" not in {w.name: w for w in output.workloads}["api"].spec
assert_reserved_names_in_the_shape = "RELIANT_IDENTITY_IDENTITY_ISSUER" in _id.managed_secrets and "RELIANT_IDENTITY_IDENTITY_JWKS" in _id.managed_secrets

# The only delivered Secret is the machine user's key.
assert_only_the_machine_key_is_delivered = len(output.control_plane_secrets) == 1 and output.control_plane_secrets[0].name == "acme-ops-key" and output.control_plane_secrets[0].kind == "identity_machine_key"
assert_machine_key_destination = output.control_plane_secrets[0].namespace == "acme-ops" and output.control_plane_secrets[0].connected_cluster == "acme-ops"
assert_machine_user_in_shape = _id.machine_users[0].key_secret.name == "acme-ops-key" and _id.machine_users[0].key_secret.connected_cluster == "acme-ops"
assert_declared_members = _id.roles[0].members == ["boss@acme.test"] and _id.roles[0].members_from == "declared"
assert_smtp_is_a_name = _id.smtp.password_secret == "SMTP_PASSWORD" and _id.smtp.port == 587
assert_private_by_default = not _id.public and not _id.domain
