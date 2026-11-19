"""
Positive test — delivery units: the declaration a Flux-applied bundle splits
each cluster into trees by.

  1. A workload's `delivery_unit` rides its Workload record as the
     `forge.dev/delivery-unit` label, and is also on the lowered entity so the
     Go side can attribute it. A workload without one carries no label (the
     default unit, `base`, is applied by the bundle, not stamped here).
  2. A forge.Manifests group stamps `delivery_unit` onto every object it
     carries. An object that already names its own unit keeps it.
  3. `Bundle.delivery_units` is projected, in declaration order. `after`
     unset renders null ("after base"); `after = []` renders an empty list
     ("waits for nothing"). The two must stay distinct.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "gke-prod"
    namespace = "control-plane-prod"
}

_bundle = forge.Bundle {
    project = "control-plane"
    cluster_target = _target
    workloads = [
        fw.Workload {
            name = "admin-server"
            image = "registry.example.com/admin-server"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
            delivery_unit = "control-plane"
        }
        fw.Workload {
            name = "plain"
            image = "registry.example.com/plain"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
        }
    ]
    manifests = [
        forge.Manifests {
            name = "base-crds"
            delivery_unit = "hub-base"
            objects = [
                {apiVersion = "apiextensions.k8s.io/v1", kind = "CustomResourceDefinition", metadata = {name = "widgets.example.com"}}
                # Already placed by its author: kept.
                {apiVersion = "v1", kind = "ConfigMap", metadata = {name = "own", labels = {"forge.dev/delivery-unit" = "infra"}}}
            ]
        }
        forge.Manifests {
            objects = [{apiVersion = "v1", kind = "ConfigMap", metadata = {name = "unplaced"}}]
        }
    ]
    delivery_units = [
        forge.DeliveryUnit {name = "reliant", after = []}
        forge.DeliveryUnit {name = "infra", after = ["reliant"]}
        forge.DeliveryUnit {name = "control-plane"}
    ]
}

_out = forge.render(_bundle)
_manifests = _out.manifests

_record = lambda name: str -> any { [m for m in _manifests if m.kind == "Workload" and m.metadata.name == name][0] }
_obj = lambda kind: str, name: str -> any { [m for m in _manifests if m.kind == kind and m.metadata.name == name][0] }
_entity = lambda name: str -> any { [w for w in _out.workloads if w.name == name][0] }

# ── (1) a workload's unit ───────────────────────────────────────────────
assert_record_unit = _record("admin-server").metadata.labels["forge.dev/delivery-unit"] == "control-plane"
assert_entity_unit = _entity("admin-server").delivery_unit == "control-plane"
assert_unplaced_record_has_no_label = "forge.dev/delivery-unit" not in _record("plain").metadata.labels
assert_unplaced_entity_has_no_unit = "delivery_unit" not in _entity("plain")

# ── (2) a Manifests group's unit ────────────────────────────────────────
assert_group_stamped = _obj("CustomResourceDefinition", "widgets.example.com").metadata.labels["forge.dev/delivery-unit"] == "hub-base"
assert_authors_unit_kept = _obj("ConfigMap", "own").metadata.labels["forge.dev/delivery-unit"] == "infra"
assert_unplaced_group_not_stamped = "forge.dev/delivery-unit" not in _obj("ConfigMap", "unplaced").metadata.labels

# ── (3) the declarations ────────────────────────────────────────────────
assert_unit_order = [u.name for u in _out.delivery_units] == ["reliant", "infra", "control-plane"]
assert_after_nothing = _out.delivery_units[0].after == []
assert_after_named = _out.delivery_units[1].after == ["reliant"]
assert_after_unset_is_null = _out.delivery_units[2].after == None or _out.delivery_units[2].after == Undefined
