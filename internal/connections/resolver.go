// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

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

// TriggerSite identifies one use of a trigger's connection outside any run: a
// poll. It carries the trigger id and nothing else. The owner, the connection
// and the integration are read from the trigger row, so there is no field a
// forged poll input could set to reach another user's connection.
type TriggerSite struct {
	TriggerID string
	Placement Placement
}

// runOwners reads who a run, or a trigger, belongs to. Satisfied by
// db.Repository.
type runOwners interface {
	GetWorkflow(ctx context.Context, id string) (*core.Workflow, error)
	GetChat(ctx context.Context, id string) (*core.Chat, error)
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
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

// NewResolver builds the resolver. Credential placement comes from the
// integrations the token source's registry was compiled from.
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
	// owner and generation identify the token for RefreshAfterRejection.
	owner      string
	generation int64
	tokens     *TokenSource
	params     map[string]string
	// hosts are the hosts this integration's catalog entry lets a request
	// reach: base_url's (with the connection's params expanded) and
	// allowed_hosts. Apply refuses any other. Empty means the integration
	// takes any public host (the generic HTTP one), where the runtime's
	// start-host pin is the guard.
	hosts map[string]bool
}

// Params are the connection's non-secret settings (a Shopify shop).
func (r *Resolved) Params() map[string]string {
	out := make(map[string]string, len(r.params))
	for k, v := range r.params {
		out[k] = v
	}
	return out
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

// Apply writes the credential into req, and only into a request to one of the
// integration's own hosts over https. The declarative runner already pins a
// credential to the host its call started at; this pin is the credential's
// own, so a Go executor or a poller that builds its own URL cannot send it
// anywhere the catalog did not name.
func (r *Resolved) Apply(req *http.Request) error {
	if req == nil || req.URL == nil || req.URL.Scheme != "https" || req.URL.Hostname() == "" || req.URL.User != nil {
		return newError(CodeFailedPrecondition, "a %s credential is only sent over https", r.IntegrationID)
	}
	if len(r.hosts) > 0 && !r.hosts[strings.ToLower(req.URL.Host)] && !r.hosts[strings.ToLower(req.URL.Hostname())] {
		return newError(CodeFailedPrecondition, "a %s credential is never sent to %q: it is not one of the integration's hosts", r.IntegrationID, req.URL.Host)
	}
	return r.auth.Apply(req, r.secret, r.Redactor)
}

// RefreshAfterRejection replaces a credential the provider refused (a 401 for
// a token that had not expired) and returns the replacement, held to the same
// hosts and scrubbing into the same Redactor (so text that echoed the old
// token is still scrubbed). A grant that is dead returns CodeNeedsReauth and
// marks the connection. Call it once per rejection: a second 401 with the
// replacement means the connection is not usable.
func (r *Resolved) RefreshAfterRejection(ctx context.Context) (*Resolved, error) {
	if r.tokens == nil {
		return nil, newError(CodeNeedsReauth, "connection %q was refused and cannot be refreshed", r.ConnectionID)
	}
	secret, gen, err := r.tokens.RefreshAfterRejection(ctx, r.owner, r.ConnectionID, r.generation)
	if err != nil {
		return nil, err
	}
	next := *r
	next.secret, next.generation = secret, gen
	return &next, nil
}

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
	actor := ActorWorker
	if call.TriggerID != "" {
		actor = ActorTrigger(call.TriggerID)
	}
	return r.resolve(ctx, owner, ref, core.ConnectionEvent{
		RunID: call.RunID, NodeID: call.NodeID, ToolCallID: call.ToolCallID, Actor: actor,
	})
}

