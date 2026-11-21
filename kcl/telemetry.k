"""
Forge KCL — declared telemetry for systems forge did not build
(docs/proposals/third-party-telemetry.md).

    forge.Telemetry    how one system's metrics, logs, traces and profiles
                       are collected, and the dashboards and alerts drawn
                       over them

It attaches in four places, and where it attaches decides what forge can
infer about the target:

    Workload.telemetry        the workload itself (kcl/workload.k)
    HostInfra.telemetry       a host-run engine (kcl/schema.k)
    Manifests.telemetry       a list: one raw group can hold several systems
    Observability.telemetry   what forge never declares as objects (a
                              HelmChart, an operator, a system outside the
                              bundle)

The render resolves every declaration into ONE normalized list,
`output.telemetry` (kcl/render.k `_render_telemetry`), which Go decodes into
`pkg/telemetry.Target`: the contract every collector consumes. forge ships no
per-system preset: every vendor fact (metric names, env keys, log formats)
is the project's own.
"""

import regex
import .dashspec
# Called only from telemetry_collector below, so the module still evaluates
# under a plugin-less runtime: a plugin import is resolved at the call.
import kcl_plugin.forge as forge_plugin

_TEL_NAME_RE: str = r"^[a-z0-9]([-a-z0-9_.]*[a-z0-9])?$"
# Collector durations: what Go's time.ParseDuration reads, restricted to ms/s/m/h.
_TEL_DURATION_RE: str = r"^([0-9]+(ms|s|m|h))+$"
_TEL_PROM_LABEL_RE: str = r"^[a-zA-Z_][a-zA-Z0-9_]*$"
_TEL_PORT_NAME_RE: str = r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
_TEL_RFC1123_RE: str = r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
# An unnamed scrape or check is identified by its index, so a name may not be all digits.
_TEL_DIGITS_RE: str = r"^[0-9]+$"
_TEL_ENV_KEY_RE: str = r"^[A-Za-z_][A-Za-z0-9_]*$"
# A field path into a parsed log record: dotted segments; a segment that holds a
# dot (or any character outside [A-Za-z0-9_$@-]) is written ["quoted"].
# Examples: "level", "@level", "record.error_severity", "t.$date", "[\"k8s.pod\"]".
_TEL_PATH_RE: str = r"^([A-Za-z0-9_$@-]+|\[\"[^\"]+\"\])(\.([A-Za-z0-9_$@-]+|\[\"[^\"]+\"\]))*$"

# The OpenTelemetry severities a log record can be given. pkg/telemetry
# Severities is the same list (TestKCLVocabularyMatches holds them equal).
TELEMETRY_SEVERITIES: [str] = ["trace", "debug", "info", "warn", "error", "fatal"]
# The facts an OtlpRef can name. Values are resolved per runtime at render.
# pkg/telemetry OTLPRefKeys is the same list.
OTLP_REF_KEYS: [str] = [
    "endpoint", "traces_endpoint", "metrics_endpoint", "logs_endpoint",
    "grpc_address", "protocol", "insecure", "service_name",
    "resource_attributes", "traces_sampler", "traces_sampler_arg",
]
# The pprof profile types a PprofScrape can pull. pkg/telemetry PprofTypes is
# the same list.
PPROF_TYPES: [str] = ["cpu", "heap", "allocs", "goroutine", "mutex", "block"]

# A duration's length in seconds (fractional for ms).
_tel_secs = lambda d: str -> float {
    sum([float(p[:-2]) / 1000.0 if p.endswith("ms") else float(p[:-1]) * {"s": 1, "m": 60, "h": 3600}[p[-1:]] \
         for p in regex.findall(d, r"[0-9]+(?:ms|s|m|h)")])
}

_tel_path_ok = lambda p: str -> bool {
    bool(regex.match(p, _TEL_PATH_RE))
}

# Every set path in paths is a field path.
_tel_paths_ok = lambda paths: [any] -> bool {
    all p in paths { not p or _tel_path_ok(p) }
}

_tel_port_ok = lambda port: any -> bool {
    (0 < port <= 65535) if typeof(port) == "int" else bool(regex.match(port, _TEL_PORT_NAME_RE))
}

