"""
Positive test — data_access resolves db_query from the declaration, else from
the lifecycle: open for local/ephemeral, audited (recorded as absence) for a
real env.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_w = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]

_real = forge.render(forge.Bundle {project = "demo", workloads = _w})
_local = forge.render(forge.Bundle {project = "demo", lifecycle = "local", workloads = _w})
_scratch = forge.render(forge.Bundle {project = "demo", lifecycle = "ephemeral", workloads = _w})
_denied = forge.render(forge.Bundle {project = "demo", data_access = forge.DataAccess {db_query = "deny"}, workloads = _w})
_denied_local = forge.render(forge.Bundle {project = "demo", lifecycle = "local", data_access = forge.DataAccess {db_query = "deny"}, workloads = _w})
_audited_local = forge.render(forge.Bundle {project = "demo", lifecycle = "local", data_access = forge.DataAccess {db_query = "audited"}, workloads = _w})
_open_real = forge.render(forge.Bundle {project = "demo", data_access = forge.DataAccess {db_query = "open"}, workloads = _w})

output = _real

# Audited is the default of an env with no lifecycle, so it is the ABSENCE of
# the key: a real env's render is byte-identical to one rendered before the
# field existed, and a reader treats absence as audited, never as open.
assert_real_is_audited_by_absence = "data_access" not in _real
assert_declared_audited_on_a_local_env = "data_access" not in _audited_local
assert_local_is_open = _local.data_access.db_query == "open"
assert_ephemeral_is_open = _scratch.data_access.db_query == "open"
assert_declared_deny_wins = _denied.data_access.db_query == "deny" and _denied_local.data_access.db_query == "deny"
assert_declared_open_on_a_real_env = _open_real.data_access.db_query == "open"
