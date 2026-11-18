"""
Negative test — Cluster.cluster_cidr must be a well-formed IPv4 CIDR.

The value is passed verbatim to k3s as --cluster-cidr. k3s rejects a
malformed one only when the server process starts — inside a container,
in a log nobody is tailing — so `k3d cluster create` appears to hang and
then fails with nothing pointing at the typo. The check block catches the
shape at load.

The octet bound is what is exercised here rather than obvious garbage:
`10.300.0.0/16` matches a dotted-quad-shaped regex and is still not an
address, so it is the case a regex-only check would let through.
"""

# expect: Cluster.cluster_cidr must be an IPv4 CIDR

import forge

_bad = forge.Cluster {
    name = "malformed"
    cluster_cidr = "10.300.0.0/16"  # 300 is not an octet
}
