package db_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The org feed's activity row (CHANGED) and an unshare (UNSHARED) race for
// the same chat. Its order must be true whichever commits first: a CHANGED
// never follows the chat's UNSHARED unless the chat was shared again. Each
// case holds one transaction open past its feed write, starts the other, and
// lets the first commit only once pg_locks shows the second waiting on it.

func shareChange(f *viewertest.Fixture, action core.ShareAction) core.ShareChange {
	return core.ShareChange{
		Kind: core.ShareKindChat, ResourceID: f.ChatID, OrgID: viewertest.OrgID, OwnerUserID: f.OwnerID,
		ActorUserID: f.OwnerID, Action: action, At: viewertest.SeededAt,
	}
}

func announce(ctx context.Context, repo *db.Repo, f *viewertest.Fixture) error {
	chatID, projectID := f.ChatID, f.ProjectID
	data, _ := json.Marshal(map[string]any{"chat_id": f.ChatID, "activity": 1, "working_threads": 1})
	return repo.CreateUserUpdate(ctx, &db.UserUpdate{
		UserID: f.OwnerID, ChatID: &chatID, ProjectID: &projectID,
		UpdateType: reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_ACTIVITY_CHANGED,
		EntityType: reliantv1.EntityType_ENTITY_TYPE_CHAT, EntityID: f.ChatID, Data: data,
	})
}

// waitForLockWaiter returns once a session of this test's database waits for
// a lock it has not been granted, or fails if done reports first.
func waitForLockWaiter(t *testing.T, repo *db.Repo, done <-chan error) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		var waiting int
		require.NoError(t, repo.ORM().Bun().NewRaw(`SELECT count(*) FROM pg_locks l
			JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE NOT l.granted AND a.datname = current_database()`).Scan(context.Background(), &waiting))
		if waiting > 0 {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("the second transaction finished without waiting on the first (err %v)", err)
		case <-deadline:
			t.Fatal("the second transaction never waited on the first")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// chatFeed is the shared chat's feed rows, in sequence order.
func chatFeed(t *testing.T, repo *db.Repo, f *viewertest.Fixture) []reliantv1.OrgUpdateType {
	t.Helper()
	rows, err := repo.OrgUpdatesSince(context.Background(), viewertest.OrgID, 0, 100)
	require.NoError(t, err)
	var out []reliantv1.OrgUpdateType
	for _, r := range rows {
		if r.ResourceID == f.ChatID {
			out = append(out, reliantv1.OrgUpdateType(r.UpdateType))
		}
	}
	return out
}

// holdOpen runs first in a transaction that stays open, after first returns,
// until release is closed, and reports its end on the returned channel.
func holdOpen(repo *db.Repo, first func(context.Context) error, entered chan<- struct{}, release <-chan struct{}) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- repo.RunTx(context.Background(), orm.ReadCommitted, func(txCtx context.Context) error {
			if err := first(txCtx); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	return done
}

const (
	shared   = reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_SHARED
	unshared = reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_UNSHARED
	changed  = reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_CHANGED
)

// The unshare holds the feed when the announcement arrives: the announcement
// waits, then reads the chat as private and writes nothing. Shared again, the
// chat's next announcement is in the feed.
func TestOrgFeed_AnActivityAfterAnUnshareWritesNothing(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	ctx := context.Background()

	entered, release := make(chan struct{}), make(chan struct{})
	unsharing := holdOpen(repo, func(txCtx context.Context) error {
		changed, err := repo.ApplyShareChange(txCtx, shareChange(f, core.ShareActionUnshare))
		require.True(t, changed)
		return err
	}, entered, release)
	<-entered

	announcing := make(chan error, 1)
	go func() { announcing <- announce(ctx, repo, f) }()
	waitForLockWaiter(t, repo, announcing)
	close(release)
	require.NoError(t, <-unsharing)
	require.NoError(t, <-announcing)

	require.Equal(t, []reliantv1.OrgUpdateType{shared, unshared}, chatFeed(t, repo, f),
		"no CHANGED follows the chat's UNSHARED")

	changedNow, err := repo.ApplyShareChange(ctx, shareChange(f, core.ShareActionShare))
	require.NoError(t, err)
	require.True(t, changedNow)
	require.NoError(t, announce(ctx, repo, f))
	require.Equal(t, []reliantv1.OrgUpdateType{shared, unshared, shared, changed}, chatFeed(t, repo, f),
		"shared again, its activity is in the feed")
}

// The announcement holds the feed when the unshare arrives: the unshare
// waits, and its UNSHARED follows the CHANGED.
func TestOrgFeed_AnUnshareAfterAnActivityComesLast(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	ctx := context.Background()

	entered, release := make(chan struct{}), make(chan struct{})
	announcing := holdOpen(repo, func(txCtx context.Context) error { return announce(txCtx, repo, f) }, entered, release)
	<-entered

	unsharing := make(chan error, 1)
	go func() {
		_, err := repo.ApplyShareChange(ctx, shareChange(f, core.ShareActionUnshare))
		unsharing <- err
	}()
	waitForLockWaiter(t, repo, unsharing)
	close(release)
	require.NoError(t, <-announcing)
	require.NoError(t, <-unsharing)

	require.Equal(t, []reliantv1.OrgUpdateType{shared, changed, unshared}, chatFeed(t, repo, f))
}
