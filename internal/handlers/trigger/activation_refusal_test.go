// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// Classifying an error for the activation tools must not change what the
// app's RPCs answer with it: the same code and the same message.
func TestActivationRefusalKeepsTheRPCError(t *testing.T) {
	cause := svcerr.FailedPrecondition("connect github first: an integration trigger listens through one of your connections")
	classified := tools.RefuseActivation("no github connection", cause)

	require.Equal(t, "no github connection", tools.ActivationRefusalReason(classified))
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(classified))
	var plain, wrapped *connect.Error
	require.True(t, errors.As(svcerr.Wrap(cause), &plain))
	require.True(t, errors.As(svcerr.Wrap(classified), &wrapped))
	require.Equal(t, plain.Message(), wrapped.Message())

	// The innermost classification is the most specific; a stage wrap keeps it.
	require.Equal(t, "no github connection", tools.ActivationRefusalReason(tools.RefuseActivation("activation settings rejected", classified)))
	require.Empty(t, tools.ActivationRefusalReason(cause))
}
