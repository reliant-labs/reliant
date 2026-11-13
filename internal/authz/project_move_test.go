// Copyright (c) 2025 Reliant Labs

package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// moveMembers answers membership, roles and default orgs from tables, or
// fails every question with err.
type moveMembers struct {
	roles    map[string]map[string]string // user → org → owner|admin|member; a role is live membership
	defaults map[string]string
	err      error
}

func (m moveMembers) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	if m.err != nil {
		return false, true, m.err
	}
	_, ok := m.roles[userID][orgID]
	return ok, true, nil
}

func (m moveMembers) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	if m.err != nil {
		return "", false, m.err
	}
	return m.roles[userID][orgID], true, nil
}

func (m moveMembers) DefaultOrg(_ context.Context, userID string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.defaults[userID], nil
}

// The authority table of a project move (control-plane
// docs/design/org-project-move.md §4.1): {owner admin, owner member-only,
// non-owner admin, machine credential, removed member, control-plane outage}
// against the source and the target.
func TestProjectOrgMove(t *testing.T) {
	m := moveMembers{
		roles: map[string]map[string]string{
			// olive owns her project and administers acme; she is a member
			// of globex and was removed from initech.
			"olive": {"acme": "admin", "globex": "member"},
			// mia owns a project in acme as a plain member.
			"mia": {"acme": "member", "globex": "member"},
			// adam administers acme but owns neither project.
			"adam": {"acme": "owner", "globex": "admin"},
			// rita was removed from acme, where her project still is.
			"rita": {"globex": "admin"},
		},
		defaults: map[string]string{"olive": "acme", "nora": ""},
	}
	acme := "acme"
	project := func(owner string, org *string) *core.Project {
		return &core.Project{ID: "p-" + owner, UserID: owner, OrgID: org}
	}
	ctx := person(WithMembership(context.Background(), m))
	cases := []struct {
		name    string
		ctx     context.Context
		user    string
		project *core.Project
		to      string
		from    string
		want    error
	}{
		{"the owner, an admin of the source, a member of the target", ctx, "olive", project("olive", &acme), "globex", "acme", nil},
		{"the owner, a member only of the source", ctx, "mia", project("mia", &acme), "globex", "", ErrNotOrgAdmin},
		{"the owner, not a member of the target", ctx, "olive", project("olive", &acme), "initech", "", ErrNotTargetMember},
		{"the owner, the target is the source", ctx, "olive", project("olive", &acme), "acme", "", ErrMoveSameOrg},
		{"an admin of the source who does not own it", ctx, "adam", project("olive", &acme), "globex", "", ErrDenied},
		{"the owner, through a machine credential", WithCredentialKind(WithMembership(context.Background(), m), CredentialKindSoftware), "olive", project("olive", &acme), "globex", "", ErrPersonRequired},
		{"the owner, through an unclassified credential", WithMembership(context.Background(), m), "olive", project("olive", &acme), "globex", "", ErrPersonRequired},
		{"the owner, removed from the source", ctx, "rita", project("rita", &acme), "globex", "", ErrNotOrgMember},
		{"control-plane is down", person(WithMembership(context.Background(), moveMembers{err: errors.New("down")})), "olive", project("olive", &acme), "globex", "", ErrMembershipUnavailable},
		{"no membership wired", person(context.Background()), "olive", project("olive", &acme), "globex", "", ErrMembershipUnavailable},
		{"a project not yet assigned an org is in the owner's default", ctx, "olive", project("olive", nil), "globex", "acme", nil},
		{"the owner has no default org", ctx, "nora", project("nora", nil), "globex", "", ErrMoveNeedsOrgs},
		{"a deployment with no orgs", person(WithMembership(context.Background(), NoOrgs{})), "olive", project("olive", nil), "globex", "", ErrMoveNeedsOrgs},
		{"no project", ctx, "olive", nil, "globex", "", ErrDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, err := ProjectOrgMove(tc.ctx, tc.user, tc.project, tc.to)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("ProjectOrgMove = %q, %v; want %q, %v", from, err, tc.from, tc.want)
			}
			if from != tc.from {
				t.Fatalf("ProjectOrgMove from = %q, want %q", from, tc.from)
			}
		})
	}
}
