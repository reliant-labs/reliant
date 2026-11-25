"""
Positive test — a workload's `env` is a MAP of name → value | reference, and
every channel lowers to its WorkloadSpec wire shape with EXACTLY ONE source.

Two defects the map shape makes structurally impossible, and this pins:

  * an entry with a literal AND a reference (the old EnvVar rendered both
    `value:` and `valueFrom:`, which the API server rejects server-side
    after client dry-run passed). A map value is ONE thing.
  * a duplicate name from `base + overrides` list concatenation (kubectl
    rejects it). A map cannot hold one; `|` is last-wins.

Plus the fieldRef channel that the old component expander silently dropped
(rendering `{name: POD_IP}` — an EMPTY variable).
"""

import forge
import forge.workloads as fw

_base = fw.Workload {
    name = "api"
    image = "localhost:5050/demo"
    ports = [fw.Port {name = "http", port = 8080}]
    env = {
        LOG_LEVEL = "info"
        DATABASE_URL = forge.SecretRef {name = "db-credentials", key = "database-url"}
        FEATURE_MAP = forge.ConfigMapRef {name = "features", key = "map"}
        POD_IP = forge.FieldRef {path = "status.podIP"}
        STRIPE_KEY = forge.ManagedSecret {name = "STRIPE_KEY"}
        SENTRY_DSN = forge.ManagedSecret {name = "SENTRY_DSN", optional = True}
        ORDERS_DB = forge.DatabaseRef {name = "orders"}
        RUNTIME_CLASS_NAME = ""
    }
}

# A per-env refinement: `|` on the env map overrides by name, last wins,
# and a REFERENCE can replace a LITERAL (and vice versa) with no residue.
_prod = _base | {env = _base.env | {
    LOG_LEVEL = "warn"
    RUNTIME_CLASS_NAME = forge.ConfigMapRef {name = "runtime", key = "class"}
    NEW_FLAG = "on"
}}

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-prod"}
output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_prod | {runtime = forge.OnCluster {target = _k3d}}]
})
_spec_env = output.workloads[0].spec.env
_env = {e.name: e for e in _spec_env}

assert_value_channel = _env["LOG_LEVEL"] == {name = "LOG_LEVEL", value = "warn"}
assert_secret_channel = _env["DATABASE_URL"].secretRef == {name = "db-credentials", key = "database-url"}
assert_config_map_channel = _env["FEATURE_MAP"].configMapRef == {name = "features", key = "map"}
assert_field_ref_channel = _env["POD_IP"].fieldRef == {fieldPath = "status.podIP"}
assert_managed_secret_channel = _env["STRIPE_KEY"].managedSecret == {name = "STRIPE_KEY"}
assert_managed_secret_optional = _env["SENTRY_DSN"].managedSecret == {name = "SENTRY_DSN", optional = True}
assert_database_ref_channel = _env["ORDERS_DB"].databaseRef == {name = "orders"}

# Last-wins override: literal → reference leaves no literal behind.
assert_override_to_reference = _env["RUNTIME_CLASS_NAME"].configMapRef.name == "runtime" and "value" not in _env["RUNTIME_CLASS_NAME"]
assert_added = _env["NEW_FLAG"].value == "on"

# Exactly one source on every entry; no sourceless entries; no duplicates.
_channels = ["value", "secretRef", "configMapRef", "fieldRef", "managedSecret", "databaseRef", "workloadURL"]
assert_exactly_one_source = all e in _spec_env {
    len([c for c in _channels if c in e]) == 1
}
assert_no_duplicate_names = len(_env) == len(_spec_env)
# Overriding keeps the base's insertion order; additions go after the
# declared keys, and the keys forge derives (here OTEL_SERVICE_NAME and
# OTEL_RESOURCE_ATTRIBUTES: no GoBuild, so no GOMEMLIMIT) come after
# everything the author wrote.
assert_order_preserved = [e.name for e in _spec_env][:3] == ["LOG_LEVEL", "DATABASE_URL", "FEATURE_MAP"]
assert_addition_last = _spec_env[-3].name == "NEW_FLAG"
assert_derived_after_declared = _spec_env[-2] == {name = "OTEL_SERVICE_NAME", value = "api"} and _spec_env[-1].name == "OTEL_RESOURCE_ATTRIBUTES"
