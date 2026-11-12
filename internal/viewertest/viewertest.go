// Copyright (c) 2025 Reliant Labs

// Package viewertest is the shared fixture of the viewer matrix (read-only
// sharing; control-plane docs/design/org-sharing.md §4, §10): a project and a
// chat with a transcript that their owner shared with their org, seeded
// straight into the database, and the three callers every sharing test asks
// as, each on the context a request through the API listener carries (its
// user, the org membership authz asks, and a person's app session):
//
//   - Owner: the chat's and project's creator, a live member of the org.
//   - Viewer: another live member of the same org.
//   - Outsider: a member of another org only.
//   - Admin: an admin of the org (for the admin-only actions).
//
// Matrix (matrix.go) says what every rpc taking a chat or project answers a
// viewer; RunService asks each of a service's rpcs as all three callers and
// holds it to that.
//
//forge:exclude-contract: test fixtures for handler packages' tests; no service, nothing to mock
package viewertest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/dbtest"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// The org the fixture's rows belong to, and another one.
const (
	OrgID      = "acme"
	OtherOrgID = "globex"
)

// Fixture is a shared project and chat and the callers that read them.
type Fixture struct {
	Repo *db.Repo
	// Membership is what every caller's context asks (a real resolver over
	// a fake control plane).
	Membership *orgs.Resolver

	OwnerID, ViewerID, OutsiderID, AdminID string
	// ProjectID is shared; ChatID, in it, is shared and holds a transcript
	// (Transcript); PrivateChatID is in the same project and not shared.
	ProjectID, ChatID, PrivateChatID string
	// PlanID is a plan in the shared chat.
	PlanID     string
	Transcript dbtest.TranscriptBodies
	// Hidden is a hidden message in the shared chat (SeedHiddenMessage):
	// RunService holds every viewer answer to never carrying its Secret.
	Hidden HiddenMessage

	// Owner, Viewer, Outsider and Admin are request contexts for the callers.
	Owner, Viewer, Outsider, Admin context.Context
}

// members answers control-plane's membership question for the fixture:
// owner, viewer and admin are members of OrgID (their default), outsider of
// OtherOrgID only. The admin's role is admin; everyone else's is member.
type members struct{ owner, viewer, outsider, admin string }

func (m members) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	home := map[string]string{m.owner: OrgID, m.viewer: OrgID, m.admin: OrgID, m.outsider: OtherOrgID}[userID]
	if orgID == "" {
		orgID = home
	}
	if home == "" || orgID != home {
		return accesstokenclient.OrgMembership{OrgID: orgID}, nil
	}
	role := orgs.RoleMember
	if userID == m.admin {
		role = orgs.RoleAdmin
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: true, Role: role}, nil
}

// New seeds repo with the fixture: a project and, in it, a chat with a
// transcript (dbtest.SeedTranscriptBodies) and a plan, both owned by the
// owner in OrgID and both shared with the org (visibility 'org', written
// directly), plus a private chat in the same project; and returns the
// callers.
func New(t *testing.T, repo *db.Repo) *Fixture {
	t.Helper()
	suffix := uuid.NewString()[:8]
	f := &Fixture{
		Repo:          repo,
		OwnerID:       "owner-" + suffix,
		ViewerID:      "viewer-" + suffix,
		OutsiderID:    "outsider-" + suffix,
		AdminID:       "admin-" + suffix,
		ProjectID:     "shared-project-" + suffix,
		PrivateChatID: "private-chat-" + suffix,
		PlanID:        "shared-plan-" + suffix,
	}
	f.Membership = orgs.New(members{owner: f.OwnerID, viewer: f.ViewerID, outsider: f.OutsiderID, admin: f.AdminID})
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	org := OrgID
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("viewertest: %s: %v", what, err)
		}
	}
	must("project", repo.CreateProject(ctx, &db.Project{
		ID: f.ProjectID, UserID: f.OwnerID, OrgID: &org, Name: "Shared", Path: "/home/owner/shared",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	f.Transcript = dbtest.SeedTranscriptBodies(t, context.WithValue(ctx, auth.UserIDContextKey, f.OwnerID), repo, f.OwnerID)
	f.ChatID = f.Transcript.ChatID
	must("private chat", repo.CreateChat(ctx, &db.Chat{
		ID: f.PrivateChatID, UserID: f.OwnerID, OrgID: &org, ProjectID: f.ProjectID, Title: "Private chat",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	must("plan", repo.CreatePlan(ctx, &db.Plan{
		ID: f.PlanID, ThreadID: f.Transcript.ThreadID, Title: "Shared plan", CreatedAt: now, UpdatedAt: now,
	}))
	for _, share := range []struct {
		stmt string
		args []any
	}{
		{`UPDATE projects SET visibility = 'org', shared_at = NOW() WHERE id = ?`, []any{f.ProjectID}},
		{`UPDATE chats SET org_id = ?, project_id = ?, visibility = 'org', shared_at = NOW() WHERE id = ?`, []any{OrgID, f.ProjectID, f.ChatID}},
	} {
		_, err := repo.ORM().Bun().NewRaw(share.stmt, share.args...).Exec(ctx)
		must("sharing", err)
	}
	f.Hidden = f.SeedHiddenMessage(t)
	f.Owner = f.Context(f.OwnerID)
	f.Viewer = f.Context(f.ViewerID)
	f.Outsider = f.Context(f.OutsiderID)
	f.Admin = f.Context(f.AdminID)
	return f
}

// Context is a request context for userID: what the API listener's edge
// installs (the user, the org membership, a person's app session).
func (f *Fixture) Context(userID string) context.Context {
	return f.ContextAs(userID, auth.CredentialSession)
}

// ContextAs is Context through credential c (auth.CredentialAccessToken: an
// agent's CLI login).
func (f *Fixture) ContextAs(userID string, c auth.Credential) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	ctx = authz.WithMembership(ctx, f.Membership)
	return auth.WithCredential(ctx, c)
}
