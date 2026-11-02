// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// countingRepo counts the reads a workflow resolution makes.
type countingRepo struct {
	*db.Repo
	catalogs, drafts atomic.Int64
}

func (r *countingRepo) GetProjectMachineWorkflowsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	r.catalogs.Add(1)
	return r.Repo.GetProjectMachineWorkflowsJSON(ctx, projectID, daemonID)
}

func (r *countingRepo) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	r.drafts.Add(1)
	return r.Repo.GetUsableWorkflowBySlug(ctx, userID, slug)
}

func (r *countingRepo) GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	r.drafts.Add(1)
	return r.Repo.GetWorkflowDraftBySlug(ctx, userID, slug)
}

// Prod (2026-11-01): ListTriggers for a user with 20 activations made 41
// reads of one machine catalog row and 70 account-draft reads, p50 4.9s
// (trace d16638c2…): every activation re-read its workflow, the workflow's
// children, and, for its definition source, all of it again. A list reads
// each catalog and draft once, and renders every activation exactly as
// GetTrigger renders it alone.
func TestListTriggersReadsEachCatalogOnce(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env := setupTriggerTest(t)
	ctx := context.Background()
	// Each workflow runs a shared child, so resolution reads beyond the root.
	child := "name: shared-step\nentry: [a]\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\n"
	stored := []config.StoredWorkflow{{Slug: "shared-step", Name: "shared-step", RelativePath: ".reliant/workflows/shared-step.yaml", YAMLContent: child}}
	const workflows = 4
	for i := 0; i < workflows; i++ {
		slug := fmt.Sprintf("triage-%d", i)
		yaml := strings.Replace(declaringWorkflowYAML, "name: triage\n", "name: "+slug+"\n", 1)
		yaml = strings.Replace(yaml, "    type: approval\n    args: {title: ok}\n",
			"    type: workflow\n    ref: shared-step\n", 1)
		stored = append(stored, config.StoredWorkflow{Slug: slug, Name: slug, RelativePath: ".reliant/workflows/" + slug + ".yaml", YAMLContent: yaml})
	}
	wj, err := config.EncodeCatalogJSON(stored)
	require.NoError(t, err)
	_, err = env.repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
		ProjectID: env.projectID, DaemonID: env.daemonID, ProjectWorkflowsJSON: wj, PushedAt: time.Now().UTC(),
	}, true)
	require.NoError(t, err)

	for i := 0; i < workflows; i++ {
		for _, name := range []string{"nightly", "after-deploy"} {
			env.create(t, env.activation(name, func(d *reliantv1.TriggerDefinition) {
				d.Name = fmt.Sprintf("%s of triage-%d", name, i)
				d.Workflow = fmt.Sprintf("triage-%d", i)
			}))
		}
	}

	counting := &countingRepo{Repo: env.repo}
	env.configure(t, func(d *Deps) { d.Repo = counting })

	resp, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{ProjectId: &env.projectID}))
	require.NoError(t, err)
	listed := resp.Msg.GetTriggers()
	require.Len(t, listed, 2*workflows)
	t.Logf("reads: catalogs=%d drafts=%d", counting.catalogs.Load(), counting.drafts.Load())
	require.EqualValues(t, 1, counting.catalogs.Load(),
		"every activation resolves against the one machine catalog; the list must read it once")
	require.LessOrEqual(t, counting.drafts.Load(), int64(2*(workflows+1)),
		"each account-draft lookup (one per workflow name and method) is read once")

	// Reading once changes nothing about what is rendered.
	for _, got := range listed {
		require.NotEqual(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, got.GetHealth().GetStatus(), got.GetHealth().GetLastFailureDetail())
		require.NotNil(t, got.GetDefinitionSource(), "%s has no definition source", got.GetName())
		one, err := env.svc.GetTrigger(env.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: got.GetId()}))
		require.NoError(t, err)
		require.True(t, proto.Equal(one.Msg.GetTrigger().GetHealth(), got.GetHealth()), "health of %s", got.GetName())
		require.True(t, proto.Equal(one.Msg.GetTrigger().GetDefinitionSource(), got.GetDefinitionSource()), "definition source of %s", got.GetName())
	}
}
