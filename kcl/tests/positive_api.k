"""
Positive test — forge.API declares an API by its contract.

Pins:
  1. An OpenAPI and a proto API reach output.apis with their spec path
     normalised, and unset base_path / database are absent, not null.
  2. `database` may name any kind of declared database.
  3. apis is present only when declared.
"""

import forge

_out = forge.render(forge.Bundle {
    project = "shop"
    env = "prod"
    schema_only_databases = [forge.Database {name = "default", migrations = forge.DatabaseMigrations {}}]
    apis = [
        forge.API {name = "billing", kind = "openapi", spec = "./api/openapi.yaml", base_path = "/billing", database = "default"}
        forge.API {name = "shop", kind = "proto", spec = "proto/"}
    ]
})
_apis = {a.name: a for a in _out.apis}

assert_openapi = _apis["billing"] == {"name": "billing", "kind": "openapi", "spec": "api/openapi.yaml", "base_path": "/billing", "database": "default"}
assert_proto = _apis["shop"] == {"name": "shop", "kind": "proto", "spec": "proto"}
assert_absent = "apis" not in forge.render(forge.Bundle {project = "shop", env = "prod"})
