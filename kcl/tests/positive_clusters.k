"""
Positive tests for forge.Cluster — declared k3d clusters forge ensures
exist before deploying.

An env declares a LIST of clusters (Bundle.clusters). `forge env up`
reconciles each (create-if-absent, no-op if present). Ownership is a
REFERENCE: a secondary cluster names its `owner` Cluster instance, and
forge DERIVES both the joined network (`k3d-<owner.name>`) and the
registry-inherit behavior from that one edge. There is no "primary"
field anywhere.

Exercises:

  * An owner cluster (no owner) projects with its name + computed
    .context (k3d-<name>), defaulted servers/agents, null network, and
    registry_inherit=false.
  * A secondary cluster (owner = <Cluster>) derives network =
    k3d-<owner.name> + registry_inherit=true + its own .context, and
    api_port projects through.
  * A config-driven cluster projects its config path.
  * `.context` is the computed kubectl context on every cluster.
  * No clusters declared => `clusters` is an EMPTY list (stable key).
  * Clusters NEVER appear in the k8s manifest stream
    (output.manifests).
"""

import forge
import forge.workloads as fw

_owner = forge.Cluster {
    name = "cp"
    api_port = 6443
}

_secondary = forge.Cluster {
    name = "workload"
    owner = _owner
    api_port = 6444
    agents = 2
}

_from_config = forge.Cluster {
    name = "configured"
    config = "deploy/k3d.workload.yaml"
}

_target = forge.ClusterTarget {
    cluster = "k3d-cp"
    namespace = "dev"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    clusters = [_owner, _secondary, _from_config]
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5051/api:dev"
        runtime = forge.OnCluster {target = _target}
    }]
}

_no_clusters_bundle = forge.Bundle {
    project = "fixture"
    workloads = []
}

output = forge.render(_bundle)
output_none = forge.render(_no_clusters_bundle)
manifests = forge.render(_bundle).manifests

# ── Invariants ─────────────────────────────────────────────────────

# Three clusters projected, in declaration order.
assert_count = len(output.clusters) == 3
assert_order = output.clusters[0].name == "cp" and \
    output.clusters[1].name == "workload" and \
    output.clusters[2].name == "configured"

# Owner cluster: defaulted servers=1, agents=0; null network, no inherit.
assert_owner_servers = output.clusters[0].servers == 1
assert_owner_agents = output.clusters[0].agents == 0
assert_owner_context = output.clusters[0].context == "k3d-cp"
assert_owner_image = output.clusters[0].image == "rancher/k3s:v1.36.3-k3s1"
assert_owner_network_null = output.clusters[0].network == None
assert_owner_no_inherit = output.clusters[0].registry_inherit == False
assert_owner_api_port = output.clusters[0].api_port == 6443

# Secondary cluster: derived ownership via the `owner` reference — the
# network is k3d-<owner.name> and registry_inherit flips true; the
# computed .context tracks the secondary's own name.
assert_secondary_context = output.clusters[1].context == "k3d-workload"
assert_secondary_image = output.clusters[1].image == "rancher/k3s:v1.36.3-k3s1"
assert_secondary_network = output.clusters[1].network == "k3d-cp"
assert_secondary_inherit = output.clusters[1].registry_inherit == True
assert_secondary_agents = output.clusters[1].agents == 2
assert_secondary_api_port = output.clusters[1].api_port == 6444

# The owner reference is usable directly: <owner>.context is the same
# string the secondary derives its network from.
assert_owner_ref_context = _owner.context == "k3d-cp"
assert_secondary_owner_edge = _secondary.network == "k3d-" + _owner.name

# Config-driven cluster: config path projects through.
assert_config = output.clusters[2].config == "deploy/k3d.workload.yaml"

# No clusters declared => stable empty list (not null garbage).
assert_none_empty = output_none.clusters == []

# Clusters are NEVER manifests — they're a host-side k3d reconcile, not
# a k8s object. No rendered manifest carries a Cluster kind.
assert_no_cluster_manifest = len([m for m in manifests if m.kind == "Cluster"]) == 0
