"""
Positive test — identity v2 on a cluster env with SOCIAL SIGN-IN ONLY
(`methods = []`). The delivered Secret gains the LOGIN_POLICY key, the API reads
it through an OPTIONAL secretKeyRef (so a control plane that does not write it
yet never blocks the pod), and the shape tells "no first-factor methods" apart
from the default by recording `methods` whenever the login policy is declared.
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
        methods = []
        social = [forge.IdentityGitHub {client_id = "Iv1.abc", client_secret = forge.ManagedSecret {name = "GITHUB_CLIENT_SECRET"}, scopes = ["user:email"]}]
    }
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "worker", kind = "worker", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})

_by = {w.name: w for w in output.workloads}
_env = {e.name: e for e in _by["api"].spec.env}
_id = output.identity
_secrets = {s.name: s for s in output.control_plane_secrets}

assert_methods_recorded_empty = _id.methods == []
assert_social_only = len(_id.social) == 1 and _id.social[0].type == "github" and _id.social[0].scopes == ["user:email"]
assert_no_signup_block_for_invite = "signup" not in _id
assert_no_owners = "owners" not in _id and "owner_role" not in _id
assert_no_email_block_when_undeclared = "email" not in _id
assert_login_policy_key_delivered = "LOGIN_POLICY" in _secrets["forge-identity-identity"].keys
assert_login_policy_secret_ref_is_optional = _env["IDP_LOGIN_POLICY"].secretRef.name == "forge-identity-identity" and _env["IDP_LOGIN_POLICY"].secretRef.key == "LOGIN_POLICY" and _env["IDP_LOGIN_POLICY"].secretRef.optional == True
assert_other_refs_stay_required = "optional" not in _env["JWT_ISSUER"].secretRef
assert_login_policy_is_not_a_file = "IDP_LOGIN_POLICY_FILE" not in _env
assert_worker_gets_nothing = "IDP_LOGIN_POLICY" not in {e.name: e for e in _by["worker"].spec.env}

# A consumer may decline it, and may map it to another field.
_omit = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {
        methods = ["password", "passkey"]
        consumers = [forge.IdentityConsumer {workload = "api", omit = ["login_policy"]}]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
assert_omitted = "IDP_LOGIN_POLICY" not in {e.name: e for e in _omit.workloads[0].spec.env}
assert_omitted_still_delivered = "LOGIN_POLICY" in {s.name: s for s in _omit.control_plane_secrets}["forge-identity-identity"].keys

_mapped = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {
        signup = "open"
        consumers = [forge.IdentityConsumer {workload = "api", fields = forge.IdentityFields {login_policy = "sign_in_policy"}}]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
_mapped_env = {e.name: e for e in _mapped.workloads[0].spec.env}
assert_mapped = _mapped_env["SIGN_IN_POLICY"].secretRef.key == "LOGIN_POLICY" and "IDP_LOGIN_POLICY" not in _mapped_env
assert_mapped_in_the_shape = _mapped.identity.consumers[0].fields == {"login_policy": "sign_in_policy"}
assert_open_signup_records_default_methods = _mapped.identity.methods == ["password"] and _mapped.identity.signup.mode == "open"
