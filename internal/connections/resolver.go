// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Placement says where the action asking for a connection runs. The zero value
// is deliberately NOT a permitted placement: a caller that forgets to say is
// refused rather than granted (decision 6, CONNECTIONS_VAULT.md §8.2).
type Placement int

const (
	PlacementUnspecified Placement = iota
	// PlacementServer: the worker process. The only placement that may use a connection.
	PlacementServer
	// PlacementDaemon: the user's machine or workspace. Never allowed, so a
	// token cannot leave the server.
	PlacementDaemon
)

// Ref names the connection a call wants.
type Ref struct {
	// ConnectionID is explicit; when empty the owner's default for
	// IntegrationID is used.
	ConnectionID  string
	IntegrationID string
}

// CallSite identifies one call for authorization and audit. UserID is
// deliberately absent: the owner is read from the run record, never supplied.
type CallSite struct {
	RunID      string
	NodeID     string
	ToolCallID string
	Placement  Placement
	// TriggerID, when set, attributes the use to an unattended fire.
	TriggerID string
}

// runOwners reads who a run belongs to. Satisfied by db.Repository.
type runOwners interface {
	GetWorkflow(ctx context.Context, id string) (*core.Workflow, error)
	GetChat(ctx context.Context, id string) (*core.Chat, error)
}

type resolverStore interface {
	tokenStore
	DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error)
	TouchConnectionUsed(ctx context.Context, userID, id string) error
}

// Resolver turns (run, connection reference) into an authenticated request
// (CONNECTIONS_VAULT.md §3.1).
type Resolver struct {
	owners runOwners
	store  resolverStore
	tokens *TokenSource
}

// NewResolver builds the resolver.
func NewResolver(owners runOwners, store resolverStore, tokens *TokenSource) *Resolver {
	return &Resolver{owners: owners, store: store, tokens: tokens}
}

// Resolved is a connection ready to authenticate one call. The plaintext is
// held as a vault.Secret and reaches a request only through Apply.
type Resolved struct {
	ConnectionID  string
	IntegrationID string
	// Redactor scrubs the applied credential out of anything the caller is
	// about to return or log.
	Redactor *Redactor

	auth   Authenticator
	secret vault.Secret
}

// Value receivers, so both Resolved and *Resolved redact. fmt cannot call
// Format on an unexported Secret field, so without these a %v of a Resolved
// VALUE would print the plaintext bytes the Secret type exists to hide.
func (r *Resolved) String() string            { return "connections.Resolved{" + r.ConnectionID + "}" }
func (r *Resolved) GoString() string          { return r.String() }
func (r Resolved) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, r.String()) }
func (r Resolved) LogValue() slog.Value {
	return slog.GroupValue(slog.String("connection_id", r.ConnectionID), slog.String("integration_id", r.IntegrationID))
}

// Apply writes the credential into req.
func (r *Resolved) Apply(req *http.Request) error { return r.auth.Apply(req, r.secret, r.Redactor) }

// OwnerOf returns the user a run belongs to, read from the database. It tries
// the run's recorded owner first and the chat second, the same order the rest
// of the engine uses while both sources exist.
func (r *Resolver) OwnerOf(ctx context.Context, runID string) (string, error) {
	if runID == "" {
		return "", newError(CodeFailedPrecondition, "no run to resolve a connection for")
	}
	run, err := r.owners.GetWorkflow(ctx, runID)
	if err != nil || run == nil {
		return "", newError(CodeFailedPrecondition, "run %q not found", runID)
	}
	if run.OwnerUserID != nil && *run.OwnerUserID != "" {
		return *run.OwnerUserID, nil
	}
	if run.ChatID != "" {
		if chat, err := r.owners.GetChat(ctx, run.ChatID); err == nil && chat != nil && chat.UserID != "" {
			return chat.UserID, nil
		}
	}
	return "", newError(CodeFailedPrecondition, "run %q has no owner", runID)
}

// ForCall resolves ref for the run's owner.
//
// Order (INTEGRATIONS §4.3, v1): the explicit connection id, then the owner's
// default for the integration. A foreign or missing id answers exactly like no
// connection at all, so a workflow cannot probe for another user's ids.
func (r *Resolver) ForCall(ctx context.Context, call CallSite, ref Ref) (*Resolved, error) {
	if call.Placement != PlacementServer {
		return nil, &Error{Code: CodeFailedPrecondition, Message: ErrDaemonPlacement.Error(), Err: ErrDaemonPlacement}
	}
	owner, err := r.OwnerOf(ctx, call.RunID)
	if err != nil {
		return nil, err
	}

	var conn *core.Connection
	switch {
	case ref.ConnectionID != "":
		conn, err = r.store.GetConnection(ctx, owner, ref.ConnectionID)
	case ref.IntegrationID != "":
		conn, err = r.store.DefaultConnection(ctx, owner, ref.IntegrationID)
	default:
		return nil, newError(CodeFailedPrecondition, "the action names no connection or integration")
	}
	if err != nil {
		if isNotFound(err) {
			return nil, noConnection(ref)
		}
		return nil, mapStoreErr(err)
	}
	if conn.OwnerKind != core.ConnectionOwnerUser || conn.UserID != owner {
		return nil, noConnection(ref)
	}
	if ref.IntegrationID != "" && conn.IntegrationID != ref.IntegrationID {
		return nil, newError(CodeFailedPrecondition, "connection %q is for %q, not %q", conn.Name, conn.IntegrationID, ref.IntegrationID)
	}
	switch conn.Status {
	case core.ConnectionStatusActive:
	case core.ConnectionStatusNeedsReauth:
		return nil, newError(CodeNeedsReauth, "connection %q needs to be reconnected", conn.Name)
	default:
		return nil, noConnection(ref)
	}

	header := ""
	if conn.AuthHeader != nil {
		header = *conn.AuthHeader
	}
	auth, err := AuthenticatorFor(conn.AuthKind, header)
	if err != nil {
		return nil, err
	}
	secret, err := r.tokens.Token(ctx, owner, conn.ID)
	if err != nil {
		return nil, err
	}

	actor := ActorWorker
	if call.TriggerID != "" {
		actor = ActorTrigger(call.TriggerID)
	}
	appendBestEffort(ctx, r.store, core.ConnectionEvent{
		ConnectionID: conn.ID, UserID: owner, Kind: core.ConnectionEventUsed,
		RunID: call.RunID, NodeID: call.NodeID, ToolCallID: call.ToolCallID, Actor: actor,
	})
	_ = r.store.TouchConnectionUsed(context.WithoutCancel(ctx), owner, conn.ID)

	return &Resolved{ConnectionID: conn.ID, IntegrationID: conn.IntegrationID, Redactor: NewRedactor(), auth: auth, secret: secret}, nil
}

func noConnection(ref Ref) error {
	if ref.IntegrationID != "" {
		return newError(CodeFailedPrecondition, "no %s connection", ref.IntegrationID)
	}
	return newError(CodeFailedPrecondition, "no such connection")
}

func isNotFound(err error) bool {
	return err != nil && (err == core.ErrConnectionNotFound || CodeOf(mapStoreErr(err)) == CodeNotFound)
}