// ForTrigger resolves the connection a trigger listens through, for a use
// that has no run: a poll.
//
// Everything that decides WHOSE credential this is comes from the trigger
// row — its owner (user_id), its connection (connection_id) and its
// integration (config.integration) — exactly as ForCall reads the owner from
// the run row. The connection is then held to every rule ForCall applies:
// server placement only, the owner's own connection (a foreign or missing id
// reads like none), for that integration, active (needs_reauth is a typed
// CodeNeedsReauth), refreshed through the TokenSource, and the use audited
// with the trigger as actor.
func (r *Resolver) ForTrigger(ctx context.Context, site TriggerSite) (*Resolved, error) {
	if site.Placement != PlacementServer {
		return nil, &Error{Code: CodeFailedPrecondition, Message: ErrDaemonPlacement.Error(), Err: ErrDaemonPlacement}
	}
	if site.TriggerID == "" {
		return nil, newError(CodeFailedPrecondition, "no trigger to resolve a connection for")
	}
	trigger, err := r.owners.GetTrigger(ctx, site.TriggerID)
	if err != nil || trigger == nil {
		return nil, newError(CodeFailedPrecondition, "trigger %q not found", site.TriggerID)
	}
	if trigger.UserID == "" {
		return nil, newError(CodeFailedPrecondition, "trigger %q has no owner", site.TriggerID)
	}
	if trigger.Kind != core.TriggerKindIntegration {
		return nil, newError(CodeFailedPrecondition, "trigger %q is a %s trigger, which listens through no connection", site.TriggerID, trigger.Kind)
	}
	var cfg core.IntegrationConfig
	if err := json.Unmarshal(trigger.Config, &cfg); err != nil || cfg.Integration == "" {
		return nil, newError(CodeFailedPrecondition, "trigger %q names no integration", site.TriggerID)
	}
	if trigger.ConnectionID == nil || *trigger.ConnectionID == "" {
		// The connection was deleted (the FK nulls it). Never fall back to the
		// owner's default: the owner picked a specific account to listen to.
		return nil, noConnection(Ref{})
	}
	return r.resolve(ctx, trigger.UserID, Ref{ConnectionID: *trigger.ConnectionID, IntegrationID: cfg.Integration},
		core.ConnectionEvent{Actor: ActorTrigger(trigger.ID)})
}

// Usable answers, for each ref, what ForCall would for a call from this run,
// short of reading the secret: nil when the run's owner has a connection a call
// could authenticate with, else the typed error ForCall would return (no
// connection, CodeNeedsReauth, a kind the catalog no longer offers, ...). The
// owner is read once, from the run record, exactly as ForCall reads it; an
// owner that cannot be read is the returned error.
//
// It records no use and decrypts nothing, so it is safe to ask before every
// model turn — which is what it is for: offering an integration's tools only
// to a run that can authenticate them. The one outcome it cannot foresee is a
// token refresh that fails at call time.
func (r *Resolver) Usable(ctx context.Context, call CallSite, refs []Ref) (map[Ref]error, error) {
	if call.Placement != PlacementServer {
		return nil, &Error{Code: CodeFailedPrecondition, Message: ErrDaemonPlacement.Error(), Err: ErrDaemonPlacement}
	}
	owner, err := r.OwnerOf(ctx, call.RunID)
	if err != nil {
		return nil, err
	}
	out := make(map[Ref]error, len(refs))
	for _, ref := range refs {
		_, err := r.lookup(ctx, owner, ref)
		out[ref] = err
	}
	return out, nil
}

// usableConnection is what lookup establishes: the connection a ref names for
// its owner, and how this deployment authenticates with it.
type usableConnection struct {
	conn  *core.Connection
	auth  Authenticator
	hosts map[string]bool
}

// lookup is the half of resolve that decides WHICH connection (owner, ref)
// names and whether it can authenticate a call — every rule short of reading
// the secret. resolve and Usable share it, so a connection Usable reports is
// one resolve would hand out.
func (r *Resolver) lookup(ctx context.Context, owner string, ref Ref) (*usableConnection, error) {
	var (
		conn *core.Connection
		err  error
	)
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

	prov, ok := r.tokens.providers.Get(conn.IntegrationID)
	if !ok {
		return nil, newError(CodeFailedPrecondition, "integration %q is not in this deployment's catalog", conn.IntegrationID)
	}
	auth, err := authenticatorFor(prov, conn)
	if err != nil {
		return nil, err
	}
	hosts, err := prov.hosts(conn.Params)
	if err != nil {
		return nil, err
	}
	return &usableConnection{conn: conn, auth: auth, hosts: hosts}, nil
}

// resolve turns (owner, ref) into a credential. It is the one implementation
// of the rules every door shares; use carries the audit attribution.
func (r *Resolver) resolve(ctx context.Context, owner string, ref Ref, use core.ConnectionEvent) (*Resolved, error) {
	found, err := r.lookup(ctx, owner, ref)
	if err != nil {
		return nil, err
	}
	conn, auth, hosts := found.conn, found.auth, found.hosts
	secret, gen, err := r.tokens.token(ctx, owner, conn.ID)
	if err != nil {
		return nil, err
	}

	use.ConnectionID, use.UserID, use.Kind = conn.ID, owner, core.ConnectionEventUsed
	appendBestEffort(ctx, r.store, use)
	_ = r.store.TouchConnectionUsed(context.WithoutCancel(ctx), owner, conn.ID)

	return &Resolved{
		ConnectionID: conn.ID, IntegrationID: conn.IntegrationID, Redactor: NewRedactor(),
		auth: auth, secret: secret, owner: owner, generation: gen, tokens: r.tokens,
		params: conn.Params, hosts: hosts,
	}, nil
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
