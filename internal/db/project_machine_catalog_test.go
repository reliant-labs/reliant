package db

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
)

// twoMachineProject is one project checked out on two machines, "default"
// and "cicd", as in the incident: default has control-plane's workflows,
// cicd was set up without the checkouts.
type twoMachineProject struct {
	repo                  *Repo
	projectID             string
	defaultID, cicdID     string
	releasePath, cpPreset string
}

func newTwoMachineProject(t *testing.T) *twoMachineProject {
	t.Helper()
	repo := NewTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := &twoMachineProject{
		repo: repo, projectID: uuid.NewString(), defaultID: uuid.NewString(), cicdID: uuid.NewString(),
		releasePath: "control-plane/.reliant/workflows/release.yaml",
		cpPreset:    "control-plane/.reliant/presets/implementer.yaml",
	}
	for id, name := range map[string]string{p.defaultID: "default", p.cicdID: "cicd"} {
		// The hostname, as a machine's registration reports it; the
		// diagnostic names a machine by name, else hostname.
		require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "test-user", Hostname: testStringPtr(name)}))
	}
	require.NoError(t, repo.CreateProject(ctx, &Project{
		ID: p.projectID, UserID: "test-user", Name: "labs", Path: "/home/workspace/projects/reliant-labs-" + p.projectID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return p
}

func (p *twoMachineProject) push(t *testing.T, daemonID string, at int64, workflows []config.StoredWorkflow, presets []config.StoredPreset) bool {
	t.Helper()
	wj, err := config.EncodeCatalogJSON(workflows)
	require.NoError(t, err)
	pj, err := config.EncodeCatalogJSON(presets)
	require.NoError(t, err)
	stored, err := p.repo.SyncProjectConfigSnapshot(context.Background(), &ProjectConfigRecord{
		ProjectID: p.projectID, DaemonID: daemonID,
		UserConfigYAML:       testStringPtr("from " + daemonID),
		ProjectWorkflowsJSON: wj, ProjectPresetsJSON: pj,
		PushedAt: time.UnixMilli(at).UTC(),
	}, false)
	require.NoError(t, err)
	return stored
}

func (p *twoMachineProject) unionWorkflows(t *testing.T) []config.StoredWorkflow {
	t.Helper()
	v, err := p.repo.GetProjectWorkflowsJSON(context.Background(), p.projectID)
	require.NoError(t, err)
	w, err := config.ParseStoredWorkflows(v)
	require.NoError(t, err)
	return w
}

func testStringPtr(s string) *string { return &s }

const releaseYAML = "name: release\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"

// The incident, end to end against Postgres: a second machine's snapshot
// without the files must not remove the workflows the first machine still
// provides, and a resolution on that second machine must say where they are.
func TestSyncProjectConfigSnapshot_AnotherMachineCannotEraseWorkflows(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	release := config.StoredWorkflow{Slug: "release", Name: "release", RelativePath: p.releasePath, YAMLContent: releaseYAML}
	preset := config.StoredPreset{Name: "implementer", RelativePath: p.cpPreset, YAMLContent: "params: {}\n"}

	require.True(t, p.push(t, p.defaultID, 1000, []config.StoredWorkflow{release}, []config.StoredPreset{preset}))
	require.True(t, p.push(t, p.cicdID, 2000, nil, nil), "cicd's newer, empty snapshot")

	union := p.unionWorkflows(t)
	require.Len(t, union, 1, "the union keeps what default provides")
	assert.Equal(t, p.releasePath, union[0].RelativePath)
	presetsJSON, err := p.repo.GetProjectPresetsJSON(ctx, p.projectID)
	require.NoError(t, err)
	presets, err := config.ParseStoredPresets(presetsJSON)
	require.NoError(t, err)
	require.Len(t, presets, 1)

	record, err := p.repo.GetProjectConfigRecord(ctx, p.projectID)
	require.NoError(t, err)
	assert.Equal(t, "from "+p.cicdID, *record.UserConfigYAML, "the rest of the record is still the last pusher's")

	cicd, synced, err := p.repo.GetProjectMachineWorkflowsJSON(ctx, p.projectID, p.cicdID)
	require.NoError(t, err)
	assert.True(t, synced)
	assert.Nil(t, cicd, "cicd's own catalog has nothing")
	def, synced, err := p.repo.GetProjectMachineWorkflowsJSON(ctx, p.projectID, p.defaultID)
	require.NoError(t, err)
	require.True(t, synced)
	require.NotNil(t, def)
	_, synced, err = p.repo.GetProjectMachineWorkflowsJSON(ctx, p.projectID, uuid.NewString())
	require.NoError(t, err)
	assert.False(t, synced, "a machine that never synced the project")

	hint, err := p.repo.WorkflowSourceDiagnostic(ctx, p.projectID, p.cicdID, "release")
	require.NoError(t, err)
	assert.Equal(t, `machine "cicd" has no workflow "release"; it is defined on machine "default" at `+p.releasePath+
		`; check that repository out on this machine, or point the trigger at a machine that has it`, hint)

	// default deletes the file: no machine provides it, and the miss names
	// where it last lived.
	require.True(t, p.push(t, p.defaultID, 3000, nil, nil))
	assert.Empty(t, p.unionWorkflows(t))
	hint, err = p.repo.WorkflowSourceDiagnostic(ctx, p.projectID, p.cicdID, "release")
	require.NoError(t, err)
	assert.Contains(t, hint, `machine "cicd" has no workflow "release"; it was last synced from `+p.releasePath+` on machine "default" (removed `)
	hint, err = p.repo.WorkflowSourceDiagnostic(ctx, p.projectID, "", "release")
	require.NoError(t, err)
	assert.Contains(t, hint, `no machine provides a workflow "release"; it was last synced from `+p.releasePath)

	// Restored: the removal is forgotten.
	require.True(t, p.push(t, p.defaultID, 4000, []config.StoredWorkflow{release}, nil))
	require.Len(t, p.unionWorkflows(t), 1)
}

// Staleness is judged per machine: a machine's snapshot older than its own
// last push is dropped, but an older clock than ANOTHER machine's push still
// stores this machine's catalog (and refreshes the union), while the rest of
// the record stays the newer pusher's.
func TestSyncProjectConfigSnapshot_StalenessIsPerMachine(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	release := config.StoredWorkflow{Name: "release", RelativePath: p.releasePath, YAMLContent: releaseYAML}

	require.True(t, p.push(t, p.cicdID, 5000, nil, nil))
	require.True(t, p.push(t, p.defaultID, 1000, []config.StoredWorkflow{release}, nil), "default's clock is behind cicd's push")
	require.Len(t, p.unionWorkflows(t), 1)
	record, err := p.repo.GetProjectConfigRecord(ctx, p.projectID)
	require.NoError(t, err)
	assert.Equal(t, "from "+p.cicdID, *record.UserConfigYAML, "the older snapshot does not replace the newer record")
	assert.Equal(t, int64(5000), record.PushedAt.UnixMilli())

	assert.False(t, p.push(t, p.defaultID, 900, nil, nil), "older than default's own last push")
	require.Len(t, p.unionWorkflows(t), 1)
}

// Two machines pushing at once each see the other's catalog in the union
// they write: the advisory lock serializes them.
func TestSyncProjectConfigSnapshot_ConcurrentMachinesBothLand(t *testing.T) {
	p := newTwoMachineProject(t)
	var wg sync.WaitGroup
	for i, id := range []string{p.defaultID, p.cicdID} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			p.push(t, id, int64(1000+i), []config.StoredWorkflow{{Name: id, RelativePath: ".reliant/workflows/" + id + ".yaml", YAMLContent: "name: " + id + "\n"}}, nil)
		}(i, id)
	}
	wg.Wait()
	assert.Len(t, p.unionWorkflows(t), 2)
}

// Removing a machine removes what it provided.
func TestProjectMachineCatalog_RemovedMachineLeavesTheUnionOnItsNextWrite(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	require.True(t, p.push(t, p.defaultID, 1000, []config.StoredWorkflow{{Name: "release", RelativePath: p.releasePath, YAMLContent: releaseYAML}}, nil))
	_, err := p.repo.ORM().Exec(ctx, `DELETE FROM daemons WHERE id = $1`, p.defaultID)
	require.NoError(t, err)
	catalogs, err := p.repo.ProjectMachineCatalogs(ctx, p.projectID)
	require.NoError(t, err)
	assert.Empty(t, catalogs, "the machine's catalog goes with it")
	require.True(t, p.push(t, p.cicdID, 2000, nil, nil))
	assert.Empty(t, p.unionWorkflows(t))
}
