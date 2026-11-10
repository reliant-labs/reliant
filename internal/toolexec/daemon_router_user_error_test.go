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
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/errreason"
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
	var wire *connect.Error
	require.ErrorAs(t, notConnected, &wire)
	assert.Equal(t, errreason.DaemonNotConnected, wire.Meta().Get(svcerr.ReasonHeader), "the reason rides the *connect.Error itself")
	assert.True(t, daemonoffline.IsError(notConnected), "the daemon-offline marker must survive")
	assert.Contains(t, ErrDaemonPending.Error(), daemonoffline.ErrorSubstring)

	// A timeout is not the machine being closed: it stays a server fault —
	// but of the DeadlineExceeded kind, which forge logs at WARN, not ERROR.
	timeout := daemonRequestError("daemon command fs.get_tree", nats.ErrTimeout)
	require.Error(t, timeout)
	assert.Equal(t, svcerr.ClassServer, svcerr.Classify(timeout))
	assert.Equal(t, connect.CodeDeadlineExceeded, svcerr.Code(timeout))
	assert.True(t, IsDaemonTimeout(timeout))
	assert.True(t, errors.Is(timeout, nats.ErrTimeout))
	assert.False(t, daemonoffline.IsError(timeout), "a slow machine is not an offline one")

	// The chunked reply's reassembly deadline is the same timeout.
	chunked := daemonRequestError("daemon command fs.search",
		fmt.Errorf("chunked reply: deadline exceeded after 2/5 chunks: %w", nats.ErrTimeout))
	assert.True(t, IsDaemonTimeout(chunked))

	// Any other transport failure is neither.
	closed := daemonRequestError("daemon command fs.get_tree", nats.ErrConnectionClosed)
	assert.False(t, IsDaemonTimeout(closed))
	assert.Equal(t, connect.CodeInternal, svcerr.Code(closed))
}

// The daemon refusing a credential store is its policy working, not a server
// fault: a user error at every layer that logs it (prod issue 1e396220 logged
// it as ERROR from machinewake.AfterFailure). Any other daemon failure text
// keeps no opinion.
func TestDaemonCredentialRefusalIsAUserError(t *testing.T) {
	for name, text := range map[string]string{
		"credential file": daemonpolicy.ErrCredentialFile.Error() + "; the daemon's file tools never read or write it",
		"viewer":          daemonpolicy.ErrViewerCredential.Error() + ": /home/u/.ssh/id_ed25519",
	} {
		assert.Equal(t, svcerr.ClassUser, svcerr.Classify(fmt.Errorf("daemon command fs.read: %w", daemonReportedFailure(text))), name)
	}
	assert.Equal(t, svcerr.ClassNone, daemonReportedFailure("permission denied: /etc/shadow").ErrorClass())
}
