// Copyright (c) 2025 Reliant Labs
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
//
//forge:exclude-contract: Temporal activity registration; the shape is dictated by the Temporal SDK
package activities

import (
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/serveraction"
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
	Repo *db.Repo
	// LLMUsage records every model request in the usage ledger. Optional: nil
	// records nothing.
	LLMUsage handlers.LLMUsageRecorder
	// ToolUsage records every tool call in the tool usage ledger. Optional:
	// nil records nothing.
	ToolUsage handlers.ToolUsageRecorder
	// ProviderBackoff marks a thread parked on a provider (the driver's retry
	// ladder, a usage-limit wait). Required with Repo.
	ProviderBackoff handlers.ProviderBackoffMarkers
	StreamingHub    streaming.StreamingHub
	Threads         *threads.Service
	ToolsFactory    *tools.ToolsFactory
	ToolExecutor    toolexec.ToolExecutor
	DaemonRouter    toolexec.DaemonRouter // Routes commands to user's daemon (required for cloud worktree ops)
	MCPBinder       toolexec.MCPContextBinder
	ConfigProvider  config.ConfigProvider
	RunExecutor     handlers.RunExecutor   // Optional: for testing shell command execution
	DriverResolver  drivers.DriverResolver // Optional: custom LLM driver resolver (nil = use drivers.GetDriver)
	TemporalClient  client.Client
	// Connections resolves a run's saved connection for integration actions.
	// Optional: nil makes an action that names a connection fail rather than
	// run unauthenticated.
	Connections httpaction.CredentialSource
	// HostedActions dispatches only explicitly granted fixed HTTP action nodes.
	// It is intentionally separate from generic integration-tool credentials.
	HostedActions handlers.HostedActionExecutor
	// SecretResolver resolves the workflow's declared secrets for the steps that
	// run a command or a request. Optional: nil makes a step that uses a secret
	// fail rather than run with the placeholder.
	SecretResolver handlers.SecretResolver
	// AllowPrivateEgress picks the dial policy for user-supplied model
	// endpoint URLs (netguard.ForDeployment); false refuses private targets.
	AllowPrivateEgress bool
	// WriteLedger records the action node's mutating integration calls.
	// Optional: nil records nothing.
	WriteLedger writeledger.Service
	// AccountRouting routes call_llm and compaction across a provider's
	// accounts. Optional: nil uses each provider's default account.
	AccountRouting *handlers.AccountRouting
	// ServerActions runs the action nodes that name a Reliant server action
	// (uses: reliant/...). Optional: nil refuses every one as switched off,
	// which is also what a dispatcher built with pipeline.enabled off does.
	ServerActions *serveraction.Dispatcher
}

// NewActivities creates a new Activities container
func NewActivities(
	repo *db.Repo,
	hub streaming.StreamingHub,
	threadsService *threads.Service,
	toolsFactory *tools.ToolsFactory,
	toolExecutor toolexec.ToolExecutor,
	daemonRouter toolexec.DaemonRouter,
	mcpBinder toolexec.MCPContextBinder,
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
		MCPBinder:      mcpBinder,
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
