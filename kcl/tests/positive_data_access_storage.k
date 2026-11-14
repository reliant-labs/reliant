"""
Positive test — data_access.storage, the storage browser's policy, resolves
from the declaration, else from db_query (declared or defaulted), and is
recorded only where it differs from db_query.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_w = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
_real_env = lambda da: forge.DataAccess -> any {
    forge.render(forge.Bundle {project = "demo", data_access = da, workloads = _w})
}
_local_env = lambda da: forge.DataAccess -> any {
    forge.render(forge.Bundle {project = "demo", lifecycle = "local", data_access = da, workloads = _w})
}

_real = _real_env(None)
_local = _local_env(None)
_sql_denied = _real_env(forge.DataAccess {db_query = "deny"})
_files_denied = _real_env(forge.DataAccess {storage = "deny"})
_files_open_sql_audited = _real_env(forge.DataAccess {db_query = "audited", storage = "open"})
_local_files_audited = _local_env(forge.DataAccess {storage = "audited"})
_spelled_fallback = _real_env(forge.DataAccess {db_query = "deny", storage = "deny"})
_spelled_default = _real_env(forge.DataAccess {db_query = "audited", storage = "audited"})

output = _files_denied

# Audited for both is the default of a real env: no key, so a render that
# predates the field is byte-identical.
assert_real_records_nothing = "data_access" not in _real and "data_access" not in _spelled_default
# Unset, storage follows db_query: a denied db_query denies files too, and the
# render records only db_query, which a reader resolves storage from.
assert_unset_follows_db_query = _sql_denied.data_access == {"db_query": "deny"} and _local.data_access == {"db_query": "open"}
assert_spelled_fallback_is_not_recorded = _spelled_fallback.data_access == {"db_query": "deny"}
# Declared and different from db_query: recorded beside it.
assert_files_denied_on_a_real_env = _files_denied.data_access == {"db_query": "audited", "storage": "deny"}
assert_files_open_sql_audited = _files_open_sql_audited.data_access == {"db_query": "audited", "storage": "open"}
assert_local_files_audited = _local_files_audited.data_access == {"db_query": "open", "storage": "audited"}
