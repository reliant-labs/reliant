"""
Positive test — an empty-string env value is legal and survives to the
wire as `{name, value: ""}`, NOT as an absent/null value.

cp-forge needs empty-string sentinels (RUNTIME_CLASS_NAME = "" to skip
runtimeClassName; webhook secret env-gate values). A lowering that treats ""
as "unset" (`if v`) would drop the entry, and the app would read its default
instead of the explicit disable.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "k3d-empty-value", namespace = "empty-value-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5050/api"
        ports = [fw.Port {name = "http", port = 8080}]
        env = {
            RUNTIME_CLASS_NAME = ""
            LOG_LEVEL = "info"
            DB_PASSWORD = forge.SecretRef {name = "app-secrets", key = "DB_PASSWORD"}
        }
        runtime = forge.OnCluster {target = _target}
    }]
})

_spec_env = output.workloads[0].spec.env
_rec_env = [m for m in output.manifests if m.kind == "Workload"][0].spec.env

# The three declared entries, then the OTEL_SERVICE_NAME and
# OTEL_RESOURCE_ATTRIBUTES forge derives.
assert_env_count = len(_spec_env) == 5 and _spec_env[3].name == "OTEL_SERVICE_NAME" and _spec_env[4].name == "OTEL_RESOURCE_ATTRIBUTES"
assert_empty_name = _spec_env[0].name == "RUNTIME_CLASS_NAME"
assert_empty_value_present = "value" in _spec_env[0]
assert_empty_value_is_empty_string = _spec_env[0].value == ""
assert_populated_value = _spec_env[1].value == "info"
assert_secret_has_no_value = "value" not in _spec_env[2]
# The record carries the same "" — Go renders it as `value: ""`.
assert_record_empty_value = "value" in _rec_env[0] and _rec_env[0].value == ""
