package project

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// machinesDisk is a set of machines, each with its own disk, behind one
// router: what a command does depends on the machine it reaches, which is
// the whole point of naming one.
type machinesDisk struct {
	toolexectest.Stub
	placesAsNamed
	defaultID string

	mu      sync.Mutex
	offline map[string]bool
	// dirs[machine][path] is a folder there, with its git remote ("" for
	// none); files[machine][path] is a file.
	dirs  map[string]map[string]string
	files map[string]map[string]bool
	// calls records "machine command" for every command that reached one.
	calls []string
}

func newMachinesDisk(defaultID string) *machinesDisk {
	return &machinesDisk{
		defaultID: defaultID,
		offline:   map[string]bool{},
		dirs:      map[string]map[string]string{},
		files:     map[string]map[string]bool{},
	}
}

func (m *machinesDisk) folder(machine, path, remote string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirs[machine] == nil {
		m.dirs[machine] = map[string]string{}
	}
	m.dirs[machine][path] = remote
}

func (m *machinesDisk) file(machine, path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files[machine] == nil {
		m.files[machine] = map[string]bool{}
	}
	m.files[machine][path] = true
}

func (m *machinesDisk) callsTo(machine string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, c := range m.calls {
		if len(c) > len(machine) && c[:len(machine)+1] == machine+" " {
			out = append(out, c[len(machine)+1:])
		}
	}
	return out
}

func (m *machinesDisk) ResolveDaemonID(context.Context, string) (string, error) {
	return m.defaultID, nil
}

func (m *machinesDisk) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return m.SendDaemonCommandToDaemon(ctx, userID, m.defaultID, commandType, payload, timeoutMs)
}

func (m *machinesDisk) SendDaemonCommandToDaemon(_ context.Context, _, machine, commandType string, payload []byte, _ int32) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, machine+" "+commandType)
	if m.offline[machine] {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("no daemon connected"))
	}
	var req struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(payload, &req)
	remote, isDir := m.dirs[machine][req.Path]
	switch commandType {
	case "fs.stat":
		return json.Marshal(map[string]any{"exists": isDir || m.files[machine][req.Path], "is_dir": isDir})
	case "fs.mkdir":
		if m.dirs[machine] == nil {
			m.dirs[machine] = map[string]string{}
		}
		if !isDir {
			m.dirs[machine][req.Path] = ""
		}
		return json.Marshal(map[string]any{})
	case "repo.discover":
		if !isDir || remote == "" {
			return json.Marshal(map[string]any{"discovered": []any{}})
		}
		return json.Marshal(map[string]any{"discovered": []any{
			map[string]any{"relative_path": "", "name": "repo", "remote_url": remote},
		}})
	case "project.init_git_repo":
		return json.Marshal(map[string]any{"success": false, "error": "not empty"})
	default:
		return json.Marshal(map[string]any{})
	}
}

// machinesFixture is a user with three machines — "laptop" (the default),
// "cloud" and "desktop" — and another user's machine.
type machinesFixture struct {
	repo                            *db.Repo
	disk                            *machinesDisk
	svc                             *Service
	ctx                             context.Context
	userID                          string
	laptop, cloud, desktop, foreign string
}

func newMachinesFixture(t *testing.T) *machinesFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := &machinesFixture{
		repo:    repo,
		userID:  "user-machines-" + uuid.NewString(),
		laptop:  "laptop-" + uuid.NewString(),
		cloud:   "cloud-" + uuid.NewString(),
		desktop: "desktop-" + uuid.NewString(),
		foreign: "foreign-" + uuid.NewString(),
	}
	f.ctx = context.WithValue(context.Background(), auth.UserIDContextKey, f.userID)
	for id, host := range map[string]string{f.laptop: "laptop.local", f.cloud: "cloud-box", f.desktop: "desktop.local"} {
		require.NoError(t, repo.UpsertDaemon(f.ctx, &db.Daemon{ID: id, UserID: f.userID, Hostname: &host}))
	}
	require.NoError(t, repo.UpsertDaemon(f.ctx, &db.Daemon{ID: f.foreign, UserID: "someone-else"}))
	f.disk = newMachinesDisk(f.laptop)
	f.svc = newProjectService(t, repo, f.disk)
	return f
}

