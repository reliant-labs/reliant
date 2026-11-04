// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// An automation runs the project's workflow of its name, not the owner's own
// account workflow of that name, and says so: the account workflow used to
// win silently ("yours wins for you"), so an account draft named like a
// production automation replaced its definition. Two machines: the trigger's
// machine reads its own catalog; one that never synced says it is reading
// the union.
func TestActivationRunsTheProjectWorkflowAndShowsWhere(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env := setupTriggerTest(t)
	ctx := context.Background()
	const path = "control-plane/.reliant/workflows/triage.yaml"
	projectYAML := strings.Replace(declaringWorkflowYAML, "name: triage\n", "name: triage\ndescription: the project's\n", 1)
	wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "triage", Name: "triage", RelativePath: path, YAMLContent: projectYAML}})
	require.NoError(t, err)
	_, err = env.repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
		ProjectID: env.projectID, DaemonID: env.daemonID, ProjectWorkflowsJSON: wj, PushedAt: time.Now().UTC(),
	}, true)
	require.NoError(t, err)
	// The owner's own account workflow of the same name, declaring nothing:
	// were it what the automation resolved, the activation would be broken.
	env.saveWorkflow(t, "triage", "name: triage\nentry: [a]\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\n")

	got := env.create(t, env.activation("nightly", nil))
	assert.NotEqual(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, got.GetHealth().GetStatus(), got.GetHealth().GetLastFailureDetail())

	one, err := env.svc.GetTrigger(env.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: got.GetId()}))
	require.NoError(t, err)
	src := one.Msg.GetTrigger().GetDefinitionSource()
	require.NotNil(t, src)
	assert.Equal(t, "project", src.GetKind())
	assert.Equal(t, path, src.GetPath())
	assert.Equal(t, env.daemonID, src.GetDaemonId(), "read from the trigger's own machine's catalog")
	require.Len(t, src.GetWarnings(), 1)
	assert.Contains(t, src.GetWarnings()[0], `your account workflow "triage" has the same name; automations run the project's workflow`)

	// A second machine that has never synced the project: the activation
	// resolves against the union and says so.
	fresh := uuid.NewString()
	require.NoError(t, env.repo.UpsertDaemon(ctx, &db.Daemon{ID: fresh, UserID: env.userID}))
	other := env.create(t, env.activation("nightly", func(d *reliantv1.TriggerDefinition) {
		d.Name = "nightly on the new machine"
		d.DaemonId = fresh
	}))
	one, err = env.svc.GetTrigger(env.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: other.GetId()}))
	require.NoError(t, err)
	src = one.Msg.GetTrigger().GetDefinitionSource()
	require.NotNil(t, src)
	assert.Empty(t, src.GetDaemonId())
	assert.Contains(t, strings.Join(src.GetWarnings(), "\n"), "has never synced this project")
}
