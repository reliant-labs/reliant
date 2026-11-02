// Copyright (c) 2025 Reliant Labs
package project

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// mkdirRecordingRouter records every fs.mkdir path the heal asks a daemon for,
// and the machine it asked. err, when set, is what every mkdir fails with.
type mkdirRecordingRouter struct {
	toolexectest.Stub
	placesAsNamed
	mu       sync.Mutex
	mkdired  []string
	targets  []string
	unpinned []string
	err      error
}

// SendDaemonCommand is default resolution, which the heal must never use: it
// is about the machine that connected. It is recorded so a test can say so.
func (r *mkdirRecordingRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	r.mu.Lock()
	r.unpinned = append(r.unpinned, commandType)
	r.mu.Unlock()
	return nil, fmt.Errorf("%s sent by default resolution: the heal must pin it to the machine that connected", commandType)
}

func (r *mkdirRecordingRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	if commandType == "fs.mkdir" {
		var req struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(payload, &req)
		r.mu.Lock()
		r.mkdired = append(r.mkdired, req.Path)
		r.targets = append(r.targets, daemonID)
		r.mu.Unlock()
	}
	if r.err != nil {
		return nil, r.err
	}
	return json.Marshal(map[string]any{})
}

// healTestProjects creates n plain (healable) projects for userID.
func healTestProjects(t *testing.T, repo *db.Repo, userID string, n int) {
	t.Helper()
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
			ID: uuid.NewString(), UserID: userID, Name: fmt.Sprintf("plain-%d", i),
			Path:      "/home/workspace/projects/plain-" + uuid.NewString(),
			CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
	}
}

// The heal mkdirs on the machine that connected. Sent to the user's default
// machine, a cloud machine's connect healed the user's laptop instead.
func TestProjectDirHeal_TargetsTheConnectedDaemon(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	userID := "user-heal-" + uuid.NewString()
	healTestProjects(t, repo, userID, 2)

	router := &mkdirRecordingRouter{}
	svc := newProjectService(t, repo, router)
	require.NoError(t, svc.ensureProjectDirsOnDaemon(userID, "cloud-daemon"))

	router.mu.Lock()
	defer router.mu.Unlock()
	require.Equal(t, []string{"cloud-daemon", "cloud-daemon"}, router.targets)
}

// A machine that stops answering mid-heal stops the heal at the first mkdir
// rather than running every later one into the same dead link (prod
// 2026-10-30: fs.mkdir after fs.mkdir timing out at 30s), and says so, so the
// connect event is redelivered. A mkdir the daemon itself refuses does not
// stop it.
func TestProjectDirHeal_StopsAtTheFirstTransportFailure(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	userID := "user-heal-" + uuid.NewString()
	healTestProjects(t, repo, userID, 3)

	router := &mkdirRecordingRouter{err: fmt.Errorf("daemon command %q via NATS failed: %w", "fs.mkdir", toolexec.ErrDaemonTimeout)}
	svc := newProjectService(t, repo, router)
	require.Error(t, svc.ensureProjectDirsOnDaemon(userID, "laptop"))
	router.mu.Lock()
	require.Len(t, router.mkdired, 1, "the heal must stop at the first mkdir whose round trip failed")
	router.mu.Unlock()

	refusing := &mkdirRecordingRouter{err: toolexec.DaemonReportedFailure("fs.mkdir", "permission denied")}
	svc = newProjectService(t, repo, refusing)
	require.NoError(t, svc.ensureProjectDirsOnDaemon(userID, "laptop"))
	refusing.mu.Lock()
	defer refusing.mu.Unlock()
	require.Len(t, refusing.mkdired, 3, "a mkdir the daemon refused is that project's problem; the heal goes on")
}

