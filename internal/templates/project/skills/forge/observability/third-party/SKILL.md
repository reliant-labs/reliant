---
name: observability/third-party
description: Metrics, logs, traces, profiles, dashboards and alerts for systems forge did not build (an image-only workload, raw Manifests, a HostInfra engine, a Helm chart) with the one generic forge.Telemetry — where it attaches, each signal, forge.dashspec dashboards, the system dashboard and default alerts, and verifying with forge obs telemetry and forge lint --telemetry. Per-runtime collection and your own collector are in observability/third-party/runtimes.
---

# Telemetry for systems forge did not build

A workload forge builds reports itself: `pkg/observe` pushes traces, metrics and profiles, and its logs are JSON lines forge tails. A system you only *run* (Temporal, ClickHouse, NATS, a vendored chart) reports in its own way, on its own port, in its own format. `forge.Telemetry` is the one declaration for all of them. forge owns the mechanism (collector config, identity, validation, dashboards, alerts). **forge ships no vendor knowledge**: no preset for any system, no metric names, no env keys. Those facts live in *your* KCL, where they can follow the version you run. Every example below is a project's own declaration used as an illustration; none of it ships in forge, and its ports and names need checking against the version you deploy.

## The model

| Signal | Declared as | How it is collected |
|---|---|---|
| metrics | `metrics: [MetricsScrape]` | Prometheus exposition, pulled by a collector |
| traces (also pushed metrics, logs) | `otlp: OtlpPush` | the system pushes OTLP; forge tells it where, per runtime |
| logs | `logs: LogParse` | lines forge already collects, parsed by typed rules |
| profiles | `profiles: [PprofScrape]` | pprof pulled into the store `forge obs profile` reads (alpha) |
| reachability | `checks: [HealthCheck]` | tcp connect or http GET, for what has nothing to scrape |

Beside them: `dashboards` (typed `forge.dashspec`), `alerts` (`forge.AlertRule`), and for free a generated **system dashboard** and two **default alerts**. Every signal of a system is stamped `service.name = service_name`, plus the env's resource attributes when `scope = "project"`. `scope = "platform"` (a shared database, a bus, the telemetry pipeline itself) carries no env identity and is refused on a hosted env; scope grants no access.

## Where it attaches

Where it attaches decides what forge can infer.

| Attach to | Use for | forge infers | You must give |
|---|---|---|---|
| `Workload.telemetry` | an image-only `fw.Workload` | service name, cluster, namespace, pod selector; a port by **name** from the workload's `ports` | nothing more; `selector`, `namespaces`, `cluster`, `address` are refused |
| `HostInfra.telemetry` | an engine `forge env up` runs on the host | the instance; ports named `server`, `monitor` (`nats_monitor_port`), `ui` (`temporal_ui_port`), `metrics` (`temporal_metrics_port`) | for Temporal, `temporal_metrics_port` (passed as `--metrics-port`; unset, the server picks a free port) |
| `Manifests.telemetry` (a **list**) | raw objects: one group can hold several systems | group's cluster and namespace (`namespaces` overrides) | `selector` (pod labels, all must match) or `address`; **numeric** ports; a `service_name` when the group has no `name` or holds several systems |
| `Observability.telemetry` | what forge never declares as objects: a Helm chart, an operator, a system outside the bundle | nothing | `service_name`, a `selector` plus the `cluster` whose collector scrapes it, or an `address` (with no cluster, a host address `forge env up` reaches) |
| `ManagedDatabase.telemetry` | a `forge.ManagedDatabase`, **watched by default** with no line at all | everything: its instances (`cnpg.io/cluster`), ports `metrics` 9187 and `postgres` 5432, service `<name>-postgres` | nothing; set it only to replace the default (`observability/third-party/runtimes`, "A ManagedDatabase") |

`service_name` is unique per env: every signal is stamped with it. A workload on `forge.OnExternal` or `forge.BuildOnly` has nothing forge can reach: describe it under `Observability.telemetry`.

