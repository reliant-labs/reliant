"""
Negative test — two databases never share an ORM package.
"""

# expect: two databases declare the same ORM package

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [
        forge.Database {name = "app", migrations = forge.DatabaseMigrations {dir = "a"}, orm = forge.DatabaseORM {package = "internal/db"}}
        forge.Database {name = "analytics", migrations = forge.DatabaseMigrations {dir = "b"}, orm = forge.DatabaseORM {package = "internal/db/"}}
    ]
})
