"""Environment size is not limited by entry count (regression: CP generation).
Runtime exec byte budgets include resolved references and arguments; they cannot
be inferred from the number of declarations here.
"""
import forge.tiers as tiers

_env = [tiers.EnvVar {name = "CONFIG_${i}", value = "small"} for i in range(300)]
workload = tiers.Workload {
    image = "example/api:v1"
    env = _env
}
sidecar = tiers.Container {
    name = "helper"
    image = "example/helper:v1"
    env = _env
}
assert_workload_env_not_capped = len(workload.env) == 300
assert_container_env_not_capped = len(sidecar.env) == 300
