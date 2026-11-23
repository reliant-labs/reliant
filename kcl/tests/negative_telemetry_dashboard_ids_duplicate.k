"""
Negative test — a Telemetry's dashboard ids are unique: each becomes one file
named for its id.
"""

# expect: Telemetry.dashboards ids must be unique

import forge
import forge.dashspec as ds

_d = ds.Dashboard {id = "sys", title = "Sys", panels = [ds.Panel {id = "p", title = "P", kind = "line", signal = "logs", series = [ds.Series {label = "n", agg = "count"}]}]}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        telemetry = [forge.Telemetry {service_name = "sys", address = "127.0.0.1", dashboards = [_d, _d]}]
    }
})
