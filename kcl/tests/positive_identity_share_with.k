"""
Positive test — share_with: a preview env reuses its project's staging users.
It declares only applications and consumers; everything else is the owner's.
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "preview-142"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {share_with = "staging"}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
})

_id = output.identity
assert_records_the_owner = _id.share_with == "staging"
assert_own_application = _id.applications[0].name == "preview-142:api"
assert_own_consumer = _id.consumers[0].workload == "api"
assert_declares_nothing_of_the_owners = len(_id.roles) == 0 and len(_id.machine_users) == 0 and not _id.smtp and len(_id.seed_users) == 0
assert_reads_the_same_outputs = {e.name: e for e in {w.name: w for w in output.workloads}["api"].spec.env}["JWT_ISSUER"].managedSecret.name == "RELIANT_IDENTITY_IDENTITY_ISSUER"
