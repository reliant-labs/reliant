package cli

import (
	"errors"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// The machine-readable half of a failed --json document's `error`.
//
// `error` is a sentence for a person. A host that renders forge's failure —
// Reliant's deploy panel, a pipeline — has to decide WHO can fix it (sign in
// again, or ask an admin for a grant), and before these fields it could only
// decide that by searching the sentence, which breaks the first time the
// control plane rewords a refusal. So the document carries what the error
// already knows in structure: the control plane's Connect code and reason,
// the scopes a scope_missing refusal names, and forge's own "there is no
// credential at all".

// ReasonNoControlPlaneCredential is the error_reason of a failure to RESOLVE
// a credential (cloud.ErrNoCredential): nothing was presented, so nothing was
// refused. Signing in fixes it.
const ReasonNoControlPlaneCredential = "no_control_plane_credential"

// jsonErrorFacts is what a document reports about its error beside the text.
// Every field is omitted when unknown, so a consumer tests for the key.
type jsonErrorFacts struct {
	// ErrorCode is the control plane's Connect code, as its wire name
	// ("permission_denied"), or "unauthenticated" for a missing credential.
	ErrorCode string `json:"error_code,omitempty"`
	// ErrorReason is the stable reason: the control plane's (its
	// x-forge-error-reason), a credential helper's server's, or
	// ReasonNoControlPlaneCredential. THE FIELD TO BRANCH ON.
	ErrorReason string `json:"error_reason,omitempty"`
	// ErrorScopes are the scopes a "scope_missing" refusal names.
	ErrorScopes []string `json:"error_scopes,omitempty"`
	// A throttled claimable deploy (reason claimable_throttled): how long to
	// wait, when that is, and whose limit it was (network|device|platform).
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
	RetryAt           string `json:"retry_at,omitempty"`
	ThrottleScope     string `json:"throttle_scope,omitempty"`
	// An unavailable claimable deploy (reason claimable_unavailable): why,
	// as the control plane's cause code (disabled | dependency_unavailable |
	// not_supported, or forge's not_deployed) and the switch or dependency.
	UnavailableCause     string `json:"unavailable_cause,omitempty"`
	UnavailableComponent string `json:"unavailable_component,omitempty"`
}

// errorFactsOf reads err's structure. The control plane's own answer wins
// over forge's classification: a credential helper's denial wrapped in
// ErrNoCredential still says scope_missing when the server said so.
func errorFactsOf(err error) jsonErrorFacts {
	if err == nil {
		return jsonErrorFacts{}
	}
	var cr *claimableRefusal
	if errors.As(err, &cr) {
		facts := jsonErrorFacts{ErrorCode: cr.code, ErrorReason: cr.reason}
		if t := cr.throttle; t != nil {
			facts.RetryAfterSeconds = int64(t.retryAfter / time.Second)
			facts.RetryAt = t.retryAt.UTC().Format(time.RFC3339)
			facts.ThrottleScope = t.scope
		}
		if c := cr.cause; c != nil {
			facts.UnavailableCause, facts.UnavailableComponent = c.Code, c.Component
		}
		return facts
	}
	var ce *cloud.Error
	if errors.As(err, &ce) && (ce.Code != "" || ce.Reason != "") {
		return jsonErrorFacts{ErrorCode: ce.Code, ErrorReason: ce.Reason, ErrorScopes: ce.Scopes}
	}
	var he *cloudcred.HelperError
	if errors.As(err, &he) && he.Reason != "" {
		facts := jsonErrorFacts{ErrorReason: he.Reason, ErrorScopes: he.Scopes}
		if he.Code == cloudcred.CodeDenied {
			facts.ErrorCode = cloud.CodePermissionDenied
		}
		return facts
	}
	if errors.Is(err, cloud.ErrNoCredential) {
		return jsonErrorFacts{ErrorCode: cloud.CodeUnauthenticated, ErrorReason: ReasonNoControlPlaneCredential}
	}
	return jsonErrorFacts{}
}
