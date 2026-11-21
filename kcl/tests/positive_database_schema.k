"""
Positive test — a database declares its schema: dialect, migrations, ORM and
migration safety, on every kind of database declaration.

Pins:
  1. A managed, an external and a schema-only (forge.Database) database each
     carry {dialect, migrations, orm, migration_safety} in the output, with
     paths normalised ("./sql/app/" is "sql/app").
  2. `driver` defaults to forge and `dir` to db/migrations; an argv is
     carried only when declared.
  3. A database that declares no migrations renders exactly as before: no
     schema keys at all.
  4. schema_only_databases is present only when declared.
"""

import forge

_bundle = forge.Bundle {
    project = "shop"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    databases = [
        forge.ManagedDatabase {
            name = "default"
            migrations = forge.DatabaseMigrations {}
            orm = forge.DatabaseORM {package = "./internal/db/"}
        }
        forge.ManagedDatabase {name = "cache"}
    ]
    external_databases = [forge.ExternalDatabase {
        name = "ledger"
        service = "pgbouncer"
        database = "ledger"
        username = "ledger_ro"
        password_secret = "ledger-ro"
        migrations = forge.DatabaseMigrations {
            dir = "./sql/ledger/"
            driver = "custom"
            apply = ["./scripts/migrate.sh", "up"]
            check = ["./scripts/migrate.sh", "check"]
        }
        migration_safety = forge.DatabaseMigrationSafety {destructive_change = "warn", require_compat_declaration = True}
    }]
    schema_only_databases = [forge.Database {
        name = "analytics"
        migrations = forge.DatabaseMigrations {dir = "analytics/migrations", driver = "goose"}
        orm = forge.DatabaseORM {package = "internal/analyticsdb"}
    }]
}

_out = forge.render(_bundle)
_dbs = {d.name: d for d in _out.databases}
_ext = {x.name: x for x in _out.external_databases}
_only = {d.name: d for d in _out.schema_only_databases}

assert_default_dialect = _dbs["default"].dialect == "postgres"
assert_default_migrations = _dbs["default"].migrations == {"dir": "db/migrations", "driver": "forge"}
assert_default_orm = _dbs["default"].orm == {"package": "internal/db"}
assert_default_no_safety = "migration_safety" not in _dbs["default"]
assert_cache_unchanged = "dialect" not in _dbs["cache"] and "migrations" not in _dbs["cache"] and "orm" not in _dbs["cache"]
assert_ledger_dir = _ext["ledger"].migrations.dir == "sql/ledger"
assert_ledger_custom = _ext["ledger"].migrations.driver == "custom" and _ext["ledger"].migrations.apply == ["./scripts/migrate.sh", "up"]
assert_ledger_check = _ext["ledger"].migrations.check == ["./scripts/migrate.sh", "check"]
assert_ledger_no_status = "status" not in _ext["ledger"].migrations
assert_ledger_safety = _ext["ledger"].migration_safety == {"enabled": True, "destructive_change": "warn", "require_compat_declaration": True}
assert_ledger_route_kept = _ext["ledger"].service == "pgbouncer"
assert_analytics_only = _only["analytics"] == {"name": "analytics", "dialect": "postgres", "migrations": {"dir": "analytics/migrations", "driver": "goose"}, "orm": {"package": "internal/analyticsdb"}}
assert_no_schema_only_key = "schema_only_databases" not in forge.render(forge.Bundle {project = "shop", env = "prod"})