// project creates a project at path whose checkout is on machine.
func (f *machinesFixture) project(t *testing.T, name, path, machine string, remote *string) *db.Project {
	t.Helper()
	now := time.Now().UTC()
	p := &db.Project{ID: uuid.NewString(), UserID: f.userID, Name: name, Path: path, RemoteURL: remote,
		CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, f.repo.CreateProject(f.ctx, p))
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, p.ID, machine, path, nil))
	f.disk.folder(machine, path, "")
	return p
}

func (f *machinesFixture) rows(t *testing.T, projectID string) map[string]string {
	t.Helper()
	rows, err := f.repo.ListProjectDaemonsForProject(f.ctx, projectID)
	require.NoError(t, err)
	out := map[string]string{}
	for _, r := range rows {
		out[r.DaemonID] = r.Path
	}
	return out
}

func codeOf(err error) connect.Code { return svcerr.ToConnect(err).Code() }

func TestCreateProject_NamedMachine_RunsEverythingThereAndRecordsItsRow(t *testing.T) {
	f := newMachinesFixture(t)
	path := "/home/workspace/projects/new-" + uuid.NewString()

	resp, err := f.svc.CreateProject(f.ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "new", Path: path, DaemonId: &f.cloud,
	}))
	require.NoError(t, err)

	assert.False(t, resp.Msg.Attached)
	require.NotNil(t, resp.Msg.ProjectDaemon)
	assert.Equal(t, f.cloud, resp.Msg.ProjectDaemon.DaemonId)
	assert.Equal(t, "cloud-box", resp.Msg.ProjectDaemon.DaemonName)
	assert.Equal(t, map[string]string{f.cloud: path}, f.rows(t, resp.Msg.Project.Id))
	assert.Empty(t, f.disk.callsTo(f.laptop), "nothing may reach the default machine")
	assert.Contains(t, f.disk.callsTo(f.cloud), "fs.mkdir")
}

func TestCreateProject_NoMachine_StillUsesTheDefaultMachine(t *testing.T) {
	f := newMachinesFixture(t)
	path := "/home/workspace/projects/default-" + uuid.NewString()

	resp, err := f.svc.CreateProject(f.ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{Name: "d", Path: path}))
	require.NoError(t, err)

	assert.Nil(t, resp.Msg.ProjectDaemon, "the response is unchanged for a request that names no machine")
	assert.Equal(t, map[string]string{f.laptop: path}, f.rows(t, resp.Msg.Project.Id))
	assert.Empty(t, f.disk.callsTo(f.cloud))
}

func TestCreateProject_NamedMachine_AttachesTheProjectAtThatPath(t *testing.T) {
	f := newMachinesFixture(t)
	path := "/home/workspace/projects/shared-" + uuid.NewString()
	existing := f.project(t, "shared", path, f.laptop, nil)
	f.disk.folder(f.cloud, path, "")

	resp, err := f.svc.CreateProject(f.ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "shared", Path: path, DaemonId: &f.cloud,
	}))
	require.NoError(t, err)

	assert.True(t, resp.Msg.Attached)
	assert.Equal(t, existing.ID, resp.Msg.Project.Id)
	assert.Equal(t, map[string]string{f.laptop: path, f.cloud: path}, f.rows(t, existing.ID))
	assert.NotContains(t, f.disk.callsTo(f.cloud), "fs.mkdir", "attaching a found project never creates its folder")
}

func TestCreateProject_NamedMachine_AttachesTheProjectOfTheFoldersRemote(t *testing.T) {
	f := newMachinesFixture(t)
	remote := "https://github.com/acme/properties-" + uuid.NewString() + ".git"
	existing := f.project(t, "properties", "/home/workspace/properties-"+uuid.NewString(), f.cloud, &remote)
	local := "/Users/me/properties-" + uuid.NewString()
	f.disk.folder(f.laptop, local, remote)

	resp, err := f.svc.CreateProject(f.ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "properties", Path: local, DaemonId: &f.laptop,
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Attached)
	assert.Equal(t, existing.ID, resp.Msg.Project.Id)
	assert.Equal(t, map[string]string{f.cloud: existing.Path, f.laptop: local}, f.rows(t, existing.ID))
}

