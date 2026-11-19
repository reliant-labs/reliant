"""
Positive test — a declaration that uses no identity v2 field renders none of
them: no new shape key, no login_policy projection, no LOGIN_POLICY key in the
delivered Secret, and no managed-secret name for it. So every env declared
before v2 renders, and hashes, exactly as it did (the render-contract goldens
pin the bytes).
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

_v2keys = ["owners", "owner_role", "signup", "methods", "passkey_rp_id", "social", "email", "dev_mail"]

_cluster = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "owner"}]
        # Explicit defaults are defaults.
        signup = "invite"
        methods = ["password"]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
assert_cluster_no_v2_keys = all k in _v2keys { k not in _cluster.identity }
assert_cluster_no_login_policy_key = all s in _cluster.control_plane_secrets { "LOGIN_POLICY" not in s.keys }
assert_cluster_no_login_policy_env = "IDP_LOGIN_POLICY" not in {e.name: e for e in _cluster.workloads[0].spec.env}

_hosted = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
})
assert_hosted_no_v2_keys = all k in _v2keys { k not in _hosted.identity }
assert_hosted_no_login_policy_name = all n in _hosted.identity.managed_secrets { not n.endswith("_LOGIN_POLICY") }

# Owners or mail addressing alone do not take over the login policy.
_owners = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "owner"}]
        owners = ["boss@acme.test"]
        email = forge.IdentityEmail {}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
})
assert_owners_recorded = _owners.identity.owners == ["boss@acme.test"] and _owners.identity.email.delivery == "platform"
assert_owners_alone_leave_the_policy = "methods" not in _owners.identity and "signup" not in _owners.identity
assert_owners_alone_no_login_policy = "IDP_LOGIN_POLICY" not in {e.name: e for e in _owners.workloads[0].spec.env}