// project-dir-heal runs on every daemon connect and creates each project's
// directory. For a project whose checkout comes from a repository, that is
// the defect behind the prod incident: a git.clone queued while the daemon
// was down never ran, the daemon connected, the heal created an EMPTY
// /home/workspace/projects/houndersclub, and the connect-time reconcile then
// saw a directory there and marked the install done. The owner was left with
// a project the product called installed and that had no files.
//
// A plain project (no remote) still has its directory healed — that is what
// the heal is for: an empty project created while the daemon was booting.
func TestProjectDirHeal_NeverCreatesARepositoryProjectsDirectory(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-heal-" + uuid.NewString()
	ctx := context.Background()
	now := time.Now().UTC()
	remote := "https://github.com/acme/widgets.git"

	cloned := &db.Project{
		ID: uuid.NewString(), UserID: userID, Name: "widgets",
		Path: "/home/workspace/projects/widgets-" + uuid.NewString(), RemoteURL: &remote,
		IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	plain := &db.Project{
		ID: uuid.NewString(), UserID: userID, Name: "notes",
		Path:      "/home/workspace/projects/notes-" + uuid.NewString(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, repo.CreateProject(ctx, cloned))
	require.NoError(t, repo.CreateProject(ctx, plain))

	router := &mkdirRecordingRouter{}
	svc := newProjectService(t, repo, router)
	require.NoError(t, svc.ensureProjectDirsOnDaemon(userID, "daemon-1"))

	router.mu.Lock()
	defer router.mu.Unlock()
	require.NotContains(t, router.mkdired, cloned.Path,
		"the heal must not create a repository project's directory; only a successful clone may")
	require.Contains(t, router.mkdired, plain.Path,
		"a plain project's directory is still healed — that is what the heal exists for")
	for _, d := range router.targets {
		require.Equal(t, "daemon-1", d, "the heal creates directories on the machine that connected, never another")
	}
}

// A connect event that names no machine heals nothing. There is no machine to
// pin the mkdirs to, and default resolution may name a machine other than the
// one that connected (a cloud machine's connect healed the user's laptop).
func TestProjectDirHeal_AConnectNamingNoMachineHealsNothing(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	userID := "user-heal-" + uuid.NewString()
	healTestProjects(t, repo, userID, 2)

	router := &mkdirRecordingRouter{}
	require.NoError(t, newProjectService(t, repo, router).ensureProjectDirsOnDaemon(userID, ""))

	router.mu.Lock()
	defer router.mu.Unlock()
	require.Empty(t, router.unpinned, "the heal must never fall back to the user's default machine")
	require.Empty(t, router.targets)
}

// A plain folder already on another machine lives there. An empty twin on
// the machine that connected would read as a second checkout (the connect
// reconcile records it installed), so the heal leaves it alone.
func TestProjectDirHeal_LeavesAFolderThatLivesOnAnotherMachine(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-heal-" + uuid.NewString()
	ctx := context.Background()
	now := time.Now().UTC()
	elsewhere := &db.Project{
		ID: uuid.NewString(), UserID: userID, Name: "notes",
		Path:      "/Users/me/notes-" + uuid.NewString(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, repo.CreateProject(ctx, elsewhere))
	laptop := &db.Daemon{ID: "laptop-" + uuid.NewString(), UserID: userID, Name: "laptop", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, repo.UpsertDaemon(ctx, laptop))
	require.NoError(t, repo.UpsertProjectDaemon(ctx, elsewhere.ID, laptop.ID, elsewhere.Path, nil))

	router := &mkdirRecordingRouter{}
	newProjectService(t, repo, router).ensureProjectDirsOnDaemon(userID, "cloud-1")

	router.mu.Lock()
	defer router.mu.Unlock()
	require.NotContains(t, router.mkdired, elsewhere.Path)
}

// Deps.Router is optional. Without one, a heal mkdir reports errNoDaemonRouter
// instead of panicking, whatever machine it names.
func TestProjectDirHeal_WithoutARouterReportsIt(t *testing.T) {
	svc := &Service{}
	for _, daemonID := range []string{"", "cloud-daemon"} {
		err := svc.healProjectDir(context.Background(), "user-1", daemonID, "/home/workspace/projects/p")
		require.ErrorIs(t, err, errNoDaemonRouter, "daemonID %q", daemonID)
	}
}

// Onboarding's default my-project is a plain folder with no remote. When the
// cloud machine that held it is replaced, the old machine's row can outlive
// it (rows written before removal released them, or a machine whose id
// churned). That leftover row must not count as "the folder lives
// elsewhere", or the replacement machine never gets the folder back and the
// project has no remote to clone from.
func TestProjectDirHeal_AReplacedMachinesLeftoverRowDoesNotBlockTheHeal(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-heal-" + uuid.NewString()
	ctx := context.Background()
	now := time.Now().UTC()
	myProject := &db.Project{
		ID: uuid.NewString(), UserID: userID, Name: "my-project",
		Path:      "/home/workspace/projects/my-project-" + uuid.NewString(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, repo.CreateProject(ctx, myProject))
	// The old cloud machine's row, whose machine no longer exists.
	require.NoError(t, repo.UpsertProjectDaemon(ctx, myProject.ID, "old-cloud-"+uuid.NewString(), myProject.Path, nil))
	// A row on a machine some other user owns is not this user's either.
	foreign := &db.Daemon{ID: "foreign-" + uuid.NewString(), UserID: "someone-else", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, repo.UpsertDaemon(ctx, foreign))
	require.NoError(t, repo.UpsertProjectDaemon(ctx, myProject.ID, foreign.ID, myProject.Path, nil))
	replacement := &db.Daemon{ID: "new-cloud-" + uuid.NewString(), UserID: userID, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, repo.UpsertDaemon(ctx, replacement))

	router := &mkdirRecordingRouter{}
	newProjectService(t, repo, router).ensureProjectDirsOnDaemon(userID, replacement.ID)

	router.mu.Lock()
	defer router.mu.Unlock()
	require.Equal(t, []string{myProject.Path}, router.mkdired, "my-project is healed onto the replacement machine")
	require.Equal(t, []string{replacement.ID}, router.targets)
}