func TestSameRemote_IgnoresSpellingOfOneRepository(t *testing.T) {
	assert.True(t, sameRemote("https://github.com/Acme/x.git", "https://github.com/Acme/x"))
	assert.True(t, sameRemote("git@github.com:Acme/x.git", "https://GitHub.com/Acme/x"))
	assert.True(t, sameRemote("https://token@github.com/Acme/x.git", "https://github.com/Acme/x/"))
	assert.False(t, sameRemote("https://github.com/Acme/x.git", "https://github.com/Acme/y.git"))
}

func TestCreateProject_NamedMachine_ProjectAlreadyThereIsAlreadyExists(t *testing.T) {
	f := newMachinesFixture(t)
	path := "/home/workspace/projects/here-" + uuid.NewString()
	f.project(t, "here", path, f.cloud, nil)

	_, err := f.svc.CreateProject(f.ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "here", Path: path, DaemonId: &f.cloud,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, codeOf(err))
}

func TestCreateProject_NamedMachine_RefusesAnotherUsersMachineAndAnOfflineOne(t *testing.T) {
	f := newMachinesFixture(t)
	path := "/home/workspace/projects/x-" + uuid.NewString()

	_, err := f.svc.CreateProject(f.ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{Name: "x", Path: path, DaemonId: &f.foreign}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, codeOf(err))

	f.disk.offline[f.desktop] = true
	_, err = f.svc.CreateProject(f.ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{Name: "x", Path: path, DaemonId: &f.desktop}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, codeOf(err))
	_, lookupErr := f.repo.GetProjectByPathAndUser(f.ctx, path, f.userID)
	assert.Error(t, lookupErr, "nothing is recorded for a machine that did not answer")
}

func TestAttachProjectToDaemon_RecordsAnExistingFolder(t *testing.T) {
	f := newMachinesFixture(t)
	remote := "https://github.com/acme/a-" + uuid.NewString() + ".git"
	p := f.project(t, "a", "/home/workspace/a-"+uuid.NewString(), f.cloud, &remote)
	local := "/Users/me/a-" + uuid.NewString()
	f.disk.folder(f.laptop, local, remote)

	resp, err := f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: p.ID, DaemonId: f.laptop, Path: local,
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.Warnings)
	assert.Equal(t, reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLED, resp.Msg.ProjectDaemon.InstallState)
	assert.NotNil(t, resp.Msg.ProjectDaemon.VerifiedAt)
	assert.Equal(t, local, f.rows(t, p.ID)[f.laptop])
	assert.NotContains(t, f.disk.callsTo(f.laptop), "fs.mkdir")
}

func TestAttachProjectToDaemon_RefusesAMissingFolderOrAFile(t *testing.T) {
	f := newMachinesFixture(t)
	p := f.project(t, "b", "/home/workspace/b-"+uuid.NewString(), f.cloud, nil)

	missing := "/Users/me/typo-" + uuid.NewString()
	_, err := f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: p.ID, DaemonId: f.laptop, Path: missing,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	assert.Contains(t, err.Error(), "does not exist on")

	file := "/Users/me/notes-" + uuid.NewString() + ".txt"
	f.disk.file(f.laptop, file)
	_, err = f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: p.ID, DaemonId: f.laptop, Path: file,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, codeOf(err))

	_, recorded := f.rows(t, p.ID)[f.laptop]
	assert.False(t, recorded, "a refused attach records nothing")

	f.disk.offline[f.desktop] = true
	_, err = f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: p.ID, DaemonId: f.desktop, Path: "/x",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, codeOf(err))
}

func TestAttachProjectToDaemon_WarnsOnAFolderOfAnotherRemote(t *testing.T) {
	f := newMachinesFixture(t)
	remote := "https://github.com/acme/c.git"
	p := f.project(t, "c", "/home/workspace/c-"+uuid.NewString(), f.cloud, &remote)
	local := "/Users/me/c-" + uuid.NewString()
	f.disk.folder(f.laptop, local, "https://github.com/acme/other.git")

	resp, err := f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: p.ID, DaemonId: f.laptop, Path: local,
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Warnings, 1)
	assert.Contains(t, resp.Msg.Warnings[0], "acme/other")
}

