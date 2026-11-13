package httpaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/reliant-labs/forge/pkg/svcerr"
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

// RejectableCredential is a Credential that can be replaced when the provider
// refuses it (a 401 for a token that had not expired: revoked or invalidated
// at the provider). Rejected returns the replacement — a refreshed OAuth
// token — or a *CredentialError with CodeNeedsReauth when there is none, which
// also marks the connection for reconnecting. Code that makes its own
// requests (a go: executor, a poller) retries a 401 ONCE with the
// replacement; a second 401 is final.
type RejectableCredential interface {
	Credential
	Rejected(ctx context.Context) (Credential, error)
}

// IdentityApp is CredentialRequest.ActingIdentity for the GitHub App's bot.
const IdentityApp = "app"

// CredentialRequest names the connection a call wants. It carries references
// only: the owner is looked up by the source from RunID, never supplied.
type CredentialRequest struct {
	RunID      string
	NodeID     string
	ToolCallID string
	// ActingIdentity is who the call speaks as: "app" for the GitHub App's
	// bot, empty for the run's owner. It is the RUN's fact (the activation's
	// acting_identity), supplied by the call site, never by the workflow or
	// the model: a params key named like it is just a param.
	ActingIdentity string
	// Automation names the trigger the run belongs to, for the footer on what
	// the bot writes.
	Automation string
	// ActionID is the action being run (e.g. "issue.comment"), so a source can
	// grant only what that action needs.
	ActionID string
	// Mutates is whether the action changes external state. A bot identity
	// applies to writes only: a read authors nothing, so it keeps the owner's
	// own (read) access.
	Mutates bool
	// Params are the action's parameters, read-only. A source that must scope
	// a credential to what the call touches (an installation token to one
	// repository) reads the target from here. They are the workflow's or the
	// model's, so a source must validate what it takes from them.
	Params map[string]any
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
	// Reason, when set, names the refusal more precisely than Code, for a
	// caller that branches on it (connections.ReasonIntegrationNotAllowed:
	// the run's org's policy does not allow the integration).
	Reason string
}

func (e *CredentialError) Error() string { return e.Message }
func (e *CredentialError) Unwrap() error { return e.Err }

// ErrorClass implements svcerr.Classed. With a cause the cause decides (the
// same failed_precondition carries both "GitHub is not connected", the user's
// to fix, and "the control plane has no App key", ours). Without one, a code
// the caller acts on is a user error; any other has no opinion.
func (e *CredentialError) ErrorClass() svcerr.Class {
	if e.Err != nil {
		return svcerr.ClassNone
	}
	switch e.Code {
	case CodeFailedPrecondition, CodeNeedsReauth, CodeNotFound, CodeInvalidArgument:
		return svcerr.ClassUser
	default:
		return svcerr.ClassNone
	}
}

