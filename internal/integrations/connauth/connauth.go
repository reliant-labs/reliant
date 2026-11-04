package connauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

var brokerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Brokers is the registry of delegated brokers, keyed by the id a manifest's
// `delegated: { broker: <id> }` names.
type Brokers struct {
	mu      sync.RWMutex
	brokers map[string]DelegatedBroker
}

// NewBrokers returns an empty registry.
func NewBrokers() *Brokers { return &Brokers{brokers: map[string]DelegatedBroker{}} }

// Register adds a broker. An invalid id, a nil broker, or a second broker
// under one id is an error: it is a wiring defect.
func (b *Brokers) Register(id string, broker DelegatedBroker) error {
	if !brokerIDPattern.MatchString(id) {
		return fmt.Errorf("delegated broker id %q must match %s", id, brokerIDPattern)
	}
	if broker == nil {
		return fmt.Errorf("delegated broker %q is nil", id)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, dup := b.brokers[id]; dup {
		return fmt.Errorf("delegated broker %q is already registered", id)
	}
	b.brokers[id] = broker
	return nil
}

// Get returns the broker registered under id.
func (b *Brokers) Get(id string) (DelegatedBroker, bool) {
	if b == nil {
		return nil, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	br, ok := b.brokers[id]
	return br, ok
}

// source is the Service: saved connections through a resolver, then the
// delegated brokers registered for an integration.
type source struct {
	resolver Resolver
	broker   func(id string) (DelegatedBroker, bool)
}

// New builds the credential source over deps.Resolver and deps.Broker.
func New(deps Deps) Service {
	return &source{resolver: deps.Resolver, broker: deps.Broker}
}

// Broker implements Service.
func (s *source) Broker(id string) (DelegatedBroker, bool) {
	if s.broker == nil {
		return nil, false
	}
	return s.broker(id)
}

type credential struct{ r *connections.Resolved }

func (c credential) ConnectionID() string          { return c.r.ConnectionID }
func (c credential) Apply(req *http.Request) error { return c.r.Apply(req) }
func (c credential) Scrub(s string) string         { return c.r.Redactor.Scrub(s) }
func (c credential) Params() map[string]string     { return c.r.Params() }

// Rejected implements httpaction.RejectableCredential.
func (c credential) Rejected(ctx context.Context) (httpaction.Credential, error) {
	next, err := c.r.RefreshAfterRejection(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return credential{r: next}, nil
}

var _ httpaction.RejectableCredential = credential{}

// Credential implements Service.
func (s *source) Credential(ctx context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	if s == nil || s.resolver == nil {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "connections are not available in this process"}
	}
	if !req.ServerPlaced {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: connections.ErrDaemonPlacement.Error(), Err: connections.ErrDaemonPlacement}
	}
	// An automation that chose to act as the App's bot writes as the bot or
	// not at all, and is asked before any saved connection: a saved GitHub
	// connection is the OWNER's, and letting it win would put the owner's own
	// name on a write the activation said the bot makes.
	if cred, handled, err := s.actingCredential(ctx, req); handled {
		return cred, err
	}
	resolved, err := s.resolver.ForCall(ctx,
		connections.CallSite{RunID: req.RunID, NodeID: req.NodeID, ToolCallID: req.ToolCallID, Placement: connections.PlacementServer},
		connections.Ref{ConnectionID: req.ConnectionID, IntegrationID: req.IntegrationID})
	if err == nil {
		return credential{r: resolved}, nil
	}
	// No saved connection: an integration with a delegated authority asks it.
	// An explicit connection id that did not resolve is NOT redirected to the
	// broker: the caller named a connection, and it does not exist for them.
	if req.ConnectionID == "" && isNoConnection(err) {
		if broker, ok := s.delegatedBroker(req.Connection); ok {
			return s.delegated(ctx, req, broker)
		}
	}
	return nil, mapError(err)
}

// actingCredential resolves a call that asked to act as a non-owner identity.
// handled is false only when the call is the owner's: no acting identity, a
// read, which authors nothing and keeps the owner's own access, or a write to
// an integration that has no bot identity at all.
//
// The acting identity names who GitHub writes speak as, and it is the default
// for every automation, so a Slack message or a plain HTTP call from a bot
// automation goes through the owner's connection exactly as it always did:
// there is no other identity it could have meant. An integration has a bot
// when its connection declares a delegated authority (GitHub's App).
//
// Every write to such an integration that asked for the bot is handled, and
// every failure is final. A write the activation said the bot makes is never
// made as the owner (or with a saved connection of the owner's) because the
// bot could not be had: the App key is missing, the broker cannot act, the
// owner cannot push, the App is not installed, minting failed, GitHub is down.
// The caller gets an error that says what to fix.
func (s *source) actingCredential(ctx context.Context, req httpaction.CredentialRequest) (cred httpaction.Credential, handled bool, err error) {
	if req.ActingIdentity == "" || !req.Mutates {
		return nil, false, nil
	}
	if req.ActingIdentity != httpaction.IdentityApp {
		return nil, true, &httpaction.CredentialError{Code: httpaction.CodeInvalidArgument,
			Message: fmt.Sprintf("unknown acting identity %q: a write is made as the owner or as the Reliant bot", req.ActingIdentity)}
	}
	if _, hasBot := manifest.Method(req.Connection, manifest.AuthDelegated); !hasBot {
		return nil, false, nil
	}
	if req.ConnectionID != "" {
		// A saved connection is somebody's own credential. Using it would write
		// as that person, and quietly ignoring it would hide a contradictory
		// configuration, so the contradiction is the error. Reads may still
		// name a connection.
		return nil, true, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "this automation writes as the Reliant bot, so a write cannot name a saved connection: remove the connection from this step, or set the automation to write as its owner"}
	}
	broker, ok := s.delegatedBroker(req.Connection)
	if !ok {
		return nil, true, noBotIdentity(req)
	}
	acting, ok := broker.(ActingBroker)
	if !ok {
		return nil, true, noBotIdentity(req)
	}
	owner, err := s.resolver.OwnerOf(ctx, req.RunID)
	if err != nil {
		var ce *connections.Error
		if errors.As(err, &ce) {
			return nil, true, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: ce.Message, Err: err}
		}
		return nil, true, &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "could not resolve the run owner", Err: err}
	}
	cred, err = acting.ActingCredential(ctx, owner, req)
	if err == nil {
		if cred == nil {
			return nil, true, &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "the Reliant bot resolved to no credential"}
		}
		return cred, true, nil
	}
	var ce *httpaction.CredentialError
	if errors.As(err, &ce) {
		return nil, true, err
	}
	return nil, true, &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "the delegated authority failed", Err: err}
}

