"""
Positive test — a dashboard declared with the generated dashspec schemas
(forge.dashspec) and its helpers: one panel of every kind, a histogram p95 and
a summary quantile among them. dashboard_json drops every unset optional, so
`dashboard` is exactly the JSON a dashboard file carries.

internal/codegen's TestDashspecKCLRoundTripsThroughParseDashboard renders this
file and requires dashspec.ParseDashboard to accept `dashboard`.
"""

import forge.dashspec as ds

_errors = ds.Predicate {field = "metric.attr.status", op = "eq", value = "error"}

_d = ds.Dashboard {
    id = "demo-telemetry"
    title = "Demo telemetry"
    scope = "platform"
    group = "platform"
    tags = ["telemetry"]
    defaultRange = "24h"
    sections = [ds.Section {id = "metrics", title = "Metrics"}]
    filters = [ds.Filter {id = "svc", label = "Service", field = "service.name", signal = "metrics"}]
    annotations = [ds.Annotation {source = "releases"}]
    panels = [
        ds.Panel {
            id = "rps", title = "Requests / s", kind = "line", signal = "metrics", section = "metrics"
            series = [ds.rate("rps", "demo.requests")]
            display = ds.Display {style = "area", stack = True, colors = {"rps" = "blue"}}
        }
        ds.Panel {
            id = "p95", title = "Latency p95", kind = "line", signal = "metrics", unit = "s"
            series = [ds.quantile("p95", "demo.request.duration", 0.95)]
        }
        ds.Panel {
            id = "summary-p99", title = "Summary p99", kind = "number", signal = "metrics", unit = "ms"
            series = [ds.quantile("p99", "demo.rpc.latency", 0.99, metric_type = "summary")]
            display = ds.Display {style = "gauge", max = 500}
        }
        ds.ratio("error-rate", "Error rate", ds.rate("errors", "demo.requests", where = [_errors]), ds.rate("all", "demo.requests"))
        ds.Panel {
            id = "queue", title = "Queue depth", kind = "table", signal = "metrics", groupBy = ["metric.attr.queue"]
            series = [ds.gauge("depth", "demo.queue.depth", agg = "max")]
        }
        ds.Panel {
            id = "top", title = "Top spans", kind = "bar", signal = "traces", groupBy = ["span.name"], limit = 5
            series = [ds.Series {label = "calls", agg = "count", metricType = ""}]
        }
        ds.Panel {
            id = "heat", title = "Latency heatmap", kind = "heatmap", signal = "traces"
            series = [ds.Series {label = "spans", agg = "count", field = "span.duration"}]
        }
        ds.Panel {id = "logs", title = "Errors", kind = "search", signal = "logs", columns = ["log.body"], where = [ds.Predicate {field = "log.severity", op = "eq", value = "error"}]}
        ds.Panel {id = "sql", title = "Spans", kind = "sql", sql = "SELECT count() FROM otel_traces"}
        ds.Panel {id = "issues", title = "Issues", kind = "issues", signal = "traces"}
        ds.Panel {id = "sessions", title = "Sessions", kind = "sessions", signal = "traces", hasError = True}
        ds.Panel {id = "funnel", title = "Sign-up", kind = "funnel", signal = "traces", funnel = ds.Funnel {steps = ["visit", "signup"], by = "user"}}
        ds.Panel {id = "pg", title = "Users", kind = "postgres", postgres = ds.PostgresQuery {database = "app", sql = "SELECT count(*) FROM users", draw = "number"}}
        ds.Panel {id = "notes", title = "Notes", kind = "text", markdown = "# What this dashboard is for"}
        ds.Panel {
            id = "browse", title = "Recent users", kind = "browse"
            browse = ds.BrowseView {
                database = "app", table = "public.users", size = 50
                filters = [ds.BrowseFilter {column = "plan", op = "eq", values = ["pro"]}]
                sort = [ds.BrowseSort {column = "created_at", desc = True}]
            }
        }
        ds.Panel {
            id = "attention", title = "Needs attention", kind = "attention"
            attention = ds.Attention {
                threshold = 0.1
                figures = [ds.Panel {
                    id = "spans-today", title = "Spans today", kind = "sql", sql = "SELECT count() AS spans FROM otel_traces"
                    display = ds.Display {good = "up"}
                }]
            }
        }
        ds.Panel {id = "health", title = "Release health", kind = "release_health"}
        ds.Panel {id = "promotions", title = "Promotions", kind = "promotions", limit = 20}
        ds.Panel {id = "dora", title = "Delivery", kind = "delivery"}
    ]
}

dashboard = ds.dashboard_json(_d)

_kinds = {p.kind: True for p in _d.panels}
assert_every_kind = all k in ds.PANEL_KIND_VALUES { k in _kinds }
assert_unset_optionals_dropped = "description" not in dashboard and "unit" not in dashboard.panels[0]
assert_rate_helper = dashboard.panels[0].series[0] == {"label": "rps", "agg": "rate", "field": "demo.requests", "metricType": "sum"}
assert_p95 = dashboard.panels[1].series[0].quantile == 0.95 and dashboard.panels[1].series[0].metricType == "histogram"
assert_summary = dashboard.panels[2].series[0].metricType == "summary"
assert_empty_optional_metric_type = dashboard.panels[5].series[0].metricType == ""
assert_ratio = dashboard.panels[3].ratio and len(dashboard.panels[3].series) == 2 and dashboard.panels[3].series[0].where[0].op == "eq"
assert_nested_figure = dashboard.panels[15].attention.figures[0].display == {"good": "up"}
assert_spec_version = ds.SPEC_VERSION == "forge.dashboards/v1"

# A helper's result takes the fields it has no parameter for with a `|` merge.
_graded = ds.rate("rps", "demo.requests") | {thresholds = ds.Thresholds {good = 1, poor = 5}}
assert_merge = ds.series_json(_graded).thresholds == {"good": 1, "poor": 5} and _graded.agg == "rate"
