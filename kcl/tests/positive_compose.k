"""
Positive test — the COMPOSE runtime.

A compose-bound workload's container is defined by the compose file; the
workload contributes its name, its runtime block and its env. Its env is
merged into the compose PROCESS env (for `${VAR}` interpolation), so a value
declared once in KCL still reaches the file.
"""

import forge
import forge.workloads as fw

_worker = fw.Workload {
    name = "worker"
    env = {QUEUE = "jobs"}
    runtime = forge.OnCompose {
        file = "deploy/docker-compose.prod.yml"
        service = "worker-prod"
        env_file = ".env.prod"
    }
}

# Defaults: file docker-compose.yml, service = workload name, wait on.
_alt = fw.Workload {name = "alt", runtime = forge.OnCompose {}}

output = forge.render(forge.Bundle {project = "demo", workloads = [_worker, _alt]})
_w = {w.name: w for w in output.workloads}

assert_discriminator = _w["worker"].runtime.type == "compose"
assert_file = _w["worker"].runtime.file == "deploy/docker-compose.prod.yml"
assert_service = _w["worker"].runtime.service == "worker-prod"
assert_env_file = _w["worker"].runtime.env_file == ".env.prod"
assert_spec_env_into_compose_env = _w["worker"].runtime.env["QUEUE"] == "jobs"

assert_default_file = _w["alt"].runtime.file == "docker-compose.yml"
assert_default_service = _w["alt"].runtime.service == "alt"
assert_default_wait = _w["alt"].runtime.wait == True
# Per-checkout by default; `shared` is the opt-in for machine infrastructure
# every git worktree uses (forge drives it from the primary checkout).
assert_default_not_shared = _w["alt"].runtime.shared == False
_infra = forge.render(forge.Bundle {project = "demo", workloads = [
    fw.Workload {name = "dev-infra", runtime = forge.OnCompose {shared = True}}
]}).workloads[0]
assert_shared_projected = _infra.runtime.shared == True

# Compose workloads are not built by forge and render no record.
assert_not_built = _w["worker"].build == None
assert_no_record = len([m for m in output.manifests if m.kind == "Workload"]) == 0
