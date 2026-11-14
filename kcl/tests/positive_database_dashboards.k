"""
Positive test — ManagedDatabase.dashboards is opt-in and fails closed.

  1. Absent, or declared with enabled = False, the ManagedDatabase record
     carries no enabled dashboards (the Go renderer then renders forge_dash
     present but NOLOGIN with its password disabled).
  2. Enabled with nothing else declared carries the documented defaults:
     row_cap 1000, timeout 5s = 5000 ms, max_concurrency 2.
  3. Declared limits reach the record in the Go spec's vocabulary, with the
     timeout converted from the KCL duration string to milliseconds.
  4. The limits are inclusive at their ceilings.
"""

import forge

_b = forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "demo-prod"}
    databases = [
        forge.ManagedDatabase {name = "plain"}
        forge.ManagedDatabase {name = "off", dashboards = forge.DatabaseDashboards {}}
        forge.ManagedDatabase {name = "defaults", dashboards = forge.DatabaseDashboards {enabled = True}}
        forge.ManagedDatabase {name = "tuned", dashboards = forge.DatabaseDashboards {enabled = True, row_cap = 250, timeout = "3s", max_concurrency = 1}}
        forge.ManagedDatabase {name = "ceiling", dashboards = forge.DatabaseDashboards {enabled = True, row_cap = 10000, timeout = "10s", max_concurrency = 4}}
    ]
}

_out = forge.render(_b)
_rec = {m.metadata.name: m for m in _out.manifests if m.kind == "ManagedDatabase"}
_ent = {d.name: d for d in _out.databases}

assert_plain_declares_none = "dashboards" not in _rec["plain"].spec
assert_off_is_not_enabled = not _rec["off"].spec.dashboards.enabled
assert_defaults_enabled = _rec["defaults"].spec.dashboards.enabled
assert_default_row_cap = _rec["defaults"].spec.dashboards.rowCap == 1000
assert_default_timeout = _rec["defaults"].spec.dashboards.timeoutMs == 5000
assert_default_concurrency = _rec["defaults"].spec.dashboards.maxConcurrency == 2
assert_tuned = _rec["tuned"].spec.dashboards == {enabled = True, rowCap = 250, timeoutMs = 3000, maxConcurrency = 1}
assert_ceiling = _rec["ceiling"].spec.dashboards == {enabled = True, rowCap = 10000, timeoutMs = 10000, maxConcurrency = 4}
assert_entity_carries_the_same_spec = _ent["tuned"].spec.dashboards == _rec["tuned"].spec.dashboards
assert_entity_plain_declares_none = "dashboards" not in _ent["plain"].spec
