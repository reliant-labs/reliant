"""
Positive test — an env that declares no forge.Tests renders no `tests` key,
whatever its lifecycle: the computed default is forge's to resolve (it reads
forge.yaml), so a project that predates Tests renders exactly as before.
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    lifecycle = "local"
    workloads = [
        fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/demo"}, args = ["server"], runtime = forge.OnHost {}}
    ]
})

assert_no_tests_key = "tests" not in output