**`network_policy` is tri-state, on purpose.** A NetworkPolicy selecting pods no policy selected before flips them to default-deny for *all* other ingress, which would black-hole a raw system's traffic. So unset means auto: on a `Workload` or `HostInfra` the collector's ingress rule joins the policy forge already renders for that workload; on `Manifests`, `Observability` and a `ManagedDatabase` forge renders **none**, because it cannot know whether those pods are already policy-selected, and admitting the collector is yours. `network_policy = True` has forge render one (only the declared scrape, pprof and check ports); `False` never does. `forge env render` warns when a pulled raw target leaves it unset.

### An image-only workload (metrics, logs, alert)

```kcl
import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-hub", namespace = "apps"}
_TEMPORAL_ALLOW = "service_(requests|errors)|service_latency(_bucket|_sum|_count)|persistence_(requests|errors)"

_temporal = fw.Workload {
    name = "temporal"
    image = "docker.io/temporalio/server:1.26.2"      # a cluster image needs its registry host
    ports = [fw.Port {name = "frontend", port = 7233}, fw.Port {name = "metrics", port = 9090}]
    runtime = forge.OnCluster {target = _k3d}
    telemetry = forge.Telemetry {
        metrics = [forge.MetricsScrape {port = "metrics", allow = _TEMPORAL_ALLOW, drop_labels = ["workflowType"]}]
        logs = forge.LogParse {
            format = "json"
            timestamp = forge.LogTimestamp {field = "ts", layout = "rfc3339"}
            severity = forge.LogSeverity {field = "level"}
            body = "msg"
        }
        alerts = [forge.AlertRule {
            alert = "TemporalErrorsHigh"
            expr = "SELECT ServiceName AS service, sum(Value) AS value FROM otel_metrics_sum WHERE MetricName = 'service_errors' AND TimeUnix >= now() - INTERVAL 10 MINUTE GROUP BY service HAVING value > 100"
            "for" = "10m"
        }]
    }
}
```

### A host-run engine

```kcl
_host_temporal = forge.HostInfra {
    name = "temporal-dev"
    engine = "temporal"
    port = 7233
    temporal_metrics_port = 9233
    telemetry = forge.Telemetry {
        metrics = [forge.MetricsScrape {port = "metrics", allow = _TEMPORAL_ALLOW}]
        checks = [forge.HealthCheck {port = "server"}]
    }
}
```

### Raw manifests, and a chart outside the bundle

```kcl
_clickhouse = forge.Telemetry {                       # one entry of Manifests.telemetry
    service_name = "clickhouse"
    scope = "platform"
    selector = {"app.kubernetes.io/name": "clickhouse"}
    metrics = [forge.MetricsScrape {port = 9363, allow = "ClickHouseMetrics_(Query|PartsActive)|ClickHouseProfileEvents_.*"}]
    network_policy = True                              # admission is ours: render the policy
}
_cert_manager = forge.Telemetry {                      # one entry of Observability.telemetry
    service_name = "cert-manager"
    cluster = "k3d-hub"
    namespaces = ["cert-manager"]
    selector = {"app.kubernetes.io/name": "cert-manager"}
    metrics = [forge.MetricsScrape {port = 9402, allow = "certmanager_certificate_(ready_status|expiration_timestamp_seconds)"}]
}
# forge.Manifests {name = "obs", cluster = "k3d-hub", namespace = "obs", telemetry = [_clickhouse], objects = [...]}
# forge.Observability {telemetry = [_cert_manager]}
```

## Metrics

`MetricsScrape` is one Prometheus endpoint. It is **bounded by construction**: `sample_limit` (default 5000) makes an over-large scrape *fail* (`up = 0`, visible and alerted) instead of storing an unbounded series set, and `allow` keeps only the names you draw or alert on. No `allow` keeps everything, and a hosted env refuses that.

| Field | Meaning |
|---|---|
| `port`, `path` (`/metrics`), `params`, `scheme` | a number, or the name of a port the attached entity declares |
| `interval` (30s, floor 10s), `timeout` (10s, at most `interval`) | durations in ms, s, m, h |
| `allow`, `deny` | RE2, anchored, over **exposition** names: `allow` keeps (plus `up`), `deny` drops after it |
| `counters` | RE2 over names an exporter types as gauges that are really cumulative counters: they become monotonic sums, so `rate` works |
| `drop_labels` | labels removed at the scrape (per-user, per-id) |
| `sample_limit`, `label_limit`, `label_value_length_limit` | the ceilings (`sample_limit` 1-200000) |
| `bearer_token`, `tls` | credentials, below |
| `name` | tells several scrapes of one system apart; at most one may omit it |

