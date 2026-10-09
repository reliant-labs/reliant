# Backend observability

Reliant backend binaries use `forge/pkg/observe` for OpenTelemetry traces and
metrics. The application only knows a vendor-neutral OTLP/gRPC collector
endpoint; ClickStack, HyperDX, and any other storage or UI remain deployment
concerns.

## Identity

Each process has a stable resource identity:

| Binary | `service.name` |
| --- | --- |
| API server | `reliant-api-server` |
| Temporal worker | `reliant-temporal-worker` |
| Daemon gateway | `daemon-gateway` |

`service.version` comes from Reliant build metadata and `service.instance.id`
comes from the hostname. `Config.Environment` is resolved from
`SENTRY_ENVIRONMENT`, then `ENVIRONMENT`, then legacy `RELIANT_ENV`; it is not
currently exported as an OTel resource attribute. Forge's current
`observe.Setup` API has no typed resource-attribute extension, so a Forge change
is required before Reliant can safely add `deployment.environment.name`.

## Configuration

`OTEL_ENABLED=true` is the explicit rollout gate. A production deployment with
`ENVIRONMENT=production` and the switch off boots without an OTLP endpoint; once
the switch is on, a missing endpoint fails startup. This preserves the current
hosted rollout while preventing an enabled deployment from silently becoming
Prometheus-only.

`OTEL_EXPORTER_OTLP_ENDPOINT` must be an OTLP/gRPC collector endpoint on port
`4317` (for example `http://otel-collector:4317`), and
`OTEL_EXPORTER_OTLP_PROTOCOL`, when set, must be `grpc`. OTLP/HTTP
(`http/protobuf`), port `4318`, and `/v1/*` paths are rejected. Startup does not probe the collector, so a later
collector outage does not crash a serving process; exporter failures surface
during flush.

Reliant retains its existing Prometheus endpoint for custom application metrics;
`PROMETHEUS_ENABLED=false` makes it return 404. Forge's returned Prometheus
handler is not mounted because it owns an independent registry, so Reliant's
`/metrics` does **not** expose OTel-instrumented runtime metrics yet. They still
export over Forge's OTLP periodic metric reader when OTLP is enabled. Merging
those Prometheus registries requires a Forge API follow-up.

Structured logs stay on stdout and are collected by the runtime collector agent;
Reliant does not install an OTel logs SDK.

Sentry remains an independent error/crash pipeline and is initialized and
flushed independently of OTel.

## Propagation

The Connect server uses one `otelconnect` server interceptor with trusted
internal trace context. Forge's generic tracing interceptor is intentionally
not enabled there, preventing duplicate server spans. Outbound Connect clients
should use `observe.NewClientStack`; plain outbound HTTP clients use
`otelhttp.NewTransport` over their existing transport.
