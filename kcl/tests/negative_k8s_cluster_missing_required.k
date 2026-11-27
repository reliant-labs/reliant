"""
Negative test — a ClusterTarget requires cluster (the kubectl context),
namespace, and registry. An OnCluster runtime points at one, so an empty
coordinate fails the target's check block at load.
"""

# expect: ClusterTarget.cluster is required

import forge

_t = forge.ClusterTarget {
    cluster = ""
    namespace = "ns"
}

_rt = forge.OnCluster {target = _t}
