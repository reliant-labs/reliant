"""
Positive test — a declared release-health policy renders resolved: the window
in seconds, fail_on sorted, every threshold carried.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {
        window = "2h"
        min_requests = 200
        max_error_rate_increase = 0.005
        max_p95_ratio = 2
        fail_on = ["latency", "error_rate"]
        require_baseline = "every"
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

assert_window = output.release_health.window_seconds == 7200
assert_sorted = output.release_health.fail_on == ["error_rate", "latency"]
assert_thresholds = output.release_health.min_requests == 200 and output.release_health.max_error_rate_increase == 0.005 and output.release_health.new_issue_min_events == 3
assert_baseline = output.release_health.require_baseline == "every"

# The default baseline policy is left out, so a render that keeps it records
# the shape it always did.
_default_baseline = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {min_requests = 10}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
assert_default_baseline_absent = "require_baseline" not in _default_baseline.release_health
