// Package connauth adapts the connections resolver to the credential source the
// declarative HTTP runtime consumes, so httpaction never imports connections
// and a credential reaches a request only through Apply.
package connauth

import (
	"context"
	"errors"
	"net/http"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

type forCaller interface {
	ForCall(ctx context.Context, call connections.CallSite, ref connections.Ref) (*connections.Resolved, error)
}

// Source is an httpaction.CredentialSource backed by a connections resolver.
type Source struct{ resolver forCaller }

// New wraps a resolver. A nil resolver yields a source that answers every
// request with a FailedPrecondition, so a process without connections refuses
// rather than silently running unauthenticated.
func New(r *connections.Resolver) *Source {
	if r == nil {
		return &Source{}
	}
	return &Source{resolver: r}
}

// NewFromResolver wraps any ForCall implementation (tests).
func NewFromResolver(r forCaller) *Source { return &Source{resolver: r} }

type credential struct{ r *connections.Resolved }

func (c credential) ConnectionID() string          { return c.r.ConnectionID }
func (c credential) Apply(req *http.Request) error { return c.r.Apply(req) }
func (c credential) Scrub(s string) string         { return c.r.Redactor.Scrub(s) }

// Credential resolves the connection for the run's owner. The integration id
// must match the connection's, and a daemon-placed call is refused.
func (s *Source) Credential(ctx context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	if s == nil || s.resolver == nil {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "connections are not available in this process"}
	}
	placement := connections.PlacementDaemon
	if req.ServerPlaced {
		placement = connections.PlacementServer
	}
	resolved, err := s.resolver.ForCall(ctx,
		connections.CallSite{RunID: req.RunID, NodeID: req.NodeID, ToolCallID: req.ToolCallID, Placement: placement},
		connections.Ref{ConnectionID: req.ConnectionID, IntegrationID: req.IntegrationID})
	if err != nil {
		return nil, mapError(err)
	}
	return credential{r: resolved}, nil
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
