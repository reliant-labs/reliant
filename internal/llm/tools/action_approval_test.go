// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionApprovalTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tool, input, want string
	}{
		{"gmail__message_send", `{"to":["ann@example.com"],"subject":"Hi"}`, "Send email to ann@example.com?"},
		{"gmail__message_send", `{"to":["a@x.io","b@x.io","c@x.io","d@x.io","e@x.io"],"subject":"Hi"}`, "Send email to a@x.io, b@x.io, c@x.io and 2 more?"},
		{"twilio__message_send", `{"to":"+15551234567","body":"Running late"}`, "Send message to +15551234567?"},
		{"slack__message_post", `{"channel":"#general","text":"Ship it"}`, "Post message in #general?"},
		{"slack__message_reply", `{"channel":"C024BE91L","thread_ts":"1.2","text":"ok"}`, "Reply in thread in C024BE91L?"},
		{"slack__reaction_add", `{"channel":"#ops","timestamp":"1.2","name":"eyes"}`, "Add reaction in #ops?"},
		{"github__issue_create", `{"owner":"acme","repo":"api","title":"Bug"}`, "Create issue in acme/api?"},
		{"github__issue_comment", `{"owner":"acme","repo":"api","issue_number":12,"body":"LGTM"}`, "Comment on issue or pull request on acme/api#12?"},
		{"github__pr_review_create", `{"owner":"acme","repo":"api","pull_number":7,"event":"APPROVE"}`, "Review pull request on acme/api#7?"},
		{"github__workflow_dispatch", `{"owner":"acme","repo":"api","workflow":"ci.yml","ref":"main"}`, "Run Actions workflow in acme/api?"},
		{"http__request", `{"url":"https://example.com/hook","method":"post"}`, "POST request to https://example.com/hook?"},
		{"http__request", `{"url":"https://example.com/status"}`, "GET request to https://example.com/status?"},
		// Parameters that say nothing about where it lands: the action alone.
		{"github__issue_create", `not json`, "Create issue?"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, ActionApprovalTitle(tc.tool, tc.input), tc.tool+" "+tc.input)
	}
}

func TestEveryMutatingActionHasAReadableName(t *testing.T) {
	t.Parallel()
	actions := mutatingIntegrationActions()
	require.NotEmpty(t, actions)
	for name, action := range actions {
		assert.NotEmpty(t, action.DisplayName, name)
		assert.NotEmpty(t, action.Integration, name)
		assert.NotEmpty(t, action.Icon, name)
	}
}

func TestAlwaysAllowSettingValueRoundTrips(t *testing.T) {
	t.Parallel()
	action, ok := MutatingIntegrationActionInfo("slack__message_post")
	require.True(t, ok)
	assert.Equal(t, "Slack", action.Integration)
	assert.True(t, AllowsAlways(AlwaysAllowSettingValue(action)))
	assert.False(t, AllowsAlways(`{"decision":"ask"}`))
	assert.False(t, AllowsAlways("yes"), "a value that does not parse allows nothing")
	assert.Equal(t, "tool.approval.slack__message_post", ActionApprovalSettingKey("slack__message_post"))
}

// The turn's capability set lists the offered mutating integration actions as
// asking first — and none on an unattended run, which keeps such an action
// only by naming it (the author's approval) and has nobody to ask.
func TestCapabilitiesApprovalRequired(t *testing.T) {
	t.Parallel()
	declared := []string{"slack__message_post", "github__issue_get", ToolView}
	resolve := func(unattended bool) *Capabilities {
		return ResolveCapabilities(CapabilityInputs{
			Access:             ResolveToolAccess(declared, nil, nil),
			Permission:         PermissionMutating,
			UsableIntegrations: map[string]bool{"slack": true, "github": true},
			Unattended:         unattended,
		})
	}

	attended := resolve(false)
	require.Contains(t, attended.Offered, "slack__message_post")
	require.Contains(t, attended.Offered, "github__issue_get")
	assert.Equal(t, []string{"slack__message_post"}, attended.Proto().GetApprovalRequired(),
		"only the action that changes something asks; read-only and other tools never do")

	unattended := resolve(true)
	require.Contains(t, unattended.Offered, "slack__message_post", "named, so the unattended run keeps it")
	assert.Empty(t, unattended.Proto().GetApprovalRequired(), "an unattended run asks nobody")
}