# Every port a Telemetry names is a number: what a Manifests or Observability
# attachment requires, because it has no declared ports to resolve a name
# against.
_tel_numeric_ports = lambda t: Telemetry -> bool {
    _ports = [m.port for m in t.metrics] + [p.port for p in t.profiles] + [c.port for c in t.checks]
    all p in _ports { typeof(p) == "int" }
}

# The fields a self-targeting attachment (Workload, HostInfra) derives, so an
# author may not set them.
_tel_targets_self = lambda t: Telemetry -> bool {
    not bool(t.selector) and not bool(t.namespaces) and not bool(t.cluster) and not bool(t.address)
}

schema Telemetry:
    """Telemetry for a system forge did not build: how its metrics, logs,
    traces and profiles are collected, and the dashboards and alerts drawn
    over them. Every vendor fact (metric names, env keys, log formats,
    panels) is yours; forge knows none of them and ships no presets.

        forge.Workload {
            name = "temporal", image = "temporalio/server:1.26.2"
            ports = [{name = "metrics", port = 9090}]
            telemetry = forge.Telemetry {
                metrics = [forge.MetricsScrape {port = "metrics", allow = _TEMPORAL_ALLOW}]
                logs = forge.LogParse {format = "json", timestamp = forge.LogTimestamp {field = "ts", layout = "epoch_s"}}
                dashboards = [_temporal_dashboard]   # ds.Dashboard {...}
                alerts = [_temporal_errors]
            }
        }

    Every Telemetry also gets a signal-generic system dashboard
    (`dashboard`) and, with `default_alerts`, <Service>TargetDown and
    <Service>CheckFailing.

    Where it attaches decides what forge can infer:
      * `Workload.telemetry` and `HostInfra.telemetry`: the target IS that
        workload. service_name, cluster, namespace and the pod selector come
        from it, and a named `port` must be one of the workload's ports. A
        HostInfra's ports are named `server` (port), `monitor`
        (nats_monitor_port), `ui` (temporal_ui_port) and `metrics`
        (temporal_metrics_port). `selector`, `namespaces`, `cluster` and
        `address` are refused.
      * `Manifests.telemetry`: forge cannot see into raw objects, so
        `selector` (or `address`) is required and ports are numbers.
        `namespaces` defaults to [Manifests.namespace], and the cluster is
        the group's.
      * `Observability.telemetry`: for what forge never declares as objects
        (a HelmChart, an operator, a system outside this bundle).
        `service_name` and `selector` or `address` are required, and a
        `selector` needs the `cluster` whose collector scrapes it. Without a
        cluster, `address` is a host address `forge env up` reaches.

    Identity: every signal collected from a matching pod or address (scraped
    metrics, its container logs, pulled profiles) is stamped
    `service.name = service_name`, plus the env's resource attributes when
    `scope = "project"`. Dashboards and alerts filter on that.

    Collection follows the env's runtime: forge's node agent on clusters it
    renders an agent for, the local telemetry agent and ClickStack under
    `forge env up`, and the platform on `forge.OnHosted` (when the control
    plane advertises it). A project that runs its own collector names it in
    `Observability.telemetry_collector`.
    """
    # The service.name every signal of this system carries. Defaults to the
    # Workload/HostInfra/Manifests name; required on Observability.telemetry.
    # Unique within the env.
    service_name?: str
    # What the data is. "project": stamped with the env's identity.
    # "platform": no env identity (a shared database, a bus, the telemetry
    # pipeline itself). It grants no access.
    scope: "project" | "platform" = "project"
    # Observability.telemetry only: the cluster whose collector scrapes it.
    cluster?: str
    # Manifests/Observability only: exact namespace names to discover pods in.
    namespaces?: [str]
    # Manifests/Observability only: pod labels that must ALL match (exact).
    selector?: {str:str}
    # Manifests/Observability only, instead of a selector: one fixed host (a
    # Service DNS name, or a host address under env up). Ports come from each
    # scrape/check.
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
    # Your dashboards for this system (forge.dashboards/v1, typed: `import
    # forge.dashspec as ds`). Each is written as
    # deploy/observability/dashboards/<project>-<id>_gen.json and ships in
    # the release like every generated dashboard; a hand file
    # <project>-<id>.json replaces it. An `id` is unique within the project,
    # and a dashboard declared in several envs must be the same in each. An
    # unset `scope` takes this Telemetry's.
    dashboards: [dashspec.Dashboard] = []
    # Your alert rules. They join Observability.alerts with labels.service =
    # service_name unless they set it, so the env needs observability on.
    alerts: [AlertRule] = []
    # <Service>TargetDown (each scrape's `up` is 0 for 5m) and
    # <Service>CheckFailing (each check fails for 5m), at severity warning.
    default_alerts: bool = True

    check:
        not service_name or regex.match(service_name, _TEL_NAME_RE), \
            "Telemetry.service_name '${service_name}' must match ${_TEL_NAME_RE}"
        not (selector and address), "Telemetry: selector and address are exclusive"
        not selector or all k, v in selector { k and v }, "Telemetry.selector keys and values must be non-empty"
        not namespaces or all n in namespaces { regex.match(n, _TEL_RFC1123_RE) }, "Telemetry.namespaces must be RFC-1123 labels"
        not container or regex.match(container, _TEL_RFC1123_RE), "Telemetry.container must be an RFC-1123 label"
        metrics or otlp or logs or profiles or checks or dashboards or alerts, \
            "Telemetry declares nothing to collect, draw or alert on: set metrics, otlp, logs, profiles, checks, dashboards or alerts"
        len([d.id for d in dashboards]) == len({d.id: 1 for d in dashboards}), \
            "Telemetry.dashboards ids must be unique"
        len([m.name for m in metrics if m.name]) == len({m.name: 1 for m in metrics if m.name}), \
            "Telemetry.metrics names must be unique"
        len([m for m in metrics if not m.name]) <= 1, \
            "Telemetry.metrics: several scrapes need a `name` each but one (it suffixes the job name)"
        len([c.name for c in checks if c.name]) == len({c.name: 1 for c in checks if c.name}), \
            "Telemetry.checks names must be unique"
        len([c for c in checks if not c.name]) <= 1, \
            "Telemetry.checks: several checks need a `name` each but one"
        len({r.alert: 1 for r in alerts}) == len(alerts), \
            "Telemetry.alerts names an alert twice: an alert name is unique within an env"

