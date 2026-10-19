// Copyright (c) 2025 Reliant Labs
package types

import "time"

// UsageLimitFacts is what the activity that was refused by a provider's usage
// limit knows and the workflow cannot: whether another account of the same
// provider could take the request, when the provider's accounts next reset,
// and whether the chat allows (and can serve) a cross-provider fallback.
//
// It rides in the Temporal error's details, so the workflow's one decision
// function (decideOnUsageLimit) stays pure. The zero value means "no facts":
// the decision is then the plain wait-until-reset it was before accounts.
type UsageLimitFacts struct {
	// Provider is the driver id that refused ("claude", "codex").
	Provider     string `json:"provider,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	AccountLabel string `json:"account_label,omitempty"`
	// ResetAt is when the refused account's limit lifts; zero when unknown.
	ResetAt time.Time `json:"reset_at,omitzero"`
	// Window names the limit hit ("five_hour", "seven_day"); empty when unknown.
	Window string `json:"window,omitempty"`
	// AlternativeAccount is true when another account of the same provider
	// can serve the request now. The refused one is already benched, so the
	// retry resolves to the other.
	AlternativeAccount bool `json:"alternative_account,omitempty"`
	// ProviderEarliestReset is the soonest any of the provider's accounts
	// stops being limited; zero when an alternative exists or none is known.
	ProviderEarliestReset time.Time `json:"provider_earliest_reset,omitzero"`
	// FallbackModel is "<model>@<driver>" when the chat's policy allows a
	// cross-provider fallback and that model is servable now; empty
	// otherwise. Cross-provider fallback is opt-in, so empty is the default.
	FallbackModel string `json:"fallback_model,omitempty"`
}

// UsageLimitFailure is a usage-limit refusal together with the facts the
// activity gathered about it. The runtime turns it into a non-retryable
// Temporal error carrying the facts as details.
type UsageLimitFailure struct {
	Err   error
	Facts UsageLimitFacts
}

func (e *UsageLimitFailure) Error() string { return e.Err.Error() }
func (e *UsageLimitFailure) Unwrap() error { return e.Err }

// FailoverPolicy is what a run allows when a provider's accounts are all
// limited. The zero value is the default: wait for the reset.
type FailoverPolicy struct {
	// FallbackModel is "<model>@<driver>" to continue on, on another provider,
	// until the original provider resets. Empty = wait.
	FallbackModel string `json:"fallback_model,omitempty"`
	// PreferredAccount maps a driver id to the account (id, email or label) the
	// run prefers on it.
	PreferredAccount map[string]string `json:"preferred_account,omitempty"`
}

// FallbackReturn tells the first call_llm after a cross-provider fallback
// lease ended that the chat is back on its own model, so it can say so.
type FallbackReturn struct {
	// Provider is the driver id whose limit the lease covered.
	Provider string `json:"provider"`
	// Model is the "<model>@<driver>" the chat had been using meanwhile.
	Model string `json:"model"`
}
