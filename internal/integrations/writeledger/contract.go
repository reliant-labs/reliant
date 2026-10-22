// Copyright (c) 2025 Reliant Labs

// Package writeledger records every MUTATING integration call a run makes
// (a GitHub comment, review, label, pull request, ...) in the integration_writes
// ledger: an intent row committed BEFORE the request goes out, completed with
// the provider's object id after. When an event later arrives, the ledger is
// what says Reliant caused it (internal/triggers' origin enricher).
//
// The intent comes first because GitHub sends the webhook for a write within
// moments, often before our own response has been read. With the intent
// already committed, "the webhook beat the response" is not a race: the event
// matches by target and content, and the id is filled in when the call returns.
//
// Recorder.Wrap decorates the one method both doors share
// (httpaction.Runner.RunAuthenticated: the workflow action node and the
// agent's integration tools), so no mutating call can skip the ledger.
//
// Built by hand, by internal/app's NewWriteLedger provider
// (providers_worker.go): never a node compose.go constructs from Deps.
//
//forge:provided
package writeledger

import (
	"context"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// Service is the write ledger: it decorates the authenticated call so no
// mutating integration call goes out unrecorded, and it prunes old entries.
// New returns it; a caller holding a nil Service records nothing and makes the
// call undecorated.
type Service interface {
	// Wrap returns inner with every mutating call recorded.
	Wrap(inner Runner) Runner
	// Prune deletes entries older than Retention. It reports how many.
	Prune(ctx context.Context, pruner Pruner) (int64, error)
	// PruneLoop prunes hourly until ctx is done.
	PruneLoop(ctx context.Context, pruner Pruner)
	// BeginPush records the intent to push a branch, with the sha it will end
	// on, BEFORE the push is made, and returns the handle that settles it. It
	// refuses (an error, nothing pushed) when the intent cannot be recorded.
	// This is how a git_publish node's push is attributed: it goes over git, not
	// through Wrap.
	BeginPush(ctx context.Context, p Push) (*PushIntent, error)
}

// Pruner deletes old ledger entries. db.IntegrationWriteLedger satisfies it.
type Pruner interface {
	PruneIntegrationWrites(ctx context.Context, before time.Time) (int64, error)
}

// Runner is the authenticated call the ledger decorates. *httpaction.Runner
// implements it, and so does the decorator itself.
type Runner interface {
	RunAuthenticated(ctx context.Context, integration *reliantv1.IntegrationManifest, action *reliantv1.ActionSpec, params map[string]any, src httpaction.CredentialSource, site httpaction.CallSite) (*httpaction.Result, error)
}

// Store is the ledger a Recorder writes to. db.IntegrationWriteLedger
// satisfies it.
type Store interface {
	CreateIntegrationWrite(ctx context.Context, w *core.IntegrationWrite) error
	CompleteIntegrationWrite(ctx context.Context, id string, res core.IntegrationWriteResult) (bool, error)
}

// RunLookup resolves the run a call is made for. *db.Repo satisfies it.
type RunLookup interface {
	GetWorkflow(ctx context.Context, id string) (*core.Workflow, error)
	GetChat(ctx context.Context, id string) (*core.Chat, error)
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
}

// Deps are the Recorder's collaborators.
type Deps struct {
	Store Store
	Runs  RunLookup
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Retention is how long a ledger entry is kept. An echo arrives within
// minutes; the rest is audit.
const Retention = 30 * 24 * time.Hour