schema MetricsScrape:
    """One Prometheus-exposition endpoint, pulled by the collector.

    Bounded by construction: `sample_limit` fails the scrape (up = 0, visible
    and alerted) rather than storing an unbounded series set, and `allow`
    keeps only the names you draw or alert on. A scrape without `allow`
    keeps every name, and a hosted env refuses it.

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
    # cumulative counters. They are converted to monotonic sums, so
    # rate/increase work.
    counters?: str
    # Labels removed at the scrape: the per-user/per-key/per-id ones that explode.
    drop_labels: [str] = []
    sample_limit: int = 5000
    label_limit: int = 30
    label_value_length_limit: int = 256
    # Authorization: Bearer, read from a FILE the collector mounts, never an
    # env var. On a cluster, `name` is a Secret in the COLLECTOR's namespace.
    bearer_token?: SecretRef
    tls?: ScrapeTLS

    check:
        not name or regex.match(name, _TEL_RFC1123_RE), "MetricsScrape.name '${name}' must be an RFC-1123 label"
        not name or not regex.match(name, _TEL_DIGITS_RE), \
            "MetricsScrape.name '${name}' needs a letter: an unnamed scrape's job name ends in its index, so all-digit names are reserved"
        _tel_port_ok(port), "MetricsScrape.port must be 1-65535 or an IANA port name"
        path.startswith("/"), "MetricsScrape.path must start with /"
        regex.match(interval, _TEL_DURATION_RE) and regex.match(timeout, _TEL_DURATION_RE), \
            "MetricsScrape interval/timeout must be durations (e.g. 30s, 1m)"
        not regex.match(interval, _TEL_DURATION_RE) or _tel_secs(interval) >= 10, \
            "MetricsScrape.interval floor is 10s"
        not (regex.match(interval, _TEL_DURATION_RE) and regex.match(timeout, _TEL_DURATION_RE)) or _tel_secs(timeout) <= _tel_secs(interval), \
            "MetricsScrape.timeout must not exceed interval"
        1 <= sample_limit <= 200000, "MetricsScrape.sample_limit must be 1-200000"
        0 <= label_limit <= 1000, "MetricsScrape.label_limit must be 0-1000"
        0 <= label_value_length_limit <= 65536, "MetricsScrape.label_value_length_limit must be 0-65536"
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
    `env` adds the system's OWN key names, each a literal or an OTLP fact
    resolved per runtime. The values are endpoints, never credentials:

        otlp = forge.OtlpPush {
            signals = ["traces"]
            env = {
                ZITADEL_INSTRUMENTATION_TRACE_EXPORTER_TYPE = "grpc"   # a literal
                ZITADEL_INSTRUMENTATION_TRACE_EXPORTER_ENDPOINT = forge.OtlpRef {key = "grpc_address"}
            }
        }

    A fact this runtime has no value for is left out of the env (every fact,
    when the env's observability is off); a hosted workload's facts are the
    platform's to fill. Raw Manifests get no env injection (forge cannot see
    their pods): use `forge.otlp_value(key, observability, cluster)` to
    interpolate the same facts into your objects.
    """
    # Which signals it pushes.
    signals: [str] = ["traces"]
    # The protocol it speaks; `endpoint` and `protocol` resolve to match.
    protocol: "grpc" | "http/protobuf" = "http/protobuf"
    # Head-sampling ratio, rendered into the standard sampler env.
    sampler_ratio?: float
    env: {str: str | OtlpRef} = {}

    check:
        all s in signals { s in ["traces", "metrics", "logs"] }, "OtlpPush.signals: traces, metrics, logs"
        not sampler_ratio or 0 < sampler_ratio <= 1, "OtlpPush.sampler_ratio must be in (0, 1]"
        all k, _ in env { regex.match(k, _TEL_ENV_KEY_RE) }, "OtlpPush.env keys must be env var names"

