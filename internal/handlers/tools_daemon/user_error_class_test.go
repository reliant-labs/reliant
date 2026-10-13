// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
)

func TestToolsDaemonService_SendDaemonCommandWithNoDaemonIsAUserError(t *testing.T) {
	s := &Service{}
	_, err := s.SendDaemonCommand(t.Context(), "user-1", nil)
	assert.ErrorContains(t, err, "no daemon connected for user")
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
}