// CallSite identifies one call for authorization and audit.
type CallSite struct {
	RunID      string
	NodeID     string
	ToolCallID string
	// ActingIdentity and Automation: see CredentialRequest.
	ActingIdentity string
	Automation     string
	// StepCredential is a credential the calling step brings itself: a
	// workflow's declared secret, in an http/request header slot or through an
	// integration's api_key method. With no connection named it is the call's
	// credential; with one (the generic HTTP integration only, which the caller
	// enforces) it is applied after the connection's. Either way the call is
	// held to https and to the host it started at, as with a connection.
	StepCredential Credential
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
		return r.run(ctx, m, a, params, withStepCredential(nil, site.StepCredential))
	}
	// The step brings its own credential (a declared secret through the
	// integration's api_key method): it stands in for the connection.
	if ref == "" && site.StepCredential != nil {
		return r.run(ctx, m, a, params, site.StepCredential)
	}
	if src == nil {
		return nil, &CredentialError{Code: CodeFailedPrecondition, Message: "connections are not available in this process"}
	}
	placement := a.GetPlacement()
	cred, err := src.Credential(ctx, CredentialRequest{
		RunID: site.RunID, NodeID: site.NodeID, ToolCallID: site.ToolCallID,
		ActingIdentity: site.ActingIdentity, Automation: site.Automation,
		ActionID: a.GetId(), Mutates: a.GetMutates(), Params: params,
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
	return r.run(ctx, m, a, params, withStepCredential(cred, site.StepCredential))
}

// withStepCredential is the credential a call is sent with: the connection's,
// the step's, or both applied in turn. Either may be nil.
func withStepCredential(conn, step Credential) Credential {
	switch {
	case step == nil:
		return conn
	case conn == nil:
		return step
	}
	return stackedCredential{conn: conn, step: step}
}

// stackedCredential applies a connection and then a step's own credential. It
// is a plain Credential: the generic HTTP integration's connections (api_key,
// basic) neither attribute nor refresh, and only that integration takes a step
// credential.
type stackedCredential struct{ conn, step Credential }

func (c stackedCredential) ConnectionID() string      { return c.conn.ConnectionID() }
func (c stackedCredential) Params() map[string]string { return c.conn.Params() }
func (c stackedCredential) Scrub(s string) string     { return c.step.Scrub(c.conn.Scrub(s)) }
func (c stackedCredential) Apply(req *http.Request) error {
	if err := c.conn.Apply(req); err != nil {
		return err
	}
	return c.step.Apply(req)
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

// Attributing is a Credential that speaks as someone other than the person
// whose automation this is, and so must say whose it is: what the GitHub App's
// bot writes ends with a footer naming the automation and its owner, because a
// bot-authored comment would otherwise read as a stranger's.
type Attributing interface {
	Credential
	// Attribution says how to sign what an action writes, or nil when the
	// action authors nothing (a label change, a read).
	Attribution(integrationID, actionID string) *Attribution
}

// Attribution is the signature an action's authored text carries.
type Attribution struct {
	// Fields are the top-level JSON body fields that hold authored text.
	Fields []string
	// Footer is appended to each present field, after a blank line.
	Footer string
	// Ensure sets a field the call left out to the footer alone, for an action
	// that creates something (a pull request opened with no description is
	// still the bot's). A field absent from an update means "unchanged" and is
	// never created.
	Ensure bool
}

// applyAttribution signs a rendered JSON body. A body that is not a JSON
// object is returned as it is: the signature is for the text the actions
// declare, and a shape that is not recognised is never guessed at.
func applyAttribution(body any, a *Attribution) any {
	if a == nil || a.Footer == "" {
		return body
	}
	obj, ok := body.(map[string]any)
	if !ok {
		return body
	}
	signed := make(map[string]any, len(obj)+len(a.Fields))
	for k, v := range obj {
		signed[k] = v
	}
	for _, field := range a.Fields {
		switch v := signed[field].(type) {
		case string:
			if v == "" {
				signed[field] = a.Footer
			} else {
				signed[field] = v + "\n\n" + a.Footer
			}
		case nil:
			if a.Ensure {
				signed[field] = a.Footer
			}
		}
	}
	return signed
}

// APIKeyPlacement is where an integration's api_key auth method sends a key:
// its header and the prefix written before the key. It is the one place a
// workflow's declared secret may authenticate an integration action, so an
// integration with no api_key method, one that sends the key in the query
// string (where it would be part of the URL), one that leaves the header to
// each connection (the generic HTTP integration, whose header slots take a
// secret instead), or one whose connection carries required settings has none.
func APIKeyPlacement(m *reliantv1.IntegrationManifest) (header, prefix string, err error) {
	// A secret is only a key: settings a connection carries alongside it (a
	// Sentry organization in the base URL) have nowhere to come from.
	for _, p := range m.GetConnection().GetConnectionParams() {
		if p.GetDefaultValue() == "" {
			return "", "", fmt.Errorf("%s needs connection settings (%s) that a secret does not carry: use a saved connection", m.GetId(), p.GetName())
		}
	}
	for _, a := range m.GetConnection().GetAuth() {
		k := a.GetApiKey()
		if k == nil {
			continue
		}
		switch {
		case k.GetIn() == "" && k.GetName() == "":
			return "", "", fmt.Errorf("%s takes no credential: it names no header for a key (put the secret in a header value instead)", m.GetId())
		case k.GetIn() != "header" || k.GetName() == "":
			return "", "", fmt.Errorf("%s sends its key in the query string, so it cannot take a secret as its credential", m.GetId())
		}
		return http.CanonicalHeaderKey(k.GetName()), k.GetPrefix(), nil
	}
	return "", "", fmt.Errorf("%s has no api_key auth method, so it cannot take a secret as its credential", m.GetId())
}
