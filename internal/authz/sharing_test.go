// Copyright (c) 2025 Reliant Labs

package authz

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
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

func chatDecisions(ctx context.Context, caller string, shared bool) []decisions {
	vis := core.VisibilityPrivate
	if shared {
		vis = core.VisibilityOrg
	}
	acme := "acme"
	c := &core.Chat{UserID: "bob", OrgID: &acme, Visibility: vis}
	r := &core.ChatRow{UserID: "bob", OrgID: &acme, Visibility: vis}
	return []decisions{
		{CanReadChat(ctx, caller, c), CanWriteChat(ctx, caller, c), IsViewer(ctx, caller, c)},
		{CanReadChat(ctx, caller, r), CanWriteChat(ctx, caller, r), IsViewer(ctx, caller, r)},
	}
}

func projectDecisions(ctx context.Context, caller string, shared bool) []decisions {
	vis := core.VisibilityPrivate
	if shared {
		vis = core.VisibilityOrg
	}
	acme := "acme"
	p := &core.Project{UserID: "bob", OrgID: &acme, Visibility: vis}
	return []decisions{{CanReadProject(ctx, caller, p), CanWriteProject(ctx, caller, p), IsProjectViewer(ctx, caller, p)}}
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
			for kind, decide := range map[string]func(context.Context, string, bool) []decisions{"chat": chatDecisions, "project": projectDecisions} {
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
	if CanReadChat(ctx, "alice", c) || IsViewer(ctx, "alice", c) || !CanReadChat(ctx, "bob", c) {
		t.Fatal("an org-less shared chat must read as the owner's alone")
	}
	unknown := &core.Chat{UserID: "bob", OrgID: strp("acme"), Visibility: "members"}
	if CanReadChat(ctx, "alice", unknown) {
		t.Fatal("a visibility this build does not know must not admit a viewer")
	}
}

func strp(s string) *string { return &s }

// Every kind, any visibility, any actor: CanWrite* allows only the owner.
// Generated over random rows and actors so a future visibility, kind or
// credential cannot open a write path without failing here.
func TestCanWriteImpliesOwner(t *testing.T) {
	rng := rand.New(rand.NewSource(20261112))
	users := []string{"", "bob", "alice", "olga", "remi", "Bob"}
	orgs := []*string{nil, strp("acme"), strp("globex")}
	vis := []core.Visibility{core.VisibilityPrivate, core.VisibilityOrg, "members", "public"}
	ctxs := []context.Context{
		context.Background(),
		person(WithMembership(context.Background(), live)),
		WithMembership(context.Background(), live),
		WithCredentialOrg(person(WithMembership(context.Background(), live)), "acme"),
		person(WithMembership(context.Background(), NoOrgs{})),
		person(WithMembership(context.Background(), failingMembership{})),
		person(WithMembership(context.Background(), allMembers{})),
	}
	pick := func(n int) int { return rng.Intn(n) }
	for i := 0; i < 5000; i++ {
		owner, caller := users[pick(len(users))], users[pick(len(users))]
		org, v, ctx := orgs[pick(len(orgs))], vis[pick(len(vis))], ctxs[pick(len(ctxs))]
		writes := map[string]bool{
			"chat":     CanWriteChat(ctx, caller, &core.Chat{UserID: owner, OrgID: org, Visibility: v}),
			"chat row": CanWriteChat(ctx, caller, &core.ChatRow{UserID: owner, OrgID: org, Visibility: v}),
			"project":  CanWriteProject(ctx, caller, &core.Project{UserID: owner, OrgID: org, Visibility: v}),
			"workflow": CanWriteWorkflow(ctx, caller, &core.WorkflowDraft{UserID: owner, OrgID: org, Visibility: v}),
			"trigger":  CanWriteTrigger(ctx, caller, &core.Trigger{UserID: owner, OrgID: org}),
			"machine":  CanWriteMachine(ctx, caller, &core.Daemon{UserID: owner}),
			"personal": CanAccessPersonal(caller, owner),
		}
		for kind, ok := range writes {
			if ok && (caller == "" || caller != owner) {
				t.Fatalf("%s: CanWrite allowed %q on %q's row (org %v, visibility %q)", kind, caller, owner, org, v)
			}
		}
		// The reads sharing does not widen stay owner-only too.
		for kind, ok := range map[string]bool{
			"workflow read": CanReadWorkflow(ctx, caller, &core.WorkflowDraft{UserID: owner, OrgID: org, Visibility: v}),
			"trigger read":  CanReadTrigger(ctx, caller, &core.Trigger{UserID: owner, OrgID: org}),
			"machine read":  CanReadMachine(ctx, caller, &core.Daemon{UserID: owner}),
		} {
			if ok && caller != owner {
				t.Fatalf("%s: allowed %q on %q's row", kind, caller, owner)
			}
		}
	}
}

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
	if CanReadChat(ctx, "alice", c) || IsViewer(ctx, "alice", c) || CanReadProject(ctx, "alice", p) || IsProjectViewer(ctx, "alice", p) {
		t.Fatal("a site that does not serve viewers admitted one")
	}
	if !CanReadChat(ctx, "bob", c) || !CanReadProject(ctx, "bob", p) {
		t.Fatal("the owner must read at any site")
	}
	if !CanReadChat(WithViewerReads(ctx), "alice", c) || !CanReadProject(WithViewerReads(ctx), "alice", p) {
		t.Fatal("a viewer-serving site must admit the viewer")
	}
}
