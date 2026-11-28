package chat

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// forge's project-name shape: a letter first, then letters, digits, '_' or
// '-'; Unicode letters and digits count, as forge's do.
func TestValidAppName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"a", "storefront", "my-app", "my_app2", "App-1", "café", "日本-app", strings.Repeat("a", 128)} {
		assert.True(t, validAppName(name), name)
	}
	for _, name := range []string{"", "9lives", "-app", "_app", "my app", "a/b", "a.b", "app!", " app", "a\nb", strings.Repeat("a", 129), "\xffbad"} {
		assert.False(t, validAppName(name), "%q", name)
	}
}

// StartChat stores the app a chat starts from, returns it on the chat, and
// leaves a chat started without one untagged. A malformed name is refused
// before anything is written.
func TestStartChat_AppName(t *testing.T) {
	t.Parallel()
	f := newBuilderTestFixture(t)
	f.saveDraft(t, "test-user", "ask-app", db.WorkflowDraftStatusComplete)
	start := func(app *string) (*connect.Response[reliantv1.StartChatResponse], error) {
		return f.service.StartChat(f.ctx, connect.NewRequest(&reliantv1.StartChatRequest{
			ProjectId: f.projectID,
			Workflow:  "ask-app",
			AppName:   app,
			Messages:  []*reliantv1.InputMessage{{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "why is checkout slow?"}},
		}))
	}

	resp, err := start(ptr.Of("storefront"))
	require.NoError(t, err)
	assert.Equal(t, "storefront", resp.Msg.GetChat().GetAppName(), "the response carries the tag")
	listed, err := f.service.ListChats(f.ctx, connect.NewRequest(&reliantv1.ListChatsRequest{AppName: ptr.Of("storefront")}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetChats(), 1, "the app's chat list holds it")
	assert.Equal(t, resp.Msg.GetChat().GetId(), listed.Msg.GetChats()[0].GetId())
	assert.Equal(t, "storefront", listed.Msg.GetChats()[0].GetAppName(), "a read carries the stored tag")

	for name, app := range map[string]*string{"unset": nil, "empty": ptr.Of("")} {
		resp, err := start(app)
		require.NoError(t, err, name)
		assert.Nil(t, resp.Msg.GetChat().AppName, "%s: a chat about no app is untagged", name)
		stored, err := store.GetChat(f.ctx, f.repo.ORM(), resp.Msg.GetChat().GetId())
		require.NoError(t, err)
		assert.Nil(t, stored.AppName, name)
	}

	before, err := f.repo.ListChats(f.ctx, db.ChatFilters{UserID: "test-user", Limit: 100})
	require.NoError(t, err)
	for _, bad := range []string{"9lives", "my app", "a/b", strings.Repeat("a", 129)} {
		_, err := start(ptr.Of(bad))
		require.Error(t, err, bad)
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "%q: %v", bad, err)
	}
	after, err := f.repo.ListChats(f.ctx, db.ChatFilters{UserID: "test-user", Limit: 100})
	require.NoError(t, err)
	assert.Len(t, after, len(before), "a refused start creates no chat")
}

