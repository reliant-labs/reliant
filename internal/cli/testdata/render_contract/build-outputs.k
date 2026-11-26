"""
render contract — declared build OUTPUTS: files a build writes that the
release ships. A files-only build-only workload (a ShellBuild packaging a CLI,
no image: it renders `image: ""` and no build_image), and a build-only image
whose ShellBuild also writes an installer.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    workloads = [
        fw.Workload {
            name = "acme-cli"
            kind = "tool"
            build = forge.ShellBuild {
                cmd = "./scripts/package-cli.sh"
                cwd = "tools/cli"
                outputs = [
                    forge.BuildOutput {path = "dist/acme_linux_amd64.tar.gz", platform = "linux/amd64"}
                    forge.BuildOutput {path = "dist/*.txt"}
                ]
            }
            runtime = forge.BuildOnly {}
        }
        fw.Workload {
            name = "installer"
            kind = "tool"
            image = "ghcr.io/acme/installer"
            build = forge.ShellBuild {
                cmd = "./scripts/installer.sh"
                outputs = [forge.BuildOutput {path = "out/acme-setup.exe", platform = "windows/amd64"}]
            }
            runtime = forge.BuildOnly {}
        }
    ]
})