Names land unchanged. A Prometheus histogram `x_bucket/_sum/_count` lands as **one** OTel histogram `x`: **allow the exposition names, draw the base name `x`**. A summary needs its bare name plus `_sum|_count`. Credentials are read from a *file* the collector mounts, over https only:

```kcl
metrics = [forge.MetricsScrape {
    port = 9363
    scheme = "https"
    allow = "ClickHouseMetrics_.*|ClickHouseProfileEvents_(Query|InsertedRows)"
    counters = "ClickHouseProfileEvents_.*"           # exposed as gauges, really counters
    sample_limit = 2000
    bearer_token = forge.SecretRef {name = "ch-metrics", key = "token"}
    tls = forge.ScrapeTLS {ca = forge.SecretRef {name = "ch-metrics", key = "ca.crt"}}   # not insecure_skip_verify
}]
```

On a cluster the `SecretRef` names a Secret in the *collector's* namespace; locally it resolves through the secret store (`runtimes`).

## Traces and pushed signals: `OtlpPush`

A workload already gets the standard `OTEL_EXPORTER_OTLP_*`, `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES`. `OtlpPush.env` adds the system's **own** key names, each a literal or an `OtlpRef` resolved per runtime (the values are endpoints, never credentials):

```kcl
_zitadel = fw.Workload {
    name = "zitadel", image = "ghcr.io/zitadel/zitadel:v4.19.5", ports = [fw.Port {name = "http", port = 8080}]
    runtime = forge.OnCluster {target = _k3d}
    telemetry = forge.Telemetry {
        otlp = forge.OtlpPush {
            signals = ["traces"]                       # traces, metrics, logs
            protocol = "grpc"                          # or "http/protobuf" (default): `endpoint` follows it
            sampler_ratio = 0.05                       # rendered into the standard sampler env
            env = {
                ZITADEL_INSTRUMENTATION_TRACE_EXPORTER_TYPE = "grpc"                                  # a literal
                ZITADEL_INSTRUMENTATION_TRACE_EXPORTER_ENDPOINT = forge.OtlpRef {key = "grpc_address"}   # a fact
            }
        }
    }
}
```

`OtlpRef.key` is one of `endpoint`, `traces_endpoint`, `metrics_endpoint`, `logs_endpoint`, `grpc_address` (host:port), `protocol`, `insecure`, `service_name`, `resource_attributes`, `traces_sampler`, `traces_sampler_arg`. A fact the runtime has no value for is **left out of the env** (the host has no gRPC fact; with observability off, every fact is absent; a hosted workload's are the platform's). `forge.otlp_env()` returns the standard four as refs, for a workload whose injection you replaced: `forge.otlp_env() | {OTEL_TRACES_SAMPLER = "parentbased_traceidratio"}`.

Raw `Manifests` get no env injection, since forge cannot see into their pods. Interpolate the transport facts into your objects with `forge.otlp_value(key, observability, cluster, protocol)`, e.g. `forge.otlp_value("grpc_address", _obs, "k3d-hub")` (empty `cluster`: the local collector). Only the transport facts (`endpoint`, `*_endpoint`, `grpc_address`, `protocol`, `insecure`) resolve there; write `service_name` and the sampler literally. `service_name` becomes `OTEL_SERVICE_NAME`.

## Logs: `LogParse`

forge already collects the lines (container logs on a cluster, `.forge/logs/<env>/<name>.log` locally) and does the generic stage (CRI unwrapping, JSON when a line starts with `{`). `LogParse` applies to **this system's lines only**, after that stage, and wins over it; `container` limits it to one container.

