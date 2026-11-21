"""
Negative test — a build variant's output_name is a file name in the build's
output directory, never a path.
"""

# expect: must be a file name, not a path

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [
        fw.Workload {
            name = "cli"
            kind = "tool"
            build = forge.GoBuild {cmd = "./cmd/cli"}
            runtime = forge.BuildOnly {build_variants = [
                forge.BuildVariant {name = "linux", output_name = "../cli"}
            ]}
        }
    ]
})
