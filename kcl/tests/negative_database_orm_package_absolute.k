"""
Negative test — an ORM package outside the repository is refused.
"""

# expect: DatabaseORM.package '/tmp/db' is not a directory inside the repository

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [forge.Database {name = "app", migrations = forge.DatabaseMigrations {}, orm = forge.DatabaseORM {package = "/tmp/db"}}]
})