| Field | Meaning |
|---|---|
| `format` | `auto` (default), `json`, `logfmt`, `regex`, `text`. `regex` is set exactly when `format = "regex"`: RE2 with named groups, each becoming an attribute |
| `timestamp` | `LogTimestamp {field, layout}`: a strptime layout or `rfc3339`, `epoch_s`, `epoch_ms`, `epoch_ns` |
| `severity` | `LogSeverity {field, mapping}`: severity (`trace` `debug` `info` `warn` `error` `fatal`) to the field values that mean it. Unmapped values keep their text. Numeric levels map by their string form. (`mapping`, since `map` is a KCL keyword) |
| `body`, `trace_id`, `span_id` | the field that becomes the body; the trace context |
| `drop` | attributes removed after parsing |
| `rules` | `LogSeverityRule`, ordered, first match sets the severity: `body_match`, or `field` + `value_match`, optionally `from_severity`. Demote an expected ERROR, promote a quiet one |

Field paths are dotted into the parsed record (`level`, `record.error_severity`, `t.$date`); a key holding a dot is `["quoted"]`.

```kcl
_nats_logs = forge.LogParse {                         # NATS-style lines: [pid] 2026/11/20 12:00:00.000000 [INF] message
    format = "regex"
    regex = r"^\[(?P<pid>\d+)\] (?P<ts>\S+ \S+) \[(?P<lvl>[A-Z]{3})\] (?P<msg>.*)$"
    timestamp = forge.LogTimestamp {field = "ts", layout = "%Y/%m/%d %H:%M:%S.%f"}
    severity = forge.LogSeverity {field = "lvl", mapping = {debug = ["DBG", "TRC"], info = ["INF"], warn = ["WRN"], error = ["ERR"], fatal = ["FTL"]}}
    body = "msg"
    rules = [forge.LogSeverityRule {body_match = "slow consumer", severity = "warn", from_severity = "error"}]
}
```

## Profiles: `PprofScrape`

Pulls net/http/pprof-format profiles into the profiles store `forge obs profile` reads. The collector's `pprof` receiver is **alpha**, so forge sets the `service.profilesSupport` feature gate where it runs one. Fields: `port`, `path_prefix` (`/debug/pprof`; some systems root it elsewhere), `types` (`cpu`, `heap`, `allocs`, `goroutine`, `mutex`, `block`; default `cpu`, `heap`), `interval` (default 5m, floor 1m), `cpu_seconds` (default 5, 1-30), `scheme`, `bearer_token`, `tls`. A hosted env refuses profiles; a cluster with a destination needs `signals.profiles = True`. The system must expose the endpoint (often behind a flag):

`profiles = [forge.PprofScrape {port = 6060, types = ["heap"], interval = "10m"}]`

## Checks

`HealthCheck {port, kind = "tcp" | "http", path, scheme, interval (30s, floor 10s), name}`, for a system with nothing to scrape; `http` is healthy on a 2xx, and `path` and `scheme` are http-only. They feed the default `CheckFailing` alert and the system dashboard: `checks = [forge.HealthCheck {port = 3310}, forge.HealthCheck {name = "ready", kind = "http", port = 8080, path = "/readyz"}]`.

## Dashboards and alerts

`Telemetry.dashboards` takes typed dashboards: `import forge.dashspec as ds` (generated from the Go types, so a bad enum or unknown key fails at render; the cross-field rules run at generate). The helpers are shorthand for a series and each returns a schema you can `|`-merge: `ds.rate(label, metric, where=[])` (a counter), `ds.quantile(label, metric, q, metric_type="histogram", where=[])`, `ds.gauge(label, metric, agg="last", where=[])`, `ds.ratio(id, title, num, den, signal="metrics", unit="percent")` (a panel).

```kcl
import forge.dashspec as ds

_ch_dash = ds.Dashboard {
    id = "clickhouse-parts"                           # unique per env; the same in every env declaring it
    title = "ClickHouse parts"
    panels = [
        ds.Panel {id = "parts", title = "Active parts", kind = "line", signal = "metrics", series = [ds.gauge("parts", "ClickHouseMetrics_PartsActive")]}
        ds.Panel {id = "ins", title = "Inserted rows / s", kind = "line", signal = "metrics", series = [ds.rate("rows", "ClickHouseProfileEvents_InsertedRows")]}
        ds.Panel {id = "lat", title = "Request p95", kind = "line", signal = "metrics", series = [ds.quantile("p95", "service_latency", 0.95)]}   # histogram base name
    ]
}
# forge.Telemetry {..., dashboards = [_ch_dash]}
```

