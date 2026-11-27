"""
Negative test — dashboards on both the wrapper and its spec are two sources of truth.
"""

# expect: declares dashboards twice

import forge
import forge.tiers

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
    databases = [forge.ManagedDatabase {
        name = "orders"
        dashboards = forge.DatabaseDashboards {enabled = True}
        spec = tiers.ManagedDatabase {dashboards = tiers.DatabaseDashboards {enabled = True}}
    }]
})
