// Copyright (c) 2025 Reliant Labs
package services

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"go.temporal.io/api/serviceerror"
)

// A machine woken by the request is asleep, not broken: a user error, both as
// it travels the handler and in the fresh wire error the interceptor builds.
func TestMachineWakingErrorIsAUserError(t *testing.T) {
	waking := &machineWakingError{daemonID: "d1", cause: fmt.Errorf("x: %w", toolexec.ErrDaemonPending)}

	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(fmt.Errorf("failed to get git status: %w", waking)))
	wire := waking.connectError()
	assert.Equal(t, connect.CodeUnavailable, wire.Code())
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(wire), "the class must survive the rebuilt wire error")
	assert.Contains(t, wire.Message(), "no daemon connected", "the client's wait marker must survive")
}

func TestFsProxyDaemonError_MachineStateIsAUserError(t *testing.T) {
	for name, err := range map[string]error{
		"starting":   fmt.Errorf("resolving daemon for command: your machine is still starting: %w", toolexec.ErrDaemonPending),
		"no machine": fmt.Errorf("resolving daemon for command: %w", toolexec.ErrNoDaemon),
	} {
		mapped := fsProxyDaemonError(err)
		assert.Equal(t, svcerr.ClassUser, svcerr.Classify(mapped), name)
	}
	assert.Equal(t, svcerr.ClassServer, svcerr.Classify(fsProxyDaemonError(errors.New("decode tree: unexpected EOF"))))
}

func TestToolsDaemonService_SendDaemonCommandWithNoDaemonIsAUserError(t *testing.T) {
	s := &ToolsDaemonService{}
	_, err := s.SendDaemonCommand(t.Context(), "user-1", nil)
	assert.ErrorContains(t, err, "no daemon connected for user")
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
}

// UpdateWorkflowParams racing a run that just finished used to answer
// Internal and log ERROR: 11 of that RPC's 12 prod ERRORs in five days.
func TestStaleWorkflowSignalErrorIsAPrecondition(t *testing.T) {
	stale := staleWorkflowSignalError(serviceerror.NewNotFound("workflow execution already completed"))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(stale))
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(stale))

	assert.Nil(t, staleWorkflowSignalError(errors.New("context deadline exceeded")),
		"any other signal failure stays an Internal the handler logs")
}
