// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The user's machine being closed, asleep or still starting was the largest
// share of prod's ERROR stream — "rpc failed GetFileTree: no daemon connected
// for user", "[TerminalWS] create terminal session: …", every poll of every
// open tab. Each is the user's machine state, not a server fault: a user error.
func TestDaemonResolutionErrorsAreUserErrors(t *testing.T) {
	notConnected := daemonRequestError("daemon command fs.get_tree", nats.ErrNoResponders)

	for name, err := range map[string]error{
		"still starting":    fmt.Errorf("your machine is still starting: %w", ErrDaemonPending),
		"suspended":         fmt.Errorf("the machine for this request is suspended and will wake when you next message it: %w", ErrDaemonPending),
		"no machine":        fmt.Errorf("%w: no machine is connected to your account yet", ErrNoDaemon),
		"not connected":     notConnected,
		"wrapped by router": connect.NewError(connect.CodeUnavailable, fmt.Errorf("resolving daemon for command: %w", ErrDaemonPending)),
	} {
		assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err), name)
	}

	// The marker changes nothing the callers key on.
	assert.True(t, IsDaemonPending(fmt.Errorf("x: %w", ErrDaemonPending)))
	assert.True(t, IsNoDaemon(fmt.Errorf("x: %w", ErrNoDaemon)))
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(notConnected))
	assert.Equal(t, "unavailable: no daemon connected for user", notConnected.Error())
	assert.True(t, daemonoffline.IsError(notConnected), "the daemon-offline marker must survive")
	assert.Contains(t, ErrDaemonPending.Error(), daemonoffline.ErrorSubstring)

	// A timeout is not the machine being closed: it stays a server fault.
	timeout := daemonRequestError("daemon command fs.get_tree", nats.ErrTimeout)
	require.Error(t, timeout)
	assert.Equal(t, svcerr.ClassServer, svcerr.Classify(timeout))
	assert.True(t, errors.Is(timeout, nats.ErrTimeout))
}
