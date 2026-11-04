// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/triggers"
)

const reactorPath = ".reliant/workflows/reactor.yaml"

// syncingLauncher syncs another version of reactor.yaml after the firing has
// described its definition and before the launch admits it: the race a
// firing's provenance must survive.
type syncingLauncher struct {
	inner Launcher
	sync  func()
}

func (l *syncingLauncher) Launch(ctx context.Context, ev launch.Event, spec launch.Spec) (*launch.Result, error) {
	l.sync()
	return l.inner.Launch(ctx, ev, spec)
}

// A machine pushes a new version of the workflow between the moment a firing
// describes its definition and the moment it launches. The firing pinned the
// catalog it described: the run is admitted and starts on that snapshot, so
// the version it recorded is the version that runs. The push is a valid,
// different workflow the first time and a broken one the second; neither
// changes what the firing recorded or runs.
func TestDispatch_RecordedDefinitionIsTheOneThatRunsWhenAPushRacesTheLaunch(t *testing.T) {
	for name, pushed := range map[string]string{
		"a valid new version":  strings.Replace(reactorYAML, `command: "echo hi"`, `command: "echo pushed"`, 1),
		"a broken new version": "name: reactor\nentry: [echo\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			f.syncReactor(reactorYAML, time.UnixMilli(1000))
			inner := f.dispatcher.launcher
			f.dispatcher.launcher = &syncingLauncher{inner: inner, sync: func() { f.syncReactor(pushed, time.UnixMilli(2000)) }}
			f.dispatcher.WithWorkflows(triggers.LaunchWorkflows{Repo: f.repo})
			tr := f.addActivation("reactor", "on-review-failure", Source{Workflows: []string{"code-review"}, Outcomes: []string{"failed"}})

			results, err := f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("code-review"), core.RunEventFailed))
			require.NoError(t, err)
			launched, err := f.repo.GetTriggerEventByChatID(ctx, launchedChat(t, results, tr.ID))
			require.NoError(t, err)
			def := launched.Definition
			require.NotNil(t, def)
			assert.Equal(t, config.DefinitionHash([]byte(reactorYAML)), def.ContentHash, "recorded: the version the firing described")
			require.NotEmpty(t, def.CatalogPin, "the firing pinned the catalog it described")

			root := f.starter.lastRoot()
			require.NotNil(t, root.ExecContext)
			assert.Equal(t, def.CatalogPin, root.ExecContext.CatalogPin, "the run starts on the firing's snapshot")
			pinned, found, err := f.repo.GetPinnedProjectCatalog(ctx, def.CatalogPin)
			require.NoError(t, err)
			require.True(t, found)
			workflows, err := config.ParseStoredWorkflows(pinned.Workflows)
			require.NoError(t, err)
			require.Len(t, workflows, 1)
			assert.Equal(t, reactorYAML, workflows[0].YAMLContent, "the snapshot it loads from holds the recorded version, not the push")
		})
	}
}

// syncReactor syncs reactor.yaml (as content) from the fixture's machine.
func (f *fixture) syncReactor(content string, at time.Time) {
	f.t.Helper()
	wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "reactor", Name: "reactor", RelativePath: reactorPath, YAMLContent: content}})
	require.NoError(f.t, err)
	_, err = f.repo.SyncProjectConfigSnapshot(context.Background(), &db.ProjectConfigRecord{
		ProjectID: f.projectID, DaemonID: f.daemonID, ProjectWorkflowsJSON: wj, PushedAt: at.UTC(),
	}, false)
	require.NoError(f.t, err)
}

// A workflow-event firing records the definition it resolved, on the row the
// launcher writes; when the newest version of the file is broken, the firing
// is refused with that version's provenance and nothing launches.
func TestDispatch_FiringRecordsItsDefinitionAndABrokenOneIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.dispatcher.WithWorkflows(triggers.LaunchWorkflows{Repo: f.repo})
	f.syncReactor(reactorYAML, time.UnixMilli(1000))
	tr := f.addActivation("reactor", "on-review-failure", Source{Workflows: []string{"code-review"}, Outcomes: []string{"failed"}})

	results, err := f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("code-review"), core.RunEventFailed))
	require.NoError(t, err)
	launched, err := f.repo.GetTriggerEventByChatID(ctx, launchedChat(t, results, tr.ID))
	require.NoError(t, err)
	require.NotNil(t, launched.Definition, "the launched row records the definition")
	assert.Equal(t, reactorPath, launched.Definition.Path)
	assert.Equal(t, "project", launched.Definition.Source)
	assert.Equal(t, f.daemonID, launched.Definition.DaemonID)
	assert.Equal(t, config.DefinitionHash([]byte(reactorYAML)), launched.Definition.ContentHash)
	assert.Empty(t, launched.Definition.Error)

	const broken = "name: reactor\nentry: [echo\n"
	f.syncReactor(broken, time.UnixMilli(2000))
	starts := f.starter.count()
	results, err = f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("code-review"), core.RunEventFailed))
	require.NoError(t, err)
	assert.Equal(t, starts, f.starter.count(), "nothing launches: no older version runs in its place")
	var refused bool
	for _, r := range results {
		if r.TriggerID == tr.ID {
			refused = true
			assert.Equal(t, core.TriggerEventFailed, r.Outcome)
			assert.True(t, strings.HasPrefix(r.Detail, "refused: "), r.Detail)
		}
	}
	require.True(t, refused, "the refusal is recorded, not skipped")
	failed := core.TriggerEventFailed
	ev, err := f.repo.GetLatestTriggerEvent(ctx, tr.ID, &failed)
	require.NoError(t, err)
	require.True(t, ev.Definition.Broken(), "%+v", ev.Definition)
	assert.Equal(t, reactorPath, ev.Definition.Path)
	assert.Equal(t, config.DefinitionHash([]byte(broken)), ev.Definition.ContentHash)
	assert.Contains(t, ev.Definition.Error, "does not parse")
}
