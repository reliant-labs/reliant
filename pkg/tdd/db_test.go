package tdd_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/tdd"
)

func TestSetupMockDB(t *testing.T) {
	if testing.Short() {
		t.Skip("SetupMockDB boots real postgres; skipped under -short")
	}
	db := tdd.SetupMockDB(t)
	if db == nil {
		t.Fatal("SetupMockDB returned nil")
	}
	// Round-trip a trivial query to confirm the connection is usable.
	if err := db.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}

	if _, err := db.Exec("CREATE TABLE t (id BIGINT)"); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func TestWithTimeout(t *testing.T) {
	ctx, cancel := tdd.WithTimeout(50 * time.Millisecond)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	if time.Until(deadline) <= 0 {
		t.Fatalf("deadline already passed: %v", deadline)
	}
	// Also sanity-check the context's Done channel triggers under the timeout.
	select {
	case <-ctx.Done():
		// expected after 50ms
	case <-time.After(200 * time.Millisecond):
		t.Fatal("context did not time out")
	}
	if ctx.Err() == nil || ctx.Err() == context.Canceled {
		// After timeout, Err should be DeadlineExceeded.
		t.Fatalf("unexpected ctx err: %v", ctx.Err())
	}
}
