package connauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// An automation that chose to act as the App's bot writes as the bot, and
	// is asked before any saved connection: a saved GitHub connection is the
	// OWNER's, and letting it win would put the owner's own name on a write
	// the activation said the bot makes.
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
// handled is false when the call is the owner's (the common case), and when the
// identity is unavailable on this deployment, in which case the owner's own
// resolution proceeds: a deployment with no App key keeps working exactly as
// before, and says so once per call in its log rather than failing a run.
//
// Anything else is handled, including every failure: a write the activation
// said the bot makes is never quietly made as the owner because minting failed
// (the owner cannot push, the App is not installed, GitHub is down).
func (s *source) actingCredential(ctx context.Context, req httpaction.CredentialRequest) (cred httpaction.Credential, handled bool, err error) {
	if req.ActingIdentity != httpaction.IdentityApp || !req.Mutates || req.ConnectionID != "" {
		// A read authors nothing, and a call that names a connection chose its
		// own credential: neither is the bot's to make.
		return nil, false, nil
	}
	broker, ok := s.delegatedBroker(req.Connection)
	if !ok {
		return nil, false, nil
	}
	acting, ok := broker.(ActingBroker)
	if !ok {
		return nil, false, nil
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
		return cred, true, nil
	}
	var ce *httpaction.CredentialError
	if errors.As(err, &ce) {
		if ce.Code == httpaction.CodeIdentityUnavailable {
			slog.Warn("the automation asked to act as the GitHub App's bot, but this deployment cannot mint its token; acting as the owner instead",
				"integration", req.IntegrationID, "automation", req.Automation, "reason", ce.Message)
			return nil, false, nil
		}
		return nil, true, err
	}
	return nil, true, &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "the delegated authority failed", Err: err}
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
