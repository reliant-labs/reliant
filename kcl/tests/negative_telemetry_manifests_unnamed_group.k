"""
Negative test — an unnamed group's telemetry needs a service_name.
"""

# expect: Manifests.telemetry on a group with no name needs a service_name

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    manifests = [forge.Manifests {cluster = "k3d-demo", telemetry = [forge.Telemetry {selector = {app = "a"}, checks = [forge.HealthCheck {port = 80}]}]}]
})
