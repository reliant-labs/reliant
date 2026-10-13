// Copyright (c) 2025 Reliant Labs
package services

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"go.temporal.io/api/serviceerror"
)

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
