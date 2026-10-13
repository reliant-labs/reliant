// Copyright (c) 2025 Reliant Labs
package machinewake

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/toolexec"
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
