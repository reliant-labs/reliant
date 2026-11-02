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
