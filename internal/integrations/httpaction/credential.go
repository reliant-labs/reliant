package httpaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Credential authenticates one call. It is a narrow, consumer-side view of a
// resolved connection: the plaintext never leaves the implementation, it is
// only written into an outgoing request by Apply, and Scrub removes it from
// anything about to be returned or logged.
type Credential interface {
	ConnectionID() string
	Apply(req *http.Request) error
	Scrub(s string) string
}

// CredentialRequest names the connection a call wants. It carries references
// only: the owner is looked up by the source from RunID, never supplied.
type CredentialRequest struct {
	RunID         string
	NodeID        string
	ToolCallID    string
	ConnectionID  string
	IntegrationID string
	// ServerPlaced is false for a daemon-placed action, which must be refused.
	ServerPlaced bool
}

// CredentialSource resolves a CredentialRequest for the run's owner.
type CredentialSource interface {
	Credential(ctx context.Context, req CredentialRequest) (Credential, error)
}

// Credential error codes, mirroring the connections layer.
const (
	CodeFailedPrecondition = "failed_precondition"
	CodeNeedsReauth        = "needs_reauth"
	CodeUnavailable        = "unavailable"
	CodeNotFound           = "not_found"
	CodeInvalidArgument    = "invalid_argument"
	CodeInternal           = "internal"
)

// CredentialError is a typed failure to obtain a credential. Message is safe to
// show a user.
type CredentialError struct {
	Code    string
	Message string
	Err     error
}

func (e *CredentialError) Error() string { return e.Message }
func (e *CredentialError) Unwrap() error { return e.Err }

// CallSite identifies one call for authorization and audit.
type CallSite struct {
	RunID      string
	NodeID     string
	ToolCallID string
}

// ConnectionParam is the action parameter that names a saved connection.
const ConnectionParam = "connection"

// RunAuthenticated runs the action, authenticating it with the connection
// named by params["connection"] when there is one. Without one it behaves
// exactly like Run. A connection is accepted only by manifests that declare
// optional_auth_kinds, and only one saved for THIS integration id, so a token
// saved for another service can never be pointed at an arbitrary host.
func (r *Runner) RunAuthenticated(ctx context.Context, m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, params map[string]any, src CredentialSource, site CallSite) (*Result, error) {
	ref, _ := params[ConnectionParam].(string)
	if ref == "" {
		return r.run(ctx, m, a, params, nil)
	}
	if len(m.GetConnection().GetOptionalAuthKinds()) == 0 {
		return nil, &CredentialError{Code: CodeInvalidArgument, Message: fmt.Sprintf("%s/%s does not accept a connection", m.GetId(), a.GetId())}
	}
	if src == nil {
		return nil, &CredentialError{Code: CodeFailedPrecondition, Message: "connections are not available in this process"}
	}
	placement := a.GetPlacement()
	cred, err := src.Credential(ctx, CredentialRequest{
		RunID: site.RunID, NodeID: site.NodeID, ToolCallID: site.ToolCallID,
		ConnectionID: ref, IntegrationID: m.GetId(),
		ServerPlaced: placement == "server" || placement == "any",
	})
	if err != nil {
		var ce *CredentialError
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, &CredentialError{Code: CodeInternal, Message: "could not resolve the connection", Err: err}
	}
	return r.run(ctx, m, a, params, cred)
}

// scrubResult removes the credential from everything a Result carries.
func scrubResult(cred Credential, res *Result) {
	if cred == nil || res == nil {
		return
	}
	res.ConnectionID = cred.ConnectionID()
	res.Content = cred.Scrub(res.Content)
	if res.Data != nil {
		if raw, err := json.Marshal(res.Data); err == nil {
			var clean map[string]any
			if json.Unmarshal([]byte(cred.Scrub(string(raw))), &clean) == nil {
				res.Data = clean
			}
		}
	}
}
