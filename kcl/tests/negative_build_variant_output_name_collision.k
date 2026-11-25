"""
Negative test — two build variants of one workload that would write the same
binary are refused: the second build would overwrite the first, and the
release ledger keys each binary by its output name.
"""

# expect: share an output_name

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
                forge.BuildVariant {name = "linux", goos = "linux", output_name = "cli"}
                forge.BuildVariant {name = "darwin", goos = "darwin", output_name = "cli"}
            ]}
        }
    ]
})
