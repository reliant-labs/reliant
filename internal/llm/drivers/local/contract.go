// Copyright (c) 2025 Reliant Labs
//
// Package local is the llm.Driver for local OpenAI-compatible model servers
// (Ollama, LM Studio, llama.cpp, vLLM): the ones a user's daemons detect, and
// the ones a user configures as model endpoints. The driver's behavioral
// contract is llm.Driver, declared in internal/llm/types.go; LocalClient
// implements it.
//
// This file declares the seams the package owns: Directory, which lists a
// user's daemons and the local model inventories they published, the optional
// EndpointLister beside it, and the narrow data sources they read.
// RepoDirectory is the production Directory; `forge generate` writes a mock of
// each interface into mock_gen.go.
//
// forge:outbound-io
//
// LocalClient calls a model server over HTTP, directly or relayed through the
// user's daemon, and serves nothing inbound.
//
//forge:not-a-component: built by internal/llm/drivers, the model catalog (internal/handlers/catalog) and the call_llm activity — never a node of the composition graph
package local

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

// Directory lists a user's daemons that published local model inventories.
// Declared here, at the consumer; RepoDirectory is the production one.
type Directory interface {
	LocalDaemons(ctx context.Context, userID string) ([]DaemonInventory, error)
}

// EndpointLister is optionally implemented by a Directory that can also list
// the user's configured endpoints.
type EndpointLister interface {
	ConfiguredEndpoints(ctx context.Context, userID string) ([]EndpointConfig, error)
}

// Source is the slice of *db.Repo RepoDirectory reads.
type Source interface {
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	ListDaemonLocalModels(ctx context.Context, userID string) (map[string]string, error)
	ListFreshDaemonAttachmentsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]*db.DaemonAttachment, error)
}

// EndpointSource is the slice of *db.Repo the endpoint listing reads,
// beyond Source.
type EndpointSource interface {
	ListModelEndpoints(ctx context.Context, userID string) ([]*db.ModelEndpoint, error)
}

// CredentialSource yields an endpoint's stored credentials. Declared here, at
// its consumer; modelendpoints.EndpointCredentials satisfies it.
type CredentialSource interface {
	Get(ctx context.Context, userID, connectionID string) (apiKey string, headers map[string]string, err error)
}
