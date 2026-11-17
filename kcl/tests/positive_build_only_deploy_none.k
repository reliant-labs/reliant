"""
Positive test — the BUILD-ONLY runtime.

A workload bound to forge.BuildOnly {} builds and pushes its artifact but
runs nowhere: no Workload record in output.manifests, while a cluster
workload in the SAME bundle still gets one. Use it for a base image other
workloads reference at runtime, or a CLI.

The build projects through, so `forge build` still builds and pushes it.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "k3d-cp", namespace = "dev"}

# A base image: built + pushed, never orchestrated.
_base_image = fw.Workload {
    name = "workspace-base"
    kind = "tool"
    image = "localhost:5051/workspace-base"
    build = forge.DockerBuild {dockerfile = "docker/Dockerfile.workspace-base"}
    runtime = forge.BuildOnly {}
}

# A CLI binary.
_cli = fw.Workload {
    name = "reliant-cli"
    kind = "tool"
    build = forge.GoBuild {cmd = "./cmd/reliant"}
    runtime = forge.BuildOnly {}
}

# A normal cluster workload in the SAME bundle.
_api = fw.Workload {
    name = "api"
    image = "localhost:5051/api"
    ports = [fw.Port {name = "http", port = 8080}]
    runtime = forge.OnCluster {target = _target}
}

output = forge.render(forge.Bundle {project = "cp", workloads = [_base_image, _cli, _api]})
_w = {w.name: w for w in output.workloads}

assert_base_runtime = _w["workspace-base"].runtime.type == "build-only"
assert_base_build_type = _w["workspace-base"].build.type == "docker"
assert_base_dockerfile = _w["workspace-base"].build.dockerfile == "docker/Dockerfile.workspace-base"
assert_cli_runtime = _w["reliant-cli"].runtime.type == "build-only"
assert_cli_build_type = _w["reliant-cli"].build.type == "go"
assert_api_runtime = _w["api"].runtime.type == "cluster"

_record_names = [m.metadata.name for m in output.manifests if m.kind == "Workload"]
assert_no_base_record = "workspace-base" not in _record_names
assert_no_cli_record = "reliant-cli" not in _record_names
assert_api_record = _record_names == ["api"]
