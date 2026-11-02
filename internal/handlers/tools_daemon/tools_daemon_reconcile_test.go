// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// TestReconcileProjectDaemonsUpsertsOnlyExistingPaths drives the reconcile core
// with a user owning three projects — two present on the daemon, one absent —
// and asserts project_daemons rows are written for exactly the two that exist,
// and that remote_url is backfilled where it was NULL.
func TestReconcileProjectDaemonsUpsertsOnlyExistingPaths(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const userID = "reconcile-user"
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))

	now := time.Now().UTC()
	// present1: exists, no remote yet -> should upsert + backfill remote.
	present1 := &db.Project{ID: uuid.NewString(), UserID: userID, Name: "present1",
		Path: "/tmp/present1-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	// present2: exists, already has a remote -> upsert, no backfill.
	existingRemote := "https://github.com/acme/present2.git"
	present2 := &db.Project{ID: uuid.NewString(), UserID: userID, Name: "present2",
		Path: "/tmp/present2-" + uuid.NewString(), RemoteURL: &existingRemote,
		CreatedAt: now, UpdatedAt: now, LastActive: now}
	// absent: not on the daemon -> no row.
	absent := &db.Project{ID: uuid.NewString(), UserID: userID, Name: "absent",
		Path: "/tmp/absent-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}

	for _, p := range []*db.Project{present1, present2, absent} {
		require.NoError(t, repo.CreateProject(ctx, p))
	}

	backfillRemote := "https://github.com/acme/present1.git"
	check := func(_ context.Context, path string) (projectPathState, error) {
		switch path {
		case present1.Path:
			return projectPathState{Exists: true, RemoteURL: backfillRemote}, nil
		case present2.Path:
			return projectPathState{Exists: true, RemoteURL: "https://github.com/acme/other.git"}, nil
		default:
			return projectPathState{Exists: false}, nil
		}
	}

	reconcileProjectDaemons(ctx, repo, daemonID, userID, check)

	// present1 + present2 have rows; absent does not.
	rows1, err := repo.ListProjectDaemonsForProject(ctx, present1.ID)
	require.NoError(t, err)
	require.Len(t, rows1, 1)
	require.Equal(t, daemonID, rows1[0].DaemonID)
	require.Equal(t, present1.Path, rows1[0].Path)

	rows2, err := repo.ListProjectDaemonsForProject(ctx, present2.ID)
	require.NoError(t, err)
	require.Len(t, rows2, 1)

	rowsAbsent, err := repo.ListProjectDaemonsForProject(ctx, absent.ID)
	require.NoError(t, err)
	require.Empty(t, rowsAbsent)

	// remote_url backfilled for present1 (was NULL); present2 untouched.
	got1, err := repo.GetProjectWithUserCheck(ctx, present1.ID, userID)
	require.NoError(t, err)
	require.NotNil(t, got1.RemoteURL)
	require.Equal(t, backfillRemote, *got1.RemoteURL)

	got2, err := repo.GetProjectWithUserCheck(ctx, present2.ID, userID)
	require.NoError(t, err)
	require.NotNil(t, got2.RemoteURL)
	require.Equal(t, existingRemote, *got2.RemoteURL)
}

// TestReconcileProjectDaemonsSkipsRemoteBackfillOnCollision verifies the partial
// unique index (user_id, remote_url) is respected: if another project already
// holds the discovered remote for the user, backfill is skipped, not errored.
func TestReconcileProjectDaemonsSkipsRemoteBackfillOnCollision(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const userID = "collision-user"
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))

	now := time.Now().UTC()
	sharedRemote := "https://github.com/acme/shared.git"
	// owner already holds sharedRemote.
	owner := &db.Project{ID: uuid.NewString(), UserID: userID, Name: "owner",
		Path: "/tmp/owner-" + uuid.NewString(), RemoteURL: &sharedRemote,
		CreatedAt: now, UpdatedAt: now, LastActive: now}
	// candidate exists on disk and discover resolves the SAME remote.
	candidate := &db.Project{ID: uuid.NewString(), UserID: userID, Name: "candidate",
		Path: "/tmp/candidate-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}

	for _, p := range []*db.Project{owner, candidate} {
		require.NoError(t, repo.CreateProject(ctx, p))
	}

	check := func(_ context.Context, path string) (projectPathState, error) {
		if path == candidate.Path {
			return projectPathState{Exists: true, RemoteURL: sharedRemote}, nil
		}
		return projectPathState{Exists: true, RemoteURL: ""}, nil
	}

	reconcileProjectDaemons(ctx, repo, daemonID, userID, check)

	// candidate got its project_daemons row despite the remote collision.
	rows, err := repo.ListProjectDaemonsForProject(ctx, candidate.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// candidate.remote_url stays NULL — backfill skipped to avoid collision.
	got, err := repo.GetProjectWithUserCheck(ctx, candidate.ID, userID)
	require.NoError(t, err)
	require.True(t, got.RemoteURL == nil || *got.RemoteURL == "")
}

// A machine's own row is the record of its checkout, and a directory at the
// creation path does not overrule it: a machine whose clone is elsewhere
// keeps that path, and a clone still landing is not marked done because
// something sits at the creation path. Only a checkout the machine had no
// row for is recorded, and returned so its config gets watched this session.
func TestReconcileProjectDaemonsNeverOverwritesTheMachinesRecord(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const userID = "reconcile-record-user"
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	now := time.Now().UTC()
	project := func(name string) *db.Project {
		p := &db.Project{ID: uuid.NewString(), UserID: userID, Name: name,
			Path: "/home/workspace/projects/" + name + "-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
		require.NoError(t, repo.CreateProject(ctx, p))
		return p
	}
	movedClone, landing, fresh := project("moved"), project("landing"), project("fresh")
	require.NoError(t, repo.UpsertProjectDaemon(ctx, movedClone.ID, daemonID, "/srv/clones/moved", nil))
	require.NoError(t, repo.UpsertQueuedProjectDaemon(ctx, landing.ID, daemonID, landing.Path, nil, "clone:"+landing.ID))

	everythingExists := func(context.Context, string) (projectPathState, error) {
		return projectPathState{Exists: true}, nil
	}
	_, added, err := reconcileProjectDaemons(ctx, repo, daemonID, userID, everythingExists)
	require.NoError(t, err)
	require.Equal(t, []string{fresh.Path}, added, "only the checkout the machine had no row for is new")

	rowOf := func(p *db.Project) *core.ProjectDaemon {
		rows, err := repo.ListProjectDaemonsForProject(ctx, p.ID)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0]
	}
	require.Equal(t, "/srv/clones/moved", rowOf(movedClone).Path, "the recorded clone elsewhere is kept")
	require.Equal(t, core.ProjectInstallInstalling, rowOf(landing).InstallState, "a clone in flight settles through its own outcome")
	require.Equal(t, fresh.Path, rowOf(fresh).Path)
}

// A clone whose outcome was lost (the machine suspended mid-clone) would hold
// its row in "installing" for good, and every machine call on that machine
// would be refused with "still being cloned". Past staleInstallAfter the
// connect reconcile settles it from the row's OWN path: installed when the
// checkout landed there, failed (so the refusal offers to clone again) when
// nothing did. A clone queued recently — a re-clone included — is left to its
// own outcome.
func TestReconcileSettlesAStaleInstallFromItsOwnPath(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const userID = "reconcile-stale-user"
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	now := time.Now().UTC()
	project := func(name string) *db.Project {
		p := &db.Project{ID: uuid.NewString(), UserID: userID, Name: name,
			Path: "/home/workspace/projects/" + name + "-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
		require.NoError(t, repo.CreateProject(ctx, p))
		return p
	}
	landed, lost, requeued, recent := project("landed"), project("lost"), project("requeued"), project("recent")
	// Each clone targets its own path on the machine, not the creation path.
	clonePath := func(p *db.Project) string { return "/srv/clones/" + p.ID }
	for _, p := range []*db.Project{landed, lost, requeued, recent} {
		require.NoError(t, repo.UpsertQueuedProjectDaemon(ctx, p.ID, daemonID, clonePath(p), nil, "clone:"+p.ID))
	}
	// Queued two hours ago to a machine that was connected then, so the
	// clone could start at once.
	age := func(p *db.Project) {
		_, err := repo.ORM().Bun().NewRaw(`UPDATE project_daemons SET cloned_at = NOW() - interval '2 hours', clone_started_at = NOW() - interval '2 hours' WHERE project_id = ? AND daemon_id = ?`, p.ID, daemonID).Exec(ctx)
		require.NoError(t, err)
	}
	age(landed)
	age(lost)
	age(requeued)
	// A re-clone of a row queued long ago is a new clone: queued now.
	require.NoError(t, repo.UpsertQueuedProjectDaemon(ctx, requeued.ID, daemonID, clonePath(requeued), nil, "clone-again:"+requeued.ID))

	var checked []string
	onDisk := func(_ context.Context, path string) (projectPathState, error) {
		checked = append(checked, path)
		return projectPathState{Exists: path == clonePath(landed)}, nil
	}
	_, added, err := reconcileProjectDaemons(ctx, repo, daemonID, userID, onDisk)
	require.NoError(t, err)
	require.Equal(t, []string{clonePath(landed)}, added, "the settled checkout is watched from now")
	require.NotContains(t, checked, landed.Path, "a clone is judged at its own path, never the creation path")

	rowOf := func(p *db.Project) *core.ProjectDaemon {
		rows, err := repo.ListProjectDaemonsForProject(ctx, p.ID)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0]
	}
	require.Equal(t, core.ProjectInstallInstalled, rowOf(landed).InstallState)
	require.Equal(t, clonePath(landed), rowOf(landed).Path)
	require.Equal(t, core.ProjectInstallFailed, rowOf(lost).InstallState)
	require.Equal(t, staleInstallReason, rowOf(lost).InstallError)
	require.Equal(t, core.ProjectInstallInstalling, rowOf(requeued).InstallState, "a re-clone queued just now is not stale")
	require.Equal(t, core.ProjectInstallInstalling, rowOf(recent).InstallState, "a clone queued just now settles through its own outcome")

	// A late outcome still lands on the row settled as failed.
	require.NoError(t, repo.ResolveQueuedProjectDaemon(ctx, "clone:"+lost.ID, ""))
	require.Equal(t, core.ProjectInstallInstalled, rowOf(lost).InstallState)
}

// A clone queued to a sleeping machine starts only when the machine wakes, so
// its stuck-clone timeout counts from that connect, not from the queue time.
// It used to count from the queue: a machine asleep for more than the timeout
// woke to the reconcile marking its just-starting clone failed ("did not
// report back") until the clone's own outcome corrected it. A clone queued to
// a machine that is connected can start at once, and its clock starts then.
func TestReconcileTimesAStuckCloneFromTheConnectThatLetItStart(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const userID = "reconcile-asleep-user"
	asleep, awake := uuid.NewString(), uuid.NewString()
	for _, id := range []string{asleep, awake} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: userID}))
	}
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{DaemonID: awake, UserID: userID, Source: db.DaemonAttachmentSourceInbound}))
	now := time.Now().UTC()
	p := &db.Project{ID: uuid.NewString(), UserID: userID, Name: "queued",
		Path: "/home/workspace/projects/queued-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateProject(ctx, p))
	clonePath := "/srv/clones/" + p.ID
	rowOn := func(daemonID string) *core.ProjectDaemon {
		rows, err := repo.ListProjectDaemonsForDaemon(ctx, daemonID)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0]
	}

	// Queued to the connected machine: it can start now.
	require.NoError(t, repo.UpsertQueuedProjectDaemon(ctx, p.ID, awake, clonePath, nil, "clone:awake"))
	require.NotNil(t, rowOn(awake).CloneStartedAt, "a clone queued to a connected machine can start at once")

	// Queued two hours ago to the sleeping machine: it has not started.
	require.NoError(t, repo.UpsertQueuedProjectDaemon(ctx, p.ID, asleep, clonePath, nil, "clone:asleep"))
	require.Nil(t, rowOn(asleep).CloneStartedAt, "a clone queued to a sleeping machine waits for it")
	_, err := repo.ORM().Bun().NewRaw(`UPDATE project_daemons SET cloned_at = NOW() - interval '2 hours' WHERE daemon_id = ?`, asleep).Exec(ctx)
	require.NoError(t, err)

	nothingOnDisk := func(context.Context, string) (projectPathState, error) { return projectPathState{}, nil }

	// The machine wakes. Its clone starts now, so it is not stuck, however
	// long ago it was queued.
	_, _, err = reconcileProjectDaemons(ctx, repo, asleep, userID, nothingOnDisk)
	require.NoError(t, err)
	woke := rowOn(asleep)
	require.Equal(t, core.ProjectInstallInstalling, woke.InstallState, "the clone the wake just started is not settled")
	require.NotNil(t, woke.CloneStartedAt, "the waking connect starts the clone's clock")
	require.WithinDuration(t, time.Now(), *woke.CloneStartedAt, time.Minute)

	// A later connect within the timeout keeps the clock it started.
	_, _, err = reconcileProjectDaemons(ctx, repo, asleep, userID, nothingOnDisk)
	require.NoError(t, err)
	require.Equal(t, core.ProjectInstallInstalling, rowOn(asleep).InstallState)
	require.True(t, woke.CloneStartedAt.Equal(*rowOn(asleep).CloneStartedAt), "a connect does not restart a clock already running")

	// The machine suspends mid-clone and the outcome is lost. A connect
	// more than the timeout after the clone started settles it.
	_, err = repo.ORM().Bun().NewRaw(`UPDATE project_daemons SET clone_started_at = NOW() - interval '31 minutes' WHERE daemon_id = ?`, asleep).Exec(ctx)
	require.NoError(t, err)
	_, _, err = reconcileProjectDaemons(ctx, repo, asleep, userID, nothingOnDisk)
	require.NoError(t, err)
	require.Equal(t, core.ProjectInstallFailed, rowOn(asleep).InstallState)
	require.Equal(t, staleInstallReason, rowOn(asleep).InstallError)
}

