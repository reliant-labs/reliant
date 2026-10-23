package audit

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/middleware"
)

// fakeStore is an in-memory Store that records every Log call and can be
// primed to fail, so we can assert the Sink's mapping and failure logging
// without a database.
type fakeStore struct {
	logged []Entry
	err    error
}

func (f *fakeStore) Log(_ context.Context, e Entry) error {
	f.logged = append(f.logged, e)
	return f.err
}

func (f *fakeStore) Query(context.Context, Filter) ([]Entry, error) {
	return f.logged, nil
}

func TestSink_MapsEventToEntry(t *testing.T) {
	fs := &fakeStore{}
	sink := Sink(fs, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	ts := time.Now().UTC()
	sink.Record(context.Background(), middleware.AuditEvent{
		Timestamp:    ts,
		Procedure:    "/svc.v1.Users/Delete",
		PeerAddress:  "10.0.0.9:42",
		UserID:       "user_7",
		Email:        "u@example.com",
		Duration:     1500 * time.Millisecond,
		Status:       "error",
		ErrorCode:    "permission_denied",
		ErrorMessage: "nope",
		TraceID:      "trace-abc",
	})

	if len(fs.logged) != 1 {
		t.Fatalf("store received %d entries, want 1", len(fs.logged))
	}
	e := fs.logged[0]
	if e.Procedure != "/svc.v1.Users/Delete" || e.PeerAddress != "10.0.0.9:42" {
		t.Errorf("proc/peer = (%q,%q)", e.Procedure, e.PeerAddress)
	}
	if e.UserID != "user_7" || e.Email != "u@example.com" {
		t.Errorf("identity = (%q,%q)", e.UserID, e.Email)
	}
	if e.DurationMs != 1500 {
		t.Errorf("DurationMs = %d, want 1500 (from a 1.5s duration)", e.DurationMs)
	}
	if e.Status != "error" || e.ErrorCode != "permission_denied" || e.ErrorMessage != "nope" {
		t.Errorf("status/error = (%q,%q,%q)", e.Status, e.ErrorCode, e.ErrorMessage)
	}
	if e.Metadata["trace_id"] != "trace-abc" {
		t.Errorf("Metadata = %v, want trace_id=trace-abc", e.Metadata)
	}
	if !e.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, ts)
	}
}

func TestSink_NoTraceIDLeavesMetadataNil(t *testing.T) {
	fs := &fakeStore{}
	Sink(fs, nil).Record(context.Background(), middleware.AuditEvent{
		Procedure: "/svc/M",
		Status:    "ok",
	})
	if len(fs.logged) != 1 {
		t.Fatalf("store received %d entries, want 1", len(fs.logged))
	}
	if fs.logged[0].Metadata != nil {
		t.Errorf("Metadata = %v, want nil when there is no trace id", fs.logged[0].Metadata)
	}
}

func TestSink_LogsWriteFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	fs := &fakeStore{err: errors.New("db down")}

	Sink(fs, logger).Record(context.Background(), middleware.AuditEvent{
		Procedure: "/svc/M",
		Status:    "ok",
	})

	out := buf.String()
	if !strings.Contains(out, "audit db write failed") {
		t.Errorf("expected failure log, got: %q", out)
	}
	if !strings.Contains(out, "/svc/M") {
		t.Errorf("failure log should name the procedure, got: %q", out)
	}
}

// A nil store must yield a nil AuditSink (not a typed-nil wrapping a nil
// store) so middleware's `sink == nil` slog-only fallback engages.
func TestSink_NilStoreReturnsNil(t *testing.T) {
	if Sink(nil, slog.Default()) != nil {
		t.Error("Sink(nil, ...) must return an untyped-nil AuditSink")
	}
}

func TestInterceptor_NilSafe(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	// Nil store + nil claimsFrom: slog-only, anonymous — must still return a
	// usable interceptor rather than panicking.
	if got := Interceptor(logger, nil, nil); got == nil {
		t.Error("Interceptor with nil store returned nil")
	}
	// With a store (DB-backed) and an explicit claims lookup.
	var claimsFrom middleware.ClaimsLookup // nil is valid (anonymous)
	if got := Interceptor(logger, claimsFrom, &fakeStore{}); got == nil {
		t.Error("Interceptor with a store returned nil")
	}
}
