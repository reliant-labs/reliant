package middleware

import (
	"context"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/observe"
)

// auditMessage is the canonical slog message string for every audit
// record forge emits — INFO on success, WARN on error. It is a stable
// SIEM/log-query anchor: log pipelines that route or alert on audit
// events should match on msg == "audit.event" (together with the
// log_type=audit attribute on the child logger).
//
// CONSUMER MIGRATION NOTE: control-plane's hand-rolled interceptor used
// the message "audit". Projects replacing their local interceptor with
// this library MUST update any SIEM rules / log queries that matched on
// msg=="audit" to msg=="audit.event". The field set (procedure, peer,
// user_id, status, code, error, trace_id, timestamp, duration) is
// otherwise identical, except that email is no longer logged — it reaches
// the durable AuditSink only.
const auditMessage = "audit.event"

// auditSinkTimeout bounds each durable sink dispatch so a slow or
// wedged sink can never block the RPC response path. It mirrors the 5s
// default control-plane used for its async DB write.
const auditSinkTimeout = 5 * time.Second

// AuditEvent is the full, structured record of a single RPC, handed to
// an [AuditSink] for durable persistence. It carries every field forge
// emits to slog plus the OTel trace correlation id, so a sink can write
// a complete compliance record without re-deriving anything from the
// request context.
//
// The field set is intentionally a superset that lets a thin adapter
// satisfy a richer store. Status is "ok" or "error"; ErrorCode is the
// connect.Code string (e.g. "permission_denied") and is empty on
// success; TraceID is the hex OTel trace id, empty when the request
// carries no span.
type AuditEvent struct {
	// Timestamp is the wall-clock time the RPC started.
	Timestamp time.Time
	// Procedure is the fully-qualified Connect procedure (e.g.
	// "/pkg.Service/Method").
	Procedure string
	// PeerAddress is the remote peer address.
	PeerAddress string
	// UserID is the authenticated user id, or "anonymous" when no
	// claims were present.
	UserID string
	// Email is the authenticated user email, empty when anonymous.
	Email string
	// Duration is the measured RPC duration.
	Duration time.Duration
	// Status is "ok" on success or "error" on failure.
	Status string
	// ErrorCode is the connect.Code string on failure, empty on success.
	ErrorCode string
	// ErrorMessage is the error string on failure, empty on success.
	ErrorMessage string
	// TraceID is the OTel trace id (hex), empty when the request carries
	// no span context.
	TraceID string
	// RequestID is the per-request correlation id (the X-Request-Id the
	// client was handed), empty when the request reached the interceptor
	// without one. It is what makes the audit trail JOINABLE: without it
	// a customer-quoted request id finds the operational "rpc completed"
	// line but nothing in the audit record, so "what did this call
	// actually do, and who made it" cannot be answered from the id alone.
	// TraceID does not substitute — it is empty whenever no OTLP/tracing
	// span exists, which is the default configuration.
	RequestID string
}

// AuditSink is a durable destination for audit events (a database, an
// append-only file, a SIEM forwarder). The interceptor calls [Record]
// for every RPC AFTER it has emitted the slog record.
//
// Record is invoked on a fresh, bounded-timeout context off the request
// path — the interceptor never blocks the RPC response on the sink. A
// sink implementation may therefore treat Record as synchronous: do its
// write, honor ctx cancellation, and surface its own errors via its own
// logger. Record has no error return precisely so the interceptor stays
// fire-and-forget; the sink owns failure handling.
//
// A thin adapter satisfies AuditSink over any richer store. For
// example, control-plane's audit store has the signature:
//
//	// pkg/audit.Store
//	Log(ctx context.Context, entry Entry) error
//
// which an adapter wraps directly:
//
//	type storeSink struct{ store pkgaudit.Store; log *slog.Logger }
//	func (s storeSink) Record(ctx context.Context, e middleware.AuditEvent) {
//	    entry := pkgaudit.Entry{
//	        Timestamp:    e.Timestamp,
//	        Procedure:    e.Procedure,
//	        PeerAddress:  e.PeerAddress,
//	        UserID:       e.UserID,
//	        Email:        e.Email,
//	        DurationMs:   int(e.Duration.Milliseconds()),
//	        Status:       e.Status,
//	        ErrorCode:    e.ErrorCode,
//	        ErrorMessage: e.ErrorMessage,
//	        Metadata:     map[string]string{"trace_id": e.TraceID},
//	    }
//	    if err := s.store.Log(ctx, entry); err != nil {
//	        s.log.Error("audit db write failed", "error", err, "procedure", e.Procedure)
//	    }
//	}
type AuditSink interface {
	Record(ctx context.Context, e AuditEvent)
}

