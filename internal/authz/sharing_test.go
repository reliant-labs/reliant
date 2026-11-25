// Copyright (c) 2025 Reliant Labs

package authz

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Read-only sharing (control-plane docs/design/org-sharing.md §3, PR S2).
// members (membership_test.go): alice is a live member of acme only. Here bob
// owns the rows; alice is the would-be viewer.

type shareMembers map[string]map[string]bool

func (m shareMembers) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return m[userID][orgID], true, nil
}

type failingMembership struct{}

func (failingMembership) IsMember(context.Context, string, string) (bool, bool, error) {
	return false, true, errors.New("control-plane down")
}

// live: bob and alice are members of acme; rémi was removed (a member of
// nothing); globex is another org.
var live = shareMembers{"bob": {"acme": true}, "alice": {"acme": true}, "olga": {"globex": true}}

// person is a viewer-serving read site acting for a person: the context every
// viewer read is decided on.
func person(ctx context.Context) context.Context {
	return WithViewerReads(WithCredentialKind(ctx, CredentialKindPerson))
}

// decisions is every decision the sharing table pins, for one row kind.
type decisions struct{ read, write, viewer bool }

// viewerOf is a read decision's "reads as a viewer": allowed, and not the
// owner's.
func viewerOf(a Access, err error) bool { return err == nil && a == AccessViewer }

func chatDecisions(ctx context.Context, caller string, shared bool) []decisions {
	vis := core.VisibilityPrivate
	if shared {
		vis = core.VisibilityOrg
	}
	acme := "acme"
	c := &core.Chat{UserID: "bob", OrgID: &acme, Visibility: vis}
	r := &core.ChatRow{UserID: "bob", OrgID: &acme, Visibility: vis}
	return []decisions{
		{CanReadChat(ctx, caller, c), CanOwnChat(ctx, caller, c), viewerOf(ReadChatAccess(ctx, caller, c))},
		{CanReadChat(ctx, caller, r), CanOwnChat(ctx, caller, r), viewerOf(ReadChatAccess(ctx, caller, r))},
	}
}

func projectDecisions(ctx context.Context, caller string, shared bool) []decisions {
	vis := core.VisibilityPrivate
	if shared {
		vis = core.VisibilityOrg
	}
	acme := "acme"
	p := &core.Project{UserID: "bob", OrgID: &acme, Visibility: vis}
	return []decisions{{CanReadProject(ctx, caller, p), CanWriteProject(ctx, caller, p), viewerOf(ReadProjectAccess(ctx, caller, p))}}
}

// workflowDecisions reads a saved workflow through ReadWorkflowAccess, the
// one read that serves viewers (CanReadWorkflow stays owner-only:
// TestCanWriteImpliesOwner, TestWorkflowReadsStayOwnerOnly).
func workflowDecisions(ctx context.Context, caller string, shared bool) []decisions {
	vis := core.VisibilityPrivate
	if shared {
		vis = core.VisibilityOrg
	}
	w := &core.WorkflowDraft{UserID: "bob", OrgID: strp("acme"), Visibility: vis}
	access, err := ReadWorkflowAccess(ctx, caller, w)
	return []decisions{{err == nil, CanWriteWorkflow(ctx, caller, w), viewerOf(access, err)}}
}

