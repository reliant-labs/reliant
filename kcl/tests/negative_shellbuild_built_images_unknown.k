# expect: ShellBuild.built_images must name ANOTHER workload
# expect: app.BASE -> runtime
# expect: app.SELF -> app
# expect: app.CTRL -> api
import forge
import forge.workloads as fw

# `runtime` is not declared at all, `app` names itself, and `api` is built by
# a DockerBuild — forge resolves only ShellBuild-pushed digests for an edge.
_api = fw.Workload {
    name = "api"
    kind = "tool"
    image = "ghcr.io/acme/api"
    runtime = forge.BuildOnly {}
    build = forge.DockerBuild {}
}

_app = fw.Workload {
    name = "app"
    kind = "tool"
    image = "ghcr.io/acme/app"
    runtime = forge.BuildOnly {}
    build = forge.ShellBuild {
        cmd = "make image"
        built_images = {BASE = "runtime", SELF = "app", CTRL = "api"}
    }
}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_api, _app]
})
