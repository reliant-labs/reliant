"""
Positive test — a persistent env (no lifecycle, not a preview) may turn
coverage on when it says why, and the reason renders with the env.
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    tests = forge.Tests {coverage = True, coverage_in_persistent_env = "staging runs the nightly e2e"}
    workloads = [
        fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/demo"}, args = ["server"], runtime = forge.OnHost {}}
    ]
})

assert_reason_rendered = output.tests.coverage_in_persistent_env == "staging runs the nightly e2e"
assert_coverage_rendered = output.tests.coverage == True
