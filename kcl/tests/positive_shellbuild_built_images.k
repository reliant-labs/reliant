"""
Positive test — ShellBuild.built_images declares that one image is built FROM
another. A render happens before any build, so the parent's digest cannot be
composed in KCL; the edge is declared as data (`ENV_NAME = "<workload>"`) and
projected onto the rendered build, where forge's build orders the two and sets
ENV_NAME to the exact `<repository>@sha256:<digest>` the parent pushed.

Pinned here: the edge renders verbatim, a build without one renders no key at
all (so existing renders are byte-identical), and the parent stays an ordinary
build-only ShellBuild workload.
"""

import forge
import forge.workloads as fw

_runtime = fw.Workload {
    name = "runtime"
    kind = "tool"
    image = "ghcr.io/acme/runtime"
    runtime = forge.BuildOnly {}
    build = forge.ShellBuild {cmd = "./scripts/build-runtime.sh"}
}

_app = fw.Workload {
    name = "app"
    kind = "tool"
    image = "ghcr.io/acme/app"
    runtime = forge.BuildOnly {}
    build = forge.ShellBuild {
        cmd = r"""docker build --build-arg "BASE=$BASE" -t ghcr.io/acme/app:dev . && docker push ghcr.io/acme/app:dev"""
        env = {"DOCKER_BUILDKIT" = "1"}
        built_images = {BASE = "runtime"}
    }
}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_runtime, _app]
})
_b = {w.name: w.build for w in output.workloads}

assert_edge_rendered = _b["app"].built_images == {"BASE": "runtime"}
assert_env_untouched = _b["app"].env == {"DOCKER_BUILDKIT": "1"}
assert_no_edge_no_key = "built_images" not in _b["runtime"]
