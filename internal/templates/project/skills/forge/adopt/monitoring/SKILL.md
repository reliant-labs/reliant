---
name: adopt/monitoring
description: Instrument an existing app, in any language, to the telemetry shape forge reads — resource attributes, one JSON log line per request, request id, one server span, RED metrics, recovery, /healthz and /readyz — with forge's own Go code as the reference to translate and initNodeTelemetry for Node. Where the data can go today, how to verify it, and when the hosted dashboards light up.
emit: both
applies: any
---

# Adopt: monitoring, the hook

Logs, traces and RED metrics (rate, errors, duration) for the app the user
already runs, with no change to how it deploys. Lead with it for most teams: it
is useful on day one, it only emits telemetry, and every later piece (release
health, alerts, deploy markers) reads it. Part of `adopt`; load that for the
survey, the rubric and the phrasing template.

**Estimate.** ~25 min agent for the first service, +10 per further service in
the same framework, +25 per further language; −10 when an OTel SDK is already
there. ~5 min of the user's time to set env vars. Verify ~10 min.

## Where the data goes — say which you mean

| Destination | Status | Notes |
|---|---|---|
| The user's backend or an OpenTelemetry Collector (Datadog, Grafana, Honeycomb, …) | **today** | standard `OTEL_*` environment; nothing forge-specific |
| Reliant's hosted store and the dashboards at `https://forge.reliantlabs.io/forge/p/<project>/observability` | **today, once forge deploys the app** | hosting injects the endpoint, token and `OTEL_SERVICE_NAME` into every workload it runs; sign-in required. That is Tier 2 of `adopt/deploy` |
| forge's local ClickStack | **today, in a forge dev env** | `forge env up dev` with the env's Bundle declaring `observability` (`observability` skill) |

**Coming soon** (do not run or promise): an `obs init` command, user-minted ingest
tokens and a public OTLP endpoint, so an app forge does **not** deploy reaches
the hosted dashboards; `obs check` on an inferred deploy marker. Until then,
for such an app: instrument now to the shape below, send it where the user
already looks, and repointing later is three environment variables.

## The contract: keep these names

Translate to the project's language and framework and **keep the attribute
names, log keys and metric names**: forge's queries, issue grouping and release
health read exactly these.

**1. Resource identity.** Set on every service, where the deploy config already
sets env vars:

```bash
OTEL_SERVICE_NAME=checkout-api                       # service.name, one per deployable
OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=prod,service.namespace=shop,service.version=<git sha or release>
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318   # base URL; /v1/<signal> is appended
OTEL_EXPORTER_OTLP_HEADERS=authorization=<token>     # a secret: the user places it
```

`service.name`, `deployment.environment.name` and `service.version` matter
most: environments are told apart by the second, a release is judged by the
third. `OTEL_SDK_DISABLED=true` turns every export off.

**2. One log path.** JSON lines on stdout collected by the platform, **or** logs
over OTLP — never both, or every line arrives twice.

**3. One structured log line per request.** forge's Go server writes these keys:

| Key | Meaning |
|---|---|
| `time`, `level`, `msg` | `msg` is `rpc completed` / `rpc failed` (RPC) or `http request` (plain HTTP) |
| `procedure` (RPC) or `method`, `path`, `status` (HTTP) | what was called |
| `duration` | Go's slog writes nanoseconds |
| `request_id` | the correlation id (4) |
| `trace_id`, `span_id` | the active span |
| `error`, `error_class`, `code`, `cause` | on failure; `error_class` is `server`, `user` or `canceled` |
| `exception.type`, `exception.message`, `exception.stacktrace` | ERROR records; issue grouping reads them |

Level policy: a **server fault** (bug, dependency down) at **ERROR**; a **user
error** (not found, invalid argument, permission denied) or a client cancel at
**INFO**, still counted in a metric. Then "alert on `level=ERROR`" is correct: a
total database outage trips it, a typo in a URL does not.

**4. Request id.** Header `X-Request-Id`: adopt the inbound value or mint one,
put it in the request context so every log line carries it, echo it on the
response.

