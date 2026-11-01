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
// and which daemon it asked ("" for the user's default). err, when set, is
// what every mkdir fails with.
type mkdirRecordingRouter struct {
	toolexectest.Stub
	mu      sync.Mutex
	mkdired []string
	targets []string
	err     error
}

func (r *mkdirRecordingRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommandToDaemon(ctx, userID, "", commandType, payload, timeoutMs)
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
}