schema OtlpRef:
    """An OTLP fact resolved per runtime: `key` is one of OTLP_REF_KEYS.
    endpoint is a URL with a scheme; grpc_address is host:port; insecure is
    "true" or "false"."""
    key: str

    check:
        key in OTLP_REF_KEYS, "OtlpRef.key '${key}' must be one of ${OTLP_REF_KEYS}"

otlp_env = lambda -> {str:OtlpRef} {
    """The standard OpenTelemetry SDK env as OtlpRefs, for a workload whose
    standard injection you replaced:

        env = forge.otlp_env() | {OTEL_TRACES_SAMPLER = "parentbased_traceidratio"}
    """
    {
        OTEL_EXPORTER_OTLP_ENDPOINT = OtlpRef {key = "endpoint"}
        OTEL_EXPORTER_OTLP_PROTOCOL = OtlpRef {key = "protocol"}
        OTEL_SERVICE_NAME = OtlpRef {key = "service_name"}
        OTEL_RESOURCE_ATTRIBUTES = OtlpRef {key = "resource_attributes"}
    }
}
# otlp_value(key, observability, cluster) -> str: the literal for a raw
# object. Defined in render.k beside _obs_env, so the facts have one source.

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
            severity = forge.LogSeverity {field = "lvl", mapping = {debug = ["DBG", "TRC"], info = ["INF"], warn = ["WRN"], error = ["ERR"], fatal = ["FTL"]}}
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
        # `regex` here is the field, which shadows the module: paths are
        # matched through _tel_path_ok.
        (format == "regex") == bool(regex), "LogParse.regex is set exactly when format is regex"
        _tel_paths_ok([body, trace_id, span_id] + drop), \
            "LogParse field paths (body, trace_id, span_id, drop) must be dotted paths: see the docstring"

schema LogTimestamp:
    field: str
    # A strptime layout, or "rfc3339", "epoch_s", "epoch_ms", "epoch_ns".
    layout: str = "rfc3339"

    check:
        regex.match(field, _TEL_PATH_RE), "LogTimestamp.field '${field}' is not a field path"
        layout, "LogTimestamp.layout is required"

