"""
Negative test — several entries in one group need a service_name each.
"""

# expect: several entries in one group need a service_name each

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    manifests = [forge.Manifests {name = "obs", cluster = "k3d-demo", telemetry = [
        forge.Telemetry {selector = {app = "a"}, checks = [forge.HealthCheck {port = 80}]}
        forge.Telemetry {selector = {app = "b"}, checks = [forge.HealthCheck {port = 81}]}
    ]}]
})