**5. One server span per request.** For RPC, the span is named by the full
procedure (`/shop.v1.Orders/Get`); for HTTP use OpenTelemetry's HTTP
instrumentation, named by the **route template** (`GET /orders/:id`), never the
raw path. On error, record the exception and set the status to error.
Propagate W3C `traceparent` on every outbound call.

**6. RED metrics.** forge's Connect servers record `rpc.server.calls`
(counter), `rpc.server.errors` (counter, extra attributes `code` and
`error_class`) and `rpc.server.duration` (histogram, **seconds**), all with
`procedure`. For an HTTP service, emit OpenTelemetry's HTTP semantic-convention
metrics (`http.server.request.duration`). Keep attribute values from closed
sets: never a message or an id.

**7. Recovery.** The outermost layer catches a panic or uncaught exception,
logs it at ERROR with the `exception.*` keys, and answers a clean internal
error.

**8. Probes.** `GET /healthz` is **liveness only**: a static 200 while the
process runs, never a dependency check (that restarts every replica during a
database outage). `GET /readyz` answers 200 when every dependency check passes
and 503 otherwise, with a body that names the check and **never the error
text**: `{"status":"not_ready","checks":[{"name":"database","ok":false}]}`
(`status` is `ready`, `not_ready` or `draining`).

## The reference: forge's own Go

Real calls from `forge/pkg` (`forge project libraries observe middleware
serverkit` prints every signature). A **Go** service can import them as they
are (`adopt/libraries`); for every other language they are the specification to
port.

```go
shutdown, metricsHandler, err := observe.Setup(ctx, observe.Config{
	ServiceName:    "checkout-api", // OTEL_SERVICE_NAME wins
	ServiceVersion: version,
})
defer shutdown(ctx)

logger := slog.New(observe.NewErrorClassHandler(observe.NewTraceHandler(
	slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))))
slog.SetDefault(logger)
mux.Handle("/metrics", metricsHandler)
```

`Setup` installs the W3C propagator even when nothing is exported, so an
inbound `traceparent` is honoured. `NewTraceHandler` adds `trace_id`/`span_id`
to every record made with a span's context, and mirrors WARN/ERROR records onto
the span as `log.warn`/`log.error` events.

A Connect service gets recovery, request id, logging, tracing and metrics in
one call; order matters, and it is this one:

```go
interceptors := observe.DefaultMiddlewares(observe.DefaultMiddlewareDeps{
	Logger: logger, Tracer: otel.Tracer("checkout-api"), Meter: otel.Meter("checkout-api"),
})
path, h := checkoutv1connect.NewOrdersHandler(svc, connect.WithInterceptors(interceptors...))
```

A plain `net/http` service: `middleware.RequestIDMiddleware()(middleware.HTTPStack(logger, nil)(mux))`
logs `http request` with `method`, `path`, `status`, `duration`, `request_id`,
at ERROR when the status is 500 or more (it also writes one `msg=audit`
record per request: recovery, logging and audit are its three layers).
Outbound calls:
`observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 10 * time.Second})`.
Probes: `serverkit.DefaultLivenessPath` is `/healthz`,
`serverkit.DefaultReadinessPath` is `/readyz`, and `serverkit.DBReadyCheck("database", db)`
is the ready-made check; `serverkit` runs a whole forge server, so for an
existing app port the probe contract (8 above) rather than adopting
`serverkit.Run`.

## Node: use forge's SDK

