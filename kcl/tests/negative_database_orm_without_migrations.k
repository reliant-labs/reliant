"""
Negative test — an ORM package with no migrations declares nothing to project.
"""

# expect: sets orm or migration_safety but no migrations

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    databases = [forge.ManagedDatabase {name = "app", orm = forge.DatabaseORM {package = "internal/db"}}]
})
