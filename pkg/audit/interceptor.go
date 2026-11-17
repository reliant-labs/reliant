package audit

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/middleware"
)

// Interceptor returns a Connect interceptor that logs every RPC to slog AND
// persists it to store. It is the DB-backed audit write-side: versioned
// library code you wire in one line.
//
// It is a thin convenience over [middleware.AuditInterceptorWithSink]: the
// interceptor machinery (slog record, claim extraction, trace correlation,
// off-request-path dispatch) lives once in pkg/middleware; this constructor
// just bridges a [Store] to that interceptor's [middleware.AuditSink] via
// [Sink]. Reach for AuditInterceptorWithSink directly if you already hold a
// custom sink.
//
// claimsFrom is your project's ClaimsFromContext — the claims context key
// stays project-owned, so a library cannot supply it (this mirrors every other
// versioned interceptor: middleware.AuditInterceptor, RateLimitInterceptor,
// HTTPStack). A nil claimsFrom logs every caller as "anonymous".
//
// Pass a nil store for slog-only mode (identical to
// [middleware.AuditInterceptor]) — nothing is written to the database.
//
//	store := audit.NewDBAuditStore(db)
//	chain := observe.Chain(observe.Deps{
//	    Audit: audit.Interceptor(logger, middleware.ClaimsFromContext, store),
//	    // ...
//	})
func Interceptor(logger *slog.Logger, claimsFrom middleware.ClaimsLookup, store Store) connect.Interceptor {
	return middleware.AuditInterceptorWithSink(logger, claimsFrom, Sink(store, logger))
}

// Sink adapts a [Store] to [middleware.AuditSink] so any audit interceptor can
// fan events into the audit_log table. It maps a [middleware.AuditEvent] onto
// an [Entry] (folding the OTel TraceID into Metadata) and reports a failed
// write through logger — matching the interceptor's fire-and-forget contract:
// [middleware.AuditSink.Record] has no error return, so the sink owns failure
// handling.
//
// Sink returns nil for a nil store, so
// middleware.AuditInterceptorWithSink(..., audit.Sink(nil, logger)) degrades
// cleanly to slog-only (no typed-nil sink). A nil logger falls back to
// [slog.Default].
func Sink(store Store, logger *slog.Logger) middleware.AuditSink {
	if store == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return storeSink{store: store, logger: logger}
}

type storeSink struct {
	store  Store
	logger *slog.Logger
}

func (s storeSink) Record(ctx context.Context, e middleware.AuditEvent) {
	entry := Entry{
		Timestamp:    e.Timestamp,
		Procedure:    e.Procedure,
		PeerAddress:  e.PeerAddress,
		UserID:       e.UserID,
		Email:        e.Email,
		DurationMs:   int(e.Duration.Milliseconds()),
		Status:       e.Status,
		ErrorCode:    e.ErrorCode,
		ErrorMessage: e.ErrorMessage,
	}
	if e.TraceID != "" {
		entry.Metadata = map[string]string{"trace_id": e.TraceID}
	}
	if err := s.store.Log(ctx, entry); err != nil {
		s.logger.ErrorContext(ctx, "audit db write failed", "error", err, "procedure", e.Procedure)
	}
}
