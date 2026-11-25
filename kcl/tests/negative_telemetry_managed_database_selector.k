"""
Negative test — a ManagedDatabase's telemetry targets its own instances; a selector is refused.
"""

# expect: telemetry targets this database's instances itself

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    databases = [forge.ManagedDatabase {
        name = "orders"
        telemetry = forge.Telemetry {selector = {app = "x"}, checks = [forge.HealthCheck {port = "postgres"}]}
    }]
})