// The viewer table: {owner, viewer, non-member, removed member, machine-bound,
// other-org credential, membership outage, no Membership wired} x {private,
// shared}. Only a live member with a person credential reads a shared row;
// nobody but the owner ever writes; the owner is never a "viewer".
func TestSharingDecisionTable(t *testing.T) {
	base := context.Background()
	cases := []struct {
		name   string
		ctx    context.Context
		caller string
		// private and shared are the (read, write, viewer) answers.
		private, shared decisions
	}{
		{"owner", person(WithMembership(base, live)), "bob", decisions{true, true, false}, decisions{true, true, false}},
		{"owner with a software credential", WithMembership(base, live), "bob", decisions{true, true, false}, decisions{true, true, false}},
		{"viewer: live member, person", person(WithMembership(base, live)), "alice", decisions{}, decisions{true, false, true}},
		{"non-member", person(WithMembership(base, live)), "olga", decisions{}, decisions{}},
		{"removed member", person(WithMembership(base, live)), "remi", decisions{}, decisions{}},
		{"live member, software credential (an agent's CLI login)", WithViewerReads(WithCredentialKind(WithMembership(base, live), CredentialKindSoftware)), "alice", decisions{}, decisions{}},
		{"live member, unclassified credential", WithViewerReads(WithMembership(base, live)), "alice", decisions{}, decisions{}},
		{"live member, machine-bound token of the org", WithViewerReads(WithCredentialOrg(WithCredentialKind(WithMembership(base, live), CredentialKindSoftware), "acme")), "alice", decisions{}, decisions{}},
		{"live member, credential bound to another org", WithCredentialOrg(person(WithMembership(base, live)), "globex"), "alice", decisions{}, decisions{}},
		{"live member, credential bound to the row's org", WithCredentialOrg(person(WithMembership(base, live)), "acme"), "alice", decisions{}, decisions{true, false, true}},
		{"membership outage", person(WithMembership(base, failingMembership{})), "alice", decisions{}, decisions{}},
		{"no Membership wired", person(base), "alice", decisions{}, decisions{}},
		{"a deployment with no orgs", person(WithMembership(base, NoOrgs{})), "alice", decisions{}, decisions{}},
		{"no caller", person(WithMembership(base, live)), "", decisions{}, decisions{}},
		{"live member, person, at a site that does not serve viewers", WithCredentialKind(WithMembership(base, live), CredentialKindPerson), "alice", decisions{}, decisions{}},
	}
	for _, tc := range cases {
		for _, shared := range []bool{false, true} {
			want := tc.private
			if shared {
				want = tc.shared
			}
			for kind, decide := range map[string]func(context.Context, string, bool) []decisions{"chat": chatDecisions, "project": projectDecisions, "workflow": workflowDecisions} {
				t.Run(fmt.Sprintf("%s/%s/shared=%v", tc.name, kind, shared), func(t *testing.T) {
					for i, got := range decide(tc.ctx, tc.caller, shared) {
						if got != want {
							t.Errorf("read %d: got %+v, want %+v", i, got, want)
						}
					}
				})
			}
		}
	}
}

// A shared row without an org (the database refuses one, but a decision never
// trusts that) has no audience: owner only.
func TestSharedRowWithoutAnOrgIsOwnerOnly(t *testing.T) {
	ctx := person(WithMembership(context.Background(), live))
	c := &core.Chat{UserID: "bob", Visibility: core.VisibilityOrg}
	if CanReadChat(ctx, "alice", c) || viewerOf(ReadChatAccess(ctx, "alice", c)) || !CanReadChat(ctx, "bob", c) {
		t.Fatal("an org-less shared chat must read as the owner's alone")
	}
	unknown := &core.Chat{UserID: "bob", OrgID: strp("acme"), Visibility: "members"}
	if CanReadChat(ctx, "alice", unknown) {
		t.Fatal("a visibility this build does not know must not admit a viewer")
	}
}

func strp(s string) *string { return &s }

// allMembers makes everyone a member of every org: the widest membership a
// write could ever be decided against.
type allMembers struct{}

func (allMembers) IsMember(context.Context, string, string) (bool, bool, error) {
	return true, true, nil
}

// A shared read decided with no Membership wired fails closed and logs at
// ERROR, as the owner path does: it means a path was never wired.
func TestSharedReadWithoutMembershipLogsError(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	c := &core.Chat{UserID: "bob", OrgID: strp("acme"), Visibility: core.VisibilityOrg}
	if CanReadChat(person(context.Background()), "alice", c) {
		t.Fatal("a shared read with no membership was allowed")
	}
	if out := buf.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "denying a shared read") {
		t.Fatalf("log = %q, want an ERROR naming the shared read", out)
	}
	buf.Reset()
	if CanReadChat(person(WithMembership(context.Background(), failingMembership{})), "alice", c) {
		t.Fatal("a shared read during an outage was allowed")
	}
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "could not check org membership") {
		t.Fatalf("log = %q, want the outage WARN", out)
	}
}

// A shared read refused to a credential bound to another org is reported,
// like the owner path's refusal, so the edge can say why.
func TestSharedReadRecordsCredentialRefusal(t *testing.T) {
	ctx := WithCredentialOrg(person(WithMembership(context.Background(), live)), "globex")
	_ = CanReadChat(ctx, "alice", &core.Chat{UserID: "bob", OrgID: strp("acme"), Visibility: core.VisibilityOrg})
	if bound, refused, ok := CredentialRefusal(ctx); !ok || bound != "globex" || refused != "acme" {
		t.Fatalf("CredentialRefusal = %q, %q, %v", bound, refused, ok)
	}
}

// Visible over a shared list keeps what a viewer may read, membership asked
// once per org (the resolver caches it).
func TestVisibleKeepsSharedRowsForAViewer(t *testing.T) {
	ctx := person(WithMembership(context.Background(), live))
	rows := []*core.Chat{
		{ID: "shared-acme", UserID: "bob", OrgID: strp("acme"), Visibility: core.VisibilityOrg},
		{ID: "private-acme", UserID: "bob", OrgID: strp("acme")},
		{ID: "shared-globex", UserID: "olga", OrgID: strp("globex"), Visibility: core.VisibilityOrg},
	}
	got := Visible(ctx, "alice", rows, CanReadChat[*core.Chat])
	if len(got) != 1 || got[0].ID != "shared-acme" {
		t.Fatalf("Visible = %v", got)
	}
}

