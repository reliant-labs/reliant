// Copyright (c) 2025 Reliant Labs
package activities

import (
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"go.temporal.io/sdk/client"
)

// Activities is the dependency container for all runtime workflow activities
// This is a SIMPLE struct that just holds shared dependencies
// Unlike V1, it has NO methods - activities are separate structs
type Activities struct {
	Repo           db.Repository
	StreamingHub   streaming.StreamingHub
	Threads        *threads.Service
	ToolsFactory   *tools.ToolsFactory
	ToolExecutor   toolexec.ToolExecutor
	DaemonRouter   toolexec.DaemonRouter // Routes commands to user's daemon (required for cloud worktree ops)
	ConfigProvider config.ConfigProvider
	RunExecutor    handlers.RunExecutor   // Optional: for testing shell command execution
	DriverResolver drivers.DriverResolver // Optional: custom LLM driver resolver (nil = use drivers.GetDriver)
	TemporalClient client.Client
}

// NewActivities creates a new Activities container
func NewActivities(
	repo db.Repository,
	hub streaming.StreamingHub,
	threadsService *threads.Service,
	toolsFactory *tools.ToolsFactory,
	toolExecutor toolexec.ToolExecutor,
	daemonRouter toolexec.DaemonRouter,
	temporalClient client.Client,
	configProvider config.ConfigProvider,
) *Activities {
	return &Activities{
		Repo:           repo,
		StreamingHub:   hub,
		Threads:        threadsService,
		ToolsFactory:   toolsFactory,
		ToolExecutor:   toolExecutor,
		DaemonRouter:   daemonRouter,
		ConfigProvider: configProvider,
		RunExecutor:    nil, // Use default executor
		TemporalClient: temporalClient,
	}
}

// WithRunExecutor returns a copy of Activities with a custom RunExecutor.
// This is primarily used for testing to inject mock executors.
func (a *Activities) WithRunExecutor(executor handlers.RunExecutor) *Activities {
	copy := *a
	copy.RunExecutor = executor
	return &copy
}

// WithToolExecutor returns a copy of Activities with a custom ToolExecutor.
// This is primarily used for testing to inject mock executors.
func (a *Activities) WithToolExecutor(executor toolexec.ToolExecutor) *Activities {
	copy := *a
	copy.ToolExecutor = executor
	return &copy
}
