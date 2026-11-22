"""
Negative test — an unknown migration driver is refused.
"""

# expect: DatabaseMigrations.driver 'liquibase' is not one of

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [forge.Database {name = "app", migrations = forge.DatabaseMigrations {driver = "liquibase"}}]
})
