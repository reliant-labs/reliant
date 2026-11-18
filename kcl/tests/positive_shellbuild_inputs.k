"""
Positive test — ShellBuild.inputs names the files the command reads, so forge
can skip a build whose inputs, recipe and pushed image are unchanged since its
last successful run. The globs render verbatim (relative to the build's cwd, a
leading ../ reaching a sibling checkout, `!` excluding), and a build without
inputs renders no key at all, so every existing render is byte-identical.
"""

import forge
import forge.workloads as fw

_gateway = fw.Workload {
    name = "gateway"
    kind = "tool"
    image = "ghcr.io/acme/gateway"
    runtime = forge.BuildOnly {}
    build = forge.ShellBuild {
        cwd = "../gateway"
        cmd = "docker build -t ghcr.io/acme/gateway:dev . && docker push ghcr.io/acme/gateway:dev"
        inputs = ["go.mod", "go.sum", "**/*.go", "Dockerfile", "!**/*_test.go", "../shared/proto"]
    }
}

_tool = fw.Workload {
    name = "tool"
    kind = "tool"
    image = "ghcr.io/acme/tool"
    runtime = forge.BuildOnly {}
    build = forge.ShellBuild {cmd = "./scripts/build-tool.sh"}
}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_gateway, _tool]
})
_b = {w.name: w.build for w in output.workloads}

assert_inputs_rendered = _b["gateway"].inputs == ["go.mod", "go.sum", "**/*.go", "Dockerfile", "!**/*_test.go", "../shared/proto"]
assert_no_inputs_no_key = "inputs" not in _b["tool"]
