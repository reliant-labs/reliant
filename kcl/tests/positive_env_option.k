"""
Positive test — `option("env")` plumbing.

The forge CLI passes `-D env=<env>` on every render so a main.k can read the
current environment and conditionally include objects in Bundle.manifests
per env. The canonical use: suppress in-cluster infra (NATS / Temporal /
LiteLLM) on a `dev-host` env where docker-compose provides it, while still
shipping it on k3d / staging / prod.

It also pins that the SAME binding makes the env-ownership stamp automatic:
`Bundle.env` defaults to `option("env")`, so every object in output.manifests
carries `forge.dev/env: <env>` without the project writing a line of KCL.
"""

# kcl-args: env=dev | env=dev-host

import forge
import forge.workloads as fw

_env_name = option("env") or "dev"
_is_dev_host = _env_name == "dev-host"

_nats_manifests = [] if _is_dev_host else [
    {apiVersion = "v1", kind = "Service", metadata = {name = "nats"}}
]

_target = forge.ClusterTarget {cluster = "k3d-myapp", namespace = "myapp-" + _env_name}

output = forge.render(forge.Bundle {
    project = "myapp"
    cluster_target = _target
    workloads = [fw.Workload {name = "api", image = "localhost:5050/myapp", runtime = forge.OnCluster {target = _target}}]
    manifests = [forge.Manifests {objects = _nats_manifests}]
})

assert_env_name = _env_name == option("env")
assert_output_env = output.env == _env_name
_nats_count = len([m for m in output.manifests if m.kind == "Service" and m.metadata.name == "nats"])
assert_nats_in_manifests_iff_not_dev_host = _nats_count == (0 if _is_dev_host else 1)
assert_env_label_on_every_manifest = all m in output.manifests {
    m.metadata.labels["forge.dev/env"] == _env_name
}
