"""
Negative test — a raw group's telemetry rides the group's cluster.
"""

# expect: Manifests.telemetry: cluster is the group's

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    manifests = [forge.Manifests {name = "ch", cluster = "k3d-demo", telemetry = [forge.Telemetry {cluster = "other", selector = {app = "ch"}, metrics = [forge.MetricsScrape {port = 9363}]}]}]
})
