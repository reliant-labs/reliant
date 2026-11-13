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

// withDefault is members with alice's default org: home.
type withDefault struct{ members }

func (withDefault) DefaultOrg(_ context.Context, userID string) (string, error) {
	if userID == "alice" {
		return "acme", nil
	}
	return "", nil
}

// A credential bound to one org acts only there, for every kind and both
// questions: another org's resource is refused even to a live member of it,
// and a row with no org yet is reached only from the caller's default org.
func TestCredentialBoundToAnOrgActsOnlyThere(t *testing.T) {
	org := func(s string) *string { return &s }
	bg := WithMembership(context.Background(), withDefault{})
	decisions := func(ctx context.Context, orgID *string) []bool {
		c, p := &core.Chat{UserID: "alice", OrgID: orgID}, &core.Project{UserID: "alice", OrgID: orgID}
		w, tr := &core.WorkflowDraft{UserID: "alice", OrgID: orgID}, &core.Trigger{UserID: "alice", OrgID: orgID}
		return []bool{
			CanReadChat(ctx, "alice", c), CanWriteChat(ctx, "alice", c),
			CanReadProject(ctx, "alice", p), CanWriteProject(ctx, "alice", p),
			CanReadWorkflow(ctx, "alice", w), CanWriteWorkflow(ctx, "alice", w),
			CanReadTrigger(ctx, "alice", tr), CanWriteTrigger(ctx, "alice", tr),
		}
	}
	cases := []struct {
		name string
		ctx  context.Context
		org  *string
		want bool
	}{
		{"bound to the row's org", WithCredentialOrg(bg, "acme"), org("acme"), true},
		{"bound to another org", WithCredentialOrg(bg, "globex"), org("acme"), false},
		{"a row with no org, bound to the default", WithCredentialOrg(bg, "acme"), nil, true},
		{"a row with no org, bound to another org", WithCredentialOrg(bg, "globex"), nil, false},
		{"no default readable: fails closed", WithCredentialOrg(WithMembership(context.Background(), members{}), "acme"), nil, false},
		{"no credential binding", bg, org("acme"), true},
		{"blank binding binds nothing", WithCredentialOrg(bg, ""), org("acme"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i, got := range decisions(tc.ctx, tc.org) {
				if got != tc.want {
					t.Errorf("decision %d = %v, want %v", i, got, tc.want)
				}
			}
		})
	}
}

// A decision whose context carries no membership (a process's background:
// a loop or consumer no listener or activity wraps) asks the membership the
// process installed at boot; a context's own still wins, and with neither it
// fails closed.
func TestProcessMembershipIsTheBackgroundsFallback(t *testing.T) {
	acme := "acme"
	chat := &core.Chat{UserID: "alice", OrgID: &acme}
	t.Cleanup(func() { SetProcessMembership(nil) })

	SetProcessMembership(nil)
	if CanReadChat(context.Background(), "alice", chat) {
		t.Fatal("no membership anywhere must fail closed")
	}
	SetProcessMembership(members{})
	if !CanReadChat(context.Background(), "alice", chat) {
		t.Fatal("the background must decide with the process's membership")
	}
	if CanReadChat(WithMembership(context.Background(), members{err: errors.New("down")}), "alice", chat) {
		t.Fatal("a context's own membership must win over the process's")
	}
}

// Using an org-owned thing (an org connection) needs a positive answer that
// the caller is a live member: no answer is a refusal, since there is no
// owner to fall back to, and an outage or an unwired path is not an answer.
func TestOrgMemberDenial(t *testing.T) {
	member := WithMembership(context.Background(), members{})
	for name, tc := range map[string]struct {
		ctx          context.Context
		user, org    string
		want         error
		wantRefusedT string
	}{
		"a live member":                 {ctx: member, user: "alice", org: "acme"},
		"a non-member":                  {ctx: member, user: "bob", org: "acme", want: ErrNotOrgMember},
		"another org":                   {ctx: member, user: "alice", org: "globex", want: ErrNotOrgMember},
		"no caller":                     {ctx: member, user: "", org: "acme", want: ErrDenied},
		"no org":                        {ctx: member, user: "alice", org: "", want: ErrDenied},
		"no answer (no orgs here)":      {ctx: WithMembership(context.Background(), NoOrgs{}), user: "alice", org: "acme", want: ErrNotOrgMember},
		"an outage":                     {ctx: WithMembership(context.Background(), members{err: errors.New("down")}), user: "alice", org: "acme", want: ErrMembershipUnavailable},
		"a path with no membership":     {ctx: context.Background(), user: "alice", org: "acme", want: ErrMembershipUnavailable},
		"a credential bound to the org": {ctx: WithCredentialOrg(member, "acme"), user: "alice", org: "acme"},
		"a credential bound elsewhere":  {ctx: WithCredentialOrg(member, "globex"), user: "alice", org: "acme", want: ErrDenied, wantRefusedT: "acme"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := OrgMemberDenial(tc.ctx, tc.user, tc.org); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("OrgMemberDenial = %v, want %v", err, tc.want)
			}
			if tc.wantRefusedT != "" {
				if _, refused, ok := CredentialRefusal(tc.ctx); !ok || refused != tc.wantRefusedT {
					t.Fatalf("CredentialRefusal = %q, %v; want %q recorded", refused, ok, tc.wantRefusedT)
				}
			}
		})
	}
}