func noBotIdentity(req httpaction.CredentialRequest) error {
	return &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
		Message: fmt.Sprintf("this automation writes to %s as the Reliant bot, which this deployment cannot provide: configure the GitHub App, or set the automation to write as its owner", req.IntegrationID)}
}

// UsableIntegrations implements Service.
func (s *source) UsableIntegrations(ctx context.Context, runID string, integrations map[string]*reliantv1.ConnectionSpec) (map[string]bool, error) {
	usable := map[string]bool{}
	if s == nil || s.resolver == nil || len(integrations) == 0 {
		return usable, nil
	}
	refs := make([]connections.Ref, 0, len(integrations))
	for id := range integrations {
		refs = append(refs, connections.Ref{IntegrationID: id})
	}
	results, err := s.resolver.Usable(ctx, connections.CallSite{RunID: runID, Placement: connections.PlacementServer}, refs)
	if err != nil {
		return usable, mapError(err)
	}
	var (
		owner    string
		ownerErr error
	)
	for _, ref := range refs {
		err := results[ref]
		if err == nil {
			usable[ref.IntegrationID] = true
			continue
		}
		if !isNoConnection(err) {
			continue
		}
		broker, ok := s.delegatedBroker(integrations[ref.IntegrationID])
		if !ok {
			continue
		}
		if owner == "" && ownerErr == nil {
			owner, ownerErr = s.resolver.OwnerOf(ctx, runID)
		}
		if ownerErr != nil {
			continue
		}
		if cred, err := broker.Credential(ctx, owner); err == nil && cred != nil {
			usable[ref.IntegrationID] = true
		}
	}
	return usable, nil
}

