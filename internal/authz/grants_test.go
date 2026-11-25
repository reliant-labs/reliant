// Copyright (c) 2025 Reliant Labs

package authz

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Chat members (control-plane docs/design/chat-collaboration.md §4, PR C2).
// Here bob owns every chat, in acme. In live (sharing_test.go) alice and bob
// are members of acme and olga of globex; this file's members add vic, rita
// and max to acme. remi is a member of nothing (removed).

var crew = shareMembers{
	"bob": {"acme": true}, "alice": {"acme": true}, "olga": {"globex": true},
	"vic": {"acme": true}, "rita": {"acme": true}, "max": {"acme": true},
}

// grantTable is a Grants over chat id → user → role.
type grantTable map[string]map[string]core.ChatMemberRole

func (g grantTable) ChatMemberRole(_ context.Context, chatID, userID string) (core.ChatMemberRole, error) {
	return g[chatID][userID], nil
}

type failingGrants struct{}

func (failingGrants) ChatMemberRole(context.Context, string, string) (core.ChatMemberRole, error) {
	return "", errors.New("database down")
}

// granted: on chat c1, vic holds view, rita reply, max manage; olga (another
// org's member) and remi (removed) hold manage rows the decision must ignore.
var granted = grantTable{"c1": {
	"vic": core.ChatMemberView, "rita": core.ChatMemberReply, "max": core.ChatMemberManage,
	"olga": core.ChatMemberManage, "remi": core.ChatMemberManage,
}}