// ListChats with app_name is an app's chat list: the caller's own chats
// tagged with that app, still narrowed by org, never another user's chat
// with the same tag.
func TestListChats_AppNameFilter(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	resolver := orgs.New(memberOfBoth{}) // every caller is in acme and home; home is the default
	service := NewTestChat(t, WithChatDeps(Deps{TriggerReads: &MockTriggerReads{}, Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Orgs: resolver, Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}))
	as := func(user string) context.Context {
		return authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, user), resolver)
	}
	alice := as("alice")
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	suffix := uuid.NewString()[:8]
	projects := map[string]string{"alice": "app-alice-" + suffix, "bob": "app-bob-" + suffix}
	for user, id := range projects {
		require.NoError(t, repo.CreateProject(alice, &db.Project{
			ID: id, UserID: user, Name: "p", Path: "/p/" + id, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
	}
	seed := func(id, user string, org, app *string) string {
		id += "-" + suffix
		require.NoError(t, repo.CreateChat(alice, &db.Chat{
			ID: id, UserID: user, OrgID: org, AppName: app, Title: id, ProjectID: projects[user],
			State: db.ChatStateIdle, Origin: db.ChatOriginUser, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		return id
	}
	acmeShop := seed("acme-shop", "alice", ptr.Of("acme"), ptr.Of("shop"))
	homeShop := seed("home-shop", "alice", ptr.Of("home"), ptr.Of("shop"))
	seed("acme-blog", "alice", ptr.Of("acme"), ptr.Of("blog"))
	seed("acme-untagged", "alice", ptr.Of("acme"), nil)
	seed("bob-acme-shop", "bob", ptr.Of("acme"), ptr.Of("shop"))

	list := func(ctx context.Context, org, app *string) []string {
		t.Helper()
		resp, err := service.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{OrgId: org, AppName: app}))
		require.NoError(t, err)
		var out []string
		for _, c := range resp.Msg.Chats {
			if strings.HasSuffix(c.GetId(), suffix) {
				out = append(out, c.GetId())
				if app != nil && *app != "" {
					assert.Equal(t, *app, c.GetAppName())
				}
			}
		}
		sort.Strings(out)
		return out
	}
	sorted := func(s ...string) []string { sort.Strings(s); return s }

	assert.Equal(t, []string{acmeShop}, list(alice, ptr.Of("acme"), ptr.Of("shop")), "one org's chats of one app, never bob's")
	assert.Equal(t, []string{homeShop}, list(alice, ptr.Of("home"), ptr.Of("shop")))
	assert.Equal(t, sorted(acmeShop, homeShop), list(alice, nil, ptr.Of("shop")), "no org named: every org's, as ListChats always did")
	assert.Empty(t, list(alice, ptr.Of("acme"), ptr.Of("nope")), "an app with no chats lists none")
	assert.Empty(t, list(alice, ptr.Of("acme"), ptr.Of("not an app")), "a malformed name lists nothing")
	assert.Len(t, list(alice, ptr.Of("acme"), ptr.Of("")), 3, "an empty app_name is unset")
	assert.Len(t, list(as("bob"), ptr.Of("acme"), ptr.Of("shop")), 1, "bob lists only his own")
}

// A branch is the chat it came from continued, so it is about the same app.
func TestBranchChat_KeepsTheAppName(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	projectID := "test-project-branch-app-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Branch App", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	sourceChatID, messageID := setupBranchAtomicitySource(t, repo, ctx, projectID)
	_, err := repo.ORM().Bun().NewRaw(`UPDATE chats SET app_name = 'storefront' WHERE id = ?`, sourceChatID).Exec(ctx)
	require.NoError(t, err)

	service := &Service{deps: Deps{Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}, threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())})}
	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{ChatId: sourceChatID, MessageId: messageID}))
	require.NoError(t, err)
	assert.Equal(t, "storefront", resp.Msg.GetChat().GetAppName())
	branch, err := store.GetChat(ctx, repo.ORM(), resp.Msg.GetChat().GetId())
	require.NoError(t, err)
	require.NotNil(t, branch.AppName)
	assert.Equal(t, "storefront", *branch.AppName)
}

// A viewer of a shared chat reads its app tag: it says what the conversation
// is about, not anything of the owner's alone.
func TestGetChat_ViewerReadsTheAppName(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	_, err := repo.ORM().Bun().NewRaw(`UPDATE chats SET app_name = 'storefront' WHERE id = ?`, f.ChatID).Exec(context.Background())
	require.NoError(t, err)
	svc := NewTestChat(t, WithChatDeps(Deps{TriggerReads: &MockTriggerReads{}, Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Orgs: f.Membership, Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}))

	resp, err := svc.GetChat(f.Viewer, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: f.ChatID}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetChat().GetViewerAccess())
	assert.Equal(t, "storefront", resp.Msg.GetChat().GetAppName())
}

// The column's CHECK holds forge's shape for ASCII and admits what the
// server accepts beyond it, so no name StartChat accepts fails the insert.
func TestChatsAppNameCheck(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	projectID := "app-check-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "p", Path: "/p/" + projectID, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	insert := func(app string) error {
		return repo.CreateChat(ctx, &db.Chat{
			ID: uuid.NewString(), UserID: "test-user", ProjectID: projectID, AppName: proto.String(app),
			State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
		})
	}
	for _, ok := range []string{"storefront", "my_app-2", "café", "日本-app", strings.Repeat("a", 128)} {
		require.True(t, validAppName(ok), ok)
		assert.NoError(t, insert(ok), ok)
	}
	for _, bad := range []string{"", "9lives", "-x", "my app", "a/b", "a.b", strings.Repeat("a", 129)} {
		assert.Error(t, insert(bad), "%q", bad)
	}
}
