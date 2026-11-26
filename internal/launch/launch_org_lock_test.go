// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/require"
)

// A launch takes its owner's org lock shared: one owner's launches only need
// the orgs they read to stay put, and queued behind one another on an
// exclusive lock they made a StartChat wait out every launch transaction of
// the same owner ahead of it (a burst of that owner's trigger fires).
func TestLaunchDoesNotQueueBehindTheOwnersOtherLaunch(t *testing.T) {
	repo, _, projectID, _ := launchFixture(t)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})
	params := mockModelParams(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inTx, release := make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
			OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
			Params: params, Messages: userSeed("first"),
			Guard: func(context.Context) (string, error) {
				close(inTx)
				<-release
				return "", nil
			},
		})
		firstDone <- err
	}()
	select {
	case <-inTx:
	case err := <-firstDone:
		t.Fatalf("the first launch ended before its transaction held the org lock: %v", err)
	case <-ctx.Done():
		t.Fatal("the first launch never reached its guard")
	}

	// The first launch holds the org lock, mid-transaction. The second must
	// not wait for it.
	second, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: params, Messages: userSeed("second"),
	})
	require.NoError(t, err, "a launch queued behind the same owner's launch in flight")
	require.NotNil(t, second.Chat)

	close(release)
	require.NoError(t, <-firstDone)
}

// Shared is still the org lock: a launch waits for an exclusive holder (the
// backfill, a project move) and never writes across one.
func TestLaunchWaitsForTheOrgLocksExclusiveHolder(t *testing.T) {
	repo, _, projectID, _ := launchFixture(t)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})
	params := mockModelParams(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	held, release := make(chan struct{}), make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
			if err := repo.LockUserOrgs(ctx, launchTestUserID); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-holderDone:
		t.Fatalf("the exclusive holder ended before it held the lock: %v", err)
	case <-ctx.Done():
		t.Fatal("the exclusive holder never took the org lock")
	}

	launched := make(chan error, 1)
	go func() {
		_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
			OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
			Params: params, Messages: userSeed("hello"),
		})
		launched <- err
	}()
	require.Eventually(t, func() bool {
		var waiting int
		err := repo.ORM().Bun().NewRaw(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE l.locktype = 'advisory' AND NOT l.granted AND a.datname = current_database()`).Scan(ctx, &waiting)
		return err == nil && waiting > 0
	}, 10*time.Second, 5*time.Millisecond, "the launch never queued on the org lock")
	select {
	case err := <-launched:
		t.Fatalf("the launch finished while an exclusive holder held its owner's org lock: %v", err)
	default:
	}

	close(release)
	require.NoError(t, <-holderDone)
	require.NoError(t, <-launched, "the launch runs once the exclusive holder commits")
}
