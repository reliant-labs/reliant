"""Per-consumer slots can bind two digests of the same repository."""
import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "prod", namespace = "app", platform = "amd64"}
_base = fw.Workload {
    name = "api"
    image = "registry.test/reliant"
    release_dependencies = {worker = "api-temporal/v1"}
    runtime = forge.OnCluster {target = _target}
    build = forge.ShellBuild {cmd = "true", unit = "reliant", inputs_file = "build-inputs.json"}
}
_worker = _base | {name = "worker", release_dependencies = {api = "api-temporal/v1"}}
output = forge.render(forge.Bundle {project = "app", env = "prod", workloads = [_base, _worker]})
assert_api_old = output.workloads[0].spec.image == "registry.test/reliant@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
assert_worker_new = output.workloads[1].spec.image == "registry.test/reliant@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
assert_recipe = output.workloads[1].build.unit == "reliant" and output.workloads[1].build.inputs_file == "build-inputs.json"
assert_cohort = output.workloads[0].release_dependencies.worker == "api-temporal/v1"
