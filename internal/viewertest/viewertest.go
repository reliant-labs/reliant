// Copyright (c) 2025 Reliant Labs

// Package viewertest is the shared fixture of the viewer matrix (read-only
// sharing; control-plane docs/design/org-sharing.md §4, §10): a project and a
// chat that their owner shared with their org, seeded straight into the
// database, and the three callers every sharing test asks as, each on the
// context a request through the API listener carries (its user, the org
// membership authz asks, and a person's credential):
//
//   - Owner: the chat's and project's creator, a live member of the org.
//   - Viewer: another live member of the same org.
//   - Outsider: a member of another org only.
//
// Run asks each rpc as all three and asserts the answer the matrix expects:
// the owner is answered, the outsider gets NotFound, and the viewer gets
// either (Viewer). A row in the table is the decision that rpc makes for a
// viewer; changing it is a deliberate, reviewed act.
//
//forge:exclude-contract: test fixtures for handler packages' tests; no service, nothing to mock
package viewertest

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
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

	OwnerID, ViewerID, OutsiderID string
	ProjectID, ChatID             string

	// Owner, Viewer and Outsider are request contexts for the three callers.
	Owner, Viewer, Outsider context.Context
}

// members answers control-plane's membership question for the fixture:
// owner and viewer are members of OrgID (owner's default), outsider of
// OtherOrgID only.
type members struct{ owner, viewer, outsider string }

func (m members) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	home := map[string]string{m.owner: OrgID, m.viewer: OrgID, m.outsider: OtherOrgID}[userID]
	if orgID == "" {
		orgID = home
	}
	if home == "" || orgID != home {
		return accesstokenclient.OrgMembership{OrgID: orgID}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: true}, nil
}

// New seeds repo with a project and a chat in it, both owned by the fixture's
// owner in OrgID and both shared with the org (visibility 'org', written
// directly, as no rpc of the head under test may), and returns the callers.
func New(t testing.TB, repo *db.Repo) *Fixture {
	t.Helper()
	suffix := uuid.NewString()[:8]
	f := &Fixture{
		Repo:       repo,
		OwnerID:    "owner-" + suffix,
		ViewerID:   "viewer-" + suffix,
		OutsiderID: "outsider-" + suffix,
		ProjectID:  "shared-project-" + suffix,
		ChatID:     "shared-chat-" + suffix,
	}
	f.Membership = orgs.New(members{owner: f.OwnerID, viewer: f.ViewerID, outsider: f.OutsiderID})
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	org := OrgID
	if err := repo.CreateProject(ctx, &db.Project{
		ID: f.ProjectID, UserID: f.OwnerID, OrgID: &org, Name: "Shared", Path: "/home/owner/shared",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("viewertest: project: %v", err)
	}
	if err := repo.CreateChat(ctx, &db.Chat{
		ID: f.ChatID, UserID: f.OwnerID, OrgID: &org, ProjectID: f.ProjectID, Title: "Shared chat",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("viewertest: chat: %v", err)
	}
	for _, share := range []struct{ stmt, id string }{
		{`UPDATE projects SET visibility = 'org', shared_at = NOW() WHERE id = ?`, f.ProjectID},
		{`UPDATE chats SET visibility = 'org', shared_at = NOW() WHERE id = ?`, f.ChatID},
	} {
		if _, err := repo.ORM().Bun().NewRaw(share.stmt, share.id).Exec(ctx); err != nil {
			t.Fatalf("viewertest: sharing: %v", err)
		}
	}
	f.Owner = f.Context(f.OwnerID)
	f.Viewer = f.Context(f.ViewerID)
	f.Outsider = f.Context(f.OutsiderID)
	return f
}

// Context is a request context for userID: what the API listener's edge
// installs (the user, the org membership, a person's app session).
func (f *Fixture) Context(userID string) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	ctx = authz.WithMembership(ctx, f.Membership)
	return auth.WithCredential(ctx, auth.CredentialSession)
}

// Viewer is what an rpc answers a viewer of the shared rows.
type Viewer int

const (
	// Refused: NotFound, exactly as an outsider. Every write, and every read
	// site that does not serve viewers.
	Refused Viewer = iota
	// Answered: the read-only answer the owner gets, shaped for a viewer.
	Answered
)

func (v Viewer) String() string {
	if v == Answered {
		return "answered"
	}
	return "refused"
}

// Case is one rpc asked as each caller.
type Case struct {
	Name   string
	Viewer Viewer
	// Call makes the request on ctx and returns its error.
	Call func(ctx context.Context) error
}

// Run asks every case as the owner (answered), the outsider (NotFound) and
// the viewer (as the case says).
func Run(t *testing.T, f *Fixture, cases []Case) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if err := c.Call(f.Owner); err != nil {
				t.Fatalf("owner: %v", err)
			}
			if err := c.Call(f.Outsider); svcerr.Code(err) != connect.CodeNotFound {
				t.Fatalf("outsider: %v (code %v), want NotFound", err, svcerr.Code(err))
			}
			err := c.Call(f.Viewer)
			switch c.Viewer {
			case Answered:
				if err != nil {
					t.Fatalf("viewer: %v, want the read-only answer", err)
				}
			default:
				if svcerr.Code(err) != connect.CodeNotFound {
					t.Fatalf("viewer: %v (code %v), want NotFound like an outsider", err, svcerr.Code(err))
				}
			}
		})
	}
}
