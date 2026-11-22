"""
A Windows target with output_name "cli" and a Linux target with output_name
"cli.exe" both write cli.exe even though their ledger keys differ.
"""

# expect: two variants write the same binary filename after adding the Windows .exe suffix

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
                forge.BuildVariant {name = "windows", goos = "windows", output_name = "cli"}
                forge.BuildVariant {name = "linux", goos = "linux", output_name = "cli.exe"}
            ]}
        }
    ]
})
