"""
Negative test — two identities whose names differ only in punctuation fold to
one hosted managed-secret namespace (RELIANT_IDENTITY_A_B_*), so a hosted
consumer would read one identity's issuer and keys as the other's. A
hosted-only env has no cluster Secret whose duplicate check would catch it.
"""

# expect: differ only in their punctuation
# expect: RELIANT_IDENTITY_A_B_*

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identities = [
        forge.ManagedIdentity {name = "a-b", consumers = [forge.IdentityConsumer {workload = "api"}]}
        forge.ManagedIdentity {name = "a--b", consumers = [forge.IdentityConsumer {workload = "web", fields = forge.IdentityFields {issuer = "other_issuer", audience = "other_audience", scopes = "other_scopes"}, omit = ["roles_claim"]}]}
    ]
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}
        fw.Workload {name = "web", image = "ghcr.io/acme/web", runtime = forge.OnHosted {}}
    ]
})
