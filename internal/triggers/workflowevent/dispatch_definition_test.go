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
	"github.com/reliant-labs/reliant/internal/triggers"
)

const reactorPath = ".reliant/workflows/reactor.yaml"

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
