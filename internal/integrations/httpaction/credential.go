package httpaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// Credential authenticates one call. It is a narrow, consumer-side view of a
// resolved connection: the plaintext never leaves the implementation, it is
// only written into an outgoing request by Apply, and Scrub removes it from
// anything about to be returned or logged.
type Credential interface {
	ConnectionID() string
	Apply(req *http.Request) error
	Scrub(s string) string
	// Params are the connection's non-secret settings (a Shopify shop). The
	// runner renders them into base_url and request templates as
	// connection.params.<name>.
	Params() map[string]string
}

// CredentialRequest names the connection a call wants. It carries references
// only: the owner is looked up by the source from RunID, never supplied.
type CredentialRequest struct {
	RunID      string
	NodeID     string
	ToolCallID string
	// ConnectionID is the connection the call names; empty means the owner's
	// default for the integration (or its delegated authority).
	ConnectionID  string
	IntegrationID string
	// Connection is the integration's connection spec, so a source can see
	// which auth methods it declares (a delegated broker id) without a
	// catalog lookup of its own.
	Connection *reliantv1.ConnectionSpec
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

// RunAuthenticated runs the action with the credential its integration needs.
//
//   - An integration that declares no auth runs unauthenticated; naming a
//     connection is an error.
//   - auth_optional (the generic HTTP integration): a credential is resolved
//     only when params["connection"] names one.
//   - Otherwise a credential is required: the connection named, else the
//     owner's default, else the integration's delegated authority. The source
//     decides which; the runner never sends such a request without one.
//
// Either way the connection must belong to THIS integration, so a token saved
// for one service can never be pointed at another's host.
func (r *Runner) RunAuthenticated(ctx context.Context, m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, params map[string]any, src CredentialSource, site CallSite) (*Result, error) {
	ref, _ := params[ConnectionParam].(string)
	conn := m.GetConnection()
	needs := len(conn.GetAuth()) > 0
	switch {
	case !needs && ref != "":
		return nil, &CredentialError{Code: CodeInvalidArgument, Message: fmt.Sprintf("%s/%s does not accept a connection", m.GetId(), a.GetId())}
	case !needs, conn.GetAuthOptional() && ref == "":
		return r.run(ctx, m, a, params, nil)
	}
	if src == nil {
		return nil, &CredentialError{Code: CodeFailedPrecondition, Message: "connections are not available in this process"}
	}
	placement := a.GetPlacement()
	cred, err := src.Credential(ctx, CredentialRequest{
		RunID: site.RunID, NodeID: site.NodeID, ToolCallID: site.ToolCallID,
		ConnectionID: ref, IntegrationID: m.GetId(), Connection: conn,
		ServerPlaced: placement == manifest.PlacementServer || placement == manifest.PlacementAny,
	})
	if err != nil {
		var ce *CredentialError
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, &CredentialError{Code: CodeInternal, Message: "could not resolve the connection", Err: err}
	}
	if cred == nil {
		return nil, &CredentialError{Code: CodeInternal, Message: "the connection resolved to no credential"}
	}
	return r.run(ctx, m, a, params, cred)
}

// connectionVars is the `connection` template variable: the credential's
// non-secret params with declared defaults filled in.
func connectionVars(m *reliantv1.IntegrationManifest, cred Credential) (map[string]any, map[string]string) {
	values := map[string]string{}
	for _, p := range m.GetConnection().GetConnectionParams() {
		if p.GetDefaultValue() != "" {
			values[p.GetName()] = p.GetDefaultValue()
		}
	}
	if cred != nil {
		for k, v := range cred.Params() {
			if v != "" {
				values[k] = v
			}
		}
	}
	asAny := make(map[string]any, len(values))
	flat := make(map[string]string, len(values))
	for k, v := range values {
		asAny[k] = v
		flat[manifest.ParamVar+k] = v
	}
	return map[string]any{"params": asAny}, flat
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
