// Copyright (c) 2026 Reliant Labs
package launch

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// attendedPinRecorder proves authorization happens before even reading a catalog.
type attendedPinRecorder struct {
	*db.Repo
	scopes []db.CatalogPinScope
}

func (r *attendedPinRecorder) PinProjectCatalog(ctx context.Context, scope db.CatalogPinScope) (string, error) {
	r.scopes = append(r.scopes, scope)
	return r.Repo.PinProjectCatalog(ctx, scope)
}

func TestAttendedLaunchCatalogIsTenantScoped(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	const foreignUser = "foreign-launch-user"
	foreignProject := uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: foreignProject, UserID: foreignUser, Name: "Foreign project", Path: t.TempDir(),
		CreatedAt: at, UpdatedAt: at, LastActive: at,
	}))
	const path = "control-plane/.reliant/workflows/tenant-proof.yaml"
	const ownYAML = `name: tenant-proof
entry: [done]
nodes:
- id: done
  type: save_message
  args: {role: assistant, content: own-project}
`
	const foreignYAML = `name: tenant-proof
entry: [done]
nodes:
- id: done
  type: save_message
  args: {role: assistant, content: foreign-project}
`
	push := func(project, daemon, content string, when time.Time) {
		t.Helper()
		data, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "tenant-proof", Name: "tenant-proof", RelativePath: path, YAMLContent: content}})
		require.NoError(t, err)
		_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: project, DaemonID: daemon, ProjectWorkflowsJSON: data, PushedAt: when,
		}, true, nil)
		require.NoError(t, err)
	}
	ownDaemon, foreignDaemon := uuid.NewString(), uuid.NewString()
	for _, tenant := range []struct{ user, project, daemon, content string }{
		{launchTestUserID, projectID, ownDaemon, ownYAML},
		{foreignUser, foreignProject, foreignDaemon, foreignYAML},
	} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: tenant.daemon, UserID: tenant.user}))
		db.HoldProjectOnMachineForTest(t, repo, tenant.project, tenant.daemon)
		push(tenant.project, tenant.daemon, tenant.content, at)
		require.NoError(t, store.InsertWorkflowDraft(ctx, repo.ORM(), &core.WorkflowDraft{
			ID: uuid.NewString(), UserID: tenant.user, Name: "tenant-proof", Slug: "tenant-proof",
			Definition: tenant.content + "# account copy for " + tenant.user + "\n",
			Status: db.WorkflowDraftStatusComplete, CreatedAt: at, UpdatedAt: at,
		}))
	}
	for i, kind := range []core.TriggerEventKind{core.TriggerEventKindCLIRun, core.TriggerEventKindManualRun} {
		t.Run(string(kind), func(t *testing.T) {
			when := at.Add(time.Duration(i+1) * time.Hour)
			push(projectID, ownDaemon, ownYAML, when)
			recorded := &attendedPinRecorder{Repo: repo}
			starter := &fakeStarter{failRootStarts: 1}
			launcher, _ := newTestLauncher(t, recorded, starter)
			// Qualify the nested repo to address its project definition explicitly;
			// project://tenant-proof still uses attended account-first precedence.
			spec := Spec{OwnerUserID: launchTestUserID, ProjectID: foreignProject, DaemonID: foreignDaemon,
				Workflow: "project://control-plane/tenant-proof", Messages: userSeed("run the tenant proof")}
			_, err := launcher.Launch(ctx, Event{Kind: kind, DedupeKey: uuid.NewString()}, spec)
			require.ErrorIs(t, err, ErrNotFound)
			assert.Empty(t, recorded.scopes, "a foreign project must be denied before pinning")
			assert.Empty(t, starter.startedIDs())

			spec.ProjectID, spec.DaemonID = projectID, ownDaemon
			ev := Event{Kind: kind, DedupeKey: uuid.NewString()}
			_, err = launcher.Launch(ctx, ev, spec)
			require.ErrorIs(t, err, ErrInternal, "the fake Temporal start fails after committing the pin")
			first := starter.lastRootInput(t)
			require.NotNil(t, first.Trigger.LaunchDefinition)
			assert.Equal(t, path, first.Trigger.LaunchDefinition.Path)
			assert.Equal(t, config.DefinitionHash([]byte(ownYAML)), first.Trigger.LaunchDefinition.ContentHash)
			require.NotEmpty(t, first.ExecContext.CatalogPin)
			stored, err := repo.GetTriggerEventByDedupe(ctx, kind, ev.DedupeKey)
			require.NoError(t, err)
			require.NotNil(t, stored.Definition)
			assert.Equal(t, first.ExecContext.CatalogPin, stored.Definition.CatalogPin)
			assert.Equal(t, config.DefinitionHash([]byte(ownYAML)), stored.Definition.ContentHash)
			require.NotEmpty(t, recorded.scopes)
			assert.Equal(t, db.CatalogPinScope{ProjectID: projectID, DaemonID: ownDaemon, UserID: launchTestUserID}, recorded.scopes[0])
			catalog, found, err := repo.GetPinnedProjectCatalog(ctx, first.ExecContext.CatalogPin)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, ownYAML+"# account copy for "+launchTestUserID+"\n", catalog.UserWorkflows["tenant-proof"].Definition)

			// The run's resolver must execute these bytes, not a newer sync or
			// the other tenant's same-named account/project workflow.
			checkPinnedBytes := func(pin string) {
				t.Helper()
				desc, err := DescribeRunWorkflow(ctx, launcher.workflowReads(), Scope{
					UserID: launchTestUserID, ProjectID: projectID, DaemonID: ownDaemon, CatalogPin: pin,
				}, spec.Workflow)
				require.NoError(t, err)
				assert.Equal(t, ownYAML, string(desc.YAML))
				account, err := DescribeRunWorkflow(ctx, launcher.workflowReads(), Scope{
					UserID: launchTestUserID, ProjectID: projectID, DaemonID: ownDaemon, CatalogPin: pin,
				}, "tenant-proof")
				require.NoError(t, err)
				assert.Equal(t, ownYAML+"# account copy for "+launchTestUserID+"\n", string(account.YAML))
			}
			checkPinnedBytes(first.ExecContext.CatalogPin)
			push(projectID, ownDaemon, foreignYAML, when.Add(time.Minute))
			_, err = launcher.Launch(ctx, ev, spec)
			require.NoError(t, err)
			retry := starter.lastRootInput(t)
			assert.Equal(t, first.ExecContext.CatalogPin, retry.ExecContext.CatalogPin)
			assert.Equal(t, first.Trigger.LaunchDefinition, retry.Trigger.LaunchDefinition)
			checkPinnedBytes(retry.ExecContext.CatalogPin)
		})
	}
}

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
