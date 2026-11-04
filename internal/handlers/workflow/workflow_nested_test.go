package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/stretchr/testify/require"
)

type nestedWorkflowStore struct{ workflows []config.StoredWorkflow }

func (s nestedWorkflowStore) GetProjectWorkflowsJSON(context.Context, string) (*string, error) {
	data, err := json.Marshal(s.workflows)
	v := string(data)
	return &v, err
}

func TestProjectWorkflowListKeepsQualifiedCollisions(t *testing.T) {
	store := nestedWorkflowStore{workflows: []config.StoredWorkflow{
		{RelativePath: "control-plane/.reliant/workflows/steward.yaml", YAMLContent: "name: merge-steward\n"},
		{RelativePath: "forge/.reliant/workflows/steward.yaml", YAMLContent: "name: merge-steward\n"},
	}}
	items, invalid := discoverProjectWorkflowsFromDB(store, context.Background(), "project")
	require.Empty(t, invalid)
	require.Len(t, items, 2)
	require.Equal(t, "control-plane/merge-steward", items[0].Filename)
	require.Equal(t, "forge/merge-steward", items[1].Filename)
	require.Equal(t, items[0].Filename, items[0].Name)
	require.Equal(t, items[1].Filename, items[1].Name)
	for _, item := range items {
		require.Len(t, item.ValidationErrors, 1)
		require.Equal(t, "warning:discovery", item.ValidationErrors[0].Type)
	}
}
