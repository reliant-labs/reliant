"""
Positive tests for forge.Cluster's pod / Service CIDRs and its derived
kubeconfig path.

WHY cluster_cidr / service_cidr EXIST
=====================================

Two k3d clusters on ONE docker network — the `owner` topology — have
mutually reachable NODES but both default to the SAME pod CIDR
(10.42.0.0/16) and the same service IP. The same pod address therefore
exists in both clusters, a local route always wins, and a workload in one
cluster CANNOT dial a pod IP in the other.

A cloud multi-cluster topology does not have this problem because each
cluster sits on a DISJOINT block inside one VPC, which makes pod IPs
directly routable between them. Without these fields, a local k3d
topology cannot model that, and the workaround is to relocate the
workload into the other cluster — which changes the topology under test,
so dev stops modelling prod at exactly the point the two are supposed to
agree.

Exercises:

  * Both fields are OPTIONAL. A cluster that declares neither projects
    NO cluster_cidr / service_cidr KEY AT ALL — not a null — so every
    existing declaration renders byte-identically. This is the
    load-bearing assertion for the whole change.
  * A cluster that declares them projects both values through.
  * Disjoint blocks at UNEQUAL prefix lengths are accepted (the real
    cloud shape: 10.16.0.0/14 pods alongside 10.20.0.0/20 services).
  * The two-cluster case this exists for: an owner and a secondary on
    one network, each on its own block, with no overlap between them.
  * Cluster.kubeconfig is DERIVED (empty without forge's binding, never
    a home-relative guess), and ClusterClient.host_kubeconfig_path can
    be fed from it.
"""

import forge
import forge.workloads as fw

# ── The default: neither CIDR declared ──────────────────────────────

_plain = forge.Cluster {
    name = "plain"
    api_port = 6443
}

# ── Declared CIDRs, the dev two-cluster topology ────────────────────
#
# Disjoint pod blocks put the two clusters' pod IPs in non-overlapping
# ranges, so a pod IP in one is not also a valid local address in the
# other and cross-cluster dialing resolves to the intended pod.

_owner = forge.Cluster {
    name = "cp"
    api_port = 6443
    cluster_cidr = "10.52.0.0/16"
    service_cidr = "10.53.0.0/16"
}

_secondary = forge.Cluster {
    name = "cp-daemon"
    owner = _owner
    api_port = 6444
    cluster_cidr = "10.62.0.0/16"
    service_cidr = "10.63.0.0/16"
}

# Unequal prefix lengths, the shape a real cloud VPC uses (a /14 pod
# block beside a /20 service block). This is the case a naive
# same-prefix overlap test gets wrong, so it is pinned as ACCEPTED.
_wide = forge.Cluster {
    name = "wide"
    cluster_cidr = "10.16.0.0/14"
    service_cidr = "10.20.0.0/20"
}

# Only ONE of the two declared — each is independently optional.
_pods_only = forge.Cluster {
    name = "pods-only"
    cluster_cidr = "10.72.0.0/16"
}

_target = forge.ClusterTarget {
    cluster = "k3d-cp"
    namespace = "dev"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    clusters = [_plain, _owner, _secondary, _wide, _pods_only]
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5051/api:dev"
        runtime = forge.OnCluster {target = _target}
    }]
}

output = forge.render(_bundle)

# ── Invariants ──────────────────────────────────────────────────────

# THE BYTE-IDENTICAL GUARANTEE. A cluster that declares no CIDRs must
# project no CIDR key whatsoever. Not `null` — ABSENT. A projected null
# would add a key to every existing cluster's rendered JSON for a
# capability it never asked for, and the Go side keys off presence.
assert_plain_omits_cluster_cidr = "cluster_cidr" not in output.clusters[0]
assert_plain_omits_service_cidr = "service_cidr" not in output.clusters[0]

# Everything else about an undeclared cluster is unchanged.
assert_plain_name = output.clusters[0].name == "plain"
assert_plain_context = output.clusters[0].context == "k3d-plain"
assert_plain_api_port = output.clusters[0].api_port == 6443
assert_plain_servers = output.clusters[0].servers == 1

# Declared values project through verbatim — they are passed to k3s as
# --cluster-cidr / --service-cidr, so any normalization here would be a
# silent difference between what was declared and what k3s receives.
assert_owner_cluster_cidr = output.clusters[1].cluster_cidr == "10.52.0.0/16"
assert_owner_service_cidr = output.clusters[1].service_cidr == "10.53.0.0/16"
assert_secondary_cluster_cidr = output.clusters[2].cluster_cidr == "10.62.0.0/16"
assert_secondary_service_cidr = output.clusters[2].service_cidr == "10.63.0.0/16"