// quietLogs discards the logs of decisions made with no Grants or Membership
// wired, or during an outage, which these tests make on purpose (each logs
// at ERROR or WARN; TestProcessGrants pins that log).
func quietLogs(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// chatAnswers is every chat decision, for one caller and row.
type chatAnswers struct {
	read, own, reply, manage bool
	access                   Access
}

func answer(ctx context.Context, caller string, c *core.Chat) chatAnswers {
	a, _ := ReadChatAccess(ctx, caller, c)
	return chatAnswers{CanReadChat(ctx, caller, c), CanOwnChat(ctx, caller, c), CanReplyChat(ctx, caller, c), CanManageChat(ctx, caller, c), a}
}

func answerRow(ctx context.Context, caller string, c *core.ChatRow) chatAnswers {
	a, _ := ReadChatAccess(ctx, caller, c)
	return chatAnswers{CanReadChat(ctx, caller, c), CanOwnChat(ctx, caller, c), CanReplyChat(ctx, caller, c), CanManageChat(ctx, caller, c), a}
}

// The member table: {owner, each level, an org-wide viewer, non-member,
// removed member, machine-bound, other-org credential, membership outage,
// grants unwired, grants outage} x {private, org-visible}. A grant at any level
// reads the chat, private or not; reply and manage come only from a grant
// held by a live member through a person credential; visibility never
// changes reply or manage; only the owner owns.
func TestChatMemberDecisionTable(t *testing.T) {
	quietLogs(t)
	base := WithMembership(context.Background(), crew)
	withGrants := func(ctx context.Context) context.Context { return WithGrants(ctx, granted) }
	owner := chatAnswers{true, true, true, true, AccessOwner}
	viewer := chatAnswers{read: true, access: AccessViewer}
	replier := chatAnswers{read: true, reply: true, access: AccessReply}
	manager := chatAnswers{read: true, reply: true, manage: true, access: AccessManage}
	none := chatAnswers{}
	cases := []struct {
		name            string
		ctx             context.Context
		caller          string
		private, shared chatAnswers
	}{
		{"owner", person(withGrants(base)), "bob", owner, owner},
		{"owner, grants unwired", person(base), "bob", owner, owner},
		{"view member", person(withGrants(base)), "vic", viewer, viewer},
		{"reply member", person(withGrants(base)), "rita", replier, replier},
		{"manage member", person(withGrants(base)), "max", manager, manager},
		{"org member with no grant", person(withGrants(base)), "alice", none, viewer},
		{"non-member holding a manage row", person(withGrants(base)), "olga", none, none},
		{"removed member holding a manage row", person(withGrants(base)), "remi", none, none},
		{"reply member, machine-bound token of the org", WithViewerReads(WithCredentialOrg(WithCredentialKind(withGrants(base), CredentialKindSoftware), "acme")), "rita", none, none},
		{"reply member, software credential", WithViewerReads(WithCredentialKind(withGrants(base), CredentialKindSoftware)), "rita", none, none},
		{"manage member, credential bound to another org", WithCredentialOrg(person(withGrants(base)), "globex"), "max", none, none},
		{"manage member, credential bound to the chat's org", WithCredentialOrg(person(withGrants(base)), "acme"), "max", manager, manager},
		{"manage member, membership outage", person(WithGrants(WithMembership(context.Background(), failingMembership{}), granted)), "max", none, none},
		{"manage member, no Membership wired", person(WithGrants(context.Background(), granted)), "max", none, none},
		{"manage member, a deployment with no orgs", person(WithGrants(WithMembership(context.Background(), NoOrgs{}), granted)), "max", none, none},
		{"manage member, grants unwired", person(base), "max", none, viewer},
		{"manage member, grants outage", person(WithGrants(base, failingGrants{})), "max", none, viewer},
		// Reply and manage are not reads: a site that serves no viewers
		// still decides them, and still refuses the member a read.
		{"manage member, at a site that does not serve viewers", WithCredentialKind(withGrants(base), CredentialKindPerson), "max",
			chatAnswers{reply: true, manage: true}, chatAnswers{reply: true, manage: true}},
		{"no caller", person(withGrants(base)), "", none, none},
	}
	for _, tc := range cases {
		for _, shared := range []bool{false, true} {
			want, vis := tc.private, core.VisibilityPrivate
			if shared {
				want, vis = tc.shared, core.VisibilityOrg
			}
			t.Run(fmt.Sprintf("%s/shared=%v", tc.name, shared), func(t *testing.T) {
				acme := "acme"
				if got := answer(tc.ctx, tc.caller, &core.Chat{ID: "c1", UserID: "bob", OrgID: &acme, Visibility: vis}); got != want {
					t.Errorf("chat: got %+v, want %+v", got, want)
				}
				if got := answerRow(tc.ctx, tc.caller, &core.ChatRow{ID: "c1", UserID: "bob", OrgID: &acme, Visibility: vis}); got != want {
					t.Errorf("chat row: got %+v, want %+v", got, want)
				}
			})
		}
	}
}

// A grant on another chat grants nothing on this one, and a chat with no org
// has no members: a grant row on it admits nobody.
func TestChatGrantsAreScopedToTheirChatAndOrg(t *testing.T) {
	quietLogs(t)
	ctx := person(WithGrants(WithMembership(context.Background(), crew), granted))
	acme := "acme"
	other := &core.Chat{ID: "c2", UserID: "bob", OrgID: &acme}
	noOrg := &core.Chat{ID: "c1", UserID: "bob"}
	for _, caller := range []string{"vic", "rita", "max"} {
		if got := answer(ctx, caller, other); got != (chatAnswers{}) {
			t.Errorf("%s on another chat: %+v", caller, got)
		}
		if got := answer(ctx, caller, noOrg); got != (chatAnswers{}) {
			t.Errorf("%s on a chat with no org: %+v", caller, got)
		}
	}
	// A project has no grants, whatever a table holds for its id.
	if CanReadProject(ctx, "max", &core.Project{ID: "c1", UserID: "bob", OrgID: &acme}) {
		t.Error("a chat grant admitted a reader to a project")
	}
}

// A grant of a level this build does not know admits nothing, not even a
// read of a private chat; on a shared one its holder is the org-wide viewer
// they would be without it.
func TestUnknownChatGrantIsADenial(t *testing.T) {
	quietLogs(t)
	ctx := person(WithGrants(WithMembership(context.Background(), crew), grantTable{"c1": {"rita": "owner"}}))
	acme := "acme"
	if got := answer(ctx, "rita", &core.Chat{ID: "c1", UserID: "bob", OrgID: &acme}); got != (chatAnswers{}) {
		t.Errorf("private: %+v", got)
	}
	if got := answer(ctx, "rita", &core.Chat{ID: "c1", UserID: "bob", OrgID: &acme, Visibility: core.VisibilityOrg}); got != (chatAnswers{read: true, access: AccessViewer}) {
		t.Errorf("shared: %+v", got)
	}
}

// A decision with no Grants on the context or the process fails closed and
// logs at ERROR: it means a process never installed its reader. The
// process's Grants serves a context that carries none, and a context's own
// wins over it.
func TestProcessGrants(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev); SetProcessGrants(nil) })

	acme := "acme"
	c := &core.Chat{ID: "c1", UserID: "bob", OrgID: &acme}
	ctx := person(WithMembership(context.Background(), crew))
	if CanReadChat(ctx, "rita", c) || CanReplyChat(ctx, "rita", c) {
		t.Fatal("a grant was honoured with no Grants wired")
	}
	if out := buf.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "no grants reader") {
		t.Fatalf("log = %q, want an ERROR naming the missing grants reader", out)
	}

	SetProcessGrants(granted)
	if !CanReadChat(ctx, "rita", c) || !CanReplyChat(ctx, "rita", c) || CanManageChat(ctx, "rita", c) {
		t.Fatal("the process's Grants was not read")
	}
	if CanReplyChat(WithGrants(ctx, grantTable{}), "rita", c) {
		t.Fatal("a context's own Grants must win over the process's")
	}
	SetProcessGrants(nil)
	if CanReplyChat(ctx, "rita", c) {
		t.Fatal("SetProcessGrants(nil) did not clear it")
	}
}

