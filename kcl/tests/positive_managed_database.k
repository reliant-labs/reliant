"""
Positive test — the ManagedDatabase tier on the KCL surface.

A database is a Bundle entry (`databases`), not a Service deploy variant: it
has no image, no build and no ports. Its spec is the generated
tiers.ManagedDatabase (from forge's Go type
pkg/deploy/v1alpha1.ManagedDatabaseSpec). output.manifests carries one
forge.dev/v1alpha1 declaration record per database, which forge's manifest
extraction expands through the Go renderer into a CloudNativePG Cluster.

Pins:
  1. Defaults: engine postgres, 1 instance, 10 GiB, and deletionPolicy
     RETAIN. Retain is the single most consequential default on the type,
     because deleting a declaration is not a statement about the data.
  2. The record is namespaced, labelled and env-stamped like every object.
  3. The entity contract carries each database for `forge env render`.
"""

import forge
import forge.tiers

_bundle = forge.Bundle {
    project = "shop"
    env = "prod"
    cluster_target = forge.ClusterTarget { cluster = "c", namespace = "shop-prod" }
    databases = [
        forge.ManagedDatabase { name = "orders", namespace = "shop-prod" }
        forge.ManagedDatabase {
            name = "analytics"
            namespace = "shop-prod"
            spec = tiers.ManagedDatabase { instances = 2, storageGiB = 50, deletionPolicy = "delete" }
        }
    ]
}

_manifests = forge.render(_bundle).manifests
_records = {m.metadata.name: m for m in _manifests if m.kind == "ManagedDatabase"}
_entities = forge.render(_bundle).databases

assert_two_records = len(_records) == 2
assert_group = _records["orders"].apiVersion == "forge.dev/v1alpha1"
assert_namespace = _records["orders"].metadata.namespace == "shop-prod"
assert_env_stamp = _records["orders"].metadata.labels["forge.dev/env"] == "prod"
assert_part_of = _records["orders"].metadata.labels["app.kubernetes.io/part-of"] == "shop"

assert_default_retain = _records["orders"].spec.deletionPolicy == "retain"
assert_default_engine = _records["orders"].spec.engine == "postgres"
assert_default_instances = _records["orders"].spec.instances == 1
assert_default_storage = _records["orders"].spec.storageGiB == 10

assert_declared_delete = _records["analytics"].spec.deletionPolicy == "delete"
assert_declared_instances = _records["analytics"].spec.instances == 2

assert_entities = len(_entities) == 2 and _entities[0].name == "orders"
# No KCL builder expands a database. The Cluster is Go's.
assert_no_kcl_cluster = len([m for m in _manifests if m.kind == "Cluster"]) == 0
