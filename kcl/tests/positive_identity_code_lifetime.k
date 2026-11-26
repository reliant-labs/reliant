"""
Positive test — ManagedIdentity.code_lifetime (forge#2265): a mail-less env
lengthens the password-set code its operators hand over by chat. The shape
carries it as a Go duration; an identity that declares none carries no key,
so its shape (and hash) is what it was.
"""

import forge
import forge.workloads as fw

_bundle = lambda lifetime: str -> any {
    forge.render(forge.Bundle {
        project = "acme"
        env = "prod"
        control_plane = forge.ControlPlane {}
        secret_provider = forge.HostedSecrets {}
        identity = forge.ManagedIdentity {
            roles = [forge.IdentityRole {key = "member"}]
            code_lifetime = lifetime
        }
        workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
    })
}

assert_days = _bundle("7d").identity.code_lifetime == "10080m"
assert_hours = _bundle("24h").identity.code_lifetime == "1440m"
assert_minutes = _bundle("30m").identity.code_lifetime == "30m"
assert_default_carries_no_key = "code_lifetime" not in _bundle("").identity
