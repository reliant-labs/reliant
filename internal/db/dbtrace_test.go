// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/forge/pkg/observe/dbtrace"
)

// A request's trace must reach its SQL: every pool this package opens goes
// through dbtrace, so a query run under a caller's span is a child span named
// for its statement. The main pool gets that from serverkit.OpenDB; the session
// pool and the read-only tooling repo open their own and must not regress to
// a raw sql.Open (forge-tracing-raw-sql-open).
func TestQueriesUnderASpanAreTraced(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	if !dbtrace.IsTraced(rawDB) {
		t.Fatal("the test harness pool is not traced; it must open the way the served pool does")
	}

	dsn, err := testDSNOf(rawDB)
	if err != nil {
		t.Fatal(err)
	}
	session, err := openSessionPool(dsn)
	if err != nil {
		t.Fatalf("openSessionPool: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if !dbtrace.IsTraced(session) {
		t.Error("the session pool (DatabaseConfig.DirectURL) is not traced")
	}

	chatID := createTestChat(t, repo)
	ctx, parent := tp.Tracer("test").Start(context.Background(), "ListMessages request")
	if _, err := repo.ListMessages(ctx, chatID, MessageListOptions{}); err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	parent.End()

	var found bool
	for _, s := range rec.Ended() {
		if s.Name() == "SELECT messages" && s.Parent().SpanID() == parent.SpanContext().SpanID() {
			found = true
		}
	}
	if !found {
		names := make([]string, 0, len(rec.Ended()))
		for _, s := range rec.Ended() {
			names = append(names, s.Name())
		}
		t.Errorf("no \"SELECT messages\" span under the request span; spans: %q", names)
	}
}
