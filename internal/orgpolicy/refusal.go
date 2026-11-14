// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"errors"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
)

// The reasons an org policy refuses a call with (control-plane
// docs/design/org-policies.md §4.1, §4.2, §4.7). The gateway sends them as
// x-forge-error-reason on a 403, never a 429: a policy is not fixed by
// topping up credit. They are part of the API and never change.
const (
	ReasonModelNotAllowed       = "model_not_allowed"
	ReasonMemberSpendCapReached = "member_spend_cap_reached"
	ReasonOrgSpendCapReached    = "org_spend_cap_reached"
	ReasonProviderNotAllowed    = "provider_not_allowed"
)

// IsRefusalReason reports whether reason is one an org policy refuses with.
func IsRefusalReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case ReasonModelNotAllowed, ReasonMemberSpendCapReached, ReasonOrgSpendCapReached, ReasonProviderNotAllowed:
		return true
	}
	return false
}

// Refusal is a call one of the org's policies refused: a VERDICT, not a fault
// (§5.1). It is never retried, never read as a dead credential, and never
// benches an account. A run a person is behind pauses on it, resumable (by
// another model, or once an admin changes the policy); an unattended run ends
// with it.
//
// It deliberately does not unwrap to the provider error it came from: a 403
// from the gateway is the org's verdict, and code that reads a provider 403
// as a rejected key must never see one.
type Refusal struct {
	// Reason is one of the Reason* constants.
	Reason string
	// Detail is the refuser's own explanation (the gateway's message), shown
	// after ours; "" when it gave none.
	Detail string
	// Unattended marks a refusal met by a run nobody is present for, set at
	// the activity edge: such a run ends instead of pausing for a person.
	Unattended bool
}

// Error is the message the chat shows, carrying the
// chatmarkers.KindOrgPolicyRefused marker (payload: the reason) so the
// verdict survives Temporal's stringification of the activity error.
func (r *Refusal) Error() string {
	return chatmarkers.Wrap(chatmarkers.KindOrgPolicyRefused, r.Reason, r.Message())
}

// Message is what was refused and what the person can do, without the
// marker: for an rpc's error, which a person reads as it is.
func (r *Refusal) Message() string {
	msg := chatmarkers.OrgPolicyRefusedLead + " " + refusalSentence(r.Reason)
	if d := strings.TrimSpace(r.Detail); d != "" {
		msg += " (" + strings.TrimSuffix(d, ".") + ")"
	}
	return msg
}

// ErrorClass: a policy the org's admins set, not a server fault. It logs at
// INFO and never reaches Sentry.
func (r *Refusal) ErrorClass() svcerr.Class { return svcerr.ClassUser }

// refusalSentence says what was refused and what the person can do. It
// follows chatmarkers.OrgPolicyRefusedLead ("Your organization's policy").
func refusalSentence(reason string) string {
	switch reason {
	case ReasonModelNotAllowed:
		return "does not allow this model. Switch the chat to an allowed model, or ask an org admin to allow it, then resume."
	case ReasonProviderNotAllowed:
		return "does not allow this provider. Switch the chat to an allowed model or provider, or ask an org admin, then resume."
	case ReasonMemberSpendCapReached:
		return "limits your AI spend, and you have reached your limit for this period. Ask an org admin to raise it, then resume."
	case ReasonOrgSpendCapReached:
		return "caps the organization's AI spend, and the cap for this period is reached. Ask an org admin to raise it, then resume."
	}
	return "refused this request. Ask an org admin, then resume."
}

// RefusalOf returns the policy refusal err is or carries: the typed Refusal
// while it is still in the chain, else one recovered from the marker every
// wrap and Temporal's stringification keep (Reason only).
func RefusalOf(err error) (*Refusal, bool) {
	if err == nil {
		return nil, false
	}
	var r *Refusal
	if errors.As(err, &r) && r != nil {
		return r, true
	}
	if kind, payload, ok := chatmarkers.Extract(err.Error()); ok && kind == chatmarkers.KindOrgPolicyRefused {
		return &Refusal{Reason: payload}, true
	}
	return nil, false
}

// IsRefusal reports whether err is, or carries, an org policy refusal.
func IsRefusal(err error) bool {
	_, ok := RefusalOf(err)
	return ok
}