schema LogSeverity:
    """Which field holds the level, and how its values map to OTel severities.
    Unmapped values keep their text and get severity number unspecified.
    Numeric levels map by their string form (klog's v:
    `mapping = {info = ["0", "1", "2"], debug = ["3", "4", "5"]}`)."""
    field: str
    # severity -> the field values that mean it. (`map` is a KCL keyword.)
    mapping: {str:[str]} = {}

    check:
        regex.match(field, _TEL_PATH_RE), "LogSeverity.field '${field}' is not a field path"
        all k, _ in mapping { k in TELEMETRY_SEVERITIES }, "LogSeverity.mapping keys must be ${TELEMETRY_SEVERITIES}"

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
        severity in TELEMETRY_SEVERITIES, "LogSeverityRule.severity must be one of ${TELEMETRY_SEVERITIES}"
        not from_severity or from_severity in TELEMETRY_SEVERITIES, "LogSeverityRule.from_severity must be one of ${TELEMETRY_SEVERITIES}"
        body_match or (field and value_match), "LogSeverityRule needs body_match, or field + value_match"
        bool(field) == bool(value_match), "LogSeverityRule: field and value_match go together"
        not field or regex.match(field, _TEL_PATH_RE), "LogSeverityRule.field '${field}' is not a field path"

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
        _tel_port_ok(port), "PprofScrape.port must be 1-65535 or an IANA port name"
        path_prefix.startswith("/"), "PprofScrape.path_prefix must start with /"
        types and all t in types { t in PPROF_TYPES }, "PprofScrape.types must be a non-empty subset of ${PPROF_TYPES}"
        len({t: 1 for t in types}) == len(types), "PprofScrape.types names a type twice"
        regex.match(interval, _TEL_DURATION_RE) and _tel_secs(interval) >= 60, "PprofScrape.interval must be a duration of at least 1m"
        1 <= cpu_seconds <= 30, "PprofScrape.cpu_seconds must be 1-30"
        not tls or scheme == "https", "PprofScrape.tls needs scheme https"
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
        not name or regex.match(name, _TEL_RFC1123_RE), "HealthCheck.name '${name}' must be an RFC-1123 label"
        not name or not regex.match(name, _TEL_DIGITS_RE), \
            "HealthCheck.name '${name}' needs a letter: an unnamed check's receiver name ends in its index, so all-digit names are reserved"
        _tel_port_ok(port), "HealthCheck.port must be 1-65535 or an IANA port name"
        kind == "http" or (path == "/" and scheme == "http"), "HealthCheck.path and scheme are http-only"
        path.startswith("/"), "HealthCheck.path must start with /"
        regex.match(interval, _TEL_DURATION_RE) and _tel_secs(interval) >= 10, "HealthCheck.interval must be a duration of at least 10s"

schema SystemDashboard:
    """The signal-generic dashboard forge draws for every Telemetry: one
    section per signal declared (collection health, resources, log volume
    and errors, span rate/error/latency, check status, a profile link). It
    knows no vendor metric; your `dashboards` go beside it. It is written as
    deploy/observability/dashboards/<project>-telemetry-<service>_gen.json,
    and a hand file `<project>-telemetry-<service>.json` replaces it."""
    enabled: bool = True
    title?: str

schema TelemetryCollector:
    """The collector that scrapes this env's Telemetry, when it is not
    forge's agent: the namespace and pod labels its NetworkPolicy admits,
    and the OTLP endpoints `forge.otlp_value()` and a workload's
    `OtlpPush.env` facts resolve to on a cluster."""
    namespace: str
    pod_labels: {str:str}
    # A URL with a scheme (http://collector.ns.svc:4318).
    otlp_http_endpoint?: str
    # host:port (collector.ns.svc:4317).
    otlp_grpc_address?: str

    check:
        regex.match(namespace, _TEL_RFC1123_RE), "TelemetryCollector.namespace must be an RFC-1123 label"
        pod_labels and all k, v in pod_labels { k and v }, "TelemetryCollector.pod_labels must name at least one non-empty label"
        not otlp_http_endpoint or regex.match(otlp_http_endpoint, r"^https?://"), "TelemetryCollector.otlp_http_endpoint must be a URL with a scheme"
        not otlp_grpc_address or not regex.match(otlp_grpc_address, r"^[a-z]+://"), "TelemetryCollector.otlp_grpc_address is host:port, with no scheme"