// The display accessors report the stored visibility, nil-safe.
func TestVisibilityAccessors(t *testing.T) {
	if ChatVisibility(&core.Chat{Visibility: core.VisibilityOrg}) != core.VisibilityOrg ||
		ChatVisibility((*core.ChatRow)(nil)) != core.VisibilityPrivate ||
		ProjectVisibility(&core.Project{Visibility: core.VisibilityOrg}) != core.VisibilityOrg ||
		ProjectVisibility(nil) != core.VisibilityPrivate {
		t.Fatal("visibility accessors")
	}
}

// A read site that has not opted in to serving viewers (WithViewerReads)
// keeps the owner rule for a shared row, whoever asks: an un-migrated read
// cannot hand a viewer the owner's unredacted shape.
func TestSharedReadNeedsAViewerServingSite(t *testing.T) {
	ctx := WithCredentialKind(WithMembership(context.Background(), live), CredentialKindPerson)
	c := &core.Chat{UserID: "bob", OrgID: strp("acme"), Visibility: core.VisibilityOrg}
	p := &core.Project{UserID: "bob", OrgID: strp("acme"), Visibility: core.VisibilityOrg}
	if CanReadChat(ctx, "alice", c) || viewerOf(ReadChatAccess(ctx, "alice", c)) || CanReadProject(ctx, "alice", p) || viewerOf(ReadProjectAccess(ctx, "alice", p)) {
		t.Fatal("a site that does not serve viewers admitted one")
	}
	if !CanReadChat(ctx, "bob", c) || !CanReadProject(ctx, "bob", p) {
		t.Fatal("the owner must read at any site")
	}
	if !CanReadChat(WithViewerReads(ctx), "alice", c) || !CanReadProject(WithViewerReads(ctx), "alice", p) {
		t.Fatal("a viewer-serving site must admit the viewer")
	}
}

// ReadChatAccess / ReadProjectAccess are the read decision with its shape: the
// owner's access, a viewer's, or a refusal. Access's zero value is shaped as a
// viewer's, so a site that forgot to set it fails closed.
func TestReadAccess(t *testing.T) {
	ctx := WithViewerReads(person(WithMembership(context.Background(), live)))
	c := &core.Chat{UserID: "bob", OrgID: strp("acme"), Visibility: core.VisibilityOrg}
	p := &core.Project{UserID: "bob", OrgID: strp("acme"), Visibility: core.VisibilityOrg}
	for _, tc := range []struct {
		caller string
		want   Access
	}{{"bob", AccessOwner}, {"alice", AccessViewer}} {
		if got, err := ReadChatAccess(ctx, tc.caller, c); err != nil || got != tc.want {
			t.Errorf("chat, %s: %v %v, want %v", tc.caller, got, err, tc.want)
		}
		if got, err := ReadProjectAccess(ctx, tc.caller, p); err != nil || got != tc.want {
			t.Errorf("project, %s: %v %v, want %v", tc.caller, got, err, tc.want)
		}
	}
	private := &core.Chat{UserID: "bob", OrgID: strp("acme"), Visibility: core.VisibilityPrivate}
	if got, err := ReadChatAccess(ctx, "alice", private); err == nil || got != AccessNone {
		t.Errorf("a private chat read by another: %v %v, want a refusal", got, err)
	}
	if !AccessNone.Viewer() || !AccessViewer.Viewer() || AccessOwner.Viewer() {
		t.Error("only the owner's access is shaped as the owner's")
	}
}

// sharedWorkflowMembers: bob owns, alice and adam are live members of acme
// (adam its admin), olga is in globex; roles are answered fresh.
type sharedWorkflowMembers struct{}

func (sharedWorkflowMembers) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return live[userID][orgID] || (userID == "adam" && orgID == "acme"), true, nil
}

func (sharedWorkflowMembers) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	switch {
	case userID == "adam" && orgID == "acme":
		return "admin", true, nil
	case live[userID][orgID]:
		return "member", true, nil
	}
	return "", true, nil
}

