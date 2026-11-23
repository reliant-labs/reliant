// Copyright (c) 2026 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// The definition load reports the org of the run's chat, as the worker's
// run-scope interceptor put it on the activity's context, so the workflow can
// name its own span with it (runtime.nameRunOrg). With no scope it reports
// none.
func TestLoadWorkflowActivity_ReportsTheRunScopesOrg(t *testing.T) {
	t.Parallel()
	activity := NewLoadWorkflowActivity(nil, nil)

	scoped := telemetry.WithRunScope(context.Background(), telemetry.RunScope{ChatID: "chat-1", OrgID: "org-1"})
	out, err := activity.Execute(scoped, LoadWorkflowInput{WorkflowName: "builtin://agent"})
	require.NoError(t, err)
	assert.Equal(t, "org-1", out.OrgID)

	out, err = activity.Execute(context.Background(), LoadWorkflowInput{WorkflowName: "builtin://agent"})
	require.NoError(t, err)
	assert.Empty(t, out.OrgID)
}
