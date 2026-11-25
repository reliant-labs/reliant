// Copyright (c) 2025 Reliant Labs
package chatupdates_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/chatupdates"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// recorder is the hub side: what was published, and when relative to commit.
type recorder struct {
	mu    sync.Mutex
	chats []core.ChatUpdate
	users []core.UserUpdate
}

func (r *recorder) PublishChat(_ string, _ int64, u core.ChatUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chats = append(r.chats, u)
}

func (r *recorder) PublishUser(u *core.UserUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users = append(r.users, *u)
}

func (r *recorder) counts() (chats, users int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.chats), len(r.users)
}

type fixture struct {
	repo   *db.Repo
	pub    *recorder
	svc    chatupdates.Service
	chatID string
	userID string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	pub := &recorder{}
	now := time.Now().UTC().Truncate(time.Millisecond)
	chat := &db.Chat{ID: uuid.NewString(), Title: "t", ProjectID: "test-project", UserID: "owner-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateChat(context.Background(), chat))
	return &fixture{repo: repo, pub: pub, svc: chatupdates.NewFromORM(repo.ORM(), repo, pub), chatID: chat.ID, userID: chat.UserID}
}

func (f *fixture) chatRows(t *testing.T) []core.ChatUpdate {
	t.Helper()
	rows, err := store.ChatUpdatesSince(context.Background(), f.repo.ORM(), f.chatID, 0, 100)
	require.NoError(t, err)
	return rows
}

// A refetch is one chat_updates row with the refetch payload, published once
// after it committed.
func TestEmitChatRefetch_WritesTheRowAndPublishesIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	require.NoError(t, f.svc.EmitChatRefetch(ctx, f.chatID, core.RefetchPlanTasks))

	rows := f.chatRows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, "refetch-plan_tasks", rows[0].EntityID)
	var data core.RefetchData
	require.NoError(t, json.Unmarshal(rows[0].Data, &data))
	assert.Equal(t, core.RefetchPlanTasks, data.Type)
	chats, _ := f.pub.counts()
	assert.Equal(t, 1, chats)
	assert.Equal(t, rows[0].SequenceNumber, f.pub.chats[0].SequenceNumber)
}

// A refetch emitted inside a transaction that rolls back writes nothing and
// tells no client: the row it announces never existed.
func TestEmitChatRefetch_RollbackPublishesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	boom := errors.New("boom")

	err := f.repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		require.NoError(t, f.svc.EmitChatRefetch(ctx, f.chatID, core.RefetchPlanTasks))
		chats, _ := f.pub.counts()
		assert.Zero(t, chats, "published before the outer transaction committed")
		return boom
	})
	require.ErrorIs(t, err, boom)

	assert.Empty(t, f.chatRows(t))
	chats, _ := f.pub.counts()
	assert.Zero(t, chats)
}

// Inside a transaction that commits, the publication waits for the commit.
func TestEmitChatRefetch_JoinsTheCallersTransactionAndPublishesAfterItCommits(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	require.NoError(t, f.repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		require.NoError(t, f.svc.EmitChatRefetch(ctx, f.chatID, core.RefetchAgentMailbox))
		require.NoError(t, f.svc.EmitChatRefetch(ctx, f.chatID, core.RefetchPlanTasks))
		chats, _ := f.pub.counts()
		assert.Zero(t, chats, "published before commit")
		return nil
	}))

	rows := f.chatRows(t)
	require.Len(t, rows, 2)
	chats, _ := f.pub.counts()
	assert.Equal(t, 2, chats)
	assert.Less(t, rows[0].SequenceNumber, rows[1].SequenceNumber)
}

// A user refetch is scoped by what it names: a worktree beats a project beats
// the system, as the facade's emitter did.
func TestEmitUserRefetch_ScopesTheEntity(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	// project_id and worktree_id are foreign keys: the rows must exist.
	now := time.Now().UTC().Truncate(time.Millisecond)
	project, worktree := "project-"+uuid.NewString(), "wt-"+uuid.NewString()[:8]
	require.NoError(t, f.repo.CreateProject(ctx, &db.Project{ID: project, UserID: f.userID, Name: "p", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, f.repo.CreateWorktree(ctx, &db.Worktree{ID: worktree, Name: worktree, Path: "/w", Branch: "b", ProjectID: project, CreatedAt: now, UpdatedAt: now, LastActive: now}))

	require.NoError(t, f.svc.EmitUserRefetch(ctx, f.userID, core.RefetchConfigHealth, core.RefetchOpts{}))
	require.NoError(t, f.svc.EmitUserRefetch(ctx, f.userID, core.RefetchWorktreeChanges, core.RefetchOpts{ProjectID: &project}))
	require.NoError(t, f.svc.EmitUserRefetch(ctx, f.userID, core.RefetchWorktreeChanges, core.RefetchOpts{ProjectID: &project, WorktreeID: &worktree}))

	require.Len(t, f.pub.users, 3)
	assert.Equal(t, "config_health", f.pub.users[0].EntityID)
	assert.Equal(t, project, f.pub.users[1].EntityID)
	assert.Equal(t, worktree, f.pub.users[2].EntityID)
	assert.Less(t, f.pub.users[0].SequenceNumber, f.pub.users[2].SequenceNumber)
}

// An empty id never reaches the log, and an unwired publisher still writes.
func TestEmitRefetch_FailsClosedOnAnEmptyScopeAndWritesWithoutAPublisher(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	require.Error(t, f.svc.EmitChatRefetch(ctx, "", core.RefetchPlanTasks))
	require.Error(t, f.svc.EmitUserRefetch(ctx, "", core.RefetchInbox, core.RefetchOpts{}))
	assert.Empty(t, f.chatRows(t))

	quiet := chatupdates.NewFromORM(f.repo.ORM(), f.repo, nil)
	require.NoError(t, quiet.EmitChatRefetch(ctx, f.chatID, core.RefetchPlanTasks))
	assert.Len(t, f.chatRows(t), 1)
}
