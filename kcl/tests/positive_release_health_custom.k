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
        regressed_issue_min_events = 1
        error_rate_min_errors = 1
        baseline_window = "48h"
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

assert_window = output.release_health.window_seconds == 7200
assert_sorted = output.release_health.fail_on == ["error_rate", "latency"]
assert_thresholds = output.release_health.min_requests == 200 and output.release_health.max_error_rate_increase == 0.005 and output.release_health.new_issue_min_events == 3
assert_baseline = output.release_health.require_baseline == "every"
assert_regressed = output.release_health.regressed_issue_min_events == 1
assert_error_rate = output.release_health.error_rate_min_errors == 1 and output.release_health.baseline_window_seconds == 172800

# The default baseline policy is left out, so a render that keeps it records
# the shape it always did.
_default_baseline = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {min_requests = 10}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
assert_default_baseline_absent = "require_baseline" not in _default_baseline.release_health

# regressed_issue_min_events follows new_issue_min_events unless set to
# something else, so unset (or set to the same) it is left out too.
assert_default_regressed_absent = "regressed_issue_min_events" not in _default_baseline.release_health
_same_regressed = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {new_issue_min_events = 5, regressed_issue_min_events = 5}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
assert_same_regressed_absent = "regressed_issue_min_events" not in _same_regressed.release_health

# error_rate_min_errors and baseline_window are left out at their defaults,
# set explicitly or not.
assert_default_error_rate_absent = "error_rate_min_errors" not in _default_baseline.release_health and "baseline_window_seconds" not in _default_baseline.release_health
_same_error_rate = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {error_rate_min_errors = 10, baseline_window = "24h"}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
assert_same_error_rate_absent = "error_rate_min_errors" not in _same_error_rate.release_health and "baseline_window_seconds" not in _same_error_rate.release_health
