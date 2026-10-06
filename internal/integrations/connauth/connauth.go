// Package connauth adapts the connections resolver, and any delegated
// authority, to the credential source the declarative HTTP runtime consumes.
// httpaction never imports connections, and a credential reaches a request
// only through Apply.
//
// Resolution for one call, first hit wins (INTEGRATIONS.md §4.3):
//
//  1. the connection the call names explicitly;
//  2. the run owner's default saved connection for the integration;
//  3. the integration's `delegated` broker, when its manifest declares one and
//     this process registered it (GitHub through the control plane).
//
// The owner is always read from the run record, never from the request. A
// poll, which has no run, resolves by trigger id and reads the owner and the
// connection from the trigger record instead (ForTrigger).
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

type forCaller interface {
	ForCall(ctx context.Context, call connections.CallSite, ref connections.Ref) (*connections.Resolved, error)
	ForTrigger(ctx context.Context, site connections.TriggerSite) (*connections.Resolved, error)
	OwnerOf(ctx context.Context, runID string) (string, error)
	Usable(ctx context.Context, call connections.CallSite, refs []connections.Ref) (map[connections.Ref]error, error)
}

// DelegatedBroker turns a run owner into a credential obtained from an
// external authority at call time. ownerUserID is reliant's user id for the
// run's owner (the IdP subject). The returned credential must pin itself to
// the hosts the authority's token is for: the runtime pins a credential to
// the host a call started at, and the broker decides where it may start.
type DelegatedBroker interface {
	Credential(ctx context.Context, ownerUserID string) (httpaction.Credential, error)
}

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

// Source is an httpaction.CredentialSource backed by a connections resolver
// and a delegated-broker registry.
type Source struct {
	resolver forCaller
	brokers  *Brokers
}

// New wraps a resolver. A nil resolver yields a source that answers every
// request with a FailedPrecondition, so a process without connections refuses
// rather than silently running unauthenticated.
func New(r *connections.Resolver) *Source {
	if r == nil {
		return &Source{}
	}
	return &Source{resolver: r}
}

// NewFromResolver wraps any resolver implementation (tests).
func NewFromResolver(r forCaller) *Source { return &Source{resolver: r} }

// Brokers is the source's delegated-broker registry (nil when it has none).
func (s *Source) Brokers() *Brokers { return s.brokers }

// WithBrokers returns the source with delegated brokers to fall back on.
func (s *Source) WithBrokers(b *Brokers) *Source {
	clone := *s
	clone.brokers = b
	return &clone
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

// Credential resolves the connection for the run's owner. The integration id
// must match the connection's, and a daemon-placed call is refused.
func (s *Source) Credential(ctx context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	if s == nil || s.resolver == nil {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "connections are not available in this process"}
	}
	if !req.ServerPlaced {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: connections.ErrDaemonPlacement.Error(), Err: connections.ErrDaemonPlacement}
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

// UsableIntegrations reports which of the integrations a call from this run
// could find a credential for now, walking Credential's order for a call that
// names no connection: the owner's default saved connection, else — only when
// there is none at all — the integration's delegated authority. integrations
// maps each id to its connection spec, which says whether a delegated broker
// backs it.
//
// It is what decides whether an integration's tools are offered to the run,
// so it must agree with what a call will meet:
//
//   - A saved connection is checked without reading its secret or recording a
//     use (connections.Resolver.Usable). One that needs re-auth is not usable,
//     and does NOT fall back to the broker, as Credential does not.
//   - A delegated authority is asked for the owner's credential, which is then
//     dropped: "has one" and "would hand one out" are the same question there
//     (for GitHub, control-plane says not connected, or answers). A failure to
//     ask is not usable: an absent tool is better than one that fails.
//
// The owner is read from the run record, never supplied. An owner that cannot
// be read (an unknown run, a run with none) makes nothing usable, and is
// returned as the error for the caller to log.
func (s *Source) UsableIntegrations(ctx context.Context, runID string, integrations map[string]*reliantv1.ConnectionSpec) (map[string]bool, error) {
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

// ForTrigger resolves the credential a polled trigger authenticates with. It
// takes the trigger id and nothing else: the owner, the connection and the
// integration are read from the trigger row (connections.Resolver.ForTrigger),
// so a poller cannot be pointed at another user's account by anything it is
// handed. A poll is always server-placed.
//
// There is no delegated fall-back: a polled trigger listens through the one
// connection its owner picked, and a missing one stays missing.
func (s *Source) ForTrigger(ctx context.Context, triggerID string) (httpaction.Credential, error) {
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
func (s *Source) delegatedBroker(conn *reliantv1.ConnectionSpec) (DelegatedBroker, bool) {
	m, ok := manifest.Method(conn, manifest.AuthDelegated)
	if !ok {
		return nil, false
	}
	return s.brokers.Get(m.GetDelegated().GetBroker())
}

func (s *Source) delegated(ctx context.Context, req httpaction.CredentialRequest, broker DelegatedBroker) (httpaction.Credential, error) {
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
