// Copyright (c) 2025 Reliant Labs

// Package orgfunding is a person's choice, per org, of whose AI accounts that
// org's chats use first and what happens when the org's accounts cannot
// serve (control-plane docs/design/org-provider-accounts.md §4.4, §10.4). It
// is the user setting provider.org_funding, a JSON object keyed by org id:
//
//	{"<org_id>": {"order": "org_first", "on_org_limit": "ask"}}
//
// A missing entry, a missing field and an unreadable value all read as the
// defaults (org_first, ask), so a bad setting can only ask or wait, never
// spend the person's money.
//
//forge:exclude-contract: a value type and its parser over one user setting; no collaborators to declare
package orgfunding

import (
	"encoding/json"
	"strings"
)

// SettingKey is the user setting (no project) that holds every org's Preference.
const SettingKey = "provider.org_funding"

// Order is whose accounts an org's chat uses first.
type Order string

const (
	// OrderOrgFirst uses the org's accounts first, then the person's own. The default.
	OrderOrgFirst Order = "org_first"
	// OrderPersonalFirst uses the person's own accounts first: it is their
	// money, so it is theirs to choose.
	OrderPersonalFirst Order = "personal_first"
	// OrderOrgOnly never uses the person's own accounts in the org.
	OrderOrgOnly Order = "org_only"
)

// OnOrgLimit is what happens when the org's accounts cannot serve a chat and
// the person's own could.
type OnOrgLimit string

const (
	// OnOrgLimitAsk parks the chat and asks (§10). The default; with nobody
	// to answer, the chat waits.
	OnOrgLimitAsk OnOrgLimit = "ask"
	// OnOrgLimitUsePersonal continues on the person's own account, with a
	// notice, for as long as the org's accounts cannot serve.
	OnOrgLimitUsePersonal OnOrgLimit = "use_personal"
	// OnOrgLimitWait waits for the org's accounts without asking.
	OnOrgLimitWait OnOrgLimit = "wait"
)

// Preference is one org's entry.
type Preference struct {
	Order      Order      `json:"order,omitempty"`
	OnOrgLimit OnOrgLimit `json:"on_org_limit,omitempty"`
}

// Default is the preference of an org with no entry.
func Default() Preference {
	return Preference{Order: OrderOrgFirst, OnOrgLimit: OnOrgLimitAsk}
}

// ParseOrder reads an order; anything it does not know is "" (unset).
func ParseOrder(s string) Order {
	switch o := Order(strings.TrimSpace(s)); o {
	case OrderOrgFirst, OrderPersonalFirst, OrderOrgOnly:
		return o
	}
	return ""
}

// ParseOnOrgLimit reads an on-limit choice; anything it does not know is "" (unset).
func ParseOnOrgLimit(s string) OnOrgLimit {
	switch o := OnOrgLimit(strings.TrimSpace(s)); o {
	case OnOrgLimitAsk, OnOrgLimitUsePersonal, OnOrgLimitWait:
		return o
	}
	return ""
}

// normalized fills unset or unknown fields with the defaults.
func (p Preference) normalized() Preference {
	d := Default()
	if o := ParseOrder(string(p.Order)); o != "" {
		d.Order = o
	}
	if l := ParseOnOrgLimit(string(p.OnOrgLimit)); l != "" {
		d.OnOrgLimit = l
	}
	return d
}

// Settings is the parsed setting, by org id.
type Settings map[string]Preference

// Parse reads the setting's value. An unreadable value is no entries: every
// org has the defaults.
func Parse(value string) Settings {
	if strings.TrimSpace(value) == "" {
		return Settings{}
	}
	var raw map[string]Preference
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return Settings{}
	}
	return Settings(raw)
}

// For is orgID's preference, the defaults filling anything unset or unknown.
func (s Settings) For(orgID string) Preference {
	return s[orgID].normalized()
}

// With returns the setting with orgID's entry replaced by p (fields left ""
// keep the entry's current value), and drops entries for orgs not in keep when
// keep is non-nil: leaving an org makes its entry inert, and it goes the next
// time the setting is written (§10.4).
func (s Settings) With(orgID string, p Preference, keep func(orgID string) bool) Settings {
	out := Settings{}
	for org, pref := range s {
		if keep == nil || org == orgID || keep(org) {
			out[org] = pref
		}
	}
	cur := out[orgID]
	if o := ParseOrder(string(p.Order)); o != "" {
		cur.Order = o
	}
	if l := ParseOnOrgLimit(string(p.OnOrgLimit)); l != "" {
		cur.OnOrgLimit = l
	}
	out[orgID] = cur
	return out
}

// Without returns the setting with orgID's entry removed (Reset).
func (s Settings) Without(orgID string) Settings {
	out := Settings{}
	for org, pref := range s {
		if org != orgID {
			out[org] = pref
		}
	}
	return out
}

// Value is the setting's stored value.
func (s Settings) Value() string {
	b, err := json.Marshal(map[string]Preference(s))
	if err != nil {
		return "{}"
	}
	return string(b)
}
