"""
Negative test — `coverage = True` on a persistent env (no lifecycle, not a
preview) is refused unless the env says why: forge treats such an env as
production, and an instrumented build there is a decision.
"""

# expect: Bundle.tests.coverage = True on a persistent env
# expect: coverage_in_persistent_env

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    tests = forge.Tests {coverage = True}
    workloads = [
        fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/demo"}, args = ["server"], runtime = forge.OnHost {}}
    ]
})
