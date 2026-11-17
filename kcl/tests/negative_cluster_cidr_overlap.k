"""
Negative test — a Cluster's cluster_cidr must not overlap its own
service_cidr.

k3s ACCEPTS this configuration and the cluster comes up. Service IPs are
then allocated from a range that also holds pod IPs, and the symptom is
intermittently unreachable Services with nothing anywhere naming a CIDR.
The check block rejects it at load instead.

Here the /20 service block sits INSIDE the /16 pod block, which is the
form a hand-picked pair most often takes — the two look different at a
glance and are not.
"""

# expect: Cluster.cluster_cidr overlaps its own service_cidr

import forge

_bad = forge.Cluster {
    name = "overlapping"
    cluster_cidr = "10.52.0.0/16"
    service_cidr = "10.52.128.0/20"  # contained in cluster_cidr
}
