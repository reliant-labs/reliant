package automationpolicy

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
)

func caps(items ...string) Capabilities {
	c := Capabilities{}
	c.Add(items...)
	return c
}

func TestDecide(t *testing.T) {
	saved := caps("identity:app", "machine:d1", "connection:github/c1", "scope:github/repo")
	for _, tc := range []struct {
		name       string
		mode       ApprovalMode
		op         Operation
		base, next Capabilities
		card       bool
		added      []string
		removed    []string
		how        string
	}{
		{name: "definition change keeps capabilities", mode: ApprovalOnCapabilityChange, op: OperationUpdate, base: saved, next: saved, how: "auto:on_capability_change"},
		{name: "resume keeps capabilities", mode: ApprovalOnCapabilityChange, op: OperationEnable, base: saved, next: saved, how: "auto:on_capability_change"},
		{name: "narrowing is not widening", mode: ApprovalOnCapabilityChange, op: OperationUpdate, base: saved, next: caps("identity:app", "connection:github/c1", "scope:github/repo"), removed: []string{"machine:d1"}, how: "auto:on_capability_change"},
		{name: "new connection", mode: ApprovalOnCapabilityChange, op: OperationUpdate, base: saved, next: caps("identity:app", "machine:d1", "connection:github/c1", "scope:github/repo", "connection:slack/default"), card: true, added: []string{"connection:slack/default"}, how: "card"},
		{name: "different connection", mode: ApprovalOnCapabilityChange, op: OperationUpdate, base: saved, next: caps("identity:app", "machine:d1", "connection:github/c2", "scope:github/repo"), card: true, added: []string{"connection:github/c2"}, removed: []string{"connection:github/c1"}, how: "card"},
		{name: "new scope", mode: ApprovalOnCapabilityChange, op: OperationUpdate, base: saved, next: caps("identity:app", "machine:d1", "connection:github/c1", "scope:github/repo", "scope:github/admin"), card: true, added: []string{"scope:github/admin"}, how: "card"},
		{name: "app to owner", mode: ApprovalOnCapabilityChange, op: OperationUpdate, base: saved, next: caps("identity:owner", "machine:d1", "connection:github/c1", "scope:github/repo"), card: true, added: []string{"identity:owner"}, removed: []string{"identity:app"}, how: "card"},
		{name: "owner to app", mode: ApprovalOnCapabilityChange, op: OperationUpdate, base: caps("identity:owner"), next: caps("identity:app"), card: true, added: []string{"identity:app"}, removed: []string{"identity:owner"}, how: "card"},
		{name: "new machine", mode: ApprovalOnCapabilityChange, op: OperationUpdate, base: saved, next: caps("identity:app", "machine:d2", "connection:github/c1", "scope:github/repo"), card: true, added: []string{"machine:d2"}, removed: []string{"machine:d1"}, how: "card"},
		{name: "new secret", mode: ApprovalOnCapabilityChange, op: OperationUpdate, base: saved, next: caps("identity:app", "machine:d1", "connection:github/c1", "scope:github/repo", "secret:hosted_use/g1"), card: true, added: []string{"secret:hosted_use/g1"}, how: "card"},
		{name: "prod deploy step", mode: ApprovalOnCapabilityChange, op: OperationUpdate, base: saved, next: caps("identity:app", "machine:d1", "connection:github/c1", "scope:github/repo", "deploy:prod"), card: true, added: []string{"deploy:prod"}, how: "card"},
		{name: "create is new standing power", mode: ApprovalOnCapabilityChange, op: OperationCreate, next: saved, card: true, added: saved.Sorted(), how: "card"},
		{name: "unresolvable baseline fails closed", mode: ApprovalOnCapabilityChange, op: OperationUpdate, next: saved, card: true, added: saved.Sorted(), how: "card"},
		{name: "always asks without widening", mode: ApprovalAlways, op: OperationEnable, base: saved, next: saved, card: true, how: "card"},
		{name: "never applies widening", mode: ApprovalNever, op: OperationUpdate, base: saved, next: caps("identity:owner", "deploy:prod"), added: []string{"deploy:prod", "identity:owner"}, removed: []string{"connection:github/c1", "identity:app", "machine:d1", "scope:github/repo"}, how: "auto:never"},
		{name: "never applies create", mode: ApprovalNever, op: OperationCreate, next: saved, added: saved.Sorted(), how: "auto:never"},
		{name: "pause under always", mode: ApprovalAlways, op: OperationPause, how: "auto:pause"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.mode, tc.op, tc.base, tc.next)
			assert.Equal(t, tc.card, d.CardRequired, d.Reason)
			assert.Equal(t, tc.added, d.Added)
			assert.Equal(t, tc.removed, d.Removed)
			assert.Equal(t, tc.how, d.AuditHow(tc.op))
			assert.NotEmpty(t, d.Reason)
			for _, item := range tc.added {
				if tc.card && tc.op == OperationUpdate && tc.mode == ApprovalOnCapabilityChange && tc.base != nil {
					assert.Contains(t, d.Reason, item, "the card names what widened")
				}
			}
		})
	}
}

func TestParseApprovalModeFailsClosed(t *testing.T) {
	assert.Equal(t, ApprovalOnCapabilityChange, ParseApprovalMode(""))
	assert.Equal(t, ApprovalNever, ParseApprovalMode(" never "))
	assert.Equal(t, ApprovalAlways, ParseApprovalMode("always"))
	assert.Equal(t, ApprovalOnCapabilityChange, ParseApprovalMode("on_capability_change"))
	assert.Equal(t, ApprovalAlways, ParseApprovalMode("nevr"), "an unknown value must not read as never")
}

func TestResolveApprovalMode(t *testing.T) {
	p1, p2 := "p1", "p2"
	setting := func(project *string, value string) *core.Setting {
		return &core.Setting{Key: ApprovalSettingKey, ProjectID: project, Value: value}
	}
	assert.Equal(t, DefaultApprovalMode, ResolveApprovalMode(nil, p1), "unset is the default")
	account := []*core.Setting{setting(nil, "never")}
	assert.Equal(t, ApprovalNever, ResolveApprovalMode(account, p1), "the account setting applies to every project")
	override := append(account, setting(&p1, "always"))
	assert.Equal(t, ApprovalAlways, ResolveApprovalMode(override, p1), "a project overrides the account")
	assert.Equal(t, ApprovalNever, ResolveApprovalMode(override, p2))
	assert.Equal(t, ApprovalAlways, ResolveApprovalMode(override, p2, p1), "a move between projects takes the stricter")
	other := []*core.Setting{{Key: "tool.bindings.x", Value: "never"}}
	assert.Equal(t, DefaultApprovalMode, ResolveApprovalMode(other, p1), "other keys are ignored")
}
