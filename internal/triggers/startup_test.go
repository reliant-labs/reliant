// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/retry/retrytest"
	"github.com/stretchr/testify/assert"
)

// A startup sync that never converges makes startupSyncAttempts calls,
// waiting 2s, 4s, 8s, 16s and 32s between them.
func TestSyncAllWithRetry_WaitLadder(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(time.Unix(0, 0))
	calls := 0
	syncAllWithRetry(context.Background(), clock, func(context.Context) error {
		calls++
		return errors.New("temporal unreachable")
	})

	assert.Equal(t, startupSyncAttempts, calls)
	assert.Equal(t, []time.Duration{
		2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second,
	}, clock.Sleeps())
}

// A sync that succeeds stops retrying at once.
func TestSyncAllWithRetry_StopsOnSuccess(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(time.Unix(0, 0))
	calls := 0
	syncAllWithRetry(context.Background(), clock, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("temporal unreachable")
		}
		return nil
	})

	assert.Equal(t, 3, calls)
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second}, clock.Sleeps())
}
