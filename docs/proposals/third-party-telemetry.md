# Proposal: declared telemetry for workloads forge did not build (`forge.Telemetry`)

**Status:** accepted in shape by the owner (2026-11-20): ONE generic primitive, four signals, no
per-system bundles. Being implemented in the phased PRs of §5; F1 (the schema, the render and
`pkg/telemetry`) is the first to land. Where an implementation PR diverged from this text, the
"Implementation notes" section at the end says so.
**Date:** 2026-11-20
**Scope:**

- forge: KCL schema, render, `pkg/telemetry`, the cluster agent, the local stack, `obsdash`,
  `pkg/release`, lint, and the skill.
- control-plane: the pin, the hosted tenant scrape, migrating selfmon, and per-system
  declarations.

**Baseline:** forge main at `d372f9b11`.

## Summary

Owner decision (2026-11-20, binding): ONE generic, configurable forge primitive.

- Any project uses it to declare, in its KCL manifest, telemetry for workloads forge did not
  build. That covers image-only `forge.Workload`, raw `forge.Manifests`, `forge.HostInfra`
  engines, and anything else on a cluster.
- It covers all four signals (metrics, logs, traces, profiles), plus dashboards and alerts over
  them.
- forge ships **no per-system bundle, preset or vendor catalog**. Every vendor fact (Temporal
  metric names, Zitadel env keys, ClickHouse panels) lives in the project's KCL.

The design:

1. **`forge.Telemetry`**, one schema with four attachment points:

   - `Workload.telemetry`
   - `HostInfra.telemetry`
   - `Manifests.telemetry` (a list, because one group can hold several systems)
   - `Observability.telemetry`, for things forge does not declare as objects: Helm charts,
     operators, and systems outside the bundle.

   It holds `metrics` (Prometheus pull), `otlp` (push, including env remapping for non-standard
   keys), `logs` (typed parse rules), `profiles` (pprof pull), `checks` (tcp/http), `dashboards`
   (typed dashspec), `alerts` (`forge.AlertRule`), and an auto-generated, signal-generic
   **system dashboard**.

2. **One normalized contract, `telemetry.Target`.** It is a Go type in `pkg/telemetry`. Render
   resolves every declaration into it, writes it into `forge env render --json` output, and
   (for hosted workloads) into `Shape.Telemetry` behind `BundleFeatureTelemetry`
   (`shape-telemetry`, the control plane DECODES it) and `CapabilityCollectsTelemetry`
   (`collects-telemetry`, it COLLECTS it). A control plane advertises `BundleDocFeatures()`
   verbatim on every forge pin bump, so the decode feature alone would claim collection;
   `collects-telemetry` is not in that list, and a control plane adds it by hand, in the change
   that makes it collect. Forge sends the targets only to a control plane that names both, and
   otherwise strips them and warns, naming the targets and the missing capability.
