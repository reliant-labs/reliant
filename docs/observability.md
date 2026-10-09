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
comes from the hostname. Forge's current `observe.Setup` API does not accept
`deployment.environment`; Forge should add a typed resource-attribute extension
before Reliant reports that attribute through the shared runtime.

## Configuration

`OTEL_EXPORTER_OTLP_ENDPOINT` is the collector endpoint. Set `OTEL_ENABLED=false`
to explicitly disable OTLP in development or tests. Production (`RELIANT_ENV`
`production` or `prod`) always requires a non-empty endpoint and fails startup
when it is missing. Startup does not probe the collector, so a later collector
outage does not crash a serving process; exporter failures surface during flush.

Reliant retains its existing Prometheus endpoint for application metrics.
Forge's OTel Prometheus reader supplies OTel metrics separately, avoiding
registration conflicts. Structured logs stay on stdout and are collected by the
runtime collector agent; Reliant does not install an OTel logs SDK.

Sentry remains an independent error/crash pipeline and is initialized and
flushed independently of OTel.

## Propagation

The Connect server uses one `otelconnect` server interceptor with trusted
internal trace context. Forge's generic tracing interceptor is intentionally
not enabled there, preventing duplicate server spans. Outbound Connect clients
should use `observe.NewClientStack`; plain outbound HTTP clients use
`otelhttp.NewTransport` over their existing transport.