func TestAttachProjectToDaemon_RefusesAnotherProjectsFolderAndAForeignMachine(t *testing.T) {
	f := newMachinesFixture(t)
	path := "/home/workspace/d-" + uuid.NewString()
	f.project(t, "d", path, f.cloud, nil)
	other := f.project(t, "e", "/home/workspace/e-"+uuid.NewString(), f.laptop, nil)

	_, err := f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: other.ID, DaemonId: f.cloud, Path: path,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, codeOf(err))

	_, err = f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: other.ID, DaemonId: f.foreign, Path: path,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, codeOf(err))
}

func TestDetachProjectFromDaemon_ForgetsTheRowAndKeepsTheFiles(t *testing.T) {
	f := newMachinesFixture(t)
	path := "/home/workspace/f-" + uuid.NewString()
	p := f.project(t, "f", path, f.cloud, nil)
	local := "/Users/me/f-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, p.ID, f.laptop, local, nil))
	f.disk.folder(f.laptop, local, "")

	_, err := f.svc.DetachProjectFromDaemon(f.ctx, connect.NewRequest(&reliantv1.DetachProjectFromDaemonRequest{
		ProjectId: p.ID, DaemonId: f.laptop,
	}))
	require.NoError(t, err)

	assert.Equal(t, map[string]string{f.cloud: path}, f.rows(t, p.ID))
	assert.Empty(t, f.disk.callsTo(f.laptop), "a detach sends the machine nothing: its files are not touched")
	detached, err := f.repo.ListProjectDaemonDetachmentsForDaemon(f.ctx, f.laptop)
	require.NoError(t, err)
	require.Len(t, detached, 1)
	assert.Equal(t, local, detached[0].Path)

	// Idempotent.
	_, err = f.svc.DetachProjectFromDaemon(f.ctx, connect.NewRequest(&reliantv1.DetachProjectFromDaemonRequest{
		ProjectId: p.ID, DaemonId: f.laptop,
	}))
	require.NoError(t, err)

	// The connect heal leaves the detached pair alone.
	require.NoError(t, f.svc.ensureProjectDirsOnDaemon(f.userID, f.laptop))
	_, recorded := f.rows(t, p.ID)[f.laptop]
	assert.False(t, recorded)

	// Attaching it again ends the detachment.
	_, err = f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: p.ID, DaemonId: f.laptop, Path: local,
	}))
	require.NoError(t, err)
	detached, err = f.repo.ListProjectDaemonDetachmentsForDaemon(f.ctx, f.laptop)
	require.NoError(t, err)
	assert.Empty(t, detached)
}

