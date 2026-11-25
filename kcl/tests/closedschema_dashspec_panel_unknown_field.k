"""
Closed-schema test — the generated dashspec schemas are closed: a field the Go
type does not declare (here `query`, which is not a Panel field) fails to
compile instead of serializing into a dashboard ParseDashboard would refuse.
"""

# expect: query

import forge.dashspec as ds

_p = ds.Panel {id = "p", title = "P", kind = "sql", query = "SELECT 1"}
