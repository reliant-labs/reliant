"""
Positive test — Build.outputs names the FILES a build produces that the
release ships (a CLI archive, an installer, a desktop bundle).

A forge.BuildOnly workload whose ShellBuild or GoBuild declares outputs and no
`image` is FILES-ONLY: it renders no artifact name (`image` is "" and no
`build_image`), so nothing pushes or resolves an image for it. An
image-bearing build-only workload, and one that declares no outputs, render
exactly as before. A build without outputs renders no `outputs` key, and an
output's platform renders only when set.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "k3d-acme", namespace = "dev"}

# Files only: a packaged CLI, no image.
_cli = fw.Workload {
    name = "acme-cli"
    kind = "tool"
    runtime = forge.BuildOnly {}
    build = forge.ShellBuild {
        cmd = "./scripts/package-cli.sh"
        outputs = [
            forge.BuildOutput {path = "dist/acme_linux_amd64.tar.gz", platform = "linux/amd64"}
            forge.BuildOutput {path = "dist/acme_darwin_arm64.tar.gz", platform = "darwin/arm64"}
            forge.BuildOutput {path = "dist/*.txt"}
        ]
    }
}

# Files only, from a GoBuild.
_agent = fw.Workload {
    name = "acme-agent"
    kind = "tool"
    runtime = forge.BuildOnly {}
    build = forge.GoBuild {
        cmd = "./cmd/agent"
        outputs = [forge.BuildOutput {path = "bin/acme-agent"}]
    }
}

# An image AND files: the image keeps its name; the files ride beside it.
_installer = fw.Workload {
    name = "installer"
    kind = "tool"
    image = "ghcr.io/acme/installer"
    runtime = forge.BuildOnly {}
    build = forge.ShellBuild {
        cmd = "./scripts/installer.sh"
        outputs = [forge.BuildOutput {path = "out/acme-setup.exe", platform = "windows/amd64"}]
    }
}

# Build-only with a named image output and no file outputs. An explicit
# output_name still supplies the image, unlike an unnamed ShellBuild.
_legacy = fw.Workload {
    name = "legacy-cli"
    kind = "tool"
    runtime = forge.BuildOnly {}
    build = forge.ShellBuild {cmd = "./scripts/legacy.sh", output_name = "legacy-cli"}
}

# Outputs on a cluster workload never drop its image.
_api = fw.Workload {
    name = "api"
    image = "localhost:5051/api"
    build = forge.DockerBuild {outputs = [forge.BuildOutput {path = "sbom.json"}]}
    ports = [fw.Port {name = "http", port = 8080}]
    runtime = forge.OnCluster {target = _target}
}

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [_cli, _agent, _installer, _legacy, _api]
})
_w = {w.name: w for w in output.workloads}

assert_cli_outputs = _w["acme-cli"].build.outputs == [
    {"path": "dist/acme_linux_amd64.tar.gz", "platform": "linux/amd64"}
    {"path": "dist/acme_darwin_arm64.tar.gz", "platform": "darwin/arm64"}
    {"path": "dist/*.txt"}
]
assert_cli_files_only = _w["acme-cli"].image == "" and _w["acme-cli"].build_image == None
assert_agent_outputs = _w["acme-agent"].build.outputs == [{"path": "bin/acme-agent"}]
assert_agent_files_only = _w["acme-agent"].image == ""
assert_installer_keeps_image = _w["installer"].image == "ghcr.io/acme/installer"
assert_installer_outputs = _w["installer"].build.outputs == [{"path": "out/acme-setup.exe", "platform": "windows/amd64"}]
assert_named_output_keeps_image = _w["legacy-cli"].image == "legacy-cli" and "outputs" not in _w["legacy-cli"].build
assert_cluster_keeps_image = _w["api"].image == "localhost:5051/api" and _w["api"].build.outputs == [{"path": "sbom.json"}]
