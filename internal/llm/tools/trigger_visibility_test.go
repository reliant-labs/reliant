package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

type diagnosticTriggerLister struct{}

func (diagnosticTriggerLister) ListTriggers(context.Context, string, TriggerListFilter) ([]TriggerSummary, error) {
	return []TriggerSummary{
		{ID: "t", Name: "steward", Workflow: "merge", DaemonID: "machine-1", DaemonName: "buildbox", MachineState: "suspended", Health: "FAILING", HealthDetail: "Node gate failed: command exited with code 2"},
		{ID: "none", Name: "web", NoMachine: true},
	}, nil
}

func TestListTriggersShowsMachineAndFailingNode(t *testing.T) {
	repo := &MockrunCallerStore{GetChatFunc: func(context.Context, string) (*db.Chat, error) { return &db.Chat{ID: "caller", UserID: "owner"}, nil }}
	resp, err := (&listTriggersTool{repo: repo, lister: diagnosticTriggerLister{}}).Execute(callerRC("caller", "tc"), ListTriggersParams{})
	require.NoError(t, err)
	require.Contains(t, resp.Content, "machine: buildbox (id machine-1), state suspended")
	require.Contains(t, resp.Content, "failing: Node gate failed: command exited with code 2")
	require.Contains(t, resp.Content, "machine: none (no-machine activation)")
}
