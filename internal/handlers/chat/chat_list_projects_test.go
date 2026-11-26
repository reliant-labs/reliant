package chat

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/store"
)

// The sidebar's multi-project filter: ListChats lists the chats of several
// projects in one request (project_ids, unioned with project_id), ordered and
// limited like any chat list, still scoped to the caller.
func TestListChats_SeveralProjects(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	svc := &Service{deps: Deps{Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}}

	// Five projects of the caller, two chats each, every chat active at its
	// own time, so the order across projects is the order of last_active.
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	projects := make([]string, 5)
	mains := make(map[string]string, 5)
	chatsOf := make(map[string][]string, 5)
	activeAt := make(map[string]time.Time)
	for i := range projects {
		p, mainID := newInvariantTestProject(t, ctx, repo, true)
		projects[i], mains[p] = p, mainID
	}
	for n := 0; n < 10; n++ {
		p := projects[n%5]
		id := uuid.NewString()
		at := base.Add(time.Duration(n) * time.Minute)
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{
			ID: id, Title: fmt.Sprintf("chat %d", n), ProjectID: p, UserID: "test-user",
			State: db.ChatStateIdle, CreatedAt: at, UpdatedAt: at, LastActive: at,
		}))
		chatsOf[p] = append(chatsOf[p], id)
		activeAt[id] = at
	}

	// Another user's project and chat: naming it lists nothing of theirs.
	othersProject := "others-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: othersProject, UserID: "other-user", Name: "theirs", Path: "/p/" + othersProject,
		CreatedAt: base, UpdatedAt: base, LastActive: base,
	}))
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: uuid.NewString(), Title: "theirs", ProjectID: othersProject, UserID: "other-user",
		State: db.ChatStateIdle, CreatedAt: base, UpdatedAt: base, LastActive: base.Add(time.Hour),
	}))

	// newest is the chats of these projects, most recently active first.
	newest := func(ps ...string) []string {
		var out []string
		for _, p := range ps {
			out = append(out, chatsOf[p]...)
		}
		sort.Slice(out, func(i, j int) bool { return activeAt[out[i]].After(activeAt[out[j]]) })
		return out
	}
	list := func(req *reliantv1.ListChatsRequest) *reliantv1.ListChatsResponse {
		t.Helper()
		resp, err := svc.ListChats(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		return resp.Msg
	}
	ids := func(msg *reliantv1.ListChatsResponse) []string {
		out := make([]string, 0, len(msg.Chats))
		for _, c := range msg.Chats {
			out = append(out, c.Id)
		}
		return out
	}
	mainsOf := func(msg *reliantv1.ListChatsResponse) []string {
		var out []string
		for _, w := range msg.MainWorktrees {
			out = append(out, w.Id)
		}
		sort.Strings(out)
		return out
	}
	wantMains := func(ps ...string) []string {
		var out []string
		for _, p := range ps {
			out = append(out, mains[p])
		}
		sort.Strings(out)
		return out
	}

	p1, p2, p3, p4 := projects[0], projects[1], projects[2], projects[3]

	t.Run("two of five projects", func(t *testing.T) {
		got := list(&reliantv1.ListChatsRequest{ProjectIds: []string{p2, p4}})
		assert.Equal(t, newest(p2, p4), ids(got), "only their chats, most recently active first")
		assert.Equal(t, int32(4), got.Total)
		assert.Equal(t, wantMains(p2, p4), mainsOf(got), "the main workspace of each listed project")

		limited := list(&reliantv1.ListChatsRequest{ProjectIds: []string{p2, p4}, Limit: ptr.Of(int32(3))})
		assert.Equal(t, newest(p2, p4)[:3], ids(limited), "limited like any chat list")
	})

	t.Run("project_id and project_ids are a union", func(t *testing.T) {
		got := list(&reliantv1.ListChatsRequest{ProjectId: p1, ProjectIds: []string{p2, p1}})
		assert.Equal(t, newest(p1, p2), ids(got))
		assert.Equal(t, wantMains(p1, p2), mainsOf(got))
	})

	t.Run("another user's project contributes nothing", func(t *testing.T) {
		got := list(&reliantv1.ListChatsRequest{ProjectIds: []string{p3, othersProject}})
		assert.Equal(t, newest(p3), ids(got))
		assert.Equal(t, wantMains(p3), mainsOf(got))

		only := list(&reliantv1.ListChatsRequest{ProjectIds: []string{othersProject}})
		assert.Empty(t, ids(only), "never every project")
		assert.Empty(t, only.MainWorktrees)
	})

	t.Run("one project_id lists as before", func(t *testing.T) {
		got := list(&reliantv1.ListChatsRequest{ProjectId: p3})
		assert.Equal(t, newest(p3), ids(got))
		assert.Equal(t, wantMains(p3), mainsOf(got))
		assert.Equal(t, ids(got), ids(list(&reliantv1.ListChatsRequest{ProjectIds: []string{p3}})),
			"one id in project_ids is the same list")

		var all []string
		for _, id := range ids(list(&reliantv1.ListChatsRequest{})) {
			if activeAt[id] != (time.Time{}) {
				all = append(all, id)
			}
		}
		assert.Equal(t, newest(projects...), all, "neither set lists every project")

		// ListChats' project_id is a plain string: "" has always meant every
		// project, and with project_ids it adds nothing to the list.
		assert.Equal(t, newest(p2), ids(list(&reliantv1.ListChatsRequest{ProjectId: "", ProjectIds: []string{p2}})))
	})

	t.Run("too many or empty ids are refused", func(t *testing.T) {
		tooMany := make([]string, core.MaxListProjectIDs+1)
		for i := range tooMany {
			tooMany[i] = uuid.NewString()
		}
		_, err := svc.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectIds: tooMany}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))

		atMost := append([]string{p4}, tooMany[:core.MaxListProjectIDs-1]...)
		assert.Equal(t, newest(p4), ids(list(&reliantv1.ListChatsRequest{ProjectIds: atMost})), "the most ids allowed still list")

		_, err = svc.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectIds: []string{p1, ""}}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	})
}
