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
exported as an OTel resource attribute by Reliant. The platform contract sets
`deployment.environment.name` through `OTEL_RESOURCE_ATTRIBUTES`, which the
Forge runtime reads once its pin includes forge#604/#617. Until then the
attribute is absent.

## Configuration

`OTEL_ENABLED=true` is the explicit rollout gate. A production deployment with
`ENVIRONMENT=production` and the switch off boots without an OTLP endpoint; once
the switch is on, a missing endpoint fails startup. This preserves the current
hosted rollout while preventing an enabled deployment from silently becoming
Prometheus-only.

`OTEL_EXPORTER_OTLP_ENDPOINT` must be an `http(s)` collector base URL (or bare
`host:port`) with no `/v1/<signal>` path. Reliant never infers a wire protocol
from the port: `:4317`, `:4318` and `https://…:443` are all accepted, and
protocol selection (`OTEL_EXPORTER_OTLP_PROTOCOL`) belongs to the Forge runtime.
Startup does not probe the collector, so a later collector outage does not crash
a serving process; exporter failures surface during flush.

Reliant's `/metrics` serves its custom registry plus the OTel instrumentation
metrics (otelhttp, otelconnect) that Forge's Prometheus reader records on the
default registry. The default registry's Go/process collectors are filtered out
because the custom registry already exposes them. `PROMETHEUS_ENABLED=false`
makes the endpoint return 404.

Structured logs stay on stdout and are collected by the runtime collector agent;
Reliant does not install an OTel logs SDK.

Sentry remains an independent error/crash pipeline and is initialized and
flushed independently of OTel.

## Propagation

`observability.Init` always installs the W3C TraceContext+Baggage propagator,
even when export is disabled, so NATS and Connect propagation keep working in
mixed deployments. (TODO(forge#617): remove the reliant-side install once the
forge pin includes #617.)

The Connect server uses one `otelconnect` server interceptor; Forge's generic
tracing interceptor is intentionally not enabled there, preventing duplicate
server spans.

Remote trace context is trusted only on the public API server, for
browser-to-API trace continuity. The daemon-facing server (daemon-gateway)
does **not** trust it: daemons run on user machines, so a caller-supplied
`traceparent` starts a new root span and is recorded only as a span link.
Outbound Connect clients should use `observe.NewClientStack`; plain outbound
HTTP clients use `otelhttp.NewTransport` over their existing transport.

## Known gaps

- Temporal: no `ContextPropagator`/OTel interceptor, so API → workflow →
  activity are separate traces.
- NATS: only the inject/extract helpers in `internal/observability/nats.go`
  exist; not every publisher/consumer uses them yet.
- Logs carry no `trace_id`/`span_id` (no slog hook yet).
