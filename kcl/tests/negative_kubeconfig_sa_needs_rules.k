"""
Negative test — a minted ServiceAccount must declare rules.

A credential granted nothing can do nothing, so a rules-less mint is
always a declaration mistake and never an intent. Failing here, where the
declaration is, beats a pod that authenticates successfully and then 403s
on every call it makes.
"""

# expect: rules is required

import forge

_k = forge.KubeconfigSecret {
    name = "hub"
    in_cluster = "k3d-cp"
    target_cluster = "prod"
    target_context = "gke_p_us-central1_prod"
    context_name = "hub"
    reachability = "endpoint"
    service_account = forge.KubeconfigServiceAccount {
        name = "applier"
        namespace = "prod"
        rules = []
    }
}
