"""
Negative test — a KubeconfigPolicyRule.resource_names entry must not be empty.

An empty `resourceNames` entry matches no object, so a rule carrying one
grants nothing for that name while looking scoped. Failing at the
declaration beats a credential that authenticates and then 403s.
"""

# expect: resource_names must not contain an empty name

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
        rules = [forge.KubeconfigPolicyRule {
            api_groups = ["rbac.authorization.k8s.io"]
            resources = ["clusterroles"]
            verbs = ["bind"]
            resource_names = ["ok", ""]
        }]
    }
}
