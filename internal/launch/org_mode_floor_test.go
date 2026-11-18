// Copyright (c) 2025 Reliant Labs

package launch

import (
	"context"
	"testing"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

type modeFloorSource struct{ mode controlplanev1.OrgPolicyMode }

func (s modeFloorSource) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{Version: 2, Policy: &controlplanev1.OrgPolicy{Approvals: &controlplanev1.ApprovalsPolicy{
		Mode: s.mode, ChatModeFloor: controlplanev1.ChatModeFloor_CHAT_MODE_FLOOR_NO_AUTO,
	}}}, nil
}

func TestChatModeFloorCoercesAutoOnly(t *testing.T) {
	prev := orgpolicy.EnforcementOf(orgpolicy.PointApprovals)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, orgpolicy.EnforcementEnforce)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, prev) })

	org := "org-a"
	chat := &db.Chat{ID: "chat-1", OrgID: &org}
	enforced := orgpolicy.WithResolver(context.Background(), orgpolicy.New(modeFloorSource{controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE}))
	audited := orgpolicy.WithResolver(context.Background(), orgpolicy.New(modeFloorSource{controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT}))

	cases := []struct {
		name string
		ctx  context.Context
		chat *db.Chat
		mode interface{}
		want interface{}
	}{
		{"auto becomes manual", enforced, chat, "auto", "manual"},
		{"plan is never coerced", enforced, chat, "plan", "plan"},
		{"manual stays", enforced, chat, "manual", "manual"},
		{"section in audit changes nothing", audited, chat, "auto", "auto"},
		{"a chat outside any org", enforced, &db.Chat{ID: "chat-2"}, "auto", "auto"},
		{"no mode input", enforced, chat, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inputs := map[string]interface{}{"model": "m"}
			if tc.mode != nil {
				inputs["mode"] = tc.mode
			}
			applyChatModeFloor(tc.ctx, tc.chat, inputs)
			if got := inputs["mode"]; got != tc.want {
				t.Fatalf("mode = %v, want %v", got, tc.want)
			}
			if inputs["model"] != "m" {
				t.Fatal("the floor touched another input")
			}
		})
	}
	applyChatModeFloor(enforced, chat, nil) // a nil map is a no-op, not a panic
}
