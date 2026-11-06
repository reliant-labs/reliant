package cloud

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/pkg/accesstoken"
)

// ReasonHeader is the Connect error-metadata key under which a control plane
// delivers a stable, machine-readable domain reason code. It matches forge's
// own server-side convention (pkg/svcerr.ReasonHeader), and the value is an
// app-defined snake_case code such as "promotion_conflict".
//
// Declared here rather than imported from pkg/svcerr on purpose: this is the
// CLIENT half of the contract, and it must not acquire a dependency on
// forge's server library to read one header name. HTTP header keys are
// case-insensitive, so the canonicalization does not matter.
const ReasonHeader = "x-forge-error-reason"

// The reasons a control plane gives, in ReasonHeader, for refusing a bearer
// that claims to be an access token. They say WHICH way the credential is dead,
// so forge can word the fix and decide whether asking for a fresh one can help.
const (
	// ReasonAccessTokenExpired: the token existed and its expiry has passed.
	ReasonAccessTokenExpired = "access_token_expired"
	// ReasonAccessTokenRevoked: the token was revoked.
	ReasonAccessTokenRevoked = "access_token_revoked"
	// ReasonAccessTokenUnknown: well-formed, but this control plane has no
	// such token — typically a token minted for another environment.
	ReasonAccessTokenUnknown = "access_token_unknown"
	// ReasonAccessTokenMalformed: not the shape of an access token at all.
	ReasonAccessTokenMalformed = "access_token_malformed"
	// ReasonAccessTokenUnavailable: the control plane could not check the
	// token. NOT a verdict on it: retrying later is the right response, and
	// signing in again is not.
	ReasonAccessTokenUnavailable = "access_token_unavailable"
	// ReasonClientUpgradeRequired: this forge is older than the control plane
	// serves (it reads VersionHeader). The server's message names the oldest
	// release it accepts and how to upgrade; nothing was executed.
	ReasonClientUpgradeRequired = "client_upgrade_required"
)

// Error is a failed control-plane call, as a TYPE rather than a sentence.
//
// WHY THIS EXISTS. Call used to flatten every failure into a formatted
// string, and callers then recovered the classification by searching that
// string — `strings.Contains(err.Error(), "already_exists")`. That is wrong
// in both directions. A server that improves its prose breaks forge's
// control flow, which is a silent break: the branch simply stops matching
// and the error falls through to a generic failure. And forge's OWN message
// text contains the words it searches for, so a message that merely mentions
// a code reads as that code.
//
// Worse, the structured half was discarded entirely. A promote refusal
// carries a DeployPromoteRefusal detail naming the promotion that actually
// landed — exactly what a pipeline needs to print instead of "someone else
// moved prod" — and flattening to a string threw it away.
//
// So every primitive maps REASONS, not message text, to exit codes.
type Error struct {
	// HTTPStatus is the response status. Kept because a response that is
	// not a Connect envelope at all (a proxy's HTML error page) has no
	// code, and the status is then the only classification available.
	HTTPStatus int

	// Code is the Connect error code as its wire name: "already_exists",
	// "not_found", "failed_precondition", "unauthenticated". Empty when
	// the body was not a Connect envelope.
	Code string

	// Reason is the app-defined domain reason from ReasonHeader, e.g.
	// "promotion_conflict" or "rollout_in_flight". Empty when the server
	// sent none.
	//
	// THIS IS THE FIELD TO BRANCH ON. The Connect code says the category
	// (FailedPrecondition), and three different refusals share it; the
	// reason says which one, and is the server's committed contract.
	Reason string

	// Scopes are the scopes a "scope_missing" refusal names, from the
	// response's accesstoken.ScopesHeader. Empty when the server named none.
	Scopes []string

	// Message is the server's own message, verbatim and untruncated. For
	// DISPLAY — never for control flow.
	Message string

	// Details are the Connect error details, each left in its wire form.
	//
	// NOT DECODED, and that is deliberate: decoding a google.protobuf.Any
	// would mean vendoring the control plane's protos into forge, which is
	// the one coupling forge's deploy story must not take on. Connect's
	// JSON binding includes a `debug` member carrying the message as
	// protojson when the server could produce it, so a caller reads the
	// payload as JSON (see DetailJSON) without a generated type.
	Details []ErrorDetail

	// Header is the response's headers, for the few facts a control plane
	// sends there beside the reason (a throttle's retry-after).
	Header http.Header

	// Procedure and Endpoint are where this happened, for the message.
	Procedure string
	Endpoint  string

	// message is the rendered, actionable text (see connectError). Held
	// separately so Error() stays the human-facing sentence while the
	// fields above stay machine-readable.
	message string
}

