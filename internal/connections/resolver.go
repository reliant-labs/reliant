// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
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
	// ConnectionID is explicit: the acting user's own connection, or a
	// connection of the run's org (org-sharing.md §7.3). When empty the
	// acting user's own default for IntegrationID is used, never the org's:
	// an unnamed reference does not change whose identity a call carries
	// (D10).
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
// *db.Repo.
type runOwners interface {
	GetWorkflow(ctx context.Context, id string) (*core.Workflow, error)
	GetThread(ctx context.Context, id string) (*core.Thread, error)
	GetChat(ctx context.Context, id string) (*core.Chat, error)
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
}

type resolverStore interface {
	tokenStore
	// GetConnection and DefaultConnection read the user's PERSONAL
	// connections; an org's are read through GetOwnedConnection.
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
	DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error)
	TouchOwnedConnectionUsed(ctx context.Context, owner core.ConnectionOwner, id string) error
}

// actor is who a use acts as: the user a run, trigger or picker belongs to,
// and the org it runs in ("" for none). The org decides which org's
// connections the use may name; the user is the one it is attributed to.
type actor struct {
	userID string
	orgID  string
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
	owner      core.ConnectionOwner
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
	secret, gen, err := r.tokens.refreshAfterRejection(ctx, r.owner, r.ConnectionID, r.generation)
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
//
// runID is the id an integration call identifies its run by, which is the
// call's THREAD (see tools.integrationRunID). That is a workflow id for the
// root run and for a spawned agent, but not for a nested `type: workflow` node
// with `thread: {mode: new}`: it runs inline under its parent's workflow row on
// a thread of its own, so the thread id names no workflow at all. Such an id is
// resolved through the thread's recorded workflow, then its chat.
func (r *Resolver) OwnerOf(ctx context.Context, runID string) (string, error) {
	a, err := r.actorOf(ctx, runID)
	return a.userID, err
}

// RunOrgOf returns the org a run belongs to: its recorded org, else its
// chat's; "" for a run of no org. The run is read exactly as OwnerOf reads it.
func (r *Resolver) RunOrgOf(ctx context.Context, runID string) (string, error) {
	a, err := r.actorOf(ctx, runID)
	return a.orgID, err
}

// actorOf is OwnerOf plus the org the run belongs to: the run's recorded org,
// else its chat's ("" when neither has one).
func (r *Resolver) actorOf(ctx context.Context, runID string) (actor, error) {
	if runID == "" {
		return actor{}, newError(CodeFailedPrecondition, "no run to resolve a connection for")
	}
	run, err := r.owners.GetWorkflow(ctx, runID)
	if err == nil && run != nil {
		return r.actorOfWorkflow(ctx, run)
	}
	if thread, terr := r.owners.GetThread(ctx, runID); terr == nil && thread != nil {
		if thread.WorkflowID != nil && *thread.WorkflowID != "" {
			if run, werr := r.owners.GetWorkflow(ctx, *thread.WorkflowID); werr == nil && run != nil {
				return r.actorOfWorkflow(ctx, run)
			}
		}
		if chat := r.actorOfChat(ctx, thread.ChatID); chat.userID != "" {
			return chat, nil
		}
	}
	return actor{}, newError(CodeFailedPrecondition, "run %q not found", runID)
}

func (r *Resolver) actorOfWorkflow(ctx context.Context, run *core.Workflow) (actor, error) {
	org := derefOr(run.OrgID, "")
	if run.OwnerUserID != nil && *run.OwnerUserID != "" {
		if org == "" {
			org = r.actorOfChat(ctx, run.ChatID).orgID
		}
		return actor{userID: *run.OwnerUserID, orgID: org}, nil
	}
	if chat := r.actorOfChat(ctx, run.ChatID); chat.userID != "" {
		return actor{userID: chat.userID, orgID: derefOr(run.OrgID, chat.orgID)}, nil
	}
	return actor{}, newError(CodeFailedPrecondition, "run %q has no owner", run.ID)
}

func (r *Resolver) actorOfChat(ctx context.Context, chatID string) actor {
	if chatID == "" {
		return actor{}
	}
	if chat, err := r.owners.GetChat(ctx, chatID); err == nil && chat != nil {
		return actor{userID: chat.UserID, orgID: derefOr(chat.OrgID, "")}
	}
	return actor{}
}

// derefOr is *p, or fallback when p is nil or empty.
func derefOr(p *string, fallback string) string {
	if p == nil || *p == "" {
		return fallback
	}
	return *p
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
	a, err := r.actorOf(ctx, call.RunID)
	if err != nil {
		return nil, err
	}
	by := ActorWorker
	if call.TriggerID != "" {
		by = ActorTrigger(call.TriggerID)
	}
	return r.resolve(ctx, a, ref, core.ConnectionEvent{
		RunID: call.RunID, NodeID: call.NodeID, ToolCallID: call.ToolCallID, Actor: by,
	})
}

// ForUser resolves ref for userID acting as themselves, outside any run: a
// picker listing the values of a typed input. It applies every rule ForCall
// does (the user's own connection only — there is no run, so no org's
// connection either — a foreign or missing id reads like
// none, for that integration, active, refreshed, the use audited with the user
// as actor); only the owner differs, being the authenticated caller instead of
// a run record's. Server placement is the caller's to enforce, as ForCall's is
// by CallSite.
func (r *Resolver) ForUser(ctx context.Context, userID string, ref Ref) (*Resolved, error) {
	if userID == "" {
		return nil, newError(CodeFailedPrecondition, "no user to resolve a connection for")
	}
	return r.resolve(ctx, actor{userID: userID}, ref, core.ConnectionEvent{Actor: ActorUser(userID)})
}

// ForTrigger resolves the connection a trigger listens through, for a use
// that has no run: a poll.
//
// Everything that decides WHOSE credential this is comes from the trigger
// row — its owner (user_id), its org (org_id), its connection
// (connection_id) and its integration (config.integration) — exactly as
// ForCall reads them from the run row. The connection is then held to every
// rule ForCall applies: server placement only, the owner's own connection or
// one of the trigger's org that the owner is a live member of (a foreign or
// missing id reads like none), for that integration, active (needs_reauth is a typed
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
	return r.resolve(ctx, actor{userID: trigger.UserID, orgID: derefOr(trigger.OrgID, "")},
		Ref{ConnectionID: *trigger.ConnectionID, IntegrationID: cfg.Integration},
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
	a, err := r.actorOf(ctx, call.RunID)
	if err != nil {
		return nil, err
	}
	out := make(map[Ref]error, len(refs))
	for _, ref := range refs {
		// Quiet: asked before every model turn with no use behind it, so it
		// applies the org's integrations policy without counting it.
		_, err := r.lookup(ctx, a, ref, true)
		out[ref] = err
	}
	return out, nil
}

// AdmitIntegration answers whether the org runID belongs to allows
// integrationID in its runs (control-plane docs/design/org-policies.md
// §4.3): nil when it does, or when nothing refuses it (no org, no policy on
// integrations, audit); the typed refusal IntegrationNotAllowed when the
// policy refuses it; CodeUnavailable when the policy must be read and cannot
// be. It is the boundary of one use, before any credential is chosen (a
// catalog action whatever authenticates it: a saved connection, a declared
// secret, a delegated identity, nothing at all), and counts the use once: the
// returned context marks it judged (orgpolicy.WithJudged), so lookup, asked
// again below it, refuses alike without counting again.
func (r *Resolver) AdmitIntegration(ctx context.Context, runID, integrationID string) (context.Context, error) {
	a, err := r.actorOf(ctx, runID)
	if err != nil {
		return ctx, err
	}
	d, err := admitIntegration(ctx, a, integrationID, false)
	return orgpolicy.WithJudged(ctx, d), err
}

// admitIntegration applies the org's integrations policy to a use of
// integrationID that acts as a. quiet judges without counting (Usable).
func admitIntegration(ctx context.Context, a actor, integrationID string, quiet bool) (orgpolicy.Decision, error) {
	d, err := orgpolicy.JudgeIntegration(ctx, a.orgID, integrationID, quiet)
	if err != nil {
		return d, &Error{Code: CodeUnavailable, Message: "could not read your organization's policy; try again", Err: err}
	}
	if d.Refused {
		return d, IntegrationNotAllowed(integrationID)
	}
	return d, nil
}

// IntegrationNotAllowed is the refusal of a use of integrationID that the
// run's org's policy does not allow: FailedPrecondition, reason
// ReasonIntegrationNotAllowed, a message the node shows as the reason.
// Permanent until an admin changes the policy, so a workflow must not retry
// it.
func IntegrationNotAllowed(integrationID string) *Error {
	return &Error{
		Code:    CodeFailedPrecondition,
		Reason:  ReasonIntegrationNotAllowed,
		Message: fmt.Sprintf("your organization's policy does not allow the %s integration in its runs; ask an org admin to allow it", integrationID),
	}
}

// usableConnection is what lookup establishes: the connection a ref names for
// its owner, and how this deployment authenticates with it.
type usableConnection struct {
	conn  *core.Connection
	auth  Authenticator
	hosts map[string]bool
}

// lookup is the half of resolve that decides WHICH connection (actor, ref)
// names and whether it can authenticate a call — every rule short of reading
// the secret. resolve and Usable share it, so a connection Usable reports is
// one resolve would hand out.
//
// An explicit id names the actor's own connection, or else one of the org the
// use runs in, which only a live member of that org may use (org-sharing.md
// §7.3). An integration alone names the actor's own default and nothing else:
// it never falls back to the org's (D10), so a call that used to fail with
// "no connection" does not start acting as the org's account.
//
// The org's integrations policy is applied here (org-policies §4.3), for
// every door: an integration the use's org does not allow is refused with
// IntegrationNotAllowed before any connection is looked for, so the refusal
// is never mistaken for "no connection" and redirected to a delegated
// authority. A ref that names only a connection id is judged by the
// connection's integration once it is found. quiet applies the policy
// without counting the decision (Usable).
func (r *Resolver) lookup(ctx context.Context, a actor, ref Ref, quiet bool) (*usableConnection, error) {
	if ref.IntegrationID != "" {
		if _, err := admitIntegration(ctx, a, ref.IntegrationID, quiet); err != nil {
			return nil, err
		}
	}
	var (
		conn *core.Connection
		err  error
	)
	switch {
	case ref.ConnectionID != "":
		conn, err = r.store.GetConnection(ctx, a.userID, ref.ConnectionID)
		if isNotFound(err) && a.orgID != "" {
			conn, err = r.store.GetOwnedConnection(ctx, core.OrgConnectionOwner(a.orgID), ref.ConnectionID)
		}
	case ref.IntegrationID != "":
		conn, err = r.store.DefaultConnection(ctx, a.userID, ref.IntegrationID)
	default:
		return nil, newError(CodeFailedPrecondition, "the action names no connection or integration")
	}
	if err != nil {
		if isNotFound(err) {
			return nil, noConnection(ref)
		}
		return nil, mapStoreErr(err)
	}
	if err := mayUse(ctx, a, conn); err != nil {
		if errors.Is(err, authz.ErrMembershipUnavailable) {
			return nil, &Error{Code: CodeUnavailable, Message: "could not check organization membership; try again", Err: err}
		}
		return nil, noConnection(ref)
	}
	if ref.IntegrationID != "" && conn.IntegrationID != ref.IntegrationID {
		return nil, newError(CodeFailedPrecondition, "connection %q is for %q, not %q", conn.Name, conn.IntegrationID, ref.IntegrationID)
	}
	if ref.IntegrationID == "" {
		if _, err := admitIntegration(ctx, a, conn.IntegrationID, quiet); err != nil {
			return nil, err
		}
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

// mayUse is the ownership rule of a use: a personal connection is its owner's
// alone; an org's is usable by a live member of that org, in a use that runs
// in that org. nil allows; a denial is authz's (ErrMembershipUnavailable is
// an outage, not an answer).
func mayUse(ctx context.Context, a actor, conn *core.Connection) error {
	switch owner := conn.Owner(); owner.Kind {
	case core.ConnectionOwnerUser:
		if authz.CanAccessPersonal(a.userID, conn.UserID) {
			return nil
		}
	case core.ConnectionOwnerOrg:
		if a.orgID != "" && owner.ID == a.orgID {
			return authz.OrgMemberDenial(ctx, a.userID, owner.ID)
		}
	}
	return authz.ErrDenied
}

// resolve turns (actor, ref) into a credential. It is the one implementation
// of the rules every door shares; use carries the audit attribution, always to
// the acting user, also for an org's connection, so an admin can answer "who
// posted as our bot".
func (r *Resolver) resolve(ctx context.Context, a actor, ref Ref, use core.ConnectionEvent) (*Resolved, error) {
	found, err := r.lookup(ctx, a, ref, false)
	if err != nil {
		return nil, err
	}
	conn, auth, hosts := found.conn, found.auth, found.hosts
	owner := conn.Owner()
	secret, gen, err := r.tokens.token(ctx, owner, conn.ID)
	if err != nil {
		return nil, err
	}

	use.ConnectionID, use.UserID, use.Kind = conn.ID, a.userID, core.ConnectionEventUsed
	appendBestEffort(ctx, r.store, use)
	_ = r.store.TouchOwnedConnectionUsed(context.WithoutCancel(ctx), owner, conn.ID)

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
