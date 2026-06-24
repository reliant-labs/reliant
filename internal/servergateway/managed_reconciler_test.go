// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// TestCreateConsumerWithRetry_RetriesWhenStreamMissing verifies the
// boot-ordering fix: when the DAEMON_STATE stream does not yet exist (the
// control plane creates it slightly later on a cold `forge up`), consumer
// creation must retry rather than fail the reconciler permanently. Once the
// stream appears, creation succeeds and the reconciler proceeds.
func TestCreateConsumerWithRetry_RetriesWhenStreamMissing(t *testing.T) {
	r := &ManagedDaemonReconciler{}

	calls := 0
	create := func(ctx context.Context) (jetstream.Consumer, error) {
		calls++
		if calls < 3 {
			// First two attempts: stream not created by the control plane yet.
			return nil, jetstream.ErrStreamNotFound
		}
		return nil, nil // success: stream now exists
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := r.createConsumerWithRetry(ctx, create)
	if err != nil {
		t.Fatalf("expected retry to eventually succeed, got error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 create attempts (2 retried + 1 success), got %d", calls)
	}
}

// TestCreateConsumerWithRetry_RetriesRawAPIError verifies that the raw
// JetStream API error (code=404 err_code=10059 description="stream not found")
// is treated as retryable, not just the normalized ErrStreamNotFound sentinel.
func TestCreateConsumerWithRetry_RetriesRawAPIError(t *testing.T) {
	r := &ManagedDaemonReconciler{}

	rawErr := &jetstream.APIError{
		Code:        404,
		ErrorCode:   jetstream.JSErrCodeStreamNotFound,
		Description: "stream not found",
	}

	calls := 0
	create := func(ctx context.Context) (jetstream.Consumer, error) {
		calls++
		if calls < 2 {
			return nil, rawErr
		}
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := r.createConsumerWithRetry(ctx, create); err != nil {
		t.Fatalf("expected raw stream-not-found API error to be retried, got: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 create attempts, got %d", calls)
	}
}

// TestCreateConsumerWithRetry_NonRetryableSurfaces verifies that errors other
// than stream-not-found are returned immediately without retrying.
func TestCreateConsumerWithRetry_NonRetryableSurfaces(t *testing.T) {
	r := &ManagedDaemonReconciler{}

	sentinel := errors.New("boom: some other failure")
	calls := 0
	create := func(ctx context.Context) (jetstream.Consumer, error) {
		calls++
		return nil, sentinel
	}

	_, err := r.createConsumerWithRetry(context.Background(), create)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected non-retryable error to surface, got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt for non-retryable error, got %d", calls)
	}
}

// TestCreateConsumerWithRetry_CtxCancelStops verifies the retry loop honors
// context cancellation while the stream remains absent (no infinite spin).
func TestCreateConsumerWithRetry_CtxCancelStops(t *testing.T) {
	r := &ManagedDaemonReconciler{}

	create := func(ctx context.Context) (jetstream.Consumer, error) {
		return nil, jetstream.ErrStreamNotFound // never recovers
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := r.createConsumerWithRetry(ctx, create)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline error, got: %v", err)
	}
}