// ForTrigger implements Service.
func (s *source) ForTrigger(ctx context.Context, triggerID string) (httpaction.Credential, error) {
	if s == nil || s.resolver == nil {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "connections are not available in this process"}
	}
	resolved, err := s.resolver.ForTrigger(ctx, connections.TriggerSite{TriggerID: triggerID, Placement: connections.PlacementServer})
	if err != nil {
		return nil, mapError(err)
	}
	return credential{r: resolved}, nil
}

// delegatedBroker returns the registered broker the integration's connection
// spec names, if any.
func (s *source) delegatedBroker(conn *reliantv1.ConnectionSpec) (DelegatedBroker, bool) {
	m, ok := manifest.Method(conn, manifest.AuthDelegated)
	if !ok {
		return nil, false
	}
	return s.Broker(m.GetDelegated().GetBroker())
}

func (s *source) delegated(ctx context.Context, req httpaction.CredentialRequest, broker DelegatedBroker) (httpaction.Credential, error) {
	owner, err := s.resolver.OwnerOf(ctx, req.RunID)
	if err != nil {
		var ce *connections.Error
		if errors.As(err, &ce) {
			return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: ce.Message, Err: err}
		}
		return nil, &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "could not resolve the run owner", Err: err}
	}
	return brokerCredential(ctx, broker, owner)
}

func brokerCredential(ctx context.Context, broker DelegatedBroker, owner string) (httpaction.Credential, error) {
	cred, err := broker.Credential(ctx, owner)
	if err != nil {
		var ce *httpaction.CredentialError
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "the delegated authority failed", Err: err}
	}
	return cred, nil
}

func isNoConnection(err error) bool {
	var ce *connections.Error
	return errors.As(err, &ce) && ce.Code == connections.CodeFailedPrecondition && !errors.Is(err, connections.ErrDaemonPlacement)
}

func mapError(err error) error {
	var ce *connections.Error
	if !errors.As(err, &ce) {
		return &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "could not resolve the connection"}
	}
	code := httpaction.CodeInternal
	switch ce.Code {
	case connections.CodeFailedPrecondition:
		code = httpaction.CodeFailedPrecondition
	case connections.CodeNeedsReauth:
		code = httpaction.CodeNeedsReauth
	case connections.CodeUnavailable:
		code = httpaction.CodeUnavailable
	case connections.CodeNotFound:
		code = httpaction.CodeNotFound
	case connections.CodeInvalidArgument:
		code = httpaction.CodeInvalidArgument
	}
	return &httpaction.CredentialError{Code: code, Message: ce.Message, Err: err}
}

// ForUser implements Service.
func (s *source) ForUser(userID string) httpaction.CredentialSource {
	return userSource{s: s, userID: userID}
}

// userSource authenticates one user's own reads.
type userSource struct {
	s      *source
	userID string
}

func (u userSource) Credential(ctx context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	if u.s == nil || u.s.resolver == nil {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "connections are not available in this process"}
	}
	if u.userID == "" {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "no user to resolve a connection for"}
	}
	if !req.ServerPlaced {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: connections.ErrDaemonPlacement.Error(), Err: connections.ErrDaemonPlacement}
	}
	// A listing reads. A write through this door would carry the caller's
	// credential with nothing of a run's audit around it, so it is refused
	// here as well as where the manifest is loaded.
	if req.Mutates {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeInvalidArgument, Message: fmt.Sprintf("%s/%s changes external state, which a picker never does", req.IntegrationID, req.ActionID)}
	}
	resolved, err := u.s.resolver.ForUser(ctx, u.userID, connections.Ref{ConnectionID: req.ConnectionID, IntegrationID: req.IntegrationID})
	if err == nil {
		return credential{r: resolved}, nil
	}
	if req.ConnectionID == "" && isNoConnection(err) {
		if broker, ok := u.s.delegatedBroker(req.Connection); ok {
			return brokerCredential(ctx, broker, u.userID)
		}
	}
	return nil, mapError(err)
}
