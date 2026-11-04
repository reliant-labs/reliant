"""
Negative test — dashboards.row_cap below 1 is refused, not treated as unset.
"""

# expect: DatabaseDashboards.row_cap 0 must be between 1 and 10000

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
    databases = [forge.ManagedDatabase {
        name = "orders"
        dashboards = forge.DatabaseDashboards {enabled = True, row_cap = 0}
    }]
})
