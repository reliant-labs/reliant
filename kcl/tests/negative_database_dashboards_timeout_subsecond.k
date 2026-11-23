"""
Negative test — sub-second timeouts are not in the grammar.
"""

# expect: must be a duration in whole seconds

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
    databases = [forge.ManagedDatabase {
        name = "orders"
        dashboards = forge.DatabaseDashboards {enabled = True, timeout = "500ms"}
    }]
})