# ---------------------------------------------------------------------------
# A project's own collector (docs/proposals/third-party-telemetry.md, "Own
# collector"). A project that runs its own OpenTelemetry Collector in KCL
# (declared as Observability.telemetry_collector) renders the SAME collector
# configuration forge's agents render, from the same declarations, through the
# one translator (pkg/telemetry/collector.Build, reached through
# kcl_plugin.forge.telemetry_collector):
#
#     import forge
#
#     _targets = forge.telemetry_targets(_bundle)
#     _tel = forge.telemetry_collector(_targets, forge.TelemetryCollectorOptions {
#         runtime = "cluster"
#         cluster = "prod-hub"
#         discovery = "node-pods"
#         mount_root = "/etc/telemetry/secrets"
#         processors = ["memory_limiter"]
#         post_processors = ["batch"]
#         metrics_exporters = ["otlp_http/platform"]
#         collector = _bundle.observability.telemetry_collector
#     })
#     # Merge _tel.receivers / processors / exporters / pipelines /
#     # extensions into the agent's config, list _tel.service_extensions in
#     # service.extensions, splice _tel.logs.preserve into the filelog
#     # operators and _tel.logs.processors into the logs pipeline after
#     # k8sattributes, append _tel.k8sattributes_labels to k8sattributes'
#     # extract.labels, and add _tel.network_policies to Bundle.manifests.
#
# The collector must mount every Secret a target's bearer_token or tls.ca
# names, NOT optional, at <mount_root>/<secret>: the prometheus receiver fails
# its config load on a missing credentials file.
# ---------------------------------------------------------------------------

telemetry_targets = lambda b: Bundle -> [any] {
    """The env's telemetry targets, normalized as the render's
    `output.telemetry` is (one entry per forge.Telemetry, resolved against
    where it attaches): the list forge.telemetry_collector takes. Pass the
    bundle the env renders, minus the collector's own objects (those consume
    this list)."""
    _render_telemetry(b)
}

schema TelemetryIsolatedExporter:
    """The exporter Telemetry.isolated targets' metrics go to: your exporter's
    config, to which forge adds a bounded in-memory sending_queue that drops
    on overflow and a bounded retry, so monitoring the telemetry pipeline
    never wedges on it."""
    # The exporter's component ID, e.g. "otlp_http/selfmon".
    name: str
    # The exporter's config, minus sending_queue and retry_on_failure.
    config: {str:any} = {}
    # The queue's size in requests (forge's default: 200).
    queue_size?: int
    # How long a batch is retried before it is dropped (forge's default: 2m).
    retry_max_elapsed?: str

    check:
        name, "TelemetryIsolatedExporter.name is the exporter's component ID"
        not queue_size or queue_size > 0, "TelemetryIsolatedExporter.queue_size must be positive"
        not retry_max_elapsed or regex.match(retry_max_elapsed, _TEL_DURATION_RE), "TelemetryIsolatedExporter.retry_max_elapsed must be a duration like 2m"

schema TelemetryCollectorOptions:
    """What forge.telemetry_collector needs to know about the collector it
    renders for (pkg/telemetry/collector.Options)."""
    # Where the collector runs. It collects only the targets it reaches: on
    # "host", host and compose targets; on "cluster", that cluster's.
    runtime: "cluster" | "host"
    # The kubectl context of the targets to keep, on "cluster". Unset keeps
    # every cluster target.
    cluster?: str
    # What the collector pulls from, on "cluster". "all": every pod and
    # address (one collector per cluster). "node-pods": only this node's pods
    # (a DaemonSet; address targets are left to an "addresses" collector).
    # "addresses": only address targets (the singleton beside a DaemonSet).
    discovery: "all" | "node-pods" | "addresses" = "all"
    # The env var holding the node's name, for "node-pods" (default
    # K8S_NODE_NAME, from the downward API's spec.nodeName).
    node_name_env?: str
    # Where the collector mounts each referenced Secret, as
    # <mount_root>/<secret>. Required when a target declares a bearer_token
    # or a tls.ca.
    mount_root?: str
    # The env's identity (e.g. deployment.environment.name), stamped on every
    # signal of a scope "project" target, never on a "platform" one.
    resource_attrs: {str:str} = {}
    # Render the pprof pulls. They are alpha in the pinned collector, which
    # then needs --feature-gates=service.profilesSupport.
    enable_profiles: bool = False
    # Processors run first in every pipeline rendered (e.g. memory_limiter).
    # Defined by you.
    processors: [str] = []
    # Processors run last in every metrics pipeline (e.g. batch). Defined by
    # you.
    post_processors: [str] = []
    # The exporters every non-isolated target's metrics and checks go to.
    # Defined by you.
    metrics_exporters: [str] = []
    # The exporters every target's profiles go to. Defined by you.
    profiles_exporters: [str] = []
    # Where isolated targets' metrics go. Required when a target is isolated.
    isolated?: TelemetryIsolatedExporter
    # The collector the NetworkPolicies admit, normally the env's
    # Observability.telemetry_collector. Set, the fragment carries the
    # standalone NetworkPolicy of every target that asks for one
    # (network_policy = True); unset, it carries none. Set it on ONE of a
    # pair of collectors that split discovery.
    collector?: TelemetryCollector