3. **One translator, `pkg/telemetry/collector`:** `Target` → OpenTelemetry Collector config
   fragments. It has four consumers:

   - forge's cluster agent (`pkg/deploy/observability.go`)
   - the local stack (a new host-side telemetry agent, plus a data-driven include in the
     ClickStack collector)
   - control-plane's hosted operator (Go, importing forge/pkg)
   - projects that run their own collector in KCL (control-plane's platform agent), through a
     new `kcl_plugin.forge.telemetry_collector` bridge function. That is the same pattern as
     `resolve_port` and `derive_jwk`.

   No second renderer exists anywhere.

4. **Dashboards are typed KCL.** `kcl/dashspec/dashspec_gen.k` is generated from `pkg/dashspec`
   Go types, following the `kcl/tiers/tiers_gen.k` precedent with a currency test, so it cannot
   drift. Cross-field rules still run in `dashspec.ParseDashboard` at generate. Hand JSON files
   keep working unchanged as the escape hatch.
5. **Every declared system gets a dashboard and a target-down alert by default.** They are built
   from signal-generic facts only: scrape health, resources, log volume and errors, span
   rate/error/latency, check status, and a profile link. They need no vendor knowledge.

control-plane then:

- declares `Telemetry` for every third-party system it runs (§6), in one file per system under
  `deploy/kcl/lib/telemetry/`;
- moves `ScrapeJob` / `self_jobs` / `platform_scrape_jobs` onto it, so there is one system;
- adds the hosted tenant scrape, so customers' image-only workloads get the same collection.

---

## 0. Verification of the open items

| Item                                                               | Result                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Evidence                                                                                                                                                                                                                                             |
| ------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| (a) contrib 0.162.0 has `pprof`, `receivercreator`, `k8s_observer` | **Yes, all three.** It also has `prometheusreceiver`, `zipkinreceiver`, `jaegerreceiver`, `tcpcheckreceiver`, `httpcheckreceiver`, `sqlqueryreceiver` and `googlecloudmonitoringreceiver`. pprof is **alpha (profiles)**, with modes `remote` (a full `confighttp.ClientConfig`, so bearer and TLS are available), `file`, `self` and `server`. It has no service discovery of its own. receivercreator is **beta for metrics, alpha for logs/traces/profiles**. `transformprocessor` has group `conditions`. OTTL has `ParseJSON`, `ParseKeyValue`, `ExtractPatterns` and `Time`. | `opentelemetry-collector-releases@v0.162.0/distributions/otelcol-contrib/manifest.yaml` lines 39, 42, 176-245; `receiver/pprofreceiver/{README.md,config.go,metadata.yaml}@v0.162.0`; `receiver/receivercreator/{factory.go,metadata.yaml}@v0.162.0` |
| (b) does cp prod use forge's Go agent?                             | **No.** cp declares no `Observability.destination` (no `destination =` anywhere in `deploy/kcl` except an unrelated seaweed lambda), so forge renders no `ObservabilityAgent`. cp runs its own KCL agent (`lib/otel_agent.k`) as `forge.Manifests` groups `otel-agent-hub` / `otel-agent-daemon` (`lib/obs_prod.k:477-506`).                                                                                                                                                                                                                                                       | see the next row                                                                                                                                                                                                                                     |
| (b) how cp renders hub third-party infra                           | Three paths:                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |                                                                                                                                                                                                                                                      |
|                                                                    | 1. temporal, temporal-ui, litellm, nats, zitadel and pgbouncer are `infra.release_contracts(...)` + `pgb.release_contracts(...)` rows, wrapped as `forge.Manifests{name, cluster, release_inputs}` (`prod/main.k:1213-1218`), with the objects themselves in `_extra_manifests` (`prod/main.k:~2089`).                                                                                                                                                                                                                                                                             |                                                                                                                                                                                                                                                      |
|                                                                    | 2. The obs stack (clickhouse, alertmanager, hyperdx/mongo, gateway, agents, nats-exporter) is `obsprod.manifests(...)`, also `forge.Manifests`.                                                                                                                                                                                                                                                                                                                                                                                                                                    |                                                                                                                                                                                                                                                      |
|                                                                    | 3. envoy-gateway and cert-manager are `forge.HelmChart` (`lib/platform.k`). openbao and ESO are typed forge entities (`Bundle.openbao`, `external_secrets_operators`).                                                                                                                                                                                                                                                                                                                                                                                                             |                                                                                                                                                                                                                                                      |
|                                                                    | **Consequence:** cp attaches telemetry with `Manifests.telemetry` and `Observability.telemetry`, and consumes the normalized targets in its own agent through the KCL plugin. forge's agent stays unrendered for cp.                                                                                                                                                                                                                                                                                                                                                               |                                                                                                                                                                                                                                                      |
| (c) host-infra logs collected locally?                             | **No.** The supervisor writes `<dataDir>/<name>.log`, i.e. `.forge/hostinfra/<name>/<name>.log` (`internal/hostinfra/supervised.go:361`, `zitadel.go:256`, `hostinfra.go:322`). ClickStack mounts only `./.forge/logs:/var/log/forge` (`docker-compose.yml.tmpl:524`). So temporal, nats and zitadel logs never reach the local store, and they also break the "one log directory" rule. This is fixed in F0, independently of the primitive.                                                                                                                                      |                                                                                                                                                                                                                                                      |
| (d) dashspec p95-from-histogram and summary quantiles              | **Both are already representable.** No dashspec change is needed.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | see the three rows below                                                                                                                                                                                                                             |
|                                                                    | `{agg: "quantile", quantile: 0.95, field: "service_latency", metricType: "histogram"}` compiles to bucket interpolation (`pkg/obsquery/metrics.go:~611`); exp_histogram goes through `metrics_exp.go`.                                                                                                                                                                                                                                                                                                                                                                             |                                                                                                                                                                                                                                                      |
|                                                                    | `metricType: "summary"` + `agg: quantile` reads the stored `ValueAtQuantiles` at exactly that level (`summaryQuantileSQL`, `metrics_exp.go:95-106`), and reports `nmissing` when a series does not store the level. Summaries cannot be merged across pods, so group by `resource.attr.service.instance.id`.                                                                                                                                                                                                                                                                       |                                                                                                                                                                                                                                                      |
|                                                                    | One subtlety the lint must model: the panel `field` is the **OTel base name** (`service_latency`), while the Prometheus allowlist must match the **exposition names** (`service_latency_bucket\|_sum\|_count`; for a summary, the bare name plus `_sum`/`_count`).                                                                                                                                                                                                                                                                                                                 |                                                                                                                                                                                                                                                      |

Also confirmed:

- `forge.StoreRef` / `forge.SecretRef{store_key}` exist (`kcl/schema.k:2272`, `kcl/workload.k:72`).
- The tiers codegen precedent exists (`kcl/tiers/tiers_gen.k`, `TestTierKCLIsCurrent`).
- The KCL plugin bridge exists (`internal/kclrender/kclrender.go:259`, `kcl_plugin.forge.*`, imported in the user's file so that the forge module still renders under a plain `kcl run`).

---

## 1. KCL schema (complete)

New file `kcl/telemetry.k`, in package `forge` (same directory, same package). Fields added
elsewhere:

```kcl
# kcl/workload.k       schema Workload:       telemetry?: Telemetry
# kcl/schema.k         schema HostInfra:      telemetry?: Telemetry
#                                             temporal_metrics_port?: int   # passed as --metrics-port
# kcl/schema.k         schema Manifests:      telemetry: [Telemetry] = []
# kcl/schema.k         schema Observability:  telemetry: [Telemetry] = []
#                                             telemetry_collector?: TelemetryCollector
```

Every field is optional with a working default, so the change is semver-safe and old
declarations render byte-identically (proved by a render-diff test in F1).

```kcl
import regex
import .dashspec   # kcl/dashspec/dashspec_gen.k (F2), generated from pkg/dashspec

_TEL_NAME_RE: str = r"^[a-z0-9]([-a-z0-9_.]*[a-z0-9])?$"
# Collector durations: what Go's time.ParseDuration reads, restricted to ms/s/m/h.
_TEL_DURATION_RE: str = r"^([0-9]+(ms|s|m|h))+$"
_TEL_PROM_LABEL_RE: str = r"^[a-zA-Z_][a-zA-Z0-9_]*$"
# A field path into a parsed log record: dotted segments; a segment that holds a
# dot (or any character outside [A-Za-z0-9_$@-]) is written ["quoted"].
# Examples: "level", "@level", "record.error_severity", "t.$date", "[\"k8s.pod\"]".
_TEL_PATH_RE: str = r"^([A-Za-z0-9_$@-]+|\[\"[^\"]+\"\])(\.([A-Za-z0-9_$@-]+|\[\"[^\"]+\"\]))*$"
TELEMETRY_SEVERITIES: [str] = ["trace", "debug", "info", "warn", "error", "fatal"]
# The facts an OtlpRef can name. Values are resolved per runtime at render.
OTLP_REF_KEYS: [str] = [
    "endpoint", "traces_endpoint", "metrics_endpoint", "logs_endpoint",
    "grpc_address", "protocol", "insecure", "service_name",
    "resource_attributes", "traces_sampler", "traces_sampler_arg",
]
PPROF_TYPES: [str] = ["cpu", "heap", "allocs", "goroutine", "mutex", "block"]

_tel_secs = lambda d: str -> float {
    sum([float(p[:-2]) / 1000.0 if p.endswith("ms") else float(p[:-1]) * {"s": 1, "m": 60, "h": 3600}[p[-1:]] \
         for p in regex.findall(d, r"[0-9]+(?:ms|s|m|h)")])
}

schema Telemetry:
    """Telemetry for a system forge did not build: how its metrics, logs,
    traces and profiles are collected, and the dashboards and alerts drawn
    over them. Every vendor fact (metric names, env keys, panels) is yours;
    forge knows none of them and ships no presets.

        forge.Workload {
            name = "temporal", image = "temporalio/server:1.26.2"
            ports = [{name = "metrics", port = 9090}]
            telemetry = forge.Telemetry {
                metrics = [forge.MetricsScrape {port = "metrics", allow = TEMPORAL_ALLOW}]
                logs = forge.LogParse {format = "json", timestamp = forge.LogTimestamp {field = "ts", layout = "epoch_s"}}
                dashboards = [_temporal_dashboard]
                alerts = [_temporal_errors]
            }
        }

    Where it attaches decides what forge can infer:
      * `Workload.telemetry` and `HostInfra.telemetry`: the target IS that
        workload. service_name, cluster, namespace and the pod selector come
        from it, and a named `port` must be one of the workload's ports.
        `selector`, `namespaces`, `cluster` and `address` are refused.
      * `Manifests.telemetry`: forge cannot see into raw objects, so
        `selector` is required. `namespaces` defaults to [Manifests.namespace],
        and the cluster is the group's.
      * `Observability.telemetry`: for what forge never declares as objects
        (a HelmChart, an operator, a system outside this bundle). `selector`
        (or `address`) and `cluster` are required on cluster envs.

    Identity: every signal collected from a matching pod or address (scraped
    metrics, its container logs, node CPU/memory, pulled profiles) is stamped
    `service.name = service_name`, plus the env's resource attributes when
    `scope = "project"`. Dashboards and alerts filter on that.

    Collection follows the env's runtime: forge's node agent on clusters it
    renders an agent for, the local telemetry agent and ClickStack under
    `forge env up`, and the platform on `forge.OnHosted` (when the control
    plane advertises it). A project that runs its own collector names it in
    `Observability.telemetry_collector`, so forge admits that collector in the
    NetworkPolicy it renders, and it renders the same targets with
    `kcl_plugin.forge.telemetry_collector` (see the observability/third-party
    skill).
    """
    # The service.name every signal of this system carries. Defaults to the
    # Workload/HostInfra/Manifests name; required on Observability.telemetry.
    # Unique within the env.
    service_name?: str
    # What the data is. "project": stamped with the env's identity, drawn in
    # project-scoped dashboards. "platform": no env identity (a shared
    # database, a bus, the telemetry pipeline itself), so its dashboards are
    # scope "platform". It grants no access.
    scope: "project" | "platform" = "project"
    # Observability.telemetry only: the cluster whose collector scrapes it.
    cluster?: str
    # Manifests/Observability only: exact namespace names to discover pods in.
    namespaces?: [str]
    # Manifests/Observability only: pod labels that must ALL match (exact).
    selector?: {str:str}
    # Instead of a selector: one fixed host (a Service DNS name, or a host
    # address under env up). Ports come from each scrape/check.
    address?: str
    # The container whose logs `logs` parses (default: every container of a
    # matching pod).
    container?: str
    # Its own pipeline: own receiver, own bounded in-memory queue, which drops
    # on overflow and never back-pressures (or shares a queue with) the
    # workload pipeline. For monitoring the telemetry pipeline itself, where a
    # durable queue would wedge the thing being watched.
    isolated: bool = False
    metrics: [MetricsScrape] = []
    otlp?: OtlpPush
    logs?: LogParse
    profiles: [PprofScrape] = []
    checks: [HealthCheck] = []
    # Whether forge admits the collector with a NetworkPolicy, on ONLY the
    # declared scrape/pprof/check ports, into the matching pods. Unset is
    # "auto". A NetworkPolicy that selects pods no policy selected before
    # flips them to default-deny for ALL other ingress, which would
    # black-hole a raw system's real traffic, so auto renders one only where
    # forge already owns the pods' policy:
    #   * unset on Workload/HostInfra: the collector's ingress rule joins the
    #     policy forge already renders for that workload (it exists either way).
    #   * unset on Manifests/Observability: NO policy. forge cannot know
    #     whether those pods are already policy-selected, so admission is
    #     yours; set True to have forge render it.
    #   * True: render it. False: never.
    network_policy?: bool
    # The signal-generic system dashboard forge draws for every Telemetry.
    dashboard: SystemDashboard = SystemDashboard {}
    # Your dashboards for this system (forge.dashboards/v1, typed). Each `id`
    # is unique within the project; a hand file of the same name replaces it.
    dashboards: [dashspec.Dashboard] = []
    # Your alert rules. They join Observability.alerts with labels.service =
    # service_name unless they set it.
    alerts: [AlertRule] = []
    # <Service>TargetDown (each scrape's `up` is 0 for 5m) and
    # <Service>CheckFailing (each check fails for 5m), at severity warning.
    default_alerts: bool = True

    check:
        not service_name or regex.match(service_name, _TEL_NAME_RE), "Telemetry.service_name must match ${_TEL_NAME_RE}"
        not (selector and address), "Telemetry: selector and address are exclusive"
        not selector or all k, v in selector { k and v }, "Telemetry.selector keys and values must be non-empty"
        not namespaces or all n in namespaces { regex.match(n, r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$") }, "Telemetry.namespaces must be RFC-1123 labels"
        metrics or otlp or logs or profiles or checks or dashboards or alerts, "Telemetry declares nothing to collect or draw"
        len([m.name for m in metrics if m.name]) == len({m.name: 1 for m in metrics if m.name}), "Telemetry.metrics names must be unique"

schema MetricsScrape:
    """One Prometheus-exposition endpoint, pulled by the collector.

    Bounded by construction: `sample_limit` fails the scrape (up = 0, visible
    on the system dashboard and alerted) rather than storing an unbounded
    series set, and `allow` keeps only the names you draw or alert on. A
    scrape without `allow` keeps every name; `forge lint` warns
    (telemetry-scrape-unbounded), and a hosted env refuses it.

    Names land unchanged (`service_requests`, `ClickHouseMetrics_PartsActive`).
    A Prometheus histogram `x_bucket/_sum/_count` lands as ONE OTel histogram
    `x`: allow the exposition names, draw the base name.
    """
    # Distinguishes several scrapes of one system (job name suffix).
    name?: str
    # A container port number, or the NAME of one of the workload's ports.
    port: int | str
    path: str = "/metrics"
    params: {str:[str]} = {}
    scheme: "http" | "https" = "http"
    interval: str = "30s"
    timeout: str = "10s"
    # RE2, anchored, over exposition names: the names kept (plus `up`).
    allow?: str
    # RE2, anchored: names dropped after `allow`.
    deny?: str
    # RE2, anchored: names the exporter types as gauges that are really
    # cumulative counters. They are converted to monotonic sums, so rate/increase work.
    counters?: str
    # Labels removed at the scrape: the per-user/per-key/per-id ones that explode.
    drop_labels: [str] = []
    sample_limit: int = 5000
    label_limit: int = 30
    label_value_length_limit: int = 256
    # Authorization: Bearer, read from a FILE the collector mounts, never an
    # env var. On a cluster, `name` is a Secret in the COLLECTOR's namespace.
    # `store_key` makes it portable: the value under that key in the env's
    # secret store, rendered into that Secret, written to a 0600 file under
    # env up, and a ManagedSecret on hosted.
    bearer_token?: SecretRef
    tls?: ScrapeTLS

    check:
        typeof(port) == "str" or 0 < port <= 65535, "MetricsScrape.port must be 1-65535 or a port name"
        typeof(port) == "int" or regex.match(port, r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"), "MetricsScrape.port name must be an IANA port name"
        path.startswith("/"), "MetricsScrape.path must start with /"
        regex.match(interval, _TEL_DURATION_RE) and regex.match(timeout, _TEL_DURATION_RE), "MetricsScrape interval/timeout: e.g. 30s, 1m"
        _tel_secs(interval) >= 10, "MetricsScrape.interval floor is 10s"
        _tel_secs(timeout) <= _tel_secs(interval), "MetricsScrape.timeout must not exceed interval"
        1 <= sample_limit <= 200000, "MetricsScrape.sample_limit must be 1-200000"
        all l in drop_labels { regex.match(l, _TEL_PROM_LABEL_RE) }, "MetricsScrape.drop_labels must be Prometheus label names"
        not tls or scheme == "https", "MetricsScrape.tls needs scheme https"
        not bearer_token or scheme == "https", "MetricsScrape.bearer_token is sent only over https"

schema ScrapeTLS:
    """TLS for an https scrape or pull. Prefer `ca` over skipping verification."""
    insecure_skip_verify: bool = False
    # A CA bundle, mounted from a Secret the same way as bearer_token.
    ca?: SecretRef
    server_name?: str
    check:
        not (insecure_skip_verify and ca), "ScrapeTLS: ca and insecure_skip_verify are exclusive"

schema OtlpPush:
    """The system pushes OTLP itself: what it sends, and how it is told
    where to send it.

    A Workload whose runtime runs a process already gets the standard
    OTEL_EXPORTER_OTLP_* / OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES env.
    `env` adds the system's OWN key names, each bound to an OTLP fact
    resolved per runtime. The values are endpoints, never credentials:

        otlp = forge.OtlpPush {
            signals = ["traces"]
            env = {
                ZITADEL_INSTRUMENTATION_TRACE_EXPORTER_TYPE = "grpc"   # a literal
                ZITADEL_INSTRUMENTATION_TRACE_EXPORTER_ENDPOINT = forge.OtlpRef {key = "grpc_address"}
            }
        }

    Raw Manifests get no env injection (forge cannot see their pods): use
    `forge.otlp_value(key, observability, cluster)` to interpolate the same
    facts into your objects.
    """
    # Which signals it pushes. It draws those sections of the system dashboard
    # and arms <Service>TelemetryStale.
    signals: [str] = ["traces"]
    protocol: "grpc" | "http/protobuf" = "http/protobuf"
    # Head-sampling ratio, rendered into the standard sampler env.
    sampler_ratio?: float
    env: {str: str | OtlpRef} = {}
    check:
        all s in signals { s in ["traces", "metrics", "logs"] }, "OtlpPush.signals: traces, metrics, logs"
        not sampler_ratio or 0 < sampler_ratio <= 1, "OtlpPush.sampler_ratio must be in (0, 1]"
        all k, _ in env { regex.match(k, r"^[A-Za-z_][A-Za-z0-9_]*$") }, "OtlpPush.env keys must be env var names"

schema OtlpRef:
    """An OTLP fact resolved per runtime: `key` is one of OTLP_REF_KEYS.
    endpoint is a URL with a scheme; grpc_address is host:port; insecure is "true" or "false"."""
    key: str
    check:
        key in OTLP_REF_KEYS, "OtlpRef.key must be one of ${OTLP_REF_KEYS}"

otlp_env = lambda -> {str: OtlpRef} {
    """The standard OpenTelemetry SDK env as OtlpRefs, for a raw object or a
    workload whose standard injection you replaced:

        env = forge.otlp_env() | {OTEL_TRACES_SAMPLER = "parentbased_traceidratio"}
    """
    {
        OTEL_EXPORTER_OTLP_ENDPOINT = OtlpRef {key = "endpoint"}
        OTEL_EXPORTER_OTLP_PROTOCOL = OtlpRef {key = "protocol"}
        OTEL_SERVICE_NAME = OtlpRef {key = "service_name"}
        OTEL_RESOURCE_ATTRIBUTES = OtlpRef {key = "resource_attributes"}
    }
}
# otlp_value(key, observability, cluster) -> str: the literal for a cluster
# runtime (the forge agent's Service, or telemetry_collector.otlp_endpoint).
# Defined in render.k beside _obs_env, so the facts have one source.

schema LogParse:
    """How this system's log lines become records: format, timestamp,
    severity, body, trace context. It applies to this system's lines only,
    after forge's generic stage (CRI unwrapping, JSON when the line starts
    with `{`), and it wins over that stage. Unset, lines keep the generic
    treatment.

    Field paths are dotted into the parsed record: "level", "@level",
    "record.error_severity", "t.$date"; a key holding a dot is ["quoted"].

        logs = forge.LogParse {                                # NATS
            format = "regex"
            regex = r"^\[(?P<pid>\d+)\] (?P<ts>\S+ \S+) \[(?P<lvl>[A-Z]{3})\] (?P<msg>.*)$"
            timestamp = forge.LogTimestamp {field = "ts", layout = "%Y/%m/%d %H:%M:%S.%f"}
            severity = forge.LogSeverity {field = "lvl", map = {debug = ["DBG", "TRC"], info = ["INF"], warn = ["WRN"], error = ["ERR"], fatal = ["FTL"]}}
            body = "msg"
        }
    """
    format: "auto" | "json" | "logfmt" | "regex" | "text" = "auto"
    # format "regex": RE2 with named groups; each group becomes an attribute.
    regex?: str
    timestamp?: LogTimestamp
    severity?: LogSeverity
    # The field that becomes the body (the rest stay attributes).
    body?: str
    trace_id?: str
    span_id?: str
    # Attribute fields removed after parsing (bulky, or high-cardinality).
    drop: [str] = []
    # Ordered; the first match sets the severity. Generalizes "this ERROR is
    # expected": demote or promote by body or field.
    rules: [LogSeverityRule] = []
    check:
        (format == "regex") == bool(regex), "LogParse.regex is set exactly when format is regex"
        all p in ([body, trace_id, span_id] + drop) { not p or regex.match(p, _TEL_PATH_RE) }, "LogParse field paths: see the docstring"

schema LogTimestamp:
    field: str
    # A strptime layout, or "rfc3339", "epoch_s", "epoch_ms", "epoch_ns".
    layout: str = "rfc3339"
    check:
        regex.match(field, _TEL_PATH_RE), "LogTimestamp.field is a field path"

schema LogSeverity:
    """Which field holds the level, and how its values map to OTel severities.
    Unmapped values keep their text and get severity number unspecified.
    Numeric levels map by their string form ("0" = ["info"] for klog's v)."""
    field: str
    map: {str: [str]} = {}
    check:
        regex.match(field, _TEL_PATH_RE), "LogSeverity.field is a field path"
        all k, _ in map { k in TELEMETRY_SEVERITIES }, "LogSeverity.map keys: ${TELEMETRY_SEVERITIES}"

schema LogSeverityRule:
    """Set `severity` on a record whose body matches `body_match`, or whose
    `field` matches `value_match` (both RE2; when both are set, both must hold)."""
    body_match?: str
    field?: str
    value_match?: str
    # Only records currently at this severity (e.g. demote error to warn).
    from_severity?: str
    severity: str
    check:
        severity in TELEMETRY_SEVERITIES, "LogSeverityRule.severity: ${TELEMETRY_SEVERITIES}"
        not from_severity or from_severity in TELEMETRY_SEVERITIES, "LogSeverityRule.from_severity: ${TELEMETRY_SEVERITIES}"
        body_match or (field and value_match), "LogSeverityRule needs body_match, or field + value_match"

schema PprofScrape:
    """Pull net/http/pprof-format profiles into the env's profiles store,
    the same table `forge obs profile` reads. A CPU profile is a
    `cpu_seconds` capture every `interval`; the other types are snapshots.
    Cadence defaults match forge's own runtime (PROFILES.md)."""
    port: int | str
    # The pprof root: /debug/pprof for net/http/pprof, /v1/sys/pprof for OpenBao.
    path_prefix: str = "/debug/pprof"
    types: [str] = ["cpu", "heap"]
    interval: str = "5m"
    cpu_seconds: int = 5
    scheme: "http" | "https" = "http"
    bearer_token?: SecretRef
    tls?: ScrapeTLS
    check:
        all t in types { t in PPROF_TYPES }, "PprofScrape.types: ${PPROF_TYPES}"
        regex.match(interval, _TEL_DURATION_RE) and _tel_secs(interval) >= 60, "PprofScrape.interval floor is 1m"
        1 <= cpu_seconds <= 30, "PprofScrape.cpu_seconds must be 1-30"
        typeof(port) == "str" or 0 < port <= 65535, "PprofScrape.port must be 1-65535 or a port name"
        not bearer_token or scheme == "https", "PprofScrape.bearer_token is sent only over https"

schema HealthCheck:
    """Reachability when there is nothing to scrape: tcp connect or an http GET (2xx is healthy)."""
    name?: str
    kind: "tcp" | "http" = "tcp"
    port: int | str
    path: str = "/"
    scheme: "http" | "https" = "http"
    interval: str = "30s"
    check:
        kind == "http" or path == "/", "HealthCheck.path is http-only"

schema SystemDashboard:
    """The signal-generic dashboard forge draws for every Telemetry: one
    section per signal declared. Collection health (up, samples vs
    sample_limit, scrape duration), CPU, memory and restarts, log volume by
    severity, recent error lines, span rate, error ratio and p95 by span name,
    check status, and a profile link. It knows no vendor metric. Your
    `dashboards` go beside it. A hand file
    `<project>-telemetry-<service>.json` replaces it."""
    enabled: bool = True
    title?: str

schema TelemetryCollector:
    """The collector that scrapes this env's Telemetry, when it is not
    forge's agent: the namespace and pod labels its NetworkPolicy admits,
    and the OTLP endpoints otlp_value() returns."""
    namespace: str
    pod_labels: {str:str}
    otlp_http_endpoint?: str
    otlp_grpc_address?: str
```

Render-time (Go) validation runs after normalization, because KCL cannot compile RE2:

- every regex compiles under RE2;
- `service_name` is unique per env;
- a named port exists on the workload;
- a `Manifests` or `Observability` target has a selector or address;
- `cluster` resolves;
- a hosted target sets `allow`, no `address`, and only ports the workload declares.

---

## 2. Dashboards: the choice

**Chosen: a typed KCL mirror of dashspec, generated from `pkg/dashspec`'s Go types**
(`kcl/dashspec/dashspec_gen.k`). It follows the `kcl/tiers/tiers_gen.k` precedent:

- closed schemas, JSON field names (`metricType`, `groupBy`), per-field enum checks;
- the header says "GENERATED by forge from pkg/dashspec — DO NOT EDIT";
- `TestDashspecKCLIsCurrent` fails when they drift.

The declarations serialize directly to `forge.dashboards/v1`. `obsdash` runs each through
`dashspec.ParseDashboard`, the same closed-world validator a release cut runs, for the
cross-field rules (quantile needs a histogram, no rate on a gauge, and so on).

Why this over the alternatives:

- **Single source of names.** Panel `field`s and the scrape `allow` regex can be built from
  the same KCL list, which is what makes the coverage lint (§4) cheap and the drift impossible.
  Today cp keeps `CLICKHOUSE_METRICS` in `selfmon.k` and the names again in
  `selfmon-clickhouse.json`.
- **Author-time errors** for unknown keys and bad enums. A dict would fail only at generate. A
  file reference cannot share constants.
- It **cannot drift** from the Go types (the generated mirror plus its currency test).

Hand JSON files (`deploy/observability/dashboards/<name>.json`) keep working exactly as today
and still override generated ones of the same name.

forge adds a few **signal-generic** KCL helpers next to the mirror. They are not vendor
knowledge, only shorthand for the common series:

- `forge.dash_rate(label, metric, where=[])`
- `forge.dash_quantile(label, metric, q, metric_type="histogram")`
- `forge.dash_gauge(label, metric, agg="last")`
- `forge.dash_ratio(title, num, den)`

**Output:**

- A declared dashboard becomes `deploy/observability/dashboards/<project>-<id>_gen.json` (spec
  part under `spec/`).
- The system dashboard becomes `<project>-telemetry-<service>_gen.json`.
- Both ship in the bundle like every generated dashboard. Dashboards are bundle-wide, so a
  dashboard declared in several envs must be identical in each; a conflict is a generate
  error naming both envs.
- `scope` defaults from `Telemetry.scope`.

**dashspec additions: none required** (§0 d). One optional nicety is deferred: a `count`
series over the generic receiver metric `scrape_samples_post_metric_relabeling` already gives
"samples vs limit" through `formula`.

---

## 3. Data flow, per runtime and per signal

### 3.1 The normalized contract

`pkg/telemetry` holds:

- `type Target struct`, with these fields: ServiceName, Scope, Source (`workload:x`,
  `hostinfra:x`, `manifests:x` or `observability`), Runtime (host, compose, cluster, hosted),
  Cluster, Namespaces, Selector, Address, Container, Isolated, Metrics, OTLP, Logs, Profiles,
  Checks.
- `Validate()`.
- `Normalize`, which resolves named ports to numbers, applies defaults, and turns
  `SecretRef`s into mount descriptors (never values).

`internal/cli/kcl_render.go` decodes `_render_telemetry` (a new `kcl/render.k` section) into
`renderedEnvs().telemetry`. `forge env render <env> --json` prints it under `telemetry`.

### 3.2 The translator

`pkg/telemetry/collector` is pure Go: `[]Target` + `Options{NodeLocal bool, Exporter
string, IsolatedExporter string, IdentityProcessors []string, PipelinePrefix string}` →
`Fragment{Receivers, Processors, Extensions, Pipelines map[string]any}`.

**Metrics:**

- One `prometheus` receiver per non-isolated set, using `kubernetes_sd_configs` role pod.
- SD is limited to `namespaces`, plus a `spec.nodeName=${env:K8S_NODE_NAME}` field selector
  when NodeLocal (DaemonSet: each pod scraped once).
- relabel `keep` on running, non-init containers, and rewrite `__address__` to the pod IP on
  the declared port (bracketed for IPv6), whether or not the pod spec declares that port: a
  manifest may serve on an undeclared port. Nothing per-container survives relabeling (the
  final labels are `instance`, `pod` and `namespace`), so a pod's per-container targets are
  identical and the scrape pool keeps one: one scrape and one `up` per pod. `job` →
  service_name.
- `metric_relabel_configs`: keep `up|allow`, drop `deny`, `labeldrop` the `drop_labels`.
- `sample_limit`, `label_limit` and `label_value_length_limit` are passed through.
- TLS and `authorization.credentials_file` are set from the mount descriptor.
- `counters` becomes `transform` `convert_gauge_to_sum("cumulative", true)` over the matching
  names. That is control-plane's proven ScrapeJob semantics.
- `address` targets use `static_configs`.

**Profiles:**

- `receiver_creator` + `k8s_observer` (node-local `observe_pods`), with one rule per target on
  the observer's pod endpoints (one per pod, so an undeclared port still works):
  `type == "pod" && namespace in [...] && labels[...] == ...`. Checks use the same rule.
- The rule instantiates `pprof` with `remote.endpoint = http://<pod IP>:N<prefix>/profile?seconds=N`
  (for cpu) or `<prefix>/<type>`, with `collection_interval` and `resource_attributes`
  `service.name`.
- An `address` target uses `pprof` directly.
- Output joins the existing `profiles` pipeline (the contrib profiles feature gate the agent
  already sets).

**Logs:**

- One `transform/telemetry_logs` processor, context `log`, with one statement group per
  target.
- Each group's `conditions` match `resource.attributes["service.name"]` (stamped earlier by the
  identity stage) and optionally `k8s.container.name`.
- The statements, generated from the typed fields:

  - `merge_maps(log.attributes, ParseJSON(log.body), "upsert")`, or `ParseKeyValue(...)` for
    logfmt, or `ExtractPatterns(...)` for regex
  - `set(log.time, Time(...))`
  - `set(log.severity_number, SEVERITY_NUMBER_WARN) where <field> == "WRN"`
  - `set(log.body, <field>)`
  - trace and span ids
  - `delete_key` for `drop`
  - the rules, in order

  Users never write OTTL; forge owns the translation and tests it.

**Identity:** `transform/telemetry_identity` (log, metric and profile contexts) sets
`service.name` (and `reliant.telemetry=1`) on records whose
`k8s.namespace.name` + `k8s.pod.labels.<k>` match a target. To make those labels available,
the fragment asks `k8sattributes` to extract exactly the selector keys (bounded: the union of
declared keys).

**Checks:** `tcpcheck` / `httpcheck` receivers.

**Isolated targets:** their own receivers and their own `batch` processor, exported to
`Options.IsolatedExporter`. That exporter has an in-memory `sending_queue` sized 200,
`block_on_overflow: false` and a 2m retry, which is cp's selfmon semantics.

**NetworkPolicy:** `collector.NetworkPolicies(targets, collector)` returns one NetworkPolicy per
(target, namespace) that asks for one. It selects the target's pods, with ingress from
`{namespaceSelector: collector ns, podSelector: collector pod labels}` on exactly the scrape,
pprof and check ports. `Target.NetworkPolicy` is tri-state, because a NetworkPolicy that selects
pods no policy selected before flips them to default-deny for ALL other ingress:

- unset ("auto") on a Workload or HostInfra: the rule is merged into the workload's own
  `ingressPolicy` (`workload_render.go:1346-1400`, which already names "a cross-namespace scraper"
  as the unmodelled case). Safe: that policy exists either way.
- unset on Manifests or Observability: no policy. forge cannot know whether those pods are
  already policy-selected, and a new policy would black-hole their real traffic. The project
  owns admission (`telemetry.Warnings` notes that the collector needs ingress).
- `True`: rendered, as a sibling object of the target's group. `False`: never.

### 3.3 Per runtime

| Signal                  | Local `forge env up`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | forge cluster agent (env has a destination)                                                                                                                                                                      | Reliant hosted (`OnHosted`)                                                                                                                                                                                                                                                                       | Own collector (control-plane)                                                                                                                                                                                                 |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **metrics**             | A new **local telemetry agent** (§3.4) runs `prometheus` with `static_configs` on 127.0.0.1:<port> and exports OTLP to ClickStack's published `127.0.0.1:4318` (or to the forwarder when a destination is set).                                                                                                                                                                                                                                                                                                                 | `AgentConfig` merges the Fragment (NodeLocal) into a `metrics/telemetry` pipeline beside `metrics/node`. The `ObservabilityAgentSpec` gains `Telemetry []telemetry.Target`. RBAC already lists and watches pods. | The `Shape.Telemetry` entries of hosted workloads, behind `BundleFeatureTelemetry`. cp's workload operator renders the tenant scrape into the platform agent's customer pipeline, stamped with tenant identity from namespace labels. `allow` is mandatory, and `sample_limit` is capped by plan. | `plugin.telemetry_collector(forge.telemetry_targets(...), opts)` returns the Fragment as a KCL dict, which cp's `lib/otel_agent.k` merges into its platform pipeline (`scope=platform`) or its selfmon pipeline (`isolated`). |
| **logs**                | The ClickStack collector's `filelog/forge` already reads `.forge/logs/<env>/*.log`; service.name = file name. HostInfra logs move there (F0). The per-target parse is a `transform/telemetry_logs` whose `log_statements` come from `${file:/etc/forge/telemetry/<env>/log_statements.yaml}`, written per `env up` (empty when nothing is declared). `otel-collector.yaml.tmpl` gains the processor and compose gains the mount; existing projects get it through an upgrade row, and `env up` warns when the file predates it. | The same `transform/telemetry_logs`, inserted after `k8sattributes` and `transform/line_resource` in the logs pipeline.                                                                                          | The cp `logs/files` pipeline gains the per-tenant transform, rendered by the operator from the Shape (later cp PR).                                                                                                                                                                               | The Fragment's processors are inserted in cp's `logs/files` pipeline.                                                                                                                                                         |
| **traces**              | Workload: standard OTEL env already points at `127.0.0.1:<local_port>`; `otlp.env` remaps add the system's keys. HostInfra: forge passes the OTLP facts as engine env.                                                                                                                                                                                                                                                                                                                                                          | `_obs_env` endpoint = the agent Service; `otlp.env` remaps resolve to it; Manifests use `forge.otlp_value`.                                                                                                      | The platform injects the standard env. Remapped keys are recorded in the Shape (`otlp_env` map: key → fact) and the operator sets them; an older CP strips them, and forge warns.                                                                                                                 | `forge.otlp_value(key, obs, cluster)` returns `telemetry_collector.otlp_*`.                                                                                                                                                   |
| **profiles**            | The local telemetry agent's `pprof` receivers export OTLP profiles to the `otel-profiles` collector (`profiles_port`), so they land in the same `otel_profiles` table `forge obs profile` reads.                                                                                                                                                                                                                                                                                                                                | `receiver_creator` + `k8s_observer` + `pprof` → the existing profiles pipeline.                                                                                                                                  | Deferred (cp phase C5): a hosted pprof pull through the same operator path.                                                                                                                                                                                                                       | The Fragment's receiver_creator joins cp's profiles pipeline.                                                                                                                                                                 |
| **checks**              | `tcpcheck`/`httpcheck` in the local agent.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | Fragment receivers.                                                                                                                                                                                              | Deferred (uptime checks already exist separately).                                                                                                                                                                                                                                                | The Fragment (replaces cp's `Probe`).                                                                                                                                                                                         |
| **dashboards / alerts** | Generated into `deploy/observability/dashboards/*_gen.json`; local HyperDX tiles as today.                                                                                                                                                                                                                                                                                                                                                                                                                                      | Same files, in the bundle.                                                                                                                                                                                       | Same files, in the bundle; alerts go to `Shape.Alerts` (no new transport, so they already work on old CPs).                                                                                                                                                                                       | Same.                                                                                                                                                                                                                         |

### 3.4 The local telemetry agent

- **What it is:** a forge-supervised `otelcol-contrib` 0.162.0 host process. It uses the same
  pin as the cluster agent (`OTEL_COLLECTOR_IMAGE`), fetched and checksummed through hostinfra's
  release cache (`e.release.ensure`).
- **When it starts:** `forge env up` starts it only when the env's targets declare metrics,
  profiles or checks.
- **Config and logs:** its config is `collector.Fragment` (NodeLocal false, static targets) and
  is written to `.forge/observability/<env>/telemetry-agent.yaml`. It logs to
  `.forge/logs/<env>/forge-telemetry-agent.log`.
- **Credentials:** files are written 0600 under `.forge/observability/<env>/secrets/` from
  the secret store. Values never go into the config.
- **Why a host process and not the ClickStack container:**

  - HostInfra and `OnHost` processes bind 127.0.0.1, which a container cannot reach. Binding
    them to the docker bridge would publish dev services on the LAN.
  - ClickStack's collector cannot write profiles.

  Compose-run targets are reached on their published loopback ports.

---

## 4. Validation

- **KCL `check:`** blocks (§1): structure, ranges, enums, exclusivity, https for credentials.
  They fail at `forge env render`.
- **Go, at render** (`pkg/telemetry.Validate`, called from `env render` and `generate`):
  - RE2 compiles
  - service_name is unique per env
  - named ports resolve
  - the attachment-point rules
  - the hosted rules (allow required; no address, bearer or profiles until cp advertises them)
  - every `bearer_token` resolves: a cluster Secret in the collector namespace (pre-flight like
    other SecretRefs), a `store_key` present in `secrets/<env>.yaml` for local, and the
    ManagedSecret on hosted
  - the `otel-collector.yaml` currency warning
  - "telemetry declared on cluster X but nothing collects it" when there is no forge agent and
    no `telemetry_collector`
- **Generate (obsdash):** every declared dashboard goes through `dashspec.ParseDashboard`, and
  cross-env identity is checked.
- **Lint lane `forge lint --telemetry`** (new: `internal/cli/lint/telemetry.go`). Every rule
  goes in `lint_rollout_table.go` `newRules` with `IntroducedIn` = next release and `GatesFrom`
  = the one after. They warn first.
  - `telemetry-scrape-unbounded`: a scrape with no `allow` (on hosted it is a render error, not
    a lint).
  - `telemetry-dashboard-metric-uncollected`: a metrics-series `field` in a Telemetry's
    dashboards (or alerts' `MetricName = '...'` literals) matched by no `allow` of a scrape of
    the same target. It applies the exposition-suffix mapping: a histogram needs
    `_bucket|_count|_sum` and a summary needs the base name plus `_sum|_count`.
  - `telemetry-dashboard-rate-on-gauge-name`: `rate`/`increase` on `metricType: sum` where the
    field is matched by no `counters` and is not `_total`/`_count` suffixed. This is a
    heuristic, so it stays a warning forever.
  - `telemetry-drop-labels-missing`: a scrape whose `allow` keeps a name while `drop_labels` is
    empty and `sample_limit` exceeds 20000. Warning only.
- **Live:** `forge obs dashboards check --against <env>` already checks stored kind vs
  `metricType` and presence. A new `forge obs telemetry <env>` lists the targets with `up`, last
  log, last profile and samples vs limit (F5).
- **Tests:**
  - Golden fragments per signal in `pkg/telemetry/collector`.
  - A `testkit.Slow` test that runs the pinned `otelcol-contrib validate` over the merged agent
    and local configs.
  - KCL `kcl/tests/telemetry_*.k` for every check (the closed-schema test pattern).
  - Render-diff: a project with no Telemetry renders byte-identically.
  - `TestDashspecKCLIsCurrent`.
  - Lint rollout rows (`TestEveryGradableRuleDeclaresItsRollout`).
  - Shape strip test for old CPs.
  - e2e `TestE2ENightlyTelemetryHostInfra`: a HostInfra temporal with
    `temporal_metrics_port` + Telemetry → metrics and logs in local ClickStack.

---

## 5. Phased PR plan

Each PR owns disjoint files. "→" means blocks.

### forge (one release; every field additive)

| PR                                                                                                                                                                                                                                   | Owns (files)                                                                                                                                                                                                                                                                                                                                                                                                        | Depends                                          |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------ |
| **F0** host-infra logs into `.forge/logs/<env>/<name>.log`; the old path becomes a symlink, `up_identity.go` message updated                                                                                                         | `internal/hostinfra/{supervised,zitadel,hostinfra}.go`, `internal/cli/up_identity.go`                                                                                                                                                                                                                                                                                                                               | —                                                |
| **F1** schema + normalize + render output                                                                                                                                                                                            | `kcl/telemetry.k` (new), the field lines in `kcl/workload.k` and `kcl/schema.k` (incl. `HostInfra.temporal_metrics_port`, `Observability.telemetry_collector`), the `kcl/render.k` `_render_telemetry` and `otlp_value` and `_obs_env` remap section, `pkg/telemetry/{target,normalize,validate}.go`, `internal/cli/kcl_render.go` decode, the `env_render.go` validate + `--json` field, `kcl/tests/telemetry_*.k` | — (its `dashboards` field lands in F5)           |
| **F2** dashspec KCL mirror                                                                                                                                                                                                           | `internal/codegen/dashspec_kcl*.go`, `kcl/dashspec/dashspec_gen.k`, `kcl/dashspec/helpers.k`                                                                                                                                                                                                                                                                                                                        | —                                                |
| **F3** translator                                                                                                                                                                                                                    | `pkg/telemetry/collector/*` (fragments, netpols, golden and slow validate tests)                                                                                                                                                                                                                                                                                                                                    | F1                                               |
| **F4** cluster agent                                                                                                                                                                                                                 | `pkg/deploy/observability.go` (spec field, merge, k8sattributes label extraction), `pkg/deploy/workload_render.go` ingress rule, Manifests sibling netpol emission in `pkg/deploy/render.go`, `kcl/render.k` `ObservabilityAgent` record (adds targets; coordinate with F1 by appending a separate block)                                                                                                           | F3                                               |
| **F5** obsdash: declared dashboards, system dashboard, default alerts, `forge obs telemetry`; adds `Telemetry.dashboards` / `dashboard`                                                                                              | `internal/obsdash/telemetry.go` (new), `internal/obsdash/spec.go` Input field, `internal/cli/generate_pipeline.go` (one Input line), `internal/cli/obs_telemetry.go` (new), `kcl/telemetry.k` (2 fields)                                                                                                                                                                                                            | F1, F2                                           |
| **F6** local: telemetry agent + ClickStack log include + upgrade row; temporal `--metrics-port`                                                                                                                                      | `internal/cli/up_telemetry.go` (new), `internal/hostinfra/telemetryagent.go` (new), `internal/hostinfra/temporal.go`, `internal/templates/project/{otel-collector.yaml.tmpl,docker-compose.yml.tmpl}`, `internal/generator/upgrade.go` row, `internal/templates/clickstack_compose_test.go`                                                                                                                         | F3, F0                                           |
| **F7** hosted: `Shape.Telemetry` + `BundleFeatureTelemetry = "shape-telemetry"` (decodes) + `CapabilityCollectsTelemetry = "collects-telemetry"` (collects, hand-advertised), strip, and a deploy warning unless both are advertised | `pkg/release/telemetry.go` (new), `pkg/release/bundle.go` (field), `pkg/release/bundle_features.go`, `internal/cli/env_shape.go`                                                                                                                                                                                                                                                                                    | F1                                               |
| **F8** lint lane `--telemetry` + rollout rows                                                                                                                                                                                        | `internal/cli/lint/telemetry*.go`, `lint_rollout_table.go` rows                                                                                                                                                                                                                                                                                                                                                     | F1, F5                                           |
| **F9** KCL bridge `kcl_plugin.forge.telemetry_collector` + `telemetry_targets` lambda                                                                                                                                                | `internal/kclrender/telemetry_plugin.go` (new), registration line in `kclrender.go`, `kcl/telemetry.k` lambda block                                                                                                                                                                                                                                                                                                 | F3                                               |
| **F10** docs + skill `observability/third-party` (worked examples: image Workload, Manifests, HostInfra, own collector); this proposal as `docs/proposals/third-party-telemetry.md`                                                  | `internal/skills/observability/third-party/*`, `docs/proposals/*`                                                                                                                                                                                                                                                                                                                                                   | content from F1-F9 (the proposal can land first) |

**Waves:**

1. F0, F2 and the proposal PR in parallel.
2. F1.
3. F3, F5 and F7 in parallel.
4. F4, F6, F8 and F9 in parallel.
5. F10, then release (rc-train).

The critical path is F1 → F3 → F4/F6/F9.

`kcl/render.k` is shared by F1 and F4: F1 adds `_render_telemetry`, and F4 only appends targets
to the existing agent record at `render.k:1963-1971`. `kcl/telemetry.k` is touched by F1, F5 and
F9 in separate blocks.

### control-plane (after the forge release)

| PR                                                                                                                                                                                                                 | Owns                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | Depends                                      |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------- |
| **C0** pin: reliant bumps forge first (`dev-docs/forge-upgrade.md`), then `task pin:forge REF=… RELIANT=…`                                                                                                         | `go.mod`, `forge.yaml`, the pin files                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | forge release                                |
| **C1** bridge + index; selfmon parity                                                                                                                                                                              | `lib/telemetry/index.k` (new; aggregates the per-system files, one stub per system carrying today's ScrapeJob semantics), `lib/otel_agent.k` (consumes `plugin.telemetry_collector` for the platform and selfmon pipelines; deletes `ScrapeJob`, `self_jobs`, `platform_scrape_jobs`, `Probe`, `platform_scrape_policies` and `store_scrape_policies`, replaced by the forge-rendered netpols with `telemetry_collector = {namespace = agent ns, pod_labels}`), `lib/selfmon.k` (reduced to constants), `lib/obs_prod.k` wiring, `internal/dashtemplates/dashtemplates.go` (derive Platform from `scope: platform`, not a hand map). A render-parity test proves the scrape configs are semantically equal before deletion. | C0                                           |
| **C2** hosted tenant scrape                                                                                                                                                                                        | `internal/reconcilestore/` reader, `internal/operators/workload/telemetry.go` (new), the customer scrape pipeline in the agent (a separate block in `lib/otel_agent.k`, coordinated with C1), advertise `collects-telemetry` by hand beside the bundle features in `internal/handlers/deploy/rpc_get_*` (in this PR, the one that makes it collect; `shape-telemetry` arrives with the C0 pin), plan caps                                                                                                                                                                                                                                                                                                                   | C0                                           |
| **C3.<sys>** one per system: fill `lib/telemetry/<sys>.k` (scrape, logs, traces, profiles, dashboards, alerts), add `observability/selfmon-alerts/<Name>.sql`, delete the migrated `dashboards/selfmon-<sys>.json` | own files only                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | C1                                           |
| **C4.<sys>** enable endpoints (paired with its C3)                                                                                                                                                                 | LiteLLM: `deploy/litellm/config.yaml` + the litellm lambda in `infra.k`. PgBouncer exporter sidecar: `lib/pgbouncer.k`. CNPG operator + instance ports: `lib/cnpg_vendored.k`. cert-manager metrics/profiling values: `lib/platform.k`. Temporal OTLP + `PPROF_PORT` + JSON logs: the temporal lambda in `infra.k`. Zitadel JSON + OTLP: the zitadel lambda. Envoy `EnvoyProxy.telemetry`: the ingress lib. Alertmanager `--log.format=json`: `observability/alertmanager.k`. OpenBao `log_format=json`: openbao config                                                                                                                                                                                                     | C1 (`infra.k` hunks sit in distinct lambdas) |
| **C5** hosted profiles + checks, tenant log parse                                                                                                                                                                  | operator + agent                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | C2                                           |

---

## 6. Per-system declarations (illustrative: what a PROJECT declares)

This table is NOT forge content. forge ships no per-system knowledge; it is a worked example of
what one project (control-plane) would declare in its own KCL with the primitive, and it is how
the design was checked against real systems. The vendor facts (metric names, env keys, log
formats) are that project's to own and keep current.

In the table, "allow" lists the exposition names; `*` marks a histogram (`_bucket|_sum|_count`)
and `†` a summary.

| System (attach)                                                                                               | Metrics                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Logs                                                                                                                 | Traces / profiles                                                                                                                    | Panels (beyond the system dashboard)                                                                                                                                             | Alerts                                                                                                                                             |
| ------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Temporal 1.26** (`Manifests` "temporal")                                                                    | :9090 `/metrics`. allow `service_requests\|service_errors\|service_error_with_type\|service_latency*\|persistence_(requests\|errors\|latency*)\|action\|task_schedule_to_start_latency*\|approximate_backlog_count\|poll_(success\|timeouts\|success_sync)\|numshards_gauge\|sharditem_(created\|removed)_count\|shard_closed_count\|membership_changed_count\|task_errors\|restarts\|num_goroutines\|workflow_(success\|failed\|timeout\|terminate)`. drop_labels `workflowType`. Replaces today's unbounded scrape.                                                           | json; ts `ts` epoch_s; severity `level`; body `msg`; rules = today's `expected_severity` (benign ERROR → warn)       | OTLP traces (`OTEL_TRACES_EXPORTER=otlp` + `forge.otlp_value("grpc_address")`, sampler 0.05); pprof `PPROF_PORT` :7936 cpu+heap (C4) | frontend rate/err%/p95 by operation (exclude `Poll*`); persistence p95/errors; actions/s; schedule-to-start p95; backlog; polls; shards; shard churn; workflow outcomes          | error ratio >5% 10m (excl. NotFound/AlreadyStarted); persistence p95 >1s 10m; schedule-to-start p95 >30s; backlog growing 15m; shards ≠ configured |
| **temporal-ui** (`Manifests`)                                                                                 | —                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | auto                                                                                                                 | —                                                                                                                                    | system dashboard only                                                                                                                                                            | TargetDown via http check `/`                                                                                                                      |
| **Zitadel v4.19.5** (`Manifests` "zitadel")                                                                   | :8080 `/debug/metrics`. allow = today's ZITADEL_METRICS + `projection_events_processed_total\|projection_handle_timer_seconds*\|projection_state_latency_seconds*\|grpc_server_grpc_status_code_total`. drop_labels `uri`                                                                                                                                                                                                                                                                                                                                                       | C4: `ZITADEL_INSTRUMENTATION_LOG_FORMAT=json`; then json, severity `level`, body `msg`, time `time`                  | OTLP traces (`ZITADEL_INSTRUMENTATION_TRACE_EXPORTER_TYPE=grpc`, endpoint `grpc_address`)                                            | requests by return_code class; 5xx %; gRPC non-OK; projection lag max/p95; projection failures; DB pool %; empty_acquire; CPU                                                    | the existing Identity\* rules move into `alerts`; 5xx >2%; pool >90%; projection lag >60s                                                          |
| **ClickHouse 26.8** (`Manifests` "observability", selector clickhouse, `scope=platform`, `isolated`)          | :9363. allow = CLICKHOUSE*METRICS + `ClickHouseMetrics_Query\|ClickHouseProfileEvents*(QueryTimeMicroseconds\|Query\|SelectQuery\|FailedSelectQuery)\|ClickHouseMetrics_PartsOutdated`                                                                                                                                                                                                                                                                                                                                                                                          | C4: `<logger><formatting><type>json`; json, severity `level`, body `message`, time `date_time`                       | —                                                                                                                                    | today's 15 panels + running queries, avg query latency, failed-select ratio                                                                                                      | the existing TooManyParts/Disk\*/InsertsRejected/IngestStopped                                                                                     |
| **clickhouse-backup** (same selector, `name="backup"` scrape :7171)                                           | BACKUP_METRICS                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | regex/text                                                                                                           | —                                                                                                                                    | last success age, status, duration                                                                                                                                               | ClickHouseBackupFailing                                                                                                                            |
| **NATS 2.10** (`Manifests` "nats-exporter" + "nats-cluster")                                                  | exporter :7777, NATS*METRICS + `jetstream_consumer_num*(pending\|ack_pending\|redelivered)`; counters NATS_COUNTERS; drop_labels none (consumers bounded by stream allowlist)                                                                                                                                                                                                                                                                                                                                                                                                   | regex `^\[(?P<pid>\d+)\] (?P<ts>\S+ \S+) \[(?P<lvl>[A-Z]{3})\] (?P<msg>.*)$`, map INF/WRN/ERR/DBG/TRC                | pprof `prof_port` 6060 (C4), cpu+heap                                                                                                | today's 9 panels + consumer pending                                                                                                                                              | NatsMemberUnhealthy, NatsHealthzFlapping, slow consumers rising, JS storage >80%                                                                   |
| **LiteLLM v1.104** (`Manifests` "litellm")                                                                    | C4: `callbacks: ["prometheus"]` (verify free tier at v1.104), `/metrics` on the proxy port. allow `litellm_(proxy_total_requests_metric\|proxy_failed_requests_metric\|request_total_latency_metric*\|llm_api_latency_metric*\|llm_api_time_to_first_token_metric*\|overhead_latency_metric*\|deployment_.*\|remaining_(requests\|tokens)_metric\|(input\|output\|total)_tokens_metric\|in_flight_requests\|request_queue_time_seconds*\|.*spend_update_queue_size)`. **drop_labels `hashed_api_key, api_key_alias, end_user, user, user_email, team, team_alias`** (mandatory) | C4 `JSON_LOGS=True`; json, body `message`, severity `level`, time `timestamp`                                        | OTLP traces via `callbacks: ["otel"]` + `OTEL_EXPORTER=otlp_http`, `OTEL_ENDPOINT` = `traces_endpoint`                               | requests by status; failure % by class; p95 latency by model; API p95 by provider; TTFT; overhead; deployment state; cooldowns/fallbacks; headroom; tokens/s; spend-update queue | failure >5% 10m; deployment_state==2 5m; spend queue growing                                                                                       |
| **OpenBao 2.6.2** (`Observability.telemetry`, selector openbao, https)                                        | :8200 `/v1/sys/metrics` params format=prometheus, `tls.ca` (the store CA, replacing insecure*skip_verify). allow `vault_core*(unsealed\|active\|handle*request\|handle_login_request\|check_token)†?\|vault_audit_log*.\*_failure\|vault_barrier_(get\|put)†\|vault*postgres*(get\|put)†\|vault*expire_num_leases\|vault_token_count\|vault_runtime*(alloc_bytes\|num_goroutines)`                                                                                                                                                                                              | C4 `log_format="json"`; json, time `@timestamp`, severity `@level`, body `@message`                                  | pprof `/v1/sys/pprof` with `bearer_token` (store key, https)                                                                         | sealed/active per pod; request rate + p99 (summary, by pod); logins; barrier/storage p99; audit failures; leases; tokens; memory                                                 | the existing 5 + storage p99 >500ms                                                                                                                |
| **ESO** (`Observability.telemetry`, both clusters)                                                            | :8080 ESO_METRICS                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | json (zap)                                                                                                           | —                                                                                                                                    | sync errors by store; controller reconcile; not-ready secrets                                                                                                                    | the existing ESO\*                                                                                                                                 |
| **PgBouncer 1.26** (`Manifests` pgb)                                                                          | C4: sidecar `pgbouncer_exporter` :9127 (`stats_users` role, secret via `SecretRef`). allow `pgbouncer_(up\|pools_.*\|stats_.*_total\|databases_(current_connections\|pool_size)\|config_max_client_connections)`                                                                                                                                                                                                                                                                                                                                                                | regex `^(?P<ts>\S+ \S+ \S+) \[(?P<pid>\d+)\] (?P<lvl>LOG\|WARNING\|ERROR\|FATAL) (?P<msg>.*)$`                       | —                                                                                                                                    | clients active/waiting; max wait; server used vs pool; QPS; avg query time; tx/s; clients vs max                                                                                 | up==0 2m; maxwait >1s 5m; waiting >0 10m; clients >85%                                                                                             |
| **CNPG 1.30 / Postgres** (`Observability.telemetry`: operator selector + instance selector `cnpg.io/cluster`) | operator :8080 `controller_runtime_.*\|workqueue_.*`; instances :9187 `cnpg_(collector_up\|backends_total\|backends_waiting_total\|pg_database_size_bytes\|pg_replication_lag\|pg_stat_archiver_.*\|collector_last_available_backup_timestamp\|pg_stat_database_(xact_commit\|xact_rollback\|deadlocks)\|pg_database_xid_age)`                                                                                                                                                                                                                                                  | json; severity `record.error_severity` when `logger == postgres` (rule), else `level`; body `record.message` / `msg` | —                                                                                                                                    | backends by state; waiting; size; lag; archival age; backup age; tx/s; deadlocks; xid age                                                                                        | collector_up==0; lag >30s; archival >1h; backup >26h; xid >1e9                                                                                     |
| **Cloud SQL** (prod)                                                                                          | **open question (§7):** not scrapable; needs a non-scrape source                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | GCP                                                                                                                  | —                                                                                                                                    | —                                                                                                                                                                                | —                                                                                                                                                  |
| **Flux** (`Observability.telemetry`, 3 selectors `app=`)                                                      | :8080 FLUX_METRICS                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | json                                                                                                                 | —                                                                                                                                    | today's 7 panels                                                                                                                                                                 | the existing 8 Flux\*                                                                                                                              |
| **Alertmanager 0.28** (`Manifests` "observability", `isolated`)                                               | :9093 ALERTMANAGER_METRICS + `alertmanager_notification_latency_seconds*`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | C4 `--log.format=json`; json, time `ts`                                                                              | pprof `/debug/pprof` :9093 heap                                                                                                      | alerts by state; received; notifications/failed by integration; latency p95; reload ok                                                                                           | the existing 3                                                                                                                                     |
| **Envoy Gateway 1.9.2** (`Observability.telemetry`, ns envoy-gateway-system)                                  | :19001 `/stats/prometheus`. allow `envoy_(http_downstream_rq_(total\|xx\|time*)\|cluster_upstream_rq_(total\|xx\|time*)\|cluster_upstream_cx_active\|cluster_membership_healthy\|server_(live\|uptime))`. drop_labels `envoy_cluster_name` if per-route explosion (decide from a live scrape)                                                                                                                                                                                                                                                                                   | access logs → OTLP sink (C4 `EnvoyProxy.telemetry.accessLog`)                                                        | C4 `EnvoyProxy.telemetry.tracing` OTel provider, samplingRate 1%                                                                     | rps by class; p95; upstream errors; active conns; healthy members                                                                                                                | 5xx ratio >2%; upstream unhealthy                                                                                                                  |
| **cert-manager 1.20** (`Observability.telemetry`)                                                             | C4 `prometheus.enabled` :9402. allow `certmanager_(certificate_expiration_timestamp_seconds\|certificate_ready_status\|http_acme_client_request_count\|controller_sync_call_count)`                                                                                                                                                                                                                                                                                                                                                                                             | C4 `--logging-format=json`; severity `v` map {info = ["0","1","2"], debug = ["3","4","5"]}                           | pprof `--enable-profiling` :6060                                                                                                     | certs by readiness; soonest expiry; ACME errors                                                                                                                                  | expiry <14d; not ready 15m                                                                                                                         |
| **OTel gateway / agents** (`Manifests` "observability" + agents, `scope=platform`, `isolated`)                | :8888 GATEWAY_METRICS / OTELCOL; sync :9101                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | json                                                                                                                 | pprof via `pprofextension` (if enabled)                                                                                              | today's 18 panels                                                                                                                                                                | the existing Gateway\* + AgentExportErrors                                                                                                         |
| **HyperDX / Mongo** (`Manifests` "observability")                                                             | checks only (tcp)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Mongo: json, time `t.$date`, severity `s` map {fatal=[F], error=[E], warn=[W], info=[I], debug=[D1..D5]}, body `msg` | —                                                                                                                                    | system dashboard                                                                                                                                                                 | SelfMonitorTargetDown becomes the default CheckFailing                                                                                             |
| **ClamAV** (`Manifests`)                                                                                      | — (http/tcp check :3310)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | text                                                                                                                 | —                                                                                                                                    | system dashboard                                                                                                                                                                 | CheckFailing                                                                                                                                       |

---

## 7. Risks, open questions, rejected alternatives

**Risks**

- `pprof` and receivercreator are alpha for profiles. To contain that, the profile section is
  separable: a failure disables only the profiles pipeline, and the slow `otelcol validate`
  test pins behaviour at 0.162.0. Bump with the profiles wire pin.
- Wrongly-typed exporter counters still need `counters`. The lint heuristic and
  `obs dashboards check --against` catch them; a static guarantee is impossible.
- The local `otel-collector.yaml` is Tier 2, user-owned. Projects that skip the upgrade row get
  scrape and profiles locally but not log parsing. `env up` says so.
- The hosted tenant scrape is new cp surface with cost exposure. It is mitigated by: allow
  mandatory, sample_limit capped by plan, ports limited to declared ones, NetworkPolicy from the
  platform agent only, and volume accounted per tenant.
- KCL gains a generated dashspec mirror: a large closed schema to keep current. The test
  enforces currency.
- Multiline logs (stack traces) are deferred. A per-target `recombine` needs filelog routing by
  file path, and selector labels are not in the path.
- C1 deletes cp's ScrapeJob. The parity render test must prove equal scrape semantics first,
  or selfmon goes blind.

**Open questions for the owner**

1. Cloud SQL (`reliant-pg`, `temporal-pg`) is not scrapable. Options:

   - (a) a generic `Telemetry.sources` for pull APIs (googlecloudmonitoring and sqlquery
     receivers are in 0.162.0). This is still generic, but it widens the primitive.
   - (b) cp runs `postgres_exporter` through cloud-sql-proxy (pg_stat only, no CPU/disk).
   - (c) alert in Cloud Monitoring.

   Recommendation: (b) now, (a) as a follow-up proposal.

2. The hosted tier caps (`sample_limit` ceiling, scrape count per env) are a billing and product
   decision.

**Rejected alternatives**

- Per-system packs or presets in forge: refused by owner decision. They would also pin vendor
  metric names to forge releases.
- A KCL-side collector renderer: it would be a second translator next to Go, with drift
  between local, cluster and hosted. Go plus the KCL plugin gives one translator.
- Shipping raw collector config in the bundle for hosted: a tenant would control the platform
  collector. Hosted gets typed targets only.
- `prometheus.io/scrape` pod annotations: they are unbounded (no allowlist), invisible to
  render-time validation, and do not work for raw objects we do not own.
- Dashboards as untyped dicts: they fail only at generate, and give no author-time errors.
- Dashboard file references only (the status quo): names drift between the allowlist and the
  panels, and the coverage lint becomes impossible. Files stay as the escape hatch.
- Free-form OTTL in `LogParse`: it leaks a collector dialect into the project API and is
  unvalidatable. The typed fields plus rules cover every system in §6.
- Zipkin/Jaeger receivers: no system in scope needs them (all speak OTLP). They can be added
  later as an `OtlpPush.receiver` enum.
- Scraping host processes from the ClickStack container: it requires binding dev services
  off-loopback.
- `receivercreator` for metrics too: prometheus `kubernetes_sd` is more mature for metrics
  (relabel, sample_limit) and is cp's proven path. receivercreator is used where pprof has no
  SD.
- A separate primitive per signal (`Scrape`, `LogRule`, ...): that is four seams that each
  restate the target selector. One `Telemetry` keeps identity in one place.

---

## Implementation notes

Divergences from the text above, recorded as each PR lands.

**F1 (schema, render, `pkg/telemetry`):**

- `LogSeverity.map` is `LogSeverity.mapping`: `map` is a KCL keyword (the `map` quantifier).
- `Telemetry.dashboards` lands with F5, which owns the typed dashspec mirror. `dashboard`
  (`SystemDashboard`) and `alerts` are in F1, and a Telemetry's `alerts` already join the env's
  `Observability.alerts` rules (labelled `service = service_name` unless they set it), so they
  reach the release shape with no new transport. The env-wide alert-name uniqueness and the
  "alerts need observability" rule are render asserts.
- A HostInfra's ports are named `server` (`port`), `monitor` (`nats_monitor_port`), `ui`
  (`temporal_ui_port`) and `metrics` (`temporal_metrics_port`), so its Telemetry can name them.
  `temporal_metrics_port` is declared and projected (`infra[].temporal_metrics_port`, only when
  set); passing it to the server as `--metrics-port` is F6's.
- The normalized contract is one type: the render emits `output.telemetry` already in the
  `Target` JSON shape, with a named port as `port: 0, port_name: <name>` beside the attached
  entity's `ports`. `telemetry.Decode` (strict), `telemetry.Normalize` (resolves names, applies
  defaults, sets `SecretMount.Path`; never fails) and `telemetry.Validate` (all problems at once,
  as a `*ValidationError`) are the pipeline. `forge env render` validates, and
  `forge env render --json` prints a `forge.env-render/v1` document whose `telemetry` is the
  normalized list. The env document schema (`schema/env.v1.schema.json`) gains `telemetry`, so a
  hand-written env document can declare targets directly.
- OTLP facts: an `OtlpRef` resolves per runtime. On a cluster, the env's `telemetry_collector`
  wins over forge's node agent (which exists only with a destination); the host has no gRPC fact
  (forge does not know the local gRPC port); hosted resolves none (the platform's, recorded as
  `{ref}` for F7). An env key bound to a fact with no value is left out. `endpoint` follows the
  system's `OtlpPush.protocol` (gRPC: `http://<grpc_address>`). `forge.otlp_value(key,
observability, cluster = "", protocol = "http/protobuf")` resolves only the transport facts;
  `cluster` empty means the local collector.
- A Telemetry's `service_name` becomes the workload's `OTEL_SERVICE_NAME`, and `scope =
"platform"` drops the standard `OTEL_RESOURCE_ATTRIBUTES` (no env identity). Both apply only
  when a Telemetry is declared, so an env without one renders byte-identically.
- `network_policy` is tri-state (`network_policy?: bool`; `Target.NetworkPolicy *bool`), not
  `bool = True`: unset is "auto" (§3.2, NetworkPolicy), so forge never puts a default-deny policy
  on raw pods it does not own. `telemetry.Warnings` returns an informational `Problem`
  (`Warning: true`) for a Manifests or Observability target that pulls with it unset, and
  `forge env render` prints it on stderr.
- Validate's hosted rules: every scrape sets `allow`; no `address`, no bearer token, no
  profiles; a numeric port must be one the workload declares. A target on `forge.OnExternal` or
  `forge.BuildOnly` is refused (nothing forge reaches runs it).

**F5 (dashboards, default alerts, `forge obs telemetry`):**

- `Telemetry.dashboards: [dashspec.Dashboard] = []` (`kcl/telemetry.k` imports `.dashspec`;
  projects write `import forge.dashspec as ds`). Each renders through `dashspec.dashboard_json`
  into `Target.Dashboards []json.RawMessage`, kept raw so `dashspec.ParseDashboard` (in
  `telemetry.Validate` and again in obsdash) is the one judge, with line and column. Ids are
  unique within a Telemetry (KCL) and within an env (Validate). The `dash_*` re-exports of §2
  were not added: the F2 helpers (`ds.rate`, `ds.quantile`, `ds.gauge`, `ds.ratio`) are the one
  spelling.
- `obsdash.TelemetrySystemsOf([]EnvTelemetry)` merges every env's targets into one
  `TelemetrySystem` per service (signals union; scope, system-dashboard title and a declared
  dashboard's content must agree across envs, else an error naming both envs). `Input.Telemetry`
  carries them. Each system's dashboards are their own spec part,
  `deploy/observability/spec/telemetry-<service>_gen.json` (`TelemetryPart`), not
  `project_gen.json`, so two branches that declare different systems touch different files;
  `ReadSpec` appends them after the project's dashboards.
- The system dashboard `telemetry-<service>` (`BuildSystemDashboard`) draws: collection health
  from the Prometheus receiver's `up`, `scrape_samples_scraped`,
  `scrape_samples_post_metric_relabeling` and `scrape_duration_seconds` (per
  `service.instance.id`); CPU, memory and restarts from the node agent (only on a cluster or
  hosted runtime; keyed by `k8s.workload.name` for a Workload attachment, by `service.name`
  otherwise); log volume by severity, error lines and recent lines; span rate, error ratio, p95
  and a RED table by span name (only with OTLP traces); tcpcheck / httpcheck status and latency
  (only with checks); and a text panel naming `forge obs profile --service <svc>` (only with
  profiles). Scope `platform` makes it dashspec scope platform with no env filter; group
  `platform`. A declared dashboard with no scope takes the system's.
- Locally, a declared dashboard's HyperDX file keeps only what HyperDX can draw
  (`localDrawable`: a summary quantile or a sql panel is the console's); the bundle ships it
  whole. A declared id that collides with a generated dashboard's is a generate error.
- The release bundle of an env carries the telemetry dashboards of the systems THAT env declares
  (`dashboardSpecForEntities`), after the process-less cut.
- Default alerts are two catalog defaults, tuned by id in `forge.Alerts.defaults`:
  `telemetry-target-down` (`<Service>TargetDown`, newest `up` of an endpoint is 0, `for` 5m) and
  `telemetry-check-failing` (`<Service>CheckFailing`, no successful tcpcheck / 2xx httpcheck probe
  in the window, `for` 5m), severity warning, labelled `service`. Each expands to one rule per
  system with `default_alerts` (`TelemetryAlertName`: UpperCamelCase of the service). They are
  env-scoped like the uptime defaults (`AlertInput.TelemetryTargets`, never written to the
  generated file), so a project with no telemetry writes a byte-identical catalog, and a static
  site still records them. A rule of the env's own that takes one's name is refused at shape
  time, naming `default_alerts = False` as the way to replace it.
- `forge obs telemetry [env]` (`forge.obs.telemetry/v1`) lists the env's targets and, from the
  local ClickStack, per signal the newest row and row count (`obsquery.TelemetryArrival`) and
  per scraped endpoint its newest `up` and samples (`obsquery.TelemetryScrapes`). Platform
  systems are read without the env predicate. A table the store lacks costs only its signal;
  an unreachable store or the cloud backend lists the targets with arrival `unknown`.

**F4 (forge's cluster node agent):**

- The agent record of a cluster carries that cluster's targets (`ObservabilityAgentSpec.Telemetry`,
  appended by `_tel_agent_spec` in `kcl/render.k`; absent when there are none, so the record and
  the agent render byte-identically). `RenderObservabilityAgent` builds them through
  `collector.Build` (`DiscoverNodePods`, `MountRoot = deploy.TelemetryMountRoot`, env identity
  from the record's env and part-of) and splices the pieces: `Logs.Preserve` after the
  `container` operator, `Logs.Processors` and `NodeMetricsProcessors` after k8sattributes, and
  `K8sAttributesLabels` into its extract. `exporterName()+"_telemetry_isolated"` is the isolated
  exporter (the destination, with Build's bounded in-memory queue).
- The identity processor also runs in every pushed (OTLP) pipeline, traces included, because they
  share k8sattributes and only it deletes the extracted selector labels. `IdentityProcessor` gained
  `trace_statements` for that.
- Declaring `Observability.telemetry_collector` moves every target off the agent: it is "the
  collector that scrapes this env's Telemetry, when it is not forge's agent". The render projects
  it as `output.telemetry_collector` for the NetworkPolicy rules.
- No singleton, so the agent REFUSES (render error) a cluster target it would pull at an
  `address`. It also refuses a target whose signal the env's observability does not ship. With a
  destination, profiles need `signals.profiles = True`; the agent already sends profiles there,
  and the feature gate follows that signal. It refuses an `optional` credential too, because the
  collector reads every credentials file at config load.
- Every referenced Secret is mounted from the agent's namespace, not optionally. The render refuses
  one that no `required_secrets`, `rendered_secrets` or `control_plane_secrets` entry supplies
  there (`_check_agent_telemetry`).
- The agent's ClusterRole is unchanged: it already lists and watches pods, namespaces and nodes, which is all
  pod discovery and `k8s_observer` need.
- NetworkPolicy (`internal/cluster/telemetry.go`): the collector for a cluster is the env's own
  collector, else the agent (`deploy.AgentCollectorRef`). A Workload target's rule joins its own
  policy (`deploy.Context.Telemetry`). A Manifests or Observability target with
  `network_policy = True` gets `collector.NetworkPolicies`, inserted after its Manifests group's
  last object with the group's owner and delivery unit (an Observability target: at the end of the
  stream). The agent namespace carries no NetworkPolicy, so its egress to the pulled ports is open.
