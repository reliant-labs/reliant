// Copyright (c) 2025 Reliant Labs
package run

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The run log's other halves are a required role of their own: a service given
// no RunLists is refused at construction, not at its first list.
func TestDepsRefuseAMissingRunLists(t *testing.T) {
	var _ RunLists = store.NewRunListExtStore(nil)

	full := Deps{
		Logger: slog.Default(), Config: &config.Config{}, Repo: &MockRepository{}, RunLists: &MockRunLists{},
		Workflows: &MockWorkflowLoader{}, ChatRows: &MockChatRows{}, ChatViews: &MockChatViews{}, Chats: &MockChat{},
	}
	require.NoError(t, full.validateDeps())

	missing := full
	missing.RunLists = nil
	require.ErrorContains(t, missing.validateDeps(), "Deps.RunLists is required")
}