schema TelemetryCollectorLogs:
    """What to splice into your collector's logs pipeline."""
    # One stanza operator for your filelog receiver, keeping a line's raw
    # text for the parse: after the `container` operator on a cluster, after
    # the file-name-to-service.name move on a host. None when no target
    # parses logs.
    preserve?: {str:any}
    # The processors to run in your logs pipeline after k8sattributes, in
    # order (defined in TelemetryCollectorFragment.processors).
    processors: [str] = []

schema TelemetryCollectorFragment:
    """The collector configuration forge.telemetry_collector rendered.
    extensions, receivers, processors, exporters and pipelines are whole
    components and pipelines, merged into your config as they are; the rest
    are pieces you splice into what you already own."""
    extensions: {str:any} = {}
    receivers: {str:any} = {}
    processors: {str:any} = {}
    exporters: {str:any} = {}
    pipelines: {str:any} = {}
    # The extensions to list in service.extensions.
    service_extensions: [str] = []
    logs: TelemetryCollectorLogs = TelemetryCollectorLogs {}
    # Processors for your node-metrics (kubeletstats) pipeline, after
    # k8sattributes.
    node_metrics_processors: [str] = []
    # Entries to append to k8sattributes' extract.labels.
    k8sattributes_labels: [any] = []
    # NetworkPolicy objects, for Bundle.manifests (see
    # TelemetryCollectorOptions.collector).
    network_policies: [any] = []
    # What was skipped, and why.
    warnings: [str] = []

telemetry_collector = lambda targets: [any], opts: TelemetryCollectorOptions -> TelemetryCollectorFragment {
    """The collector configuration for targets (forge.telemetry_targets),
    rendered by pkg/telemetry/collector.Build: the same translation forge's
    own agents use. A target Build refuses fails the render, naming it."""
    _iso = opts.isolated
    _c = opts.collector
    _r = forge_plugin.telemetry_collector(targets, {
        "runtime": opts.runtime
        "cluster": opts.cluster or ""
        "discovery": opts.discovery
        "node_name_env": opts.node_name_env or ""
        "mount_root": opts.mount_root or ""
        "resource_attrs": opts.resource_attrs
        "enable_profiles": opts.enable_profiles
        "processors": opts.processors
        "post_processors": opts.post_processors
        "metrics_exporters": opts.metrics_exporters
        "profiles_exporters": opts.profiles_exporters
        "isolated": {
            "name": _iso.name
            "config": _iso.config
            "queue_size": _iso.queue_size or 0
            "retry_max_elapsed": _iso.retry_max_elapsed or ""
        } if _iso else None
        "collector": {"namespace": _c.namespace, "pod_labels": _c.pod_labels} if _c else None
    })
    TelemetryCollectorFragment {
        extensions = _r.extensions
        receivers = _r.receivers
        processors = _r.processors
        exporters = _r.exporters
        pipelines = _r.pipelines
        service_extensions = _r.service_extensions
        logs = TelemetryCollectorLogs {
            preserve = _r.logs.preserve
            processors = _r.logs.processors
        }
        node_metrics_processors = _r.node_metrics_processors
        k8sattributes_labels = _r.k8sattributes_labels
        network_policies = _r.network_policies
        warnings = _r.warnings
    }
}
