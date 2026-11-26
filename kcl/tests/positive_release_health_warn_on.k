"""
Positive test — the browser findings fail by default, warn_on demotes one of
them without restating fail_on, and an explicit fail_on keeps its meaning.
The shape records only the failing set.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_api = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]

# Defaults, carried because the window is not the default: every browser
# finding fails.
_default = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {window = "30m"}
    workloads = _api
})
assert_default_fails_browser = _default.release_health.fail_on == ["alert_firing", "browser_errors", "browser_failed_requests", "browser_page_load", "error_rate", "new_issue", "probe_failed", "regressed_issue"]

# warn_on demotes page load alone; the other browser findings still fail.
_demoted = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {warn_on = ["browser_page_load"]}
    workloads = _api
})
assert_demoted = _demoted.release_health.fail_on == ["alert_firing", "browser_errors", "browser_failed_requests", "error_rate", "new_issue", "probe_failed", "regressed_issue"]
assert_no_warn_on_key = "warn_on" not in _demoted.release_health

# An explicit fail_on is what it says: no browser finding fails unless listed.
_explicit = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {fail_on = ["new_issue", "error_rate"]}
    workloads = _api
})
assert_explicit = _explicit.release_health.fail_on == ["error_rate", "new_issue"]

# warn_on takes a code out of an explicit list too, and naming one fail_on
# does not hold is no error.
_both = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {fail_on = ["new_issue", "latency"], warn_on = ["latency", "browser_errors"]}
    workloads = _api
})
assert_both = _both.release_health.fail_on == ["new_issue"]
