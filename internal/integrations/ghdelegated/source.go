package ghdelegated

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// ownerResolver reads who a run belongs to, from the database.
// *connections.Resolver satisfies it.
type ownerResolver interface {
	OwnerOf(ctx context.Context, runID string) (string, error)
}

// Source is the httpaction.CredentialSource the worker hands the HTTP runtime.
// It answers the `github` integration from the delegated broker when one is
// configured (hosted), and every other request — and `github` when self-hosted
// — from next, the saved-connection source.
type Source struct {
	owners ownerResolver
	broker DelegatedBroker
	next   httpaction.CredentialSource
}

// NewSource wraps next. A nil broker makes Source a pass-through: that is the
// self-hosted case, where GitHub is reliant's own OAuth provider.
func NewSource(owners ownerResolver, broker DelegatedBroker, next httpaction.CredentialSource) *Source {
	return &Source{owners: owners, broker: broker, next: next}
}

var _ httpaction.CredentialSource = (*Source)(nil)

// Credential resolves req.
//
// For the delegated path the OWNER is read from the run record, exactly as a
// saved connection's is: nothing in the request — not the workflow, not the
// model, not a connection id — can name whose GitHub token is used. A
// daemon-placed call is refused, because a token must never leave the server.
func (s *Source) Credential(ctx context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	if s.broker == nil || req.IntegrationID != IntegrationID {
		if s.next == nil {
			return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "connections are not available in this process"}
		}
		return s.next.Credential(ctx, req)
	}
	if !req.ServerPlaced {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: connections.ErrDaemonPlacement.Error()}
	}
	if s.owners == nil {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "connections are not available in this process"}
	}
	owner, err := s.owners.OwnerOf(ctx, req.RunID)
	if err != nil {
		var ce *connections.Error
		if errors.As(err, &ce) {
			return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: ce.Message, Err: err}
		}
		return nil, &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "could not resolve the run owner", Err: err}
	}
	return s.broker.Credential(ctx, owner)
}
