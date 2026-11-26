// Copyright (c) 2026 Reliant Labs
package launch

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestAttendedLaunchPinsTheServerResolvedDefinitionAndKeepsInputs(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	const path = "control-plane/.reliant/workflows/release-proof.yaml"
	const original = `# authored bytes, including this comment
name: release-proof
automation_only: true
inputs:
  cut_now: {type: boolean, default: false}
  require_approval: {type: boolean, default: true}
entry: [done]
nodes:
- id: done
  type: save_message
  args: {role: assistant, content: done}
`
	daemon := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemon, UserID: launchTestUserID}))
	db.HoldProjectOnMachineForTest(t, repo, projectID, daemon)
	push := func(t *testing.T, content string, at time.Time) {
		t.Helper()
		data, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "release-proof", Name: "release-proof", RelativePath: path, YAMLContent: content}})
		require.NoError(t, err)
		_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{ProjectID: projectID, DaemonID: daemon, ProjectWorkflowsJSON: data, PushedAt: at}, true, nil)
		require.NoError(t, err)
	}
	now := time.Now().UTC()
	for i, kind := range []core.TriggerEventKind{core.TriggerEventKindCLIRun, core.TriggerEventKindManualRun} {
		t.Run(string(kind), func(t *testing.T) {
			at := now.Add(time.Duration(i) * time.Hour)
			push(t, original, at)
			starter := &fakeStarter{failRootStarts: 1}
			launcher, _ := newTestLauncher(t, repo, starter)
			ev := Event{Kind: kind, DedupeKey: uuid.NewString(), Payload: map[string]any{"launch_definition": map[string]any{"content_hash": "forged"}}}
			spec := Spec{OwnerUserID: launchTestUserID, ProjectID: projectID, DaemonID: daemon, Workflow: "project://release-proof", Messages: userSeed("recover the release"),
				Params: map[string]*structpb.Value{"cut_now": structpb.NewBoolValue(true), "require_approval": structpb.NewBoolValue(false)}}
			_, err := launcher.Launch(ctx, ev, spec)
			require.ErrorIs(t, err, ErrInternal)
			first := starter.lastRootInput(t)
			require.NotNil(t, first.Trigger.LaunchDefinition)
			assert.Equal(t, path, first.Trigger.LaunchDefinition.Path)
			assert.Equal(t, config.DefinitionHash([]byte(original)), first.Trigger.LaunchDefinition.ContentHash)
			require.NotEmpty(t, first.ExecContext.CatalogPin)
			assert.Equal(t, true, first.Inputs["cut_now"])
			assert.Equal(t, false, first.Inputs["require_approval"])
			stored, err := repo.GetTriggerEventByDedupe(ctx, kind, ev.DedupeKey)
			require.NoError(t, err)
			require.NotNil(t, stored.Definition)
			assert.Equal(t, first.ExecContext.CatalogPin, stored.Definition.CatalogPin)
			assert.Equal(t, first.Trigger.LaunchDefinition.ContentHash, stored.Definition.ContentHash)

			later := original + "# newer catalog\n"
			push(t, later, at.Add(time.Minute))
			_, err = launcher.Launch(ctx, ev, spec)
			require.NoError(t, err)
			retry := starter.lastRootInput(t)
			assert.Equal(t, first.ExecContext.CatalogPin, retry.ExecContext.CatalogPin)
			assert.Equal(t, first.Trigger.LaunchDefinition, retry.Trigger.LaunchDefinition)
			assert.Equal(t, true, retry.Inputs["cut_now"])
			assert.Equal(t, false, retry.Inputs["require_approval"])
		})
	}
}
