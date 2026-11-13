// Copyright (c) 2025 Reliant Labs
package orgfunding

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A bad setting can only ask or wait, never spend: anything unreadable,
// missing or unknown reads as the defaults (§10.4).
func TestParse_UnreadableIsTheDefaults(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "   ", "not json", `[]`, `{"acme": "personal"}`, `{"acme": {"order": "free_money", "on_org_limit": "spend"}}`} {
		assert.Equal(t, Default(), Parse(value).For("acme"), "%q", value)
	}
	assert.Equal(t, Preference{Order: OrderOrgFirst, OnOrgLimit: OnOrgLimitAsk}, Default())
}

func TestParse_ReadsEachOrgsEntry(t *testing.T) {
	t.Parallel()
	s := Parse(`{"acme": {"order": "personal_first", "on_org_limit": "use_personal"}, "globex": {"on_org_limit": "wait"}}`)
	assert.Equal(t, Preference{Order: OrderPersonalFirst, OnOrgLimit: OnOrgLimitUsePersonal}, s.For("acme"))
	assert.Equal(t, Preference{Order: OrderOrgFirst, OnOrgLimit: OnOrgLimitWait}, s.For("globex"), "a missing field is its default")
	assert.Equal(t, Default(), s.For("initech"))
}

// remember writes one field and keeps the rest; leaving an org drops its
// entry the next time the setting is written; Reset removes it.
func TestWithWithoutAndValue(t *testing.T) {
	t.Parallel()
	s := Parse(`{"acme": {"order": "org_only"}, "left": {"on_org_limit": "use_personal"}}`)
	s = s.With("acme", Preference{OnOrgLimit: OnOrgLimitUsePersonal}, func(org string) bool { return org != "left" })
	assert.Equal(t, Preference{Order: OrderOrgOnly, OnOrgLimit: OnOrgLimitUsePersonal}, Parse(s.Value()).For("acme"))
	assert.Equal(t, Default(), Parse(s.Value()).For("left"), "the org the person left is dropped")

	s = s.Without("acme")
	assert.Equal(t, Default(), Parse(s.Value()).For("acme"))
	assert.Equal(t, "{}", s.Value())
}
