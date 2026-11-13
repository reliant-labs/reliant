// Copyright (c) 2025 Reliant Labs
package core

import "time"

// PersonalFunding is one chat's question "continue on your own account?" and
// its answer (control-plane docs/design/org-provider-accounts.md §10): before
// a chat in an org moves from the org's accounts to the person's own, it parks
// and asks once, for every thread of the chat (chat_personal_funding).
type PersonalFunding struct {
	ChatID, UserID, OrgID string
	// State is PersonalFundingAsked, PersonalFundingUsePersonal or PersonalFundingKeepWaiting.
	State string
	// Reason is PersonalFundingLimited, PersonalFundingUnusable or PersonalFundingUnserved.
	Reason string
	// Model is the registry model the turn resolved to.
	Model string
	// PersonalProvider and PersonalAccountLabel name the person's own account that would serve.
	PersonalProvider, PersonalAccountLabel string
	// OrgResetAt is when the org's accounts reset (Limited); nil when unknown.
	OrgResetAt *time.Time
	// LeaseUntil ends a UsePersonal answer to a Limited question.
	LeaseUntil *time.Time
	AskedAt    time.Time
	AnsweredAt *time.Time
	// Announced reports that the transcript has said the person's own account
	// serves under a UsePersonal answer.
	Announced bool
	UpdatedAt time.Time
}

// chat_personal_funding.state values.
const (
	PersonalFundingAsked       = "asked"
	PersonalFundingUsePersonal = "use_personal"
	PersonalFundingKeepWaiting = "keep_waiting"
)

// chat_personal_funding.reason values: the three cases of §10.1.
const (
	// PersonalFundingLimited: the org's accounts are rate-limited until OrgResetAt.
	PersonalFundingLimited = "limited"
	// PersonalFundingUnusable: no org account is usable and none will be at a known time.
	PersonalFundingUnusable = "unusable"
	// PersonalFundingUnserved: no org account serves Model.
	PersonalFundingUnserved = "unserved"
)

// LeaseActive reports whether a UsePersonal answer still lets the chat use the
// person's own account for a call of model at now: until the org's reset for
// Limited; until a turn start finds the org's accounts usable again for
// Unusable (the caller ends it then); while the chat stays on its model for
// Unserved.
func (p *PersonalFunding) LeaseActive(now time.Time, model string) bool {
	if p == nil || p.State != PersonalFundingUsePersonal {
		return false
	}
	switch p.Reason {
	case PersonalFundingLimited:
		return p.LeaseUntil != nil && now.Before(*p.LeaseUntil)
	case PersonalFundingUnusable:
		return true
	case PersonalFundingUnserved:
		return model != "" && model == p.Model
	}
	return false
}
