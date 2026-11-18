"""
Negative test — KubeconfigSecret.reachability must be 'in-network' or
'endpoint'. Any other value should fail the check block.
"""

# expect: KubeconfigSecret.reachability must be 'in-network'

import forge

_k = forge.KubeconfigSecret {
    name = "x"
    in_cluster = "k3d-cp"
    target_cluster = "workload"
    context_name = "workload"
    reachability = "public"  # not in-network / endpoint
}
