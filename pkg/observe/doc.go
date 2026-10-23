// Package observe provides observability primitives for forge-generated
// services: Connect interceptors for logging, tracing, metrics, recovery,
// and request-id correlation at the RPC edge, and the in-process
// ComponentChain that forge's generated decorators route every internal
// Service method through.
//
// # Where spans come from
//
// A request should read as a tree: the RPC, the business methods it calls,
// and the SQL and outbound calls those make. Each layer is instrumented where
// forge owns the boundary, so none of it depends on someone remembering to
// add a span:
//
//   - the RPC edge: the Connect interceptor chain below (Chain /
//     DefaultMiddlewares), one server span per call;
//   - business methods: every contract.go package whose constructor returns
//     its Service interface gets a generated decorator (middleware_gen.go)
//     that opens one span per method, records call metrics and logs failures,
//     through DefaultComponentChain or the package's own observe_chain.go
//     seam. It is the default; `// forge:no-observe` on the package (or one
//     method) opts out. compose.go constructs the package wrapped;
//   - outbound calls: NewClientStack for Connect clients.
//
// The per-call helpers (TraceCall, LogCall, RecordCall) remain for code
// that is not a contract package. They are the exception: an earlier
// version of this package recommended them over generated decorators, and
// in practice they were not written — most traces were a single span.
//
// # The interceptor chain
//
// Most projects want a canonical chain. DefaultMiddlewares returns it:
//
//	interceptors := observe.DefaultMiddlewares(observe.DefaultMiddlewareDeps{
//	    Logger:  logger,
//	    Tracer:  tracer,
//	    Meter:   meter,
//	})
//
// Order matters; see the DefaultMiddlewares docstring for the rationale.
// Both DefaultMiddlewares and Chain end with svcerr.Interceptor, so a
// handler's svcerr error reaches the client with its Connect code.
//
// Projects that want a custom chain compose interceptors directly, and add
// svcerr.Interceptor() last themselves:
//
//	interceptors := []connect.Interceptor{
//	    observe.RecoveryInterceptor(logger),
//	    observe.RequestIDInterceptor(),
//	    observe.LoggingInterceptor(logger),
//	    auth.Interceptor(...),
//	}
//
// # The environment contract
//
// The interface between a forge app and whatever collector exists is the
// standard OpenTelemetry environment. The app never learns the backend: the
// platform renders these variables and Setup obeys them.
//
//	OTEL_EXPORTER_OTLP_ENDPOINT   base URL of the collector, e.g. http://127.0.0.1:4318
//	OTEL_EXPORTER_OTLP_PROTOCOL   http/protobuf (default, port 4318) or grpc (port 4317)
//	OTEL_EXPORTER_OTLP_HEADERS    k=v,k2=v2 — e.g. authorization=<key>
//	OTEL_EXPORTER_OTLP_{TRACES,METRICS,LOGS}_{ENDPOINT,PROTOCOL,HEADERS}
//	                              per-signal overrides of the three above
//	OTEL_SERVICE_NAME             wins over Config.ServiceName (the compile-time default)
//	OTEL_RESOURCE_ATTRIBUTES      merged into the resource; carries
//	                              deployment.environment.name=<forge env name>
//	OTEL_SDK_DISABLED=true        every export off
//	OTEL_{TRACES,METRICS}_EXPORTER=none   that signal off
//	OTEL_LOGS_EXPORTER=otlp       the explicit switch that turns OTLP logs ON
//
// Timeout, compression, TLS and the rest of the OTLP exporter variables are
// read by the exporters themselves. Config.OTLPEndpoint, when set, overrides the
// endpoint variables (it is a base URL; /v1/<signal> is appended for
// http/protobuf); headers and protocol always come from the environment.
// otelenv.go is the only file in this package that calls os.Getenv.
//
// With no endpoint from any source nothing is exported — only the Prometheus
// /metrics reader is wired — but the W3C TraceContext+Baggage propagator is
// installed regardless, so an incoming traceparent is still honoured and
// forwarded by every observe client.
//
// With export on, Setup also records Go runtime metrics
// (go.opentelemetry.io/contrib/instrumentation/runtime).
//
// # Logs
//
// serverkit's logger writes JSON lines to stdout. NewLogHandler adds trace_id
// and span_id to every record logged with a context that holds a valid span
// (use the *Context methods: logger.InfoContext(ctx, ...)). Log shipping is
// ONE path per environment: the collector reads the stdout/file stream. The
// OTLP logs exporter (OTEL_LOGS_EXPORTER=otlp or Config.OTLPLogs) is off by
// default and is MUTUALLY EXCLUSIVE with that collection: it does not replace
// stdout, so enabling both ships every line twice.
//
// # Error records
//
// A log record at ERROR or above is a server error event, and NewLogHandler
// gives it what an issue is grouped by, on both shipping paths (the stdout
// line and the OTLP bridge), and nothing at lower levels:
//
//   - code.function.name, code.file.path, code.line.number: the call site,
//     read from the record's PC, so a logger that builds its own record and
//     calls Handler().Handle directly (Temporal's slog adapter) names its
//     caller rather than a handler frame.
//   - exception.type and exception.message: the innermost error of the
//     record's first error-valued attribute (svcerr's withheld cause first,
//     then Unwrap to the end, the first branch of a multi-error). Type is
//     %T; the message is bounded to MaxExceptionMessage (1 KiB). Attributes
//     the call site already set are kept.
//   - exception.stacktrace, from LogPanic only. LogPanic is what every
//     recover path in forge calls; its record's PC is the panicking frame, so
//     panics group by where they happened, not by where they were recovered.
//   - error.group, from ErrorGroup(ctx, name) only: an explicit issue name
//     for the failure whose derived grouping is wrong.
package observe
