"""
Positive test — under a GCPSecretManager bootstrap source the bootstrap
component (not an out-of-band prerequisite) delivers the seal key and DSN
password, so the render declares no prerequisite Secrets for the store.
"""

# kcl-args: env=prod

import forge

_t = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo"}

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = _t
    openbao = forge.OpenBao {replicas = 1}
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}
})

assert_no_prereqs = len(output.required_secrets) == 0
assert_record = len([m for m in output.manifests if m.kind == "OpenBao"]) == 1
assert_single_node_raft = [m for m in output.manifests if m.kind == "OpenBao"][0].spec.replicas == 1
