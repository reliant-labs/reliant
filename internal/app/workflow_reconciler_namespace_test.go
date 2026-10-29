// Copyright (c) 2025 Reliant Labs
package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
)

// Reset-and-replay failed 9,510 times in prod with "Namespace reliant is not
// found": the reset named a hardcoded namespace instead of the one the Temporal
// client was dialed for. #1797 fixed the PauseService and ResetExpiredWorkflow
// paths; the reconciler's own reset (stuck-task recovery) reads
// ReconcilerConfig.Namespace, whose default is the hardcoded one. The
// composition root must override it from the process config, and fall back to
// the client's namespace — not the constant — when the config names none.
func TestNewWorkflowReconciler_NamesTheNamespaceTheClientWasDialedFor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, configured, dialed, want string
	}{
		{"configured", "reliant-prod", "reliant-prod", "reliant-prod"},
		{"not the constant", "default", "default", "default"},
		{"config names none: the client's", "", "reliant-prod", "reliant-prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, err := NewWorkflowReconciler(WorkflowReconcilerDeps{
				Temporal:  &TemporalClient{Client: client.Client(nil), Namespace: tc.dialed},
				Router:    &APIDaemonRouter{},
				Namespace: tc.configured,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, r.reconciler.Namespace())
		})
	}
}