// ErrorDetail is one Connect error detail in its wire form.
type ErrorDetail struct {
	// Type is the protobuf full name, e.g.
	// "controlplane.v1.DeployPromoteRefusal".
	Type string `json:"type"`
	// Value is the base64 (raw std, unpadded) proto binary.
	Value string `json:"value"`
	// Debug is the same message as protojson, present when the server had
	// descriptors for it. This is the member a forge caller reads.
	Debug json.RawMessage `json:"debug,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.message != "" {
		return e.message
	}
	return fmt.Sprintf("%s failed (HTTP %d%s) against %s%s",
		e.Procedure, e.HTTPStatus, codeSuffix(e.Code), e.Endpoint, detailSuffix(e.Message))
}

// MissingScope reports the scope a 403 says the credential lacks (the
// server's message names it as "<scope> scope"), so a caller can tell a
// credential that needs a scope from a request the server refused.
func (e *Error) MissingScope() (string, bool) {
	if e == nil || e.HTTPStatus != http.StatusForbidden {
		return "", false
	}
	m := missingScopeRe.FindStringSubmatch(e.Message)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// HasCode reports whether this failure carries the given Connect code. Use
// it instead of matching message text.
func (e *Error) HasCode(code string) bool { return e != nil && e.Code == code }

// HasReason reports whether this failure carries the given domain reason.
func (e *Error) HasReason(reason string) bool { return e != nil && e.Reason == reason }

// Unauthenticated reports whether the control plane refused the credential
// itself (HTTP 401), as opposed to refusing what it may do (403).
func (e *Error) Unauthenticated() bool { return e != nil && e.HTTPStatus == http.StatusUnauthorized }

// DetailJSON returns the protojson payload of the first detail whose type
// matches typeName, and whether one was found.
//
// Matches on the SUFFIX after the last '/' and accepts either a bare full
// name or a type URL, because a server may send "controlplane.v1.X" or
// "type.googleapis.com/controlplane.v1.X" and both name the same message.
//
// A detail with no `debug` member is reported as not found: the base64 proto
// bytes alone are unreadable without the descriptor forge deliberately does
// not have, and returning them would hand a caller a payload it cannot use.
func (e *Error) DetailJSON(typeName string) (json.RawMessage, bool) {
	if e == nil {
		return nil, false
	}
	want := bareTypeName(typeName)
	for _, d := range e.Details {
		if bareTypeName(d.Type) == want && len(d.Debug) > 0 {
			return d.Debug, true
		}
	}
	return nil, false
}

func bareTypeName(s string) string {
	if slash := strings.LastIndex(s, "/"); slash >= 0 {
		return s[slash+1:]
	}
	return s
}

// The Connect error codes forge branches on. Named so a caller never writes
// the literal, and never searches for it in a message.
const (
	// CodeNotFound: the thing does not exist. Several reads turn this into
	// an ANSWER rather than a failure — "never cut" is not an error.
	CodeNotFound = "not_found"
	// CodeAlreadyExists: a create collided with an existing row.
	CodeAlreadyExists = "already_exists"
	// CodeFailedPrecondition: the server refused given the current state.
	// The category every promote refusal shares; Reason says which.
	CodeFailedPrecondition = "failed_precondition"
	// CodeInvalidArgument: the request itself was wrong.
	CodeInvalidArgument = "invalid_argument"
	// CodeUnimplemented: this control plane does not serve the procedure.
	CodeUnimplemented = "unimplemented"
	// CodeUnauthenticated and CodePermissionDenied: the credential was
	// missing/stale, or lacks the scope.
	CodeUnauthenticated   = "unauthenticated"
	CodePermissionDenied  = "permission_denied"
	CodeUnavailable       = "unavailable"
	CodeDeadlineExceeded  = "deadline_exceeded"
	CodeResourceExhausted = "resource_exhausted"
)

// connectError renders a failed call as a typed, actionable error.
//
// An auth failure gets special handling because a bare 401 is the least
// useful thing forge could print: the user cannot tell whether they have no
// credential, a wrong one, or the right one for a different environment.
// Naming the endpoint, the source of the credential forge actually used, and
// both ways to supply a better one turns it into a message that can be acted
// on without a second run.
func (c *Client) connectError(procedure string, resp *http.Response, raw []byte) error {
	var envelope struct {
		Code    string        `json:"code"`
		Message string        `json:"message"`
		Details []ErrorDetail `json:"details,omitempty"`
	}
	_ = json.Unmarshal(raw, &envelope)

	out := &Error{
		HTTPStatus: resp.StatusCode,
		Code:       strings.TrimSpace(envelope.Code),
		Reason:     strings.TrimSpace(resp.Header.Get(ReasonHeader)),
		Scopes:     accesstoken.ParseScopesHeader(resp.Header.Get(accesstoken.ScopesHeader)),
		Message:    strings.TrimSpace(envelope.Message),
		Details:    envelope.Details,
		Header:     resp.Header.Clone(),
		Procedure:  procedure,
		Endpoint:   c.Endpoint.URL,
	}
	// A body that was not a Connect envelope — a proxy's HTML page — has
	// no message of its own, so the raw body is the only thing that says
	// anything. Used for DISPLAY only, which is why it is bounded.
	detail := out.Message
	if detail == "" {
		detail = strings.TrimSpace(string(raw))
	}
	if len(detail) > 500 {
		detail = detail[:500] + "…"
	}

	if out.Reason == ReasonClientUpgradeRequired {
		// Not a failure of this procedure, so the procedure and HTTP status
		// are noise: the whole remedy is the server's sentence.
		out.message = fmt.Sprintf("the control plane at %s refused this forge (%s): %s",
			c.Endpoint.URL, buildinfo.Version(), detail)
		return out
	}

	cred := c.credential()
	switch resp.StatusCode {
	case http.StatusServiceUnavailable:
		if out.Reason == ReasonAccessTokenUnavailable {
			// Our side could not check the token. Telling the user to sign in
			// again would send them round a loop that cannot fix it.
			out.message = fmt.Sprintf(
				"%s could not be authorized: the control plane at %s could not verify the credential right now (HTTP %d%s)\n"+
					"fix: retry in a moment; the credential itself has not been judged invalid%s",
				procedure, c.Endpoint.URL, resp.StatusCode, codeSuffix(out.Code), detailSuffix(detail))
			break
		}
		out.message = fmt.Sprintf("%s failed (HTTP %d%s) against %s%s",
			procedure, resp.StatusCode, codeSuffix(out.Code), c.Endpoint.URL, detailSuffix(detail))
	case http.StatusUnauthorized, http.StatusForbidden:
		out.message = fmt.Sprintf(
			"%s rejected the credential (HTTP %d%s)\n"+
				"  endpoint:   %s   (declared by env %q)\n"+
				"  credential: %s from %s%s\n%s%s",
			procedure, resp.StatusCode, codeSuffix(out.Code), c.Endpoint.URL, c.Endpoint.Env,
			cred.Identity(), cred.From, statusHint(c.Endpoint.Env),
			credentialProblem(out.Reason)+authFix(detail, c.Endpoint.TokenEnv, cred), detailSuffix(detail))
	case http.StatusNotImplemented:
		out.message = fmt.Sprintf("%s is not implemented by %s%s", procedure, c.Endpoint.URL, detailSuffix(detail))
	default:
		out.message = fmt.Sprintf("%s failed (HTTP %d%s) against %s%s",
			procedure, resp.StatusCode, codeSuffix(out.Code), c.Endpoint.URL, detailSuffix(detail))
	}
	if out.Reason != "" {
		out.message += "\n  reason: " + out.Reason
	}
	return out
}

// statusHint points at the command that describes a credential, so nobody
// opens the credentials file (which holds the secret) to find out which one
// was refused.
func statusHint(env string) string {
	if env == "" {
		return ""
	}
	return "   (inspect it with `forge cloud status " + env + "`; never print the token)"
}

func codeSuffix(code string) string {
	if code == "" {
		return ""
	}
	return ", " + code
}

func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return "\n  server said: " + detail
}

// credentialProblem says, in one line, which way a rejected credential is dead,
// when the server told us. Empty for a server that sent no reason (an older
// control plane), in which case authFix's generic remedy stands alone.
func credentialProblem(reason string) string {
	switch reason {
	case ReasonAccessTokenExpired:
		return "The credential has expired.\n"
	case ReasonAccessTokenRevoked:
		return "The credential was revoked, so it will not work again.\n"
	case ReasonAccessTokenUnknown:
		return "The control plane does not recognize this credential: it may belong to another environment, or have been deleted.\n"
	case ReasonAccessTokenMalformed:
		return "The credential is not a valid access token (truncated or mistyped?).\n"
	}
	return ""
}

var missingScopeRe = regexp.MustCompile(`\b([a-z]+:(?:read|write|manage|triage|query|invoke|connect))\b scope`)

// authFix is the remedy for a rejected credential. When the server names a
// missing scope the generic "log in again" is wrong advice on its own — a
// token minted without the scope stays without it — so it says what is
// actually true about scopes and how to get one.
//
// A credential a HELPER minted gets the host's remedy, never `forge login`:
// the user is meant to be signed in once, to the host, and the helper only
// ever hands out what that session holds. So a missing scope is the session's
// missing permission, and the fix is wherever that session is managed.
func authFix(detail, tokenEnv string, cred Credential) string {
	if cred.Source == SourceHelper {
		if m := missingScopeRe.FindStringSubmatch(detail); m != nil {
			return fmt.Sprintf(
				"The token is valid but lacks the %[1]s scope: the session it was minted from (%[2]s) does not hold %[1]s.\n"+
					"fix: grant that session %[1]s (or sign in to it again) in the application that provides it.\n"+
					"     If you still lack it, your role has no %[1]s grant: ask an org admin.\n"+
					"     For CI, a token with the scope bypasses the helper: export %[3]s=<token>",
				m[1], cred.From, tokenEnv)
		}
		return fmt.Sprintf(
			"fix: sign in again in the application that provided this credential (%s)\n"+
				"     export %s=<token>      (CI — a token bypasses the credential helper)", cred.From, tokenEnv)
	}
	if m := missingScopeRe.FindStringSubmatch(detail); m != nil {
		return fmt.Sprintf(
			"The token is valid but lacks the %[1]s scope.\n"+
				"fix: re-run `forge login` to pick up the scope — a token only carries scopes the control plane granted it at login.\n"+
				"     If the new token still lacks it, your role has no %[1]s grant: ask an org admin.\n"+
				"     An admin can mint one: `forge cloud token create --env <env> --name <name> --scopes %[1]s`, then export %[2]s=<token>",
			m[1], tokenEnv)
	}
	return fmt.Sprintf(
		"fix: forge login            (human — opens a browser)\n"+
			"     export %s=<token>      (CI — a pipeline has no browser)", tokenEnv)
}
