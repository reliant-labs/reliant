# Backend observability

Internal note on how Reliant's own server binaries are instrumented. The
public page on observability for apps users deploy is
`docs/forge/observability.mdx`.

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
`ENVIRONMENT` (`ObservabilityConfig.environment`), then the root config's
`RELIANT_ENV`, then `development`; it is not
exported as an OTel resource attribute by Reliant. The platform contract sets
`deployment.environment.name` through `OTEL_RESOURCE_ATTRIBUTES`, which forge's
KCL renders for every workload and the Forge runtime merges into the resource.
`OTEL_SERVICE_NAME`, when set, overrides the compiled-in name above; forge
derives it from the workload name, which matches that table.

## Configuration

Export follows the standard OTel environment, which the Forge runtime owns: a
process exports traces and metrics whenever `OTEL_EXPORTER_OTLP_ENDPOINT` is
set (an empty value counts as unset), and `OTEL_SDK_DISABLED=true` turns export
off. To keep a deployment's export off, leave the endpoint unset.

`OTEL_ENABLED=true` no longer gates export by itself: it makes a missing
endpoint fail startup, so a deployment that means to export cannot silently
become Prometheus-only.

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

There is no Sentry: server errors reach ClickStack with the rest of the logs and
traces.

## Propagation

Forge's `observe.Setup` (called by `observability.Init`) always installs the
W3C TraceContext+Baggage propagator, even when export is disabled, so NATS and
Connect propagation keep working in mixed deployments.

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

- Temporal: the worker's `NewOTelWorkerInterceptor` (`internal/observability/temporal.go`)
  makes workflow and activity spans, but there is no `ContextPropagator`, so
  API → workflow → activity are still separate traces.
- NATS: the inject/extract helpers in `internal/observability/nats.go` exist,
  but no publisher or consumer calls them yet.
- Only the daemon's logs carry `trace_id`/`span_id` (`observe.NewTraceHandler`
  is installed with the daemon's identity fields); the server roles' logs do not.
