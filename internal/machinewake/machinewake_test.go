// Copyright (c) 2025 Reliant Labs
package machinewake

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/wirecheck"
)

// A machine woken by the request is asleep, not broken: a user error, both as
// it travels the handler and in the fresh wire error the interceptor builds.
func TestMachineWakingErrorIsAUserError(t *testing.T) {
	waking := &wakingError{daemonID: "d1", cause: fmt.Errorf("x: %w", toolexec.ErrDaemonPending)}

	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(fmt.Errorf("failed to get git status: %w", waking)))
	wire := waking.connectError()
	assert.Equal(t, connect.CodeUnavailable, wire.Code())
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(wire), "the class must survive the rebuilt wire error")
	assert.Contains(t, wire.Message(), "no daemon connected", "the client's wait marker must survive")
}

type wakeResumer struct{}

func (wakeResumer) Wake(context.Context, string, *toolexec.DaemonSelector) (toolexec.WakeResult, error) {
	return toolexec.WakeResult{Resumed: true, DaemonID: "d-wake"}, nil
}

// A request that woke its machine reads daemon_waking (13-p9 §7.1 #1) whichever
// way it reaches the client: through a handler that wrapped AfterFailure's
// error in its own words, and through the interceptor's rebuilt wire error.
// The wait marker in the message is unchanged.
func TestMachineWakingReasonOnTheWire(t *testing.T) {
	auth.SetUserJWT("user-wake", "jwt")
	w := New(Deps{Resumer: wakeResumer{}})
	err := w.AfterFailure(context.Background(), "user-wake", Target{},
		fmt.Errorf("resolving: %w", toolexec.ErrDaemonSuspended))
	require.True(t, IsWaking(err))

	ce := wirecheck.RoundTrip(t, svcerr.WithCause(svcerr.Internal("failed to get git status"), err))
	assert.Empty(t, ce.Meta().Get(svcerr.ReasonHeader), "a cause's reason is never the client's: the interceptor is what answers")

	var waking *wakingError
	require.ErrorAs(t, err, &waking)

	// Returned as-is, the reason is daemon_waking and the message is the one
	// the unwrapped error always published (the machine state underneath).
	ce = wirecheck.RoundTrip(t, err)
	assert.Equal(t, errreason.DaemonWaking, ce.Meta().Get(svcerr.ReasonHeader))
	assert.Equal(t, svcerr.ToConnect(waking).Message(), ce.Message())

	wirecheck.Assert(t, waking.connectError(), wirecheck.Want{
		Code:    connect.CodeUnavailable,
		Message: "your machine is waking up: no daemon connected yet",
		Reason:  errreason.DaemonWaking,
	})
}
