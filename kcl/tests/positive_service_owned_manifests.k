"""
Positive test — raw objects in `Bundle.manifests`, the ONE home for anything
that is not a typed entity (a CRD, a ClusterIssuer, a vendored chart's
output, a DaemonSet). PLACEMENT and SELECTION are declared per group,
never inferred from a primary-cluster heuristic:

  1. A NAMED group is its own `forge env deploy --target <name>`: every
     object is stamped `forge.dev/workload: <name>` — forge's routing key,
     which it always owns.
  2. The object's own `app.kubernetes.io/*` labels SURVIVE (a selector, a
     ServiceMonitor, an operator may key on them); forge's value is only a
     default for a key the object does not set. A chart-decoded object whose
     labels disagree with forge's renders (no KCL unification error).
  3. Every group stamps its resolved cluster, `forge.dev/cluster: <context>`
     (its own `cluster`, else the primary's), so the multi-cluster router
     sends it there and ONLY there. An unstamped object is replicated to
     every cluster, so a primary group must be stamped too.
  4. `namespace` fills a NAMESPACED object that names none; an object that
     names its own keeps it; a cluster-scoped kind never gets one.
  5. An UNNAMED group belongs to no --target: no owner stamp.

Replaces the per-workload `manifests` carrier (RenderedWorkload.manifests /
an image-less "infra service"): a raw object is an env fact, and a
workload never applies YAML (the hosted runtime refuses rendered YAML).
"""

import forge
import forge.workloads as fw
import yaml

_target = forge.ClusterTarget {
    cluster = "k3d-control-plane"
    namespace = "control-plane-e2e"
}

_bundle = forge.Bundle {
    project = "control-plane"
    cluster_target = _target
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5051/example"
        ports = [fw.Port {name = "http", port = 8080}]
        runtime = forge.OnCluster {target = _target}
    }]
    manifests = [
        # 1+4. A named group on the primary cluster: a CRD (cluster-scoped,
        # carries a user label) and a Gateway with NO metadata namespace.
        forge.Manifests {
            name = "cp-infra"
            objects = [
                {
                    apiVersion = "apiextensions.k8s.io/v1"
                    kind = "CustomResourceDefinition"
                    metadata = {name = "widgets.example.com", labels = {"team" = "platform"}}
                    spec = {group = "example.com"}
                }
                {
                    apiVersion = "gateway.networking.k8s.io/v1"
                    kind = "Gateway"
                    metadata = {name = "public-gateway"}
                    spec = {gatewayClassName = "traefik"}
                }
                # No metadata at all: the stamp creates metadata.labels.
                {apiVersion = "cert-manager.io/v1", kind = "ClusterIssuer", spec = {acme = {}}}
            ]
        }
        # 2+3. A named group on ANOTHER cluster, chart-shaped objects whose own
        # app.kubernetes.io labels disagree with forge's.
        forge.Manifests {
            name = "prepull"
            cluster = "k3d-cp-daemon"
            namespace = "prepull"
            objects = [
                {
                    apiVersion = "apps/v1"
                    kind = "DaemonSet"
                    metadata = {name = "image-prepull", labels = {"app.kubernetes.io/name" = "image-prepull"}}
                    spec = {
                        selector.matchLabels = {"app.kubernetes.io/name" = "image-prepull"}
                        template.metadata.labels = {"app.kubernetes.io/name" = "image-prepull"}
                    }
                }
                yaml.decode("apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: operator\n  namespace: operator-ns\n  labels:\n    app.kubernetes.io/name: operator-chart\n    app.kubernetes.io/managed-by: Helm\n    forge.dev/workload: someone-else\n    helm.sh/chart: operator-1.0.0\n")
            ]
        }
        # 5. An unnamed group: applies on a bare deploy, owned by no target.
        forge.Manifests {
            objects = [{apiVersion = "v1", kind = "ConfigMap", metadata = {name = "loose"}, data = {a = "b"}}]
        }
    ]
}

manifests = forge.render(_bundle).manifests

_one = lambda kind: str -> any { [m for m in manifests if m.kind == kind][0] }
_crd = _one("CustomResourceDefinition")
_gw = _one("Gateway")
_issuer = _one("ClusterIssuer")
_ds = _one("DaemonSet")
_sa = [m for m in manifests if m.kind == "ServiceAccount" and m.metadata.name == "operator"][0]
_loose = [m for m in manifests if m.kind == "ConfigMap" and m.metadata.name == "loose"][0]

# ── (1) the named group is its own --target ────────────────────────────
assert_crd_routed = _crd.metadata.labels["forge.dev/workload"] == "cp-infra"
assert_gw_routed = _gw.metadata.labels["forge.dev/workload"] == "cp-infra"
assert_issuer_routed = _issuer.metadata.labels["forge.dev/workload"] == "cp-infra"
# The object's own metadata survives the stamp.
assert_crd_user_label = _crd.metadata.labels["team"] == "platform"
assert_crd_name = _crd.metadata.name == "widgets.example.com"
assert_crd_spec = _crd.spec.group == "example.com"
# forge's app labels are DEFAULTS where the object set none.
assert_crd_name_defaulted = _crd.metadata.labels["app.kubernetes.io/name"] == "cp-infra"
assert_part_of_defaulted = _gw.metadata.labels["app.kubernetes.io/part-of"] == "control-plane"

# ── (2) the object's own labels win; forge's routing key always wins ───
assert_ds_routed_to_owner = _ds.metadata.labels["forge.dev/workload"] == "prepull"
assert_ds_name_label_kept = _ds.metadata.labels["app.kubernetes.io/name"] == "image-prepull"
assert_ds_selector_untouched = _ds.spec.selector.matchLabels == {"app.kubernetes.io/name" = "image-prepull"}
assert_decoded_routed_to_owner = _sa.metadata.labels["forge.dev/workload"] == "prepull"
assert_decoded_managed_by_kept = _sa.metadata.labels["app.kubernetes.io/managed-by"] == "Helm"
assert_decoded_name_kept = _sa.metadata.labels["app.kubernetes.io/name"] == "operator-chart"
assert_decoded_other_label_kept = _sa.metadata.labels["helm.sh/chart"] == "operator-1.0.0"

# ── (3) cluster placement ──────────────────────────────────────────────
assert_off_primary_stamped = _ds.metadata.labels["forge.dev/cluster"] == "k3d-cp-daemon" and \
    _sa.metadata.labels["forge.dev/cluster"] == "k3d-cp-daemon"
assert_primary_stamped = _crd.metadata.labels["forge.dev/cluster"] == "k3d-control-plane" and \
    _gw.metadata.labels["forge.dev/cluster"] == "k3d-control-plane" and \
    _loose.metadata.labels["forge.dev/cluster"] == "k3d-control-plane"

# ── (4) namespace placement ────────────────────────────────────────────
assert_ns_filled_from_group = _ds.metadata.namespace == "prepull"
assert_own_ns_kept = _sa.metadata.namespace == "operator-ns"
assert_ns_filled_from_primary = _gw.metadata.namespace == "control-plane-e2e"
assert_cluster_scoped_no_ns = "namespace" not in _crd.metadata and "namespace" not in _issuer.metadata

# ── (5) unnamed group: no owner ────────────────────────────────────────
assert_unnamed_unowned = "forge.dev/workload" not in (_loose.metadata.labels or {})
assert_unnamed_ns_filled = _loose.metadata.namespace == "control-plane-e2e"

# The workload record is unaffected by the raw groups beside it.
assert_workload_record = [m.metadata.name for m in manifests if m.kind == "Workload"] == ["api"]