// Access's levels shape as a viewer's at every level but the owner's, and
// name the grant they were read through.
func TestAccessLevels(t *testing.T) {
	for _, tc := range []struct {
		a      Access
		viewer bool
		role   core.ChatMemberRole
	}{
		{AccessNone, true, ""}, {AccessOwner, false, ""}, {AccessViewer, true, core.ChatMemberView},
		{AccessReply, true, core.ChatMemberReply}, {AccessManage, true, core.ChatMemberManage},
	} {
		if tc.a.Viewer() != tc.viewer || tc.a.Role() != tc.role {
			t.Errorf("Access %d: Viewer %v Role %q, want %v %q", tc.a, tc.a.Viewer(), tc.a.Role(), tc.viewer, tc.role)
		}
	}
}

// ctxCase is one context the property test decides on, with what it carries:
// whether it acts for a person, the org its credential is bound to, who it
// says is a live member of which org, and whether it reads grants.
type ctxCase struct {
	ctx    context.Context
	person bool
	bound  string
	member func(user, org string) bool
	grants bool
}

// TestChatDecisionsFollowTheGrant replaces TestCanWriteImpliesOwner
// (chat-collaboration.md §4.4). Over random rows, actors, credentials and
// grants: CanOwnChat is exactly the owner rule; CanManageChat is the owner,
// or a manage grant held by a live member of the chat's org acting as a
// person through no credential bound to another org; CanReplyChat the same
// with reply or manage; and the chat's visibility never changes either
// answer. Every other kind stays owner-only, so the widening cannot leak
// sideways.
func TestChatDecisionsFollowTheGrant(t *testing.T) {
	quietLogs(t)
	rng := rand.New(rand.NewSource(20261124))
	users := []string{"", "bob", "alice", "olga", "remi", "vic", "Bob"}
	orgs := []*string{nil, strp("acme"), strp("globex")}
	vis := []core.Visibility{core.VisibilityPrivate, core.VisibilityOrg, "members", "public"}
	roles := []core.ChatMemberRole{"", core.ChatMemberView, core.ChatMemberReply, core.ChatMemberManage, "owner"}
	isLive := func(user, org string) bool { return crew[user][org] }
	never := func(string, string) bool { return false }
	everyone := func(string, string) bool { return true }
	bg := context.Background()
	pick := func(n int) int { return rng.Intn(n) }

	for i := 0; i < 5000; i++ {
		table := grantTable{"c1": {}}
		for _, u := range users {
			if r := roles[pick(len(roles))]; r != "" {
				table["c1"][u] = r
			}
		}
		cases := []ctxCase{
			{ctx: bg, member: never},
			{ctx: person(WithGrants(WithMembership(bg, crew), table)), person: true, member: isLive, grants: true},
			{ctx: WithGrants(WithMembership(bg, crew), table), member: isLive, grants: true},
			{ctx: WithCredentialKind(WithGrants(WithMembership(bg, crew), table), CredentialKindSoftware), member: isLive, grants: true},
			{ctx: WithCredentialOrg(person(WithGrants(WithMembership(bg, crew), table)), "acme"), person: true, bound: "acme", member: isLive, grants: true},
			{ctx: WithCredentialOrg(person(WithGrants(WithMembership(bg, crew), table)), "globex"), person: true, bound: "globex", member: isLive, grants: true},
			{ctx: person(WithGrants(WithMembership(bg, NoOrgs{}), table)), person: true, member: never, grants: true},
			{ctx: person(WithGrants(WithMembership(bg, failingMembership{}), table)), person: true, member: never, grants: true},
			{ctx: person(WithGrants(WithMembership(bg, allMembers{}), table)), person: true, member: everyone, grants: true},
			{ctx: person(WithGrants(bg, table)), person: true, member: never, grants: true},
			{ctx: person(WithMembership(bg, allMembers{})), person: true, member: everyone},
			{ctx: person(WithGrants(WithMembership(bg, allMembers{}), failingGrants{})), person: true, member: everyone},
		}
		owner, caller := users[pick(len(users))], users[pick(len(users))]
		org, d := orgs[pick(len(orgs))], cases[pick(len(cases))]
		ctx := d.ctx

		// The model: what a non-owner's grant admits on this context.
		holds := func(min core.ChatMemberRole) bool {
			o := orgOf(org)
			return caller != "" && caller != owner && o != "" && d.person && d.grants &&
				(d.bound == "" || d.bound == o) && table["c1"][caller].AtLeast(min) && d.member(caller, o)
		}
		var first *[3]bool
		for _, v := range vis {
			chat := &core.Chat{ID: "c1", UserID: owner, OrgID: org, Visibility: v}
			row := &core.ChatRow{ID: "c1", UserID: owner, OrgID: org, Visibility: v}
			got := [3]bool{CanOwnChat(ctx, caller, chat), CanReplyChat(ctx, caller, chat), CanManageChat(ctx, caller, chat)}
			if rowGot := [3]bool{CanOwnChat(ctx, caller, row), CanReplyChat(ctx, caller, row), CanManageChat(ctx, caller, row)}; rowGot != got {
				t.Fatalf("the chat and its row disagree: %v vs %v", got, rowGot)
			}
			if got[0] && (caller == "" || caller != owner) {
				t.Fatalf("CanOwnChat allowed %q on %q's chat (org %v, visibility %q)", caller, owner, org, v)
			}
			if want := got[0] || holds(core.ChatMemberManage); got[2] != want {
				t.Fatalf("CanManageChat(%q on %q's chat, org %v, visibility %q, grant %q) = %v, want %v", caller, owner, org, v, table["c1"][caller], got[2], want)
			}
			if want := got[0] || holds(core.ChatMemberReply); got[1] != want {
				t.Fatalf("CanReplyChat(%q on %q's chat, org %v, visibility %q, grant %q) = %v, want %v", caller, owner, org, v, table["c1"][caller], got[1], want)
			}
			if first == nil {
				first = &got
			} else if got != *first {
				t.Fatalf("visibility %q changed a chat decision: %v, was %v", v, got, *first)
			}
		}

		writes := map[string]bool{
			"project":  CanWriteProject(ctx, caller, &core.Project{ID: "c1", UserID: owner, OrgID: org}),
			"workflow": CanWriteWorkflow(ctx, caller, &core.WorkflowDraft{ID: "c1", UserID: owner, OrgID: org}),
			"trigger":  CanWriteTrigger(ctx, caller, &core.Trigger{ID: "c1", UserID: owner, OrgID: org}),
			"machine":  CanWriteMachine(ctx, caller, &core.Daemon{ID: "c1", UserID: owner}),
			"personal": CanAccessPersonal(caller, owner),
			// The reads sharing and grants do not widen stay owner-only too.
			"workflow read": CanReadWorkflow(ctx, caller, &core.WorkflowDraft{ID: "c1", UserID: owner, OrgID: org}),
			"trigger read":  CanReadTrigger(ctx, caller, &core.Trigger{ID: "c1", UserID: owner, OrgID: org}),
			"machine read":  CanReadMachine(ctx, caller, &core.Daemon{ID: "c1", UserID: owner}),
		}
		for kind, ok := range writes {
			if ok && (caller == "" || caller != owner) {
				t.Fatalf("%s: allowed %q on %q's row (org %v)", kind, caller, owner, org)
			}
		}
	}
}

