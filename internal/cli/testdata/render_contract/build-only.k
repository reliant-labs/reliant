"""
render contract — the BUILD-ONLY runtime: built and shipped, never run. A Go
CLI with a build variant, and a docker-built image other workloads pull.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    workloads = [
        fw.Workload {
            name = "cli"
            kind = "tool"
            build = forge.GoBuild {cmd = "./cmd/acme-cli", output_name = "acme-cli"}
            runtime = forge.BuildOnly {build_variants = [
                forge.BuildVariant {name = "acme-cli-linux", ldflags = ["-s", "-w"], goos = "linux", goarch = "amd64"}
            ]}
        }
        fw.Workload {
            name = "workspace-base"
            kind = "tool"
            build = forge.DockerBuild {dockerfile = "images/base/Dockerfile"}
            runtime = forge.BuildOnly {}
        }
    ]
})
