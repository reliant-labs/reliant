// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// wholeConfigRowReads is a real repository with one read recorded: the read
// of the WHOLE project config record, with the function that made it. That
// record carries every skill body and repo memory the daemon indexed — 18 MB
// in prod — and none of the paths below needs more than one small column.
//
// It embeds the concrete *db.Repo, so every other read is real. Repository
// (deps.go) does not declare GetProjectConfigRecord, so the compiler already
// keeps the gateway off the whole row; the spy catches the role growing it
// back.
type wholeConfigRowReads struct {
	*db.Repo

	mu      sync.Mutex
	callers []string
}

func (r *wholeConfigRowReads) GetProjectConfigRecord(ctx context.Context, projectID string) (*db.ProjectConfigRecord, error) {
	pc, _, _, _ := runtime.Caller(1)
	r.mu.Lock()
	r.callers = append(r.callers, runtime.FuncForPC(pc).Name())
	r.mu.Unlock()
	return r.Repo.GetProjectConfigRecord(ctx, projectID)
}

func (r *wholeConfigRowReads) reads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.callers...)
}

// bigSkillsJSON stands in for the prod row's payload: the column nothing below
// needs, and the reason a whole-row read is expensive.
func bigSkillsJSON() *string {
	s := `[{"name":"big","body":"` + strings.Repeat("x", 64<<10) + `"}]`
	return &s
}

// The daemon config sync drops a snapshot or delta older than the stored one.
// It runs on every push, and needs only the stored pushed_at — it used to read
// the whole record to get it.
func TestDaemonConfigSync_StalenessCheckReadsOnlyPushedAt(t *testing.T) {
	repo := db.NewTestRepo(t)
	spy := &wholeConfigRowReads{Repo: repo}
	svc := newTestService(spy)
	defer svc.Close()
	ctx := context.Background()

	now := time.Now().UTC()
	daemonID := uuid.NewString()
	projectID := uuid.NewString()
	projectPath := "/tmp/config-row-tail-" + uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: "test-user"}))
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "tail", Path: projectPath, IsGitRepo: true,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
		ProjectID: projectID, DaemonID: daemonID,
		UserConfigYAML:    testStringPtr("user: existing"),
		ProjectSkillsJSON: bigSkillsJSON(),
		PushedAt:          time.UnixMilli(2000).UTC(),
	}, false)
	require.NoError(t, err)

	conn := &daemonConnection{
		userID: "test-user", daemonID: daemonID,
		sendCh: make(chan *reliantv1.ServerMessage, 8), done: make(chan struct{}),
	}
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()

	// A delta older than the stored push is dropped; a newer one asks the
	// daemon for its snapshot.
	require.NoError(t, svc.handleProjectConfigDelta(ctx, conn, &reliantv1.ProjectConfigDelta{
		ProjectPath: projectPath, ConfigVersion: "v1", DaemonTimestampUnixMs: 1000,
	}))
	select {
	case <-conn.sendCh:
		t.Fatal("a delta older than the stored push must be dropped")
	default:
	}
	require.NoError(t, svc.handleProjectConfigDelta(ctx, conn, &reliantv1.ProjectConfigDelta{
		ProjectPath: projectPath, ConfigVersion: "v3", DaemonTimestampUnixMs: 3000,
	}))
	select {
	case msg := <-conn.sendCh:
		require.NotNil(t, msg.GetLoadProjectConfigs(), "a newer delta must request the snapshot")
	default:
		t.Fatal("a newer delta must request the snapshot")
	}

	// The same check gates snapshots: an older one is not written, a newer one is.
	snapshot := func(ts int64, userYAML string) *reliantv1.ProjectConfigSnapshot {
		return &reliantv1.ProjectConfigSnapshot{
			ProjectPath: projectPath, ConfigVersion: "v", DaemonTimestampUnixMs: ts,
			UserConfigYaml: []byte(userYAML),
		}
	}
	require.NoError(t, svc.persistProjectConfigSnapshot(ctx, conn, snapshot(1500, "user: stale"), false))
	record, err := repo.GetProjectConfigRecord(ctx, projectID)
	require.NoError(t, err)
	require.Equal(t, "user: existing", *record.UserConfigYAML, "a snapshot older than the stored push must not be written")

	require.NoError(t, svc.persistProjectConfigSnapshot(ctx, conn, snapshot(4000, "user: fresh"), false))
	record, err = repo.GetProjectConfigRecord(ctx, projectID)
	require.NoError(t, err)
	require.Equal(t, "user: fresh", *record.UserConfigYAML, "a newer snapshot must be written")
	require.Equal(t, int64(4000), record.PushedAt.UnixMilli())

	require.Empty(t, spy.reads(), "the staleness check read the whole project config row")
}