// A checkout the connect reconcile records gets its project config (skills,
// memory, MCP servers) loaded and watched this session. Registration asked
// only for the checkouts the machine had on record before the pass, so
// without this a newly recorded one went unwatched until the next reconnect.
func TestReconcileWatchesTheCheckoutsItRecords(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const userID = "reconcile-watch-user"
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	now := time.Now().UTC()
	known := &db.Project{ID: uuid.NewString(), UserID: userID, Name: "known", Path: "/tmp/known-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	fresh := &db.Project{ID: uuid.NewString(), UserID: userID, Name: "fresh", Path: "/tmp/fresh-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	for _, p := range []*db.Project{known, fresh} {
		require.NoError(t, repo.CreateProject(ctx, p))
	}
	require.NoError(t, repo.UpsertProjectDaemon(ctx, known.ID, daemonID, known.Path, nil))

	svc := newTestService(repo)
	conn := &daemonConnection{userID: userID, daemonID: daemonID, sendCh: make(chan *reliantv1.ServerMessage, 32), done: make(chan struct{})}
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()

	svc.reconcileAndWatch(ctx, conn, func(context.Context, string) (projectPathState, error) {
		return projectPathState{Exists: true}, nil
	})

	var watched []string
	for len(conn.sendCh) > 0 {
		if w := (<-conn.sendCh).GetWatchProjectConfigs(); w != nil {
			watched = append(watched, w.ProjectPath)
		}
	}
	require.Equal(t, []string{fresh.Path}, watched, "the newly recorded checkout is watched; the known one already was")
}
