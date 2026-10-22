// Copyright (c) 2025 Reliant Labs
package core

import (
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// IntegrationWriteStatus is where a ledger entry is in its life.
type IntegrationWriteStatus string

const (
	// IntegrationWriteIntent is recorded BEFORE the request goes out.
	IntegrationWriteIntent IntegrationWriteStatus = "intent"
	// IntegrationWriteDone is a call the provider accepted.
	IntegrationWriteDone IntegrationWriteStatus = "done"
	// IntegrationWriteFailed is a call that did not take effect (Ambiguous
	// says whether that is certain).
	IntegrationWriteFailed IntegrationWriteStatus = "failed"
)

// The actors a write can be made as.
const (
	// IntegrationActorOwner is the run owner's own credential.
	IntegrationActorOwner = "owner"
	// IntegrationActorApp is an app installation's.
	IntegrationActorApp = "app"
)

// IntegrationWrite is one mutating integration call made on a run's behalf:
// the ledger an arriving event is attributed against. It is an audit trail, so
// it has no foreign keys and outlives the chat it describes.
type IntegrationWrite struct {
	ID     string
	UserID string
	// ChatID is the run that made the call; WorkflowName its ROOT workflow.
	ChatID       string
	WorkflowName string
	// TriggerID is the trigger that launched that run ("" when none did);
	// TriggerName the declared trigger's name, frozen at write time.
	TriggerID   string
	TriggerName string
	NodeID      string
	ToolCallID  string

	Integration  string
	Action       string
	ConnectionID string
	// Actor is IntegrationActorOwner or IntegrationActorApp.
	Actor string
	// Scope is the container written into ("github:owner/repo"); Target the
	// thing written ("github:owner/repo#12", "github:owner/repo@branch"), or
	// the scope itself until a creation's response names the new object.
	Scope  string
	Target string
	// Correlation is what an echo would carry; its keys are named by
	// internal/integrations/provenance.
	Correlation map[string]any
	// ObjectID and ObjectURL are the provider's id for the object the write
	// produced or touched, set when the call returns.
	ObjectID  string
	ObjectURL string

	Status IntegrationWriteStatus
	// Ambiguous marks a failed write whose outcome is unknown (a timeout, a
	// 5xx): the provider may have applied it, so its echo still counts.
	Ambiguous bool
	Error     string

	CreatedAt   time.Time
	CompletedAt *time.Time
}

// IntegrationWriteResult settles an intent row.
type IntegrationWriteResult struct {
	Status       IntegrationWriteStatus
	ConnectionID string
	// Target replaces the intent's target when non-empty (a creation learns
	// its number from the response).
	Target    string
	ObjectID  string
	ObjectURL string
	Ambiguous bool
	Error     string
	At        time.Time
}

// ErrIntegrationWriteNotFound is returned when no ledger entry matches.
var ErrIntegrationWriteNotFound = svcerr.NotFound("integration write")