// Two machines push snapshots of one project through the gateway: the newer
// one, from a machine without control-plane's checkout, carries no workflows
// or presets. The project's catalog keeps the first machine's files — the
// incident where a new machine's snapshot made every trigger's workflow
// vanish.
func TestDaemonConfigSync_SecondMachineDoesNotEraseTheFirstsWorkflows(t *testing.T) {
	repo := db.NewTestRepo(t)
	svc := newTestService(repo)
	defer svc.Close()
	ctx := context.Background()

	now := time.Now().UTC()
	projectID := uuid.NewString()
	projectPath := "/tmp/two-machines-" + uuid.NewString()
	conns := map[string]*daemonConnection{}
	for _, name := range []string{"default", "cicd"} {
		id := uuid.NewString()
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: "test-user"}))
		conns[name] = &daemonConnection{userID: "test-user", daemonID: id,
			sendCh: make(chan *reliantv1.ServerMessage, 8), done: make(chan struct{})}
	}
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "labs", Path: projectPath, IsGitRepo: true,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	require.NoError(t, svc.persistProjectConfigSnapshot(ctx, conns["default"], &reliantv1.ProjectConfigSnapshot{
		ProjectPath: projectPath, DaemonTimestampUnixMs: 1000,
		Workflows: []*reliantv1.IndexedWorkflow{{Slug: "release", Name: "release",
			RelativePath: "control-plane/.reliant/workflows/release.yaml", YamlContent: []byte("name: release\n")}},
		Presets: []*reliantv1.IndexedPreset{{Name: "implementer",
			RelativePath: "control-plane/.reliant/presets/implementer.yaml", YamlContent: []byte("params: {}\n")}},
	}, false))
	require.NoError(t, svc.persistProjectConfigSnapshot(ctx, conns["cicd"], &reliantv1.ProjectConfigSnapshot{
		ProjectPath: projectPath, DaemonTimestampUnixMs: 2000,
	}, true))

	workflowsJSON, err := repo.GetProjectWorkflowsJSON(ctx, projectID)
	require.NoError(t, err)
	workflows, err := cfgpkg.ParseStoredWorkflows(workflowsJSON)
	require.NoError(t, err)
	require.Len(t, workflows, 1)
	require.Equal(t, "control-plane/.reliant/workflows/release.yaml", workflows[0].RelativePath)

	presetsJSON, err := repo.GetProjectPresetsJSON(ctx, projectID)
	require.NoError(t, err)
	presets, err := cfgpkg.ParseStoredPresets(presetsJSON)
	require.NoError(t, err)
	require.Len(t, presets, 1)
	require.Equal(t, "control-plane/.reliant/presets/implementer.yaml", presets[0].RelativePath, "a nested preset keeps its path")

	cicd, synced, err := repo.GetProjectMachineWorkflowsJSON(ctx, projectID, conns["cicd"].daemonID)
	require.NoError(t, err)
	require.True(t, synced)
	require.Nil(t, cicd, "cicd's own catalog is what it sent: nothing")
}
