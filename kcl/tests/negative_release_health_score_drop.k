"""
Negative test — release_health refuses: seo_max_score_drop = 101 (a score is 0-100)
"""

# expect: seo_max_score_drop

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {seo_max_score_drop = 101}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
