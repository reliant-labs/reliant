"""
Negative test — a panel kind dashspec does not declare is refused at author
time by the generated enum check, naming the values it may be.
"""

# expect: Panel.kind must be one of
# expect: "release_health"

import forge.dashspec as ds

_d = ds.Dashboard {
    id = "d", title = "D"
    panels = [ds.Panel {id = "p", title = "P", kind = "graph", signal = "metrics"}]
}

dashboard = ds.dashboard_json(_d)
