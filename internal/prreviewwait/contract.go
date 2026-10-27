// Copyright (c) 2025 Reliant Labs

// Package prreviewwait owns the narrow original-author review subscription.
// It is not a public event bus. Authenticated internal activities supply caller
// execution; trusted provider adapters supply outcomes. No app root enables it.
//
//forge:not-a-component: disabled review wait stage; registered only explicitly in tests until trusted producer/retention wiring approved
package prreviewwait

import (
	"context"
	"errors"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
)

// ErrStaleRead means a newer provider revision arrived between read/consume.
// The workflow rereads durable state; it is not a polling or transport failure.
var ErrStaleRead = errors.New("review wait read superseded")

// Subscription pins immutable original ownership plus a proven current cursor.
type Subscription struct {
	RequestKey string
	// RepoID/PR are server-resolved output metadata, not subscription authority.
	RepoID           int64
	PR               int64
	ID               string
	BindingID        string
	HeadSHA          string
	WorkflowID       string
	OriginalRunID    string
	CurrentRunID     string
	State            string
	Ready            bool
	NotifiedSequence int64
	ConsumedSequence int64
}

// Outcome contains only an authorized provider reference and source ordering.
// Sequence is an authoritative monotonic provider revision, never arrival time.
type Outcome struct {
	// OwnerID and AuthorityID come only from authenticated server producers.
	OwnerID     string
	AuthorityID string
	ID          string
	BindingID   string
	HeadSHA     string
	ProviderRef string
	Sequence    int64
	State       string
}

// NodeInput is the typed workflow node's subject, never its destination.
type NodeInput struct {
	// Occurrence is internal workflow node-path/iteration identity, not YAML args.
	Occurrence string
	Repo       string
	PR         int64
	HeadSHA    string
}

// Request comes from the executing activity's server context, never node args.
type Request struct {
	RequestKey string
	OwnerID    string
	ChatID     string
	Execution  runcontinuity.Execution
	RepoID     int64
	PR         int64
	HeadSHA    string
}

// AuthoritySubject is derived from the stored binding, never YAML or prefixes.
type AuthoritySubject struct {
	OwnerID      string
	RepositoryID int64
	PR           int64
	HeadSHA      string
}

// Authority pins one producer and policy revision for an entire head round.
// Outcome.Sequence is independent: it orders revisions within this authority.
type Authority struct {
	ID             string
	PolicyRevision int64
}

// AuthorityPolicy resolves authenticated owner/repository review policy. Nil is
// never permissive. Rotation on the same head requires a new round, not takeover.
type AuthorityPolicy interface {
	Resolve(context.Context, AuthoritySubject) (Authority, error)
}

// Store persists atomic arm, cursor, inbox and consumption transitions.
// Arm requires exact binding owner/chat/head and a proven original→caller chain.
type Store interface {
	Binding(context.Context, string, int64, int64) (prhandoff.Binding, error)
	// BindingByID must scope by authenticated owner before exposing policy subject.
	BindingByID(context.Context, string, string) (prhandoff.Binding, error)
	// BindAuthority verifies immutable binding owner/current head atomically.
	BindAuthority(context.Context, prhandoff.Binding, Authority) error
	Arm(context.Context, Request, runcontinuity.Result, Authority) (Subscription, error)
	Ready(context.Context, Subscription) error
	Read(context.Context, Subscription) (Outcome, error)
	Consume(context.Context, Subscription, Outcome) error
	Cancel(context.Context, Subscription) error
	CancelRequest(context.Context, Request) error
	Put(context.Context, Outcome, Authority) (Subscription, error)
	Advance(context.Context, Subscription, runcontinuity.Result) (Subscription, error)
	Notified(context.Context, Subscription, int64) error
	// Unavailable marks a terminal original author without inventing a successor.
	Unavailable(context.Context, Subscription) error
}

// Continuity proves current recipients from the immutable enrolled execution.
type Continuity interface {
	Resolve(context.Context, runcontinuity.Execution) (runcontinuity.Result, error)
}

// Signaler sends only an exact wfID+runID doorbell; no body or resume/restart.
type Signaler interface {
	SignalReview(context.Context, runcontinuity.Execution, string) error
}

// Service is the original-author wait lifecycle, not arbitrary message routing.
type Service interface {
	Arm(context.Context, Request) (Subscription, error)
	Ready(context.Context, Subscription) error
	Read(context.Context, Subscription) (Outcome, error)
	Consume(context.Context, Subscription, Outcome) error
	Cancel(context.Context, Subscription) error
	CancelRequest(context.Context, Request) error
	Publish(context.Context, Outcome) error
}

// Deps are the persistence and authenticated Temporal proof/delivery seams.
type Deps struct {
	Policy     AuthorityPolicy
	Store      Store
	Continuity Continuity
	Signals    Signaler
}