// ChatMemberChange (chat-collaboration.md §3.2): the owner and a manage
// member grant, as a person, to a live member of the chat's org who is not
// the owner, up to manage; a reader below manage is told they may not; anyone
// else gets NotFound; a member may leave; a chat with no org has no members.
func TestChatMemberChange(t *testing.T) {
	quietLogs(t)
	acme := "acme"
	chat := &core.Chat{ID: "c1", UserID: "bob", OrgID: &acme}
	shared := &core.Chat{ID: "c1", UserID: "bob", OrgID: &acme, Visibility: core.VisibilityOrg}
	noOrg := &core.Chat{ID: "c1", UserID: "bob"}
	ctx := person(WithGrants(WithMembership(context.Background(), crew), granted))
	software := WithCredentialKind(WithGrants(WithMembership(context.Background(), crew), granted), CredentialKindSoftware)
	outage := person(WithGrants(WithMembership(context.Background(), failingMembership{}), granted))
	cases := []struct {
		name          string
		ctx           context.Context
		chat          *core.Chat
		actor, target string
		role          core.ChatMemberRole
		want          error
	}{
		{"owner grants view", ctx, chat, "bob", "alice", core.ChatMemberView, nil},
		{"owner grants manage", ctx, chat, "bob", "alice", core.ChatMemberManage, nil},
		{"owner changes a level", ctx, chat, "bob", "rita", core.ChatMemberManage, nil},
		{"owner removes", ctx, chat, "bob", "max", "", nil},
		{"owner removes a departed member's row", ctx, chat, "bob", "remi", "", nil},
		{"manager grants reply", ctx, chat, "max", "alice", core.ChatMemberReply, nil},
		{"manager grants manage", ctx, chat, "max", "alice", core.ChatMemberManage, nil},
		{"manager demotes another member", ctx, chat, "max", "rita", core.ChatMemberView, nil},
		{"manager removes a member", ctx, chat, "max", "vic", "", nil},
		{"manager cannot grant above manage", ctx, chat, "max", "alice", "owner", ErrInvalidChatRole},
		{"owner cannot grant above manage", ctx, chat, "bob", "alice", "owner", ErrInvalidChatRole},
		{"manager cannot touch the owner (grant)", ctx, chat, "max", "bob", core.ChatMemberView, ErrOwnerNotMember},
		{"manager cannot touch the owner (remove)", ctx, chat, "max", "bob", "", ErrOwnerNotMember},
		{"owner cannot grant themselves", ctx, chat, "bob", "bob", core.ChatMemberManage, ErrOwnerNotMember},
		{"grantee must be a live member", ctx, chat, "bob", "remi", core.ChatMemberView, ErrGranteeNotOrgMember},
		{"grantee must be of the chat's org", ctx, chat, "bob", "olga", core.ChatMemberView, ErrGranteeNotOrgMember},
		{"reply member cannot grant", ctx, chat, "rita", "alice", core.ChatMemberView, ErrNotChatManager},
		{"view member cannot grant", ctx, chat, "vic", "alice", core.ChatMemberView, ErrNotChatManager},
		{"reply member cannot promote themselves", ctx, chat, "rita", "rita", core.ChatMemberManage, ErrNotChatManager},
		{"view member may leave", ctx, chat, "vic", "vic", "", nil},
		{"reply member may leave", ctx, chat, "rita", "rita", "", nil},
		{"view member cannot remove another", ctx, chat, "vic", "rita", "", ErrNotChatManager},
		{"org-wide viewer cannot grant", ctx, shared, "alice", "vic", core.ChatMemberView, ErrNotChatManager},
		{"a non-reader is not told the chat exists", ctx, chat, "alice", "vic", core.ChatMemberView, ErrDenied},
		{"a removed member's manage row grants nothing", ctx, chat, "remi", "alice", core.ChatMemberView, ErrDenied},
		{"another org's member's manage row grants nothing", ctx, chat, "olga", "alice", core.ChatMemberView, ErrDenied},
		{"owner through software", software, chat, "bob", "alice", core.ChatMemberView, ErrPersonRequired},
		{"manager through software", software, chat, "max", "alice", core.ChatMemberView, ErrDenied},
		{"no org", ctx, noOrg, "bob", "alice", core.ChatMemberView, ErrShareNeedsOrg},
		{"no org, remove", ctx, noOrg, "bob", "vic", "", ErrShareNeedsOrg},
		{"membership outage", outage, chat, "bob", "alice", core.ChatMemberView, ErrMembershipUnavailable},
		{"manager, membership outage", outage, chat, "max", "alice", core.ChatMemberView, ErrMembershipUnavailable},
		{"no chat", ctx, nil, "bob", "alice", core.ChatMemberView, ErrDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ChatMemberChange(tc.ctx, tc.actor, tc.chat, tc.target, tc.role)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if err != nil {
				if got != (core.ChatMemberChange{}) {
					t.Fatalf("a refusal carried a change: %+v", got)
				}
				return
			}
			want := core.ChatMemberChange{ChatID: "c1", OrgID: "acme", OwnerUserID: "bob", ActorUserID: tc.actor, TargetUserID: tc.target, Role: tc.role}
			if got != want {
				t.Fatalf("change = %+v, want %+v", got, want)
			}
		})
	}
}
