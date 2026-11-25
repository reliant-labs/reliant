// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
)

// A list describes each workflow once, however many of its activations it
// renders: a description resolves, and parses, the workflow's whole YAML. It
// reads through the store it is given, so the count of catalog reads under a
// describer with no request memo is the count of descriptions.
func TestWorkflowDescriberDescribesEachWorkflowOnce(t *testing.T) {
	env := setupTriggerTest(t)
	wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{
		{Slug: "triage", Name: "triage", RelativePath: ".reliant/workflows/triage.yaml", YAMLContent: declaringWorkflowYAML},
	})
	require.NoError(t, err)
	db.HoldProjectOnMachineForTest(t, env.repo, env.projectID, env.daemonID)
	_, err = env.repo.SyncProjectConfigSnapshot(env.ctx, &db.ProjectConfigRecord{
		ProjectID: env.projectID, DaemonID: env.daemonID, ProjectWorkflowsJSON: wj, PushedAt: time.Now().UTC(),
	}, true, nil)
	require.NoError(t, err)

	counting := &countingRepo{Repo: env.repo}
	sources := newWorkflowDescriber(counting)
	activation := func(id, workflow string) *core.Trigger {
		return &core.Trigger{ID: id, UserID: env.userID, ProjectID: env.projectID, DaemonID: env.daemonID, Workflow: workflow}
	}
	first := definitionSource(env.ctx, sources, activation("a", "triage"))
	require.NotNil(t, first)
	require.Equal(t, "triage.yaml", first.GetPath())
	for _, id := range []string{"b", "c"} {
		require.Equal(t, first.GetPath(), definitionSource(env.ctx, sources, activation(id, "triage")).GetPath())
	}
	require.EqualValues(t, 1, counting.catalogs.Load(), "three activations of one workflow are described once")

	require.Nil(t, definitionSource(env.ctx, sources, activation("d", "missing")), "a workflow that does not resolve has no source")
	require.EqualValues(t, 2, counting.catalogs.Load(), "another workflow is described on its own")
}

// Embed the unused resolver methods: describing this project workflow needs
// only its machine catalog and the owner's same-name draft lookup.
type descriptionScopeResolver struct {
	launch.WorkflowResolver
	catalog *string
	reads   int
	owners  []string
}

func (r *descriptionScopeResolver) GetProjectMachineWorkflowsJSON(context.Context, string, string) (*string, bool, error) {
	r.reads++
	return r.catalog, true, nil
}

func (r *descriptionScopeResolver) GetUsableWorkflowBySlug(_ context.Context, owner, _ string) (*db.WorkflowDraft, error) {
	r.owners = append(r.owners, owner)
	return nil, nil
}

func TestWorkflowDescriberSeparatesScopes(t *testing.T) {
	catalog, err := config.EncodeCatalogJSON([]config.StoredWorkflow{
		{Slug: "triage", Name: "triage", RelativePath: ".reliant/workflows/triage.yaml", YAMLContent: declaringWorkflowYAML},
	})
	require.NoError(t, err)
	for _, dimension := range []string{"owner", "project", "machine"} {
		t.Run(dimension, func(t *testing.T) {
			resolver := &descriptionScopeResolver{catalog: catalog}
			describer := newWorkflowDescriber(resolver)
			first := core.Trigger{ID: "a", UserID: "owner-a", ProjectID: "project-a", DaemonID: "machine-a", Workflow: "triage"}
			other := first
			other.ID = "b"
			switch dimension {
			case "owner":
				other.UserID = "owner-b"
			case "project":
				other.ProjectID = "project-b"
			case "machine":
				other.DaemonID = "machine-b"
			}

			ctx := context.Background()
			a, err := describer.describe(ctx, &first)
			require.NoError(t, err)
			require.NotNil(t, a)
			b, err := describer.describe(ctx, &other)
			require.NoError(t, err)
			require.NotNil(t, b)
			require.NotSame(t, a, b, "a different scope must resolve its own description even for identical catalog content")
			require.Equal(t, 2, resolver.reads)
			require.Equal(t, []string{first.UserID, other.UserID}, resolver.owners)

			// Interleave both scopes again: each must retain its own memo hit.
			for _, activation := range []*core.Trigger{&first, &other} {
				want := a
				if activation == &other {
					want = b
				}
				again, err := describer.describe(ctx, activation)
				require.NoError(t, err)
				require.Same(t, want, again)
			}
			require.Equal(t, 2, resolver.reads, "repeat activations must not resolve either scope again")
			require.Len(t, resolver.owners, 2)
		})
	}
}
