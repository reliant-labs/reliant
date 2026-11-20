// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"testing"

	"connectrpc.com/connect/v2"
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
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.CodeV2(classified))
	// What the RPC sends for each: svcerr.InterceptorV2's wire form.
	plain, wrapped := svcerr.ToConnectV2(svcerr.Wrap(cause)), svcerr.ToConnectV2(svcerr.Wrap(classified))
	require.NotNil(t, plain)
	require.NotNil(t, wrapped)
	require.Equal(t, plain.Code(), wrapped.Code())
	require.Equal(t, plain.Message(), wrapped.Message())

	// The innermost classification is the most specific; a stage wrap keeps it.
	require.Equal(t, "no github connection", tools.ActivationRefusalReason(tools.RefuseActivation("activation settings rejected", classified)))
	require.Empty(t, tools.ActivationRefusalReason(cause))
}
