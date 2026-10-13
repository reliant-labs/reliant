// Copyright (c) 2025 Reliant Labs
package activities

import (
	"testing"

	"github.com/stretchr/testify/require"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

// The workflow dispatches the stopped-sub-agent lookup by name, and the
// handler registers under its own; the two packages cannot import each other,
// so this is where a rename on one side is caught. A mismatch would leave
// every message to an agent that stopped in an earlier run unread.
func TestLookupStoppedSubAgentNameMatchesTheWorkflow(t *testing.T) {
	require.Equal(t, v2.LookupStoppedSubAgentActivityName, handlers.NewLookupStoppedSubAgentActivity(nil).Name())
}