func TestDetachProjectFromDaemon_RefusesTheOnlyMachineAndOneWithWorkspaces(t *testing.T) {
	f := newMachinesFixture(t)
	p := f.project(t, "g", "/home/workspace/g-"+uuid.NewString(), f.cloud, nil)

	_, err := f.svc.DetachProjectFromDaemon(f.ctx, connect.NewRequest(&reliantv1.DetachProjectFromDaemonRequest{
		ProjectId: p.ID, DaemonId: f.cloud,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	assert.Contains(t, err.Error(), "only machine")

	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, p.ID, f.laptop, "/Users/me/g", nil))
	now := time.Now().UTC()
	require.NoError(t, f.repo.CreateWorktree(f.ctx, &db.Worktree{
		ID: uuid.NewString(), Name: "feature", Path: "/home/workspace/.reliant/worktrees/x", Branch: "feature",
		BaseBranch: "main", ProjectID: p.ID, DaemonID: &f.cloud,
		Status:    int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err = f.svc.DetachProjectFromDaemon(f.ctx, connect.NewRequest(&reliantv1.DetachProjectFromDaemonRequest{
		ProjectId: p.ID, DaemonId: f.cloud,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	assert.Contains(t, err.Error(), "workspace")
	assert.Len(t, f.rows(t, p.ID), 2, "a refused detach changes nothing")
}

func TestListProjectDaemons_NamesEachMachineAndWhenItWasVerified(t *testing.T) {
	f := newMachinesFixture(t)
	p := f.project(t, "h", "/home/workspace/h-"+uuid.NewString(), f.cloud, nil)

	resp, err := f.svc.ListProjectDaemons(f.ctx, connect.NewRequest(&reliantv1.ListProjectDaemonsRequest{}))
	require.NoError(t, err)
	var found *reliantv1.ProjectDaemon
	for _, pd := range resp.Msg.ProjectDaemons {
		if pd.ProjectId == p.ID {
			found = pd
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, "cloud-box", found.DaemonName)
	require.NotNil(t, found.VerifiedAt)
	_, err = time.Parse(time.RFC3339, *found.VerifiedAt)
	assert.NoError(t, err)
}

// Two detaches of a project's last two checkouts, at once: the guard runs
// under the project's catalog lock, so the second sees the first's commit and
// is refused. One checkout always survives.
func TestDetachProjectFromDaemon_ConcurrentDetachesLeaveOneCheckout(t *testing.T) {
	f := newMachinesFixture(t)
	p := f.project(t, "race", "/home/workspace/race-"+uuid.NewString(), f.cloud, nil)
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, p.ID, f.laptop, "/Users/me/race", nil))

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{f.cloud, f.laptop} {
		wg.Add(1)
		go func(daemonID string) {
			defer wg.Done()
			_, err := f.svc.DetachProjectFromDaemon(f.ctx, connect.NewRequest(&reliantv1.DetachProjectFromDaemonRequest{
				ProjectId: p.ID, DaemonId: daemonID,
			}))
			errs <- err
		}(id)
	}
	wg.Wait()
	close(errs)
	var ok, refused int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case codeOf(err) == connect.CodeFailedPrecondition && strings.Contains(err.Error(), "only machine"):
			refused++
		default:
			t.Fatalf("unexpected detach error: %v", err)
		}
	}
	assert.Equal(t, 1, ok)
	assert.Equal(t, 1, refused)
	assert.Len(t, f.rows(t, p.ID), 1, "one checkout survives")
}

// moveBeforeDetach is the repository with an attach that moves the checkout
// landing between the detach's unlocked read and its locked one.
type moveBeforeDetach struct {
	Repository
	move func(ctx context.Context)
}

func (m moveBeforeDetach) DetachProjectFromDaemon(ctx context.Context, projectID, daemonID, path string, guard func(context.Context) error) error {
	m.move(ctx)
	return m.Repository.DetachProjectFromDaemon(ctx, projectID, daemonID, path, guard)
}

// A checkout moved by a concurrent attach is not detached under its old path:
// that would delete the new row and tombstone the old path, and the moved
// checkout's watcher would then make a duplicate project. The detach is
// refused, nothing changes, and a retry detaches the new path.
func TestDetachProjectFromDaemon_RefusesACheckoutMovedUnderIt(t *testing.T) {
	f := newMachinesFixture(t)
	p := f.project(t, "m", "/home/workspace/m-"+uuid.NewString(), f.cloud, nil)
	oldPath, newPath := "/Users/me/m-"+uuid.NewString(), "/Users/me/moved-"+uuid.NewString()
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, p.ID, f.laptop, oldPath, nil))
	svc := newProjectService(t, moveBeforeDetach{Repository: f.repo, move: func(ctx context.Context) {
		require.NoError(t, f.repo.UpsertProjectDaemon(ctx, p.ID, f.laptop, newPath, nil))
	}}, f.disk)

	_, err := svc.DetachProjectFromDaemon(f.ctx, connect.NewRequest(&reliantv1.DetachProjectFromDaemonRequest{
		ProjectId: p.ID, DaemonId: f.laptop,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAborted, codeOf(err))
	assert.Equal(t, newPath, f.rows(t, p.ID)[f.laptop], "the moved checkout's row stays")
	detached, err := f.repo.ListProjectDaemonDetachmentsForDaemon(f.ctx, f.laptop)
	require.NoError(t, err)
	assert.Empty(t, detached, "no tombstone for the path it moved away from")

	_, err = f.svc.DetachProjectFromDaemon(f.ctx, connect.NewRequest(&reliantv1.DetachProjectFromDaemonRequest{
		ProjectId: p.ID, DaemonId: f.laptop,
	}))
	require.NoError(t, err)
	detached, err = f.repo.ListProjectDaemonDetachmentsForDaemon(f.ctx, f.laptop)
	require.NoError(t, err)
	require.Len(t, detached, 1)
	assert.Equal(t, newPath, detached[0].Path)
}
