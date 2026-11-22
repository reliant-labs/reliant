"""
Positive test — DockerBuild.inputs names the files the image is built from, so
a --release build can reuse an earlier release's image whose inputs and
declaration are unchanged. The globs render verbatim (relative to the project
root, or to a GitSource context's checkout; `!` excluding), and a build without
inputs renders no key at all, so every existing render is byte-identical.
"""

import forge
import forge.workloads as fw

_api = fw.Workload {
    name = "api"
    kind = "tool"
    image = "ghcr.io/acme/api"
    runtime = forge.BuildOnly {}
    build = forge.DockerBuild {
        inputs = ["go.mod", "go.sum", "**/*.go", "!**/*_test.go", ".dockerignore"]
    }
}

_billing = fw.Workload {
    name = "billing"
    kind = "tool"
    image = "ghcr.io/acme/billing"
    runtime = forge.BuildOnly {}
    build = forge.DockerBuild {
        context = forge.GitSource {repo = "github.com/acme/billing", ref = "v2.3.1"}
        inputs = ["**/*.py", "requirements.txt"]
    }
}

_web = fw.Workload {
    name = "web"
    kind = "tool"
    image = "ghcr.io/acme/web"
    runtime = forge.BuildOnly {}
    build = forge.DockerBuild {}
}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_api, _billing, _web]
})
_b = {w.name: w.build for w in output.workloads}

assert_inputs_rendered = _b["api"].inputs == ["go.mod", "go.sum", "**/*.go", "!**/*_test.go", ".dockerignore"]
assert_git_source_inputs_rendered = _b["billing"].inputs == ["**/*.py", "requirements.txt"]
assert_no_inputs_no_key = "inputs" not in _b["web"]