# The CIDRs are orthogonal to the ownership derivation — a secondary
# still derives its network and registry_inherit from the `owner` edge.
assert_secondary_network = output.clusters[2].network == "k3d-cp"
assert_secondary_inherit = output.clusters[2].registry_inherit == True
assert_owner_network_null = output.clusters[1].network == None

# THE POINT OF THE WHOLE CAPABILITY: the two clusters sharing one docker
# network are on DISJOINT pod blocks, which is what makes a pod IP in one
# unambiguous from the other's.
assert_blocks_disjoint = not forge.cidrs_overlap( \
    output.clusters[1].cluster_cidr, output.clusters[2].cluster_cidr)

# Unequal prefix lengths, disjoint — accepted.
assert_wide_cluster_cidr = output.clusters[3].cluster_cidr == "10.16.0.0/14"
assert_wide_service_cidr = output.clusters[3].service_cidr == "10.20.0.0/20"

# Independently optional: pods declared, services left to k3s.
assert_pods_only_has_cluster = output.clusters[4].cluster_cidr == "10.72.0.0/16"
assert_pods_only_omits_service = "service_cidr" not in output.clusters[4]

# ── The CIDR predicates themselves ──────────────────────────────────
#
# These back the schema's check block, so they are pinned directly:
# a check that silently stopped rejecting things would otherwise look
# identical to one that had nothing to reject.

assert_cidr_ok = forge.is_ipv4_cidr("10.42.0.0/16")
assert_cidr_octet_bound = forge.is_ipv4_cidr("300.0.0.0/16") == False
assert_cidr_prefix_bound = forge.is_ipv4_cidr("10.0.0.0/33") == False
assert_cidr_not_a_cidr = forge.is_ipv4_cidr("10.42.0.0") == False
assert_cidr_garbage = forge.is_ipv4_cidr("nope") == False

# k3s's own defaults do not overlap each other — the baseline every
# cluster that declares nothing runs on.
assert_k3s_defaults_disjoint = forge.cidrs_overlap("10.42.0.0/16", "10.43.0.0/16") == False
# Containment IS overlap, including across unequal prefixes — this is
# the case the check block rejects.
assert_contained_overlaps = forge.cidrs_overlap("10.42.0.0/16", "10.42.128.0/20")
assert_identical_overlaps = forge.cidrs_overlap("10.42.0.0/16", "10.42.0.0/16")
# Prod's real disjoint GKE layout, at unequal prefixes.
assert_prod_layout_disjoint = forge.cidrs_overlap("10.16.0.0/14", "10.20.0.0/20") == False

# ── The derived kubeconfig path ─────────────────────────────────────
#
# `Cluster.kubeconfig` is where forge ACTUALLY writes this cluster's
# kubeconfig: `k3d cluster create` + `k3d kubeconfig merge
# --kubeconfig-merge-default` put the entry in the DEFAULT kubeconfig,
# and forge resolves that path host-side ($KUBECONFIG's first entry,
# else ~/.kube/config) and binds it as `-D kubeconfig=`.
#
# forge's render seam (kclrender.Run) ALWAYS binds `-D kubeconfig=`
# (#277), so the field is exactly that binding — never a KCL-side guess.
# The predecessor of this field guessed `~/.kube/config`, which is wrong
# wherever $KUBECONFIG is set; a guess that is usually right fails on
# exactly one machine, reported as "context does not exist". With no
# binding (a bare `kcl run`) it would be "", never a guess.
assert_kubeconfig_is_the_binding = _owner.kubeconfig == (option("kubeconfig") or "")

# It is a per-cluster field, so a consumer names the CLUSTER it wants
# rather than re-deriving a path — that is the whole reason this is not
# a free-standing helper.
assert_kubeconfig_on_every_cluster = _secondary.kubeconfig == _owner.kubeconfig

# It is NOT projected into the rendered contract. The path is a host
# fact for KCL consumers; the Go reconcile that reads these entities
# already knows the machine's kubeconfig, and projecting it would put a
# machine-specific absolute path into what `forge env render` prints.
assert_kubeconfig_not_projected = "kubeconfig" not in output.clusters[1]

# The host projection it feeds. host_kubeconfig_path takes the cluster's
# own derived path instead of a hand-written literal; with no binding
# that is "", so this fixture states the wiring with an explicit path and
# asserts the derivation is what a real render would substitute.
_host_link = forge.ClusterClient {
    id = "cp-daemon"
    cluster = _secondary
    in_cluster = _owner
    mount_path = "/etc/daemon-cluster"
    host_kubeconfig_path = "/tmp/forge-test/.kube/config"
}
assert_host_context_from_cluster = _host_link.host_config_entry.context == "k3d-cp-daemon"
assert_host_path_used = _host_link.host_config_entry.kubeconfigPath == "/tmp/forge-test/.kube/config"
# The pod form is untouched by any of this.
assert_pod_path_unchanged = _host_link.config_entry.kubeconfigPath == "/etc/daemon-cluster/kubeconfig"
