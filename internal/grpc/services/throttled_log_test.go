// Copyright (c) 2025 Reliant Labs
package services

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

func TestThrottledLog_OncePerWindowPerKey_CountingWhatItSuppressed(t *testing.T) {
	now := time.Date(2026, 10, 9, 22, 17, 0, 0, time.UTC)
	log := newThrottledLog(5*time.Minute, func() time.Time { return now })

	suppressed, ok := log.allow("u1|not_connected")
	assert.True(t, ok, "the first occurrence logs")
	assert.Zero(t, suppressed)

	// The browser's retries inside the window are the same news.
	for i := 0; i < 20; i++ {
		now = now.Add(10 * time.Second)
		_, ok = log.allow("u1|not_connected")
		assert.False(t, ok)
	}
	// A different user, or a different state, is different news.
	_, ok = log.allow("u2|not_connected")
	assert.True(t, ok)
	_, ok = log.allow("u1|no_machine")
	assert.True(t, ok)

	now = now.Add(5 * time.Minute)
	suppressed, ok = log.allow("u1|not_connected")
	assert.True(t, ok, "the next window logs again")
	assert.Equal(t, 20, suppressed, "and says how many it held back")
}

func TestThrottledLog_ForgetsClosedWindowsPastItsBound(t *testing.T) {
	now := time.Date(2026, 10, 9, 22, 17, 0, 0, time.UTC)
	log := newThrottledLog(time.Minute, func() time.Time { return now })
	for i := 0; i < throttledLogMaxKeys; i++ {
		log.allow(fmt.Sprintf("u%d", i))
	}
	now = now.Add(time.Minute)
	log.allow("one-more")
	assert.Len(t, log.keys, 1, "keys whose window closed are forgotten once the bound is reached")
}

// The terminal's expected states — no machine, starting or asleep, not
// connected — are told apart from real failures, which stay at ERROR.
func TestTerminalMachineState(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		state string
		ok    bool
	}{
		{"no machine at all", fmt.Errorf("resolving daemon for command: %w: no machine is connected to your account yet", toolexec.ErrNoDaemon), "no_machine", true},
		{"starting", fmt.Errorf("resolving daemon for command: your machine is still starting: %w", toolexec.ErrDaemonPending), "starting_or_asleep", true},
		{"suspended", fmt.Errorf("the machine for this request is suspended and will wake when you next message it: %w", toolexec.ErrDaemonPending), "starting_or_asleep", true},
		{"not connected", connect.NewError(connect.CodeUnavailable, errors.New("no daemon connected for user")), "not_connected", true},
		{"a real failure", errors.New("terminal.create via NATS failed: nats: timeout"), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, ok := terminalMachineState(tc.err)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.state, state)
		})
	}
}
