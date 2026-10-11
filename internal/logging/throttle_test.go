// Copyright (c) 2025 Reliant Labs
package logging

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestThrottle_OncePerWindowPerKey_CountingWhatItSuppressed(t *testing.T) {
	now := time.Date(2026, 10, 9, 22, 17, 0, 0, time.UTC)
	log := NewThrottle(5*time.Minute, func() time.Time { return now })

	suppressed, ok := log.Allow("u1|not_connected")
	assert.True(t, ok, "the first occurrence logs")
	assert.Zero(t, suppressed)

	// The browser's retries inside the window are the same news.
	for i := 0; i < 20; i++ {
		now = now.Add(10 * time.Second)
		_, ok = log.Allow("u1|not_connected")
		assert.False(t, ok)
	}
	// A different user, or a different state, is different news.
	_, ok = log.Allow("u2|not_connected")
	assert.True(t, ok)
	_, ok = log.Allow("u1|no_machine")
	assert.True(t, ok)

	now = now.Add(5 * time.Minute)
	suppressed, ok = log.Allow("u1|not_connected")
	assert.True(t, ok, "the next window logs again")
	assert.Equal(t, 20, suppressed, "and says how many it held back")
}

func TestThrottle_ForgetsClosedWindowsPastItsBound(t *testing.T) {
	now := time.Date(2026, 10, 9, 22, 17, 0, 0, time.UTC)
	log := NewThrottle(time.Minute, func() time.Time { return now })
	for i := 0; i < throttleMaxKeys; i++ {
		log.Allow(fmt.Sprintf("u%d", i))
	}
	now = now.Add(time.Minute)
	log.Allow("one-more")
	assert.Len(t, log.keys, 1, "keys whose window closed are forgotten once the bound is reached")
}