// Sharing a saved workflow widens no workflow read but ReadWorkflowAccess:
// CanReadWorkflow (every lookup by id, slug or name, and the launch path)
// stays the owner's even on a viewer-serving site, so a teammate can read a
// shared definition and never run it as its owner.
func TestWorkflowReadsStayOwnerOnly(t *testing.T) {
	ctx := person(WithMembership(context.Background(), live))
	w := &core.WorkflowDraft{ID: "wf", UserID: "bob", OrgID: strp("acme"), Visibility: core.VisibilityOrg}
	if CanReadWorkflow(ctx, "alice", w) || CanWriteWorkflow(ctx, "alice", w) {
		t.Fatal("a shared saved workflow widened CanReadWorkflow / CanWriteWorkflow")
	}
	if !viewerOf(ReadWorkflowAccess(ctx, "alice", w)) {
		t.Fatal("a live member at a viewer-serving site must read a shared saved workflow as a viewer")
	}
	if got, err := ReadWorkflowAccess(ctx, "bob", w); err != nil || got != AccessOwner {
		t.Fatalf("owner: %v %v", got, err)
	}
	notServing := WithCredentialKind(WithMembership(context.Background(), live), CredentialKindPerson)
	if _, err := ReadWorkflowAccess(notServing, "alice", w); err == nil {
		t.Fatal("a site that does not serve viewers admitted one")
	}
	rows := []*core.WorkflowDraft{
		w,
		{ID: "private", UserID: "bob", OrgID: strp("acme")},
		{ID: "globex", UserID: "olga", OrgID: strp("globex"), Visibility: core.VisibilityOrg},
		{ID: "own", UserID: "alice", OrgID: strp("acme"), Visibility: core.VisibilityOrg},
	}
	if got := SharedWorkflowDraftsFor(ctx, "alice", rows); len(got) != 1 || got[0].ID != "wf" {
		t.Fatalf("SharedWorkflowDraftsFor = %v", got)
	}
	if WorkflowVisibility(w) != core.VisibilityOrg || WorkflowVisibility(nil) != core.VisibilityPrivate {
		t.Fatal("WorkflowVisibility")
	}
}

// WorkflowShareChange follows ChatShareChange's rules: the owner shares as a
// person in an org and unshares with any credential; a viewer never shares;
// an admin of the org makes a member's shared one private; a non-reader is
// ErrDenied.
func TestWorkflowShareChange(t *testing.T) {
	m := sharedWorkflowMembers{}
	personCtx := person(WithMembership(context.Background(), m))
	software := WithViewerReads(WithCredentialKind(WithMembership(context.Background(), m), CredentialKindSoftware))
	private := &core.WorkflowDraft{ID: "wf", UserID: "bob", OrgID: strp("acme")}
	shared := &core.WorkflowDraft{ID: "wf", UserID: "bob", OrgID: strp("acme"), Visibility: core.VisibilityOrg}
	orgless := &core.WorkflowDraft{ID: "wf", UserID: "bob"}
	cases := []struct {
		name   string
		ctx    context.Context
		caller string
		w      *core.WorkflowDraft
		to     core.Visibility
		want   error
		action core.ShareAction
	}{
		{"owner shares", personCtx, "bob", private, core.VisibilityOrg, nil, core.ShareActionShare},
		{"owner unshares", personCtx, "bob", shared, core.VisibilityPrivate, nil, core.ShareActionUnshare},
		{"owner unshares through software", software, "bob", shared, core.VisibilityPrivate, nil, core.ShareActionUnshare},
		{"owner shares through software", software, "bob", private, core.VisibilityOrg, ErrPersonRequired, ""},
		{"owner shares an org-less one", personCtx, "bob", orgless, core.VisibilityOrg, ErrShareNeedsOrg, ""},
		{"viewer shares", personCtx, "alice", shared, core.VisibilityOrg, ErrOnlyOwnerShares, ""},
		{"viewer unshares", personCtx, "alice", shared, core.VisibilityPrivate, ErrNotOrgAdmin, ""},
		{"admin unshares", personCtx, "adam", shared, core.VisibilityPrivate, nil, core.ShareActionAdminUnshare},
		{"admin shares", personCtx, "adam", shared, core.VisibilityOrg, ErrOnlyOwnerShares, ""},
		{"admin of a private one", personCtx, "adam", private, core.VisibilityPrivate, ErrDenied, ""},
		{"admin through software", software, "adam", shared, core.VisibilityPrivate, ErrDenied, ""},
		{"outsider", personCtx, "olga", shared, core.VisibilityPrivate, ErrDenied, ""},
		{"no such workflow", personCtx, "bob", nil, core.VisibilityOrg, ErrDenied, ""},
		{"unknown visibility", personCtx, "bob", shared, "members", ErrInvalidVisibility, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := WorkflowShareChange(tc.ctx, tc.caller, tc.w, tc.to)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want != nil {
				return
			}
			want := core.ShareChange{Kind: core.ShareKindWorkflow, ResourceID: "wf", OrgID: "acme", OwnerUserID: "bob", ActorUserID: tc.caller, Action: tc.action}
			if c != want {
				t.Fatalf("change = %+v, want %+v", c, want)
			}
		})
	}
}