A panel's `field` is the **OTel base name**; the scrape's `allow` matches the **exposition names**: that mismatch is the usual cause of an empty panel, and `forge lint --telemetry` finds it. Each declared dashboard is written to `deploy/observability/dashboards/<project>-<id>_gen.json` and ships in the release bundle; a hand file `<project>-<id>.json` there replaces it (`observability/dashboards`).

**The system dashboard.** Every `Telemetry` also gets `<project>-telemetry-<service>_gen.json`, drawn from signal-generic facts only, one section per signal you declared: collection health (`up`, samples against `sample_limit`, scrape duration, per instance), CPU, memory and restarts (cluster, hosted), log volume by severity and recent errors, span rate, error ratio and p95 (OTLP traces), check status and latency, and a profiles note. It knows no vendor metric. Retitle or disable it with `dashboard = forge.SystemDashboard {title = "...", enabled = False}`; **override it** by committing `deploy/observability/dashboards/<project>-telemetry-<service>.json`.

**Alerts.** `Telemetry.alerts` are ordinary `forge.AlertRule`s (`observability/alerts`): a ClickHouse `SELECT` over `otel_metrics_sum`, `otel_metrics_gauge`, `otel_metrics_histogram`, `otel_logs` or `otel_traces`, where `MetricName` is the exposition name. They join the env's alerts labelled `service = service_name`, so the env's `Observability` must be on (render refuses otherwise); names are unique per env. Two warning-severity defaults come with every system, one rule each, named for the service (`cert-manager` gives `CertManagerTargetDown`):

| Default id | Rule | Fires when |
|---|---|---|
| `telemetry-target-down` | `<Service>TargetDown` | the newest `up` of a scraped endpoint is 0 for 5m |
| `telemetry-check-failing` | `<Service>CheckFailing` | a check has no success (tcp connect, http 2xx) for 5m |

Tune one for the whole env by id, `forge.Alerts {defaults = {"telemetry-target-down": forge.AlertDefault {"for" = "15m", labels = {severity = "critical"}}}}`; switch both off for one system with `default_alerts = False`. An env alert may not reuse a default's name.

## Verify

```bash
forge env render dev --json | jq .telemetry       # forge.env-render/v1: every target, normalized (ports resolved, defaults applied)
forge obs telemetry dev                           # each target, its signals, dashboards, and what has ARRIVED
forge obs telemetry dev --service temporal --since 15m --json
forge lint --telemetry                            # what the declarations collect, with the KCL file:line
```

`forge env render` refuses with **every** problem at once, naming target and field. `forge obs telemetry` (`forge.obs.telemetry/v1`) reads the env's store: per signal `arriving`, `absent` or `unknown` with the newest row, and per scraped endpoint its newest `up` and samples kept against `sample_limit`. A hosted env's targets list `unknown`; read them on the console's `telemetry-<service>` dashboard. An `up = 0` with samples near `sample_limit` means the limit tripped: narrow `allow` or `drop_labels` rather than raising it.

`forge lint --telemetry` runs in every `forge lint`, only for a project whose KCL declares a `Telemetry`, and warns before it gates:

| Rule | Severity | Finds |
|---|---|---|
| `telemetry-dashboard-metric-uncollected` | error (after the rollout window) | a panel series no scrape of that system keeps (`allow` minus `deny`; a histogram counts as `_bucket`, `_sum` or `_count`) |
| `telemetry-scrape-unbounded` | warning | a scrape with no `allow` |
| `telemetry-rate-on-gauge` | warning | `rate`/`increase` over a name no `counters` and no `_total`/`_count`/`_sum` suffix marks as a counter |
| `telemetry-high-cardinality-risk` | warning | a raised `sample_limit`, or no `allow`, with no `drop_labels` |

Suppress one with `# forge:lint-disable <rule>: <why>` on the line above (an error rule needs a reason). Where each runtime collects, the hosted limits and running your own collector: `observability/third-party/runtimes`.