`@reliantlabs/forge-web-runtime/node` gives a logger, a tracer, uncaught-error
capture in the same `exception.*` shape, credential redaction and batching,
with `@opentelemetry/api` as its only peer (Node ≥ 22). It posts OTLP/HTTP
**JSON** to `${endpoint}/v1/logs` and `/v1/traces`: point it at an
OpenTelemetry Collector (which accepts JSON) unless the backend documents JSON
ingest. Logs and traces only, no metrics. It does not instrument Express; this
middleware is a sketch to adapt and test, not shipped code. `initNodeTelemetry`
installs an AsyncLocalStorage context manager, so the span is active for what
`next()` runs synchronously and for the async work that code starts (the SDK's
own test: "exports spans, parents them through async work"). A middleware that
resumes from a stream or callback (a body parser calling `next()` from the
request's `end` event) runs in the context the request arrived in, not this
span's, and everything after it loses the parent. So mount this middleware
after the body parsers and anything else that resumes that way, directly before
the routes, and check one request's parentage (Verify, below) before relying on
the correlation. The `finish` listener is bound to the span's context, so its
log line carries the span's `trace_id`:

```js
import { initNodeTelemetry } from "@reliantlabs/forge-web-runtime/node";
import { SpanKind, SpanStatusCode, context } from "@opentelemetry/api";
import { randomUUID } from "node:crypto";

const telemetry = initNodeTelemetry({
  serviceName: "checkout-api",
  serviceVersion: process.env.GIT_SHA,
  environment: process.env.DEPLOY_ENV, // → deployment.environment.name
  serviceNamespace: "shop",
  endpoint: process.env.OTLP_ENDPOINT, // unset, empty or "off": installs nothing
  headers: { authorization: process.env.OTLP_TOKEN ?? "" },
}); // null when off; the app must not depend on it

app.use(express.json()); // body parsers first: they resume outside any span
app.use((req, res, next) => {
  const requestId = req.get("x-request-id") ?? randomUUID();
  res.set("x-request-id", requestId);
  if (!telemetry) return next();
  const route = () => `${req.method} ${req.route?.path ?? "unmatched"}`; // template, not raw path
  telemetry.tracer.startActiveSpan(route(), { kind: SpanKind.SERVER }, (span) => {
    const start = process.hrtime.bigint();
    res.on("finish", context.bind(context.active(), () => {
      span.updateName(route());
      span.setAttribute("http.response.status_code", res.statusCode);
      if (res.statusCode >= 500) span.setStatus({ code: SpanStatusCode.ERROR });
      telemetry.logger[res.statusCode >= 500 ? "error" : "info"]("http request", {
        method: req.method, path: req.route?.path ?? "unmatched", status: res.statusCode,
        duration: Number(process.hrtime.bigint() - start), request_id: requestId,
      });
      span.end();
    }));
    next();
  });
});
```

## Other languages

Python, Ruby, Java, .NET, PHP: the language's OpenTelemetry
auto-instrumentation gives the spans and HTTP metrics (`adopt/deploy` Tier 2
has the package and start command per language). The work left is the contract
above: the JSON log formatter with those keys, the request-id middleware, the
level policy, the recovery layer and the two probes. Port forge's Go, keep the
names.

## Steps

1. **Survey** (`adopt`): services, languages, existing OTel, logger, middleware
   layer, where env vars are set.
2. **Instrument one service end to end first**, in a branch: middleware,
   logger, probes. Smallest diff; do not touch handlers. With an OTel SDK
   already present, add only the resource attributes and the log keys.
3. **The user sets the env vars** (1) in their deploy config and places any
   token; you never read or print it.
4. **Verify**, then repeat for the next service with the same code.
5. **Offer the next piece**: `forge lint --tracing` (Go; `adopt/linters`) finds
   where a trace is still cut; `adopt/deploy` brings the hosted dashboards.

## Verify

Observed, not assumed. Send a few requests, one failing on purpose, and check
in whatever receives the data:

- a trace with `service.name`, `deployment.environment.name` and
  `service.version` set (a literal `unknown` means the env vars did not reach
  the process);
- the failing request's log line at `level=ERROR` with `exception.*`, carrying
  its span's `trace_id`;
- a successful request at INFO with `request_id`, matching the response's
  `X-Request-Id`;
- `/healthz` 200, and `/readyz` 503 naming the dependency, with no error text,
  when you make that dependency unreachable.

For an app forge deploys: `forge obs status <env>` lists the service with a
request rate, and `forge obs check <env> --wait` exits 0 or 1, not 2.

## Leaving

It is standard OTLP and the standard `OTEL_*` environment: pointing it at
another backend is changing `OTEL_EXPORTER_OTLP_ENDPOINT` and `_HEADERS`. No
vendor SDK is in the code, and forge's libraries are Apache-2.0.
