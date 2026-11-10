// Copyright (c) 2025 Reliant Labs

package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// members: alice is a live member of acme only.
type members struct{ err error }

func (m members) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	if m.err != nil {
		return false, true, m.err
	}
	return userID == "alice" && orgID == "acme", true, nil
}

// A list keeps, in order, exactly the rows the single-resource read allows,
// and leaves the slice it was given alone.
func TestVisibleKeepsWhatTheReadAllows(t *testing.T) {
	acme, globex := "acme", "globex"
	rows := []*core.Chat{
		{ID: "acme", UserID: "alice", OrgID: &acme},
		{ID: "globex", UserID: "alice", OrgID: &globex},
		{ID: "none", UserID: "alice"},
		{ID: "bobs", UserID: "bob"},
	}
	ctx := WithMembership(context.Background(), members{})
	got := Visible(ctx, "alice", rows, CanReadChat[*core.Chat])
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if len(ids) != 2 || ids[0] != "acme" || ids[1] != "none" {
		t.Fatalf("Visible = %v, want [acme none]", ids)
	}
	if rows[1].ID != "globex" {
		t.Fatalf("Visible modified its input: %v", rows[1].ID)
	}
}

// A resource in an org is its owner's only while they are a live member of
// that org, for every kind that carries an org, and the decision fails closed.
func TestOrgResourcesNeedLiveMembership(t *testing.T) {
	org := func(s string) *string { return &s }
	kinds := map[string]func(ctx context.Context, caller string, orgID *string) []bool{
		"chat": func(ctx context.Context, caller string, orgID *string) []bool {
			c, r := &core.Chat{UserID: caller, OrgID: orgID}, &core.ChatRow{UserID: caller, OrgID: orgID}
			return []bool{CanReadChat(ctx, caller, c), CanWriteChat(ctx, caller, c), CanReadChat(ctx, caller, r), CanWriteChat(ctx, caller, r)}
		},
		"project": func(ctx context.Context, caller string, orgID *string) []bool {
			p := &core.Project{UserID: caller, OrgID: orgID}
			return []bool{CanReadProject(ctx, caller, p), CanWriteProject(ctx, caller, p)}
		},
		"workflow": func(ctx context.Context, caller string, orgID *string) []bool {
			w := &core.WorkflowDraft{UserID: caller, OrgID: orgID}
			return []bool{CanReadWorkflow(ctx, caller, w), CanWriteWorkflow(ctx, caller, w)}
		},
		"trigger": func(ctx context.Context, caller string, orgID *string) []bool {
			tr := &core.Trigger{UserID: caller, OrgID: orgID}
			return []bool{CanReadTrigger(ctx, caller, tr), CanWriteTrigger(ctx, caller, tr)}
		},
	}
	bg := context.Background()
	cases := []struct {
		name   string
		ctx    context.Context
		caller string
		org    *string
		want   bool
	}{
		{"a live member's own resource", WithMembership(bg, members{}), "alice", org("acme"), true},
		{"removed from the org", WithMembership(bg, members{}), "alice", org("globex"), false},
		{"another owner who is not a member", WithMembership(bg, members{}), "bob", org("acme"), false},
		{"an outage fails closed", WithMembership(bg, members{err: errors.New("down")}), "alice", org("acme"), false},
		{"no membership wired fails closed", bg, "alice", org("acme"), false},
		{"no orgs on this deployment: owner only", WithMembership(bg, NoOrgs{}), "alice", org("acme"), true},
		{"a resource with no org: owner only", bg, "alice", nil, true},
	}
	for kind, decide := range kinds {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				for i, got := range decide(tc.ctx, tc.caller, tc.org) {
					if got != tc.want {
						t.Errorf("decision %d = %v, want %v", i, got, tc.want)
					}
				}
			})
		}
	}
}
