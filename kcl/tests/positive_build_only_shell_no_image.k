"""
Positive test — a forge.BuildOnly ShellBuild that declares no `image` builds
no image, so the render names none.

Its command (a desktop bundle, static files a frontend's `bundle` copies in)
owns everything it writes, and forge composes no image reference into it.
Deriving `<workload>:<tag>` from its name recorded a placeholder image, made
the build overwrite the env's deploy-side build state, and made every release
cut demand an image nothing pushed (forge bug 882ce226).

Everything that does name an image keeps it: a declared `image`, an
`output_name`, a build-only DockerBuild or GoBuild, and a ShellBuild bound
to a runtime that runs an image.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "k3d-acme", namespace = "dev"}

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [
        # The defect's shape: build-only, ShellBuild, no image, no outputs.
        fw.Workload {
            name = "desktop"
            kind = "tool"
            build = forge.ShellBuild {cwd = "electron", cmd = "node scripts/pack.js"}
            runtime = forge.BuildOnly {}
        }
        fw.Workload {
            name = "base"
            kind = "tool"
            image = "localhost:5051/base"
            build = forge.ShellBuild {cmd = "./scripts/build-base.sh"}
            runtime = forge.BuildOnly {}
        }
        fw.Workload {
            name = "named"
            kind = "tool"
            build = forge.ShellBuild {cmd = "./scripts/build-named.sh", output_name = "named-artifact"}
            runtime = forge.BuildOnly {}
        }
        fw.Workload {
            name = "dockered"
            kind = "tool"
            build = forge.DockerBuild {dockerfile = "Dockerfile"}
            runtime = forge.BuildOnly {}
        }
        fw.Workload {
            name = "cli"
            kind = "tool"
            build = forge.GoBuild {cmd = "./cmd/cli"}
            runtime = forge.BuildOnly {}
        }
        fw.Workload {
            name = "api"
            image = "localhost:5051/api"
            build = forge.ShellBuild {cmd = "./scripts/build-api.sh"}
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
        }
    ]
})
_w = {w.name: w for w in output.workloads}

assert_desktop_names_no_image = _w["desktop"].image == ""
assert_desktop_has_no_build_image = _w["desktop"].build_image == None
assert_desktop_still_builds = _w["desktop"].build.type == "shell" and _w["desktop"].build.cmd == "node scripts/pack.js"
assert_desktop_build_only = _w["desktop"].runtime.type == "build-only"

assert_declared_image_kept = _w["base"].image == "localhost:5051/base"
assert_output_name_kept = _w["named"].image == "named-artifact"
assert_docker_derived = _w["dockered"].image == "dockered"
assert_go_derived = _w["cli"].image == "cli"
assert_cluster_shell_kept = _w["api"].image == "localhost:5051/api"
