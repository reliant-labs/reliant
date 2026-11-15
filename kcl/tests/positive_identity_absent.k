"""
Positive test — an env that declares no identity renders no identity, no
identity Secret and no identity env: BYO issuers are untouched.
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _prod
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api"
        env = {JWT_ISSUER = "https://acme.eu.auth0.com/"}
        runtime = forge.OnCluster {target = _prod}
    }]
})

assert_no_identity_key = "identity" not in output
assert_no_delivered_secrets = len(output.control_plane_secrets) == 0
assert_byo_issuer_is_untouched = {e.name: e for e in output.workloads[0].spec.env}["JWT_ISSUER"].value == "https://acme.eu.auth0.com/"