// AuditInterceptor creates a Connect interceptor that produces audit log
// entries for every RPC call. Audit logs capture: who made the call
// (user ID — the email goes to the durable sink only, never to slog), what
// procedure was called, when, the result (success/error code), the
// duration, and the OTel trace id when the request carries a span.
//
// Audit records are emitted with log_type=audit on a child logger, so
// they can be routed to a dedicated audit sink (separate file, SIEM,
// compliance database) independent of operational logs. The record
// message is "audit.event" ([auditMessage]) — INFO on success, WARN on
// error.
//
// claimsFrom is the project's ClaimsFromContext (the project owns the
// claims context key — see [ClaimsLookup]). When nil, or when no claims
// are present (unauthenticated request), the user is logged as
// "anonymous".
//
// This is the slog-only constructor; it is fully backward compatible.
// For compliance-grade durable persistence (so projects can delete a
// hand-rolled DB-persisting interceptor), use [AuditInterceptorWithSink]
// to additionally fan each event out to an [AuditSink].
func AuditInterceptor(logger *slog.Logger, claimsFrom ClaimsLookup) connect.Interceptor {
	return AuditInterceptorWithSink(logger, claimsFrom, nil)
}

// AuditInterceptorWithSink is [AuditInterceptor] plus a durable sink.
// Every RPC is logged to slog exactly as the slog-only variant does AND
// dispatched to sink.Record on a fresh bounded-timeout context off the
// request path, so the sink never blocks the RPC response. Passing a nil
// sink yields behavior identical to [AuditInterceptor].
func AuditInterceptorWithSink(logger *slog.Logger, claimsFrom ClaimsLookup, sink AuditSink) connect.Interceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return &auditInterceptor{
		logger:     logger.With("log_type", "audit"),
		claimsFrom: claimsFrom,
		sink:       sink,
	}
}

type auditInterceptor struct {
	logger     *slog.Logger
	claimsFrom ClaimsLookup
	sink       AuditSink
}

func (a *auditInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()

		resp, err := next(ctx, req)

		a.logAudit(ctx, req.Spec().Procedure, req.Peer().Addr, start, err)

		return resp, err
	}
}

func (a *auditInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *auditInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()

		err := next(ctx, conn)

		a.logAudit(ctx, conn.Spec().Procedure, conn.Peer().Addr, start, err)

		return err
	}
}

func (a *auditInterceptor) logAudit(ctx context.Context, procedure, peerAddr string, start time.Time, err error) {
	duration := time.Since(start)

	event := AuditEvent{
		Timestamp:   start,
		Procedure:   procedure,
		PeerAddress: peerAddr,
		Duration:    duration,
		Status:      "ok",
	}

	attrs := []slog.Attr{
		slog.String("procedure", procedure),
		slog.String("peer", peerAddr),
		slog.Duration("duration", duration),
		slog.Time("timestamp", start),
	}

	// Correlate with the request the client was given a receipt for. This
	// is the ONLY id an incident starts from — the customer quotes their
	// X-Request-Id — so an audit record without it cannot be joined to the
	// operational log line for the same call.
	if rid := RequestIDFromContext(ctx); rid != "" {
		event.RequestID = rid
		attrs = append(attrs, slog.String("request_id", rid))
	}

	// Correlate with the active trace when one is present.
	if spanCtx := trace.SpanContextFromContext(ctx); spanCtx.HasTraceID() {
		event.TraceID = spanCtx.TraceID().String()
		attrs = append(attrs, slog.String("trace_id", event.TraceID))
	}

	// Extract user identity from auth claims if available.
	//
	// The slog record carries user_id only. It is emitted for EVERY RPC into
	// the general log stream, so an email there would put personal data in
	// every log line; user_id joins to it wherever it is actually needed. The
	// durable sink — the access-controlled audit store — still receives Email.
	var identified bool
	if a.claimsFrom != nil {
		if claims, ok := a.claimsFrom(ctx); ok && claims != nil {
			event.UserID = claims.UserID
			event.Email = claims.Email
			attrs = append(attrs, slog.String("user_id", claims.UserID))
			identified = true
		}
	}
	if !identified {
		event.UserID = "anonymous"
		attrs = append(attrs, slog.String("user_id", "anonymous"))
	}

	if err != nil {
		code := connect.CodeOf(err)
		event.Status = "error"
		event.ErrorCode = code.String()
		// The CLIENT-VISIBLE message, deliberately: an audit stream is
		// routinely shipped to a SIEM or a compliance store, and svcerr has
		// already decided what text is safe to hand out. The unredacted
		// diagnostic lives on the operational "rpc failed" record, which
		// this one joins by request_id.
		event.ErrorMessage = err.Error()
		attrs = append(attrs,
			slog.String("status", "error"),
			slog.String("code", code.String()),
			slog.String("error", err.Error()),
		)
		// Same severity policy as the operational log — a server fault is
		// ERROR, a rejected request is a user error at INFO. Two streams
		// disagreeing about how bad the same RPC was is its own incident.
		a.logger.LogAttrs(ctx, observe.LevelForError(err), auditMessage, attrs...)
	} else {
		attrs = append(attrs, slog.String("status", "ok"))
		a.logger.LogAttrs(ctx, slog.LevelInfo, auditMessage, attrs...)
	}

	a.dispatch(event)
}

// dispatch fans the event out to the durable sink, if one is configured,
// on a fresh bounded-timeout context off the request path. Fire-and-
// forget: the RPC response never waits on the sink, and a slow sink is
// bounded by auditSinkTimeout rather than the (already-returned) request
// deadline.
func (a *auditInterceptor) dispatch(event AuditEvent) {
	if a.sink == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), auditSinkTimeout)
		defer cancel()
		a.sink.Record(ctx, event)
	}()
}
