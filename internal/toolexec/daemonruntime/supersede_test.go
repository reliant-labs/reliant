// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

// A daemon whose id was claimed by another daemon PROCESS must stop, not
// redial. Redialing evicts the new holder, which redials and evicts this one:
// two local daemons sharing one id flapped every 15-30s for 37 minutes on
// 2026-07-27, dropping every in-flight tool request each time.
//
// The gateway signals this with CodeAborted (see supersedeIncumbent in
// internal/grpc/services). This test asserts the code, not the message, so it
// survives a reword and fails on a rewire.
func TestSupersededDaemonDoesNotReconnect(t *testing.T) {
	superseded := connect.NewError(connect.CodeAborted,
		errors.New("daemon identity claimed by another daemon process"))

	require.True(t, isFatalError(superseded),
		"a superseded daemon that reconnects re-enters the eviction loop it was just removed from")

	// It must survive the wrapping the session loop applies before the check.
	wrapped := fmt.Errorf("daemon stream receive: %w", superseded)
	require.True(t, isFatalError(wrapped))
}

// Only supersession is terminal. Ordinary stream loss — EOF, a gateway
// restart, a laptop waking up — must still reconnect, or a transient blip
// leaves the machine with no daemon at all.
func TestOrdinaryStreamLossStillReconnects(t *testing.T) {
	for name, err := range map[string]error{
		"eof":         errors.New("daemon stream receive: unknown: EOF"),
		"unavailable": connect.NewError(connect.CodeUnavailable, errors.New("connection refused")),
		"internal":    connect.NewError(connect.CodeInternal, errors.New("boom")),
		"canceled":    connect.NewError(connect.CodeCanceled, errors.New("canceled")),
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, isFatalError(err))
		})
	}
}
