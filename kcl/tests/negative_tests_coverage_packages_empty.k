"""
Negative test — Tests.coverage_packages is "main" or a non-empty list of
-coverpkg patterns; an empty list would instrument nothing while saying it
instruments something.
"""

# expect: Tests.coverage_packages is "main" or a non-empty list

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    lifecycle = "local"
    tests = forge.Tests {coverage_packages = []}
    workloads = [
        fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/demo"}, args = ["server"], runtime = forge.OnHost {}}
    ]
})
