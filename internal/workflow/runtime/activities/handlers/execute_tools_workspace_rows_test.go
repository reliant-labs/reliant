package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

func TestLoadToolExecutionContextWithoutWorkspaceRows(t *testing.T) {
	repo := withChatExecuteTools{GetChatFunc: func(context.Context, string) (*db.Chat, error) {
		return &db.Chat{ID: "chat", ProjectID: "project"}, nil
	}, MockExecuteToolsStore: &MockExecuteToolsStore{}}
	a := NewExecuteToolsActivity(ExecuteToolsDeps{Store: repo})

	tec, errMsg := a.loadToolExecutionContext(context.Background(), "chat", "root", "shell", "{}", "call", "")

	require.Nil(t, tec)
	require.Equal(t, "Failed to load project: workspace storage is unavailable", errMsg)
}
