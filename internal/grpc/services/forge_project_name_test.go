// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// SetProjectForgeName completes forgeProjectLookup for the shared stub: the
// tests that use it assert on the dispatch, not on the row.
func (p forgeTestProjects) SetProjectForgeName(context.Context, string, string, string) (bool, error) {
	return false, nil
}

// forgeNameRecordingProjects is a project row whose forge name writes are
// recorded, so a test can assert what GetTopology persisted — and that the
// steady state persists nothing.
type forgeNameRecordingProjects struct {
	project *db.Project
	writes  []string
}

func (p *forgeNameRecordingProjects) GetProjectWithUserCheck(_ context.Context, id, _ string) (*db.Project, error) {
	row := *p.project
	row.ID = id
	return &row, nil
}

func (p *forgeNameRecordingProjects) SetProjectForgeName(_ context.Context, _, _ string, name string) (bool, error) {
	p.writes = append(p.writes, name)
	p.project.IsForge = true
	p.project.ForgeProjectName = &name
	return true, nil
}

func topologyForProjectName(t *testing.T, projects *forgeNameRecordingProjects, reply []byte) {
	t.Helper()
	svc := NewForgeService(&forgeTestRouter{reply: reply}, projects)
	_, err := svc.GetTopology(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeTopologyRequest{ProjectId: "proj-1"}))
	require.NoError(t, err)
}

// The forge project name is the key the web joins a project to its
// control-plane environments on, and it must be readable with the daemon
// offline. So a topology report that names the project persists that name —
// which is what backfills a project created before the column existed.
func TestForgeService_GetTopology_PersistsForgeProjectName(t *testing.T) {
	projects := &forgeNameRecordingProjects{project: &db.Project{Path: "/daemon/barksocial"}}

	topologyForProjectName(t, projects, forgeDaemonReply(t, true, true, 0, `{"project":"hounders"}`))
	require.Equal(t, []string{"hounders"}, projects.writes)

	// Steady state: the row already says so, so nothing is written.
	topologyForProjectName(t, projects, forgeDaemonReply(t, true, true, 0, `{"project":"hounders"}`))
	assert.Equal(t, []string{"hounders"}, projects.writes, "an unchanged name must not be rewritten")

	// A rename in forge.yaml follows through.
	topologyForProjectName(t, projects, forgeDaemonReply(t, true, true, 0, `{"project":"barksocial"}`))
	assert.Equal(t, []string{"hounders", "barksocial"}, projects.writes)
}

// Anything short of forge naming the project leaves the row alone: a name is
// never guessed, and a non-answer never erases one already known.
func TestForgeService_GetTopology_KeepsForgeProjectNameWithoutAReport(t *testing.T) {
	known := "hounders"
	for name, reply := range map[string][]byte{
		"not a forge project": forgeDaemonReply(t, false, true, 0, ""),
		"unsupported forge":   forgeDaemonReply(t, true, false, 0, ""),
		"report names nobody": forgeDaemonReply(t, true, true, 0, `{"latest_release":"v1"}`),
		"blank name":          forgeDaemonReply(t, true, true, 0, `{"project":"  "}`),
		"exit 2, no report":   forgeDaemonReply(t, true, true, 2, ""),
	} {
		t.Run(name, func(t *testing.T) {
			projects := &forgeNameRecordingProjects{project: &db.Project{
				Path: "/daemon/barksocial", IsForge: true, ForgeProjectName: &known,
			}}
			topologyForProjectName(t, projects, reply)
			assert.Empty(t, projects.writes)
		})
	}
}
