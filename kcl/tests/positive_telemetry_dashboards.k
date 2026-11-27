"""
Positive test — a Telemetry's typed dashboards (forge.dashspec) render into
its target as forge.dashboards/v1 objects, exactly the JSON a dashboard file
carries; a Telemetry that only draws (dashboards, no signal) is accepted; and
a Telemetry with none renders an empty list.
"""

import forge
import forge.dashspec as ds

_parts = ds.Dashboard {
    id = "clickhouse-parts"
    title = "ClickHouse parts"
    panels = [ds.Panel {
        id = "parts", title = "Active parts", kind = "line", signal = "metrics"
        series = [ds.gauge("parts", "ClickHouseMetrics_PartsActive")]
    }]
}

_out = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        telemetry = [
            forge.Telemetry {service_name = "clickhouse", scope = "platform", address = "127.0.0.1", dashboards = [_parts]}
            forge.Telemetry {service_name = "devbox", address = "127.0.0.1", checks = [forge.HealthCheck {port = 22}]}
        ]
    }
})

_t = {t.service_name: t for t in _out.telemetry}

assert_dashboards_rendered = _t.clickhouse.dashboards == [ds.dashboard_json(_parts)]
assert_dashboard_shape = _t.clickhouse.dashboards[0].panels[0].series[0] == {"label": "parts", "agg": "last", "field": "ClickHouseMetrics_PartsActive", "metricType": "gauge"}
assert_no_scope_in_kcl = "scope" not in _t.clickhouse.dashboards[0]
assert_none_is_empty = _t.devbox.dashboards == []
