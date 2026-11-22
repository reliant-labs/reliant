"""
Negative test — a timeout that is not a duration is refused with a message that says what is accepted.
"""

# expect: must be a duration in whole seconds

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
    databases = [forge.ManagedDatabase {
        name = "orders"
        dashboards = forge.DatabaseDashboards {enabled = True, timeout = "five seconds"}
    }]
})
