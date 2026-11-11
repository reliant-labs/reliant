"""
Negative test — reachability = "in-cluster" without a service_account.

"in-cluster" means the reader is a POD. A pod cannot present the
operator's credential: for a managed cluster that credential is an exec
plugin the pod has no binary for, and for k3d it is a client certificate
the pod has no access to. Setting the address without minting a usable
credential produces a kubeconfig that renders fine and authenticates
never — exactly the failure forge defect F1b was about. Refuse it at the
declaration.
"""

# expect: requires service_account

import forge

_k = forge.KubeconfigSecret {
    name = "hub"
    in_cluster = "gke_p_us-central1_prod"
    target_cluster = "prod"
    target_context = "gke_p_us-central1_prod"
    context_name = "hub"
    reachability = "in-cluster"
}
