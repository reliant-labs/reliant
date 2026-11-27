// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/store/triggertest"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// countingRepo counts the reads a workflow resolution makes.
type countingRepo struct {
	*db.Repo
	catalogs, drafts, draftBatches atomic.Int64
}

func (r *countingRepo) GetWorkflowDraftsBySlugs(ctx context.Context, userID string, slugs []string) (*db.WorkflowDraftsBySlug, error) {
	r.draftBatches.Add(1)
	return r.Repo.GetWorkflowDraftsBySlugs(ctx, userID, slugs)
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

// Prod (2026-11-05): ListInbox for a user with 22 activations read machine
// catalogs 13 times and account drafts 24 times a call, p50 516ms, and the web
// calls it more than any other slow rpc. Every activation resolved its
// workflow and its children through the repository again. One ListInbox reads
// each catalog once, and lists what it listed before.
func TestListInbox_ReadsEachCatalogOnce(t *testing.T) {
	f := newInboxFixture(t)
	daemonID := uuid.NewString()
	host := "default"
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID, Hostname: &host}))

	// Each workflow runs a shared child, so resolution reads beyond the root.
	child := "name: shared-step\nentry: [a]\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\n"
	stored := []config.StoredWorkflow{{Slug: "shared-step", Name: "shared-step", RelativePath: ".reliant/workflows/shared-step.yaml", YAMLContent: child}}
	const workflows = 3
	for i := 0; i < workflows; i++ {
		slug := fmt.Sprintf("release-%d", i)
		yaml := strings.Replace(releaseWorkflow, "name: release\n", "name: "+slug+"\n", 1)
		yaml = strings.Replace(yaml, "    type: approval\n    args: {title: ok}\n", "    type: workflow\n    ref: shared-step\n", 1)
		stored = append(stored, config.StoredWorkflow{Slug: slug, Name: slug, RelativePath: ".reliant/workflows/" + slug + ".yaml", YAMLContent: yaml})
	}
	wj, err := config.EncodeCatalogJSON(stored)
	require.NoError(t, err)
	db.HoldProjectOnMachineForTest(t, f.repo, f.project(), daemonID)
	_, err = f.repo.SyncProjectConfigSnapshot(f.ctx, &db.ProjectConfigRecord{
		ProjectID: f.project(), DaemonID: daemonID, ProjectWorkflowsJSON: wj, PushedAt: time.Now().UTC(),
	}, false, nil)

	require.NoError(t, err)

	cfg, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < workflows; i++ {
		for j := 0; j < 2; j++ {
			name := "merged"
			require.NoError(t, triggertest.Create(f.ctx, f.repo, &core.Trigger{
				ID: fmt.Sprintf("act-%d-%d", i, j), UserID: f.userID, ProjectID: f.project(), Name: fmt.Sprintf("Release %d/%d", i, j),
				Kind: core.TriggerKindSchedule, Enabled: true, Workflow: fmt.Sprintf("release-%d", i), WorkflowTrigger: &name,
				DaemonID: daemonID, Presets: map[string]string{}, Params: map[string]any{}, Config: cfg,
				CreatedAt: now, UpdatedAt: now,
			}))
		}
	}
	direct := f.list(t, nil)

	counting := &countingRepo{Repo: f.repo}
	f.inbox = NewTestInbox(t, WithInboxDeps(mockDeps(counting)))
	got := f.list(t, nil)
	t.Logf("reads: catalogs=%d drafts=%d draft batches=%d", counting.catalogs.Load(), counting.drafts.Load(), counting.draftBatches.Load())
	require.EqualValues(t, 1, counting.catalogs.Load(),
		"every activation resolves against the one machine catalog; one ListInbox must read it once")
	// Prod (2026-11-18, trace ed2301a0…): 32 draft reads a call, one at a
	// time. Every activation's workflow is looked up in one batch; only the
	// child no activation names is read on its own.
	require.EqualValues(t, 1, counting.draftBatches.Load(), "the activations' account workflows are read in one query")
	require.EqualValues(t, 1, counting.drafts.Load(), "only shared-step, which no activation names, is read alone")
	require.Equal(t, len(direct.Items), len(got.Items), "reading once changes nothing about what is listed")
}

// The batched draft read lists exactly what reading each draft alone lists:
// an activation whose account workflow of the same name is complete (the
// project's still wins, shadowing it), one whose account workflow is
// unfinished, one whose workflow no machine has (broken, with where it
// lives), and plain ones.
func TestListInbox_BatchedDraftReadListsTheSame(t *testing.T) {
	f := newInboxFixture(t)
	daemonID := uuid.NewString()
	host := "default"
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID, Hostname: &host}))

	var stored []config.StoredWorkflow
	for _, slug := range []string{"release", "deploy", "triage"} {
		yaml := strings.Replace(releaseWorkflow, "name: release\n", "name: "+slug+"\n", 1)
		stored = append(stored, config.StoredWorkflow{Slug: slug, Name: slug, RelativePath: ".reliant/workflows/" + slug + ".yaml", YAMLContent: yaml})
	}
	wj, err := config.EncodeCatalogJSON(stored)
	require.NoError(t, err)
	db.HoldProjectOnMachineForTest(t, f.repo, f.project(), daemonID)
	_, err = f.repo.SyncProjectConfigSnapshot(f.ctx, &db.ProjectConfigRecord{
		ProjectID: f.project(), DaemonID: daemonID, ProjectWorkflowsJSON: wj, PushedAt: time.Now().UTC(),
	}, false, nil)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	for slug, status := range map[string]db.WorkflowDraftStatus{"release": db.WorkflowDraftStatusComplete, "deploy": db.WorkflowDraftStatusDraft} {
		require.NoError(t, f.repo.CreateWorkflowDraft(f.ctx, &db.WorkflowDraft{
			ID: uuid.NewString(), UserID: f.userID, Name: slug, Slug: slug, Status: status,
			Definition: strings.Replace(releaseWorkflow, "name: release\n", "name: "+slug+"\n", 1), CreatedAt: now, UpdatedAt: now,
		}))
	}
	cfg, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
	for i, workflow := range []string{"release", "deploy", "triage", "gone", "release"} {
		name := "merged"
		require.NoError(t, triggertest.Create(f.ctx, f.repo, &core.Trigger{
			ID: fmt.Sprintf("act-%d", i), UserID: f.userID, ProjectID: f.project(), Name: fmt.Sprintf("%s %d", workflow, i),
			Kind: core.TriggerKindSchedule, Enabled: true, Workflow: workflow, WorkflowTrigger: &name,
			DaemonID: daemonID, Presets: map[string]string{}, Params: map[string]any{}, Config: cfg,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	f.inbox = NewTestInbox(t, WithInboxDeps(mockDeps(noDraftBatchRepo{f.repo})))
	alone := f.list(t, nil)
	counting := &countingRepo{Repo: f.repo}
	f.inbox = NewTestInbox(t, WithInboxDeps(mockDeps(counting)))
	batched := f.list(t, nil)

	require.EqualValues(t, 1, counting.draftBatches.Load())
	require.Zero(t, counting.drafts.Load(), "every slug an activation names was in the batch")
	require.NotEmpty(t, batched.Items, "the activation of a workflow no machine has is listed")
	require.True(t, proto.Equal(alone, batched), "batched:\n%v\nalone:\n%v", batched, alone)
}

// noDraftBatchRepo is the repository without its batched draft read: the
// method of that name does not match, so resolution reads each draft alone.
type noDraftBatchRepo struct{ *db.Repo }

func (noDraftBatchRepo) GetWorkflowDraftsBySlugs() {}

// Owner grouping in the activation helper must preserve both tenant keys and
// lookup-time org authorization, even when owners share workflow names.
func TestPrefetchActivationDraftsIsolatesOwners(t *testing.T) {
	f := newInboxFixture(t)
	other := uuid.NewString()
	member := authz.WithMembership(f.ctx, memberOf{"home": true, "acme": true})
	left := authz.WithMembership(f.ctx, memberOf{"home": true})
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	acme := "acme"
	want := map[string]string{}
	var activations []*core.Trigger
	for _, owner := range []string{f.userID, other} {
		id := uuid.NewString()
		want[owner] = id
		require.NoError(t, f.repo.CreateWorkflowDraft(member, &db.WorkflowDraft{
			ID: id, UserID: owner, Name: "release", Slug: "release", Status: db.WorkflowDraftStatusComplete,
			Definition: releaseWorkflow, CreatedAt: at, UpdatedAt: at,
		}))
		require.NoError(t, f.repo.CreateWorkflowDraft(member, &db.WorkflowDraft{
			ID: uuid.NewString(), UserID: owner, Name: "private", Slug: "private", Status: db.WorkflowDraftStatusComplete,
			OrgID: &acme, Definition: releaseWorkflow, CreatedAt: at, UpdatedAt: at,
		}))
		activations = append(activations,
			&core.Trigger{UserID: owner, Workflow: "release"},
			&core.Trigger{UserID: owner, Workflow: "release"},
			&core.Trigger{UserID: owner, Workflow: "private"})
	}
	counting := &countingRepo{Repo: f.repo}
	r := launch.NewRequestResolver(counting)
	triggers.PrefetchActivationDrafts(member, r, activations, "test")
	require.EqualValues(t, 2, counting.draftBatches.Load(), "one batch per owner, not per slug")
	for _, owner := range []string{f.userID, other} {
		draft, err := r.GetWorkflowDraftBySlug(left, owner, "release")
		require.NoError(t, err)
		require.NotNil(t, draft)
		require.Equal(t, want[owner], draft.ID)
		usable, err := r.GetUsableWorkflowBySlug(left, owner, "release")
		require.NoError(t, err)
		require.NotNil(t, usable)
		require.Equal(t, want[owner], usable.ID)
		for _, ctx := range []context.Context{left, member, left} {
			draft, err = r.GetWorkflowDraftBySlug(ctx, owner, "private")
			require.NoError(t, err)
			usable, err = r.GetUsableWorkflowBySlug(ctx, owner, "private")
			require.NoError(t, err)
			if ctx == member {
				require.NotNil(t, draft)
				require.NotNil(t, usable)
			} else {
				require.Nil(t, draft, "an inaccessible org draft must stay hidden")
				require.Nil(t, usable, "an inaccessible org workflow must stay hidden")
			}
		}
	}
	require.Zero(t, counting.drafts.Load(), "all assertions must exercise the prefetched batches")
}

// A prefetched draft is authorized under each lookup's own context, as the
// single read is, never under the context it was prefetched with: an org
// workflow is hidden once its owner has left the org, and seen again while
// they are back, within one request resolver.
func TestPrefetchedDraftsAreAuthorizedPerLookup(t *testing.T) {
	f := newInboxFixture(t)
	member := authz.WithMembership(f.ctx, memberOf{"home": true, "acme": true})
	left := authz.WithMembership(f.ctx, memberOf{"home": true})
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	acme := "acme"
	require.NoError(t, f.repo.CreateWorkflowDraft(member, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: f.userID, Name: "release", Slug: "release", Status: db.WorkflowDraftStatusComplete,
		OrgID: &acme, Definition: releaseWorkflow, CreatedAt: at, UpdatedAt: at,
	}))

	for _, prefetchCtx := range []context.Context{member, left} {
		r := launch.NewRequestResolver(f.repo)
		require.NoError(t, launch.PrefetchWorkflowDrafts(prefetchCtx, r, f.userID, []string{"release"}))
		for _, c := range []struct {
			ctx     context.Context
			visible bool
		}{{left, false}, {member, true}, {left, false}} {
			draft, err := r.GetWorkflowDraftBySlug(c.ctx, f.userID, "release")
			require.NoError(t, err)
			require.Equal(t, c.visible, draft != nil, "draft")
			usable, err := r.GetUsableWorkflowBySlug(c.ctx, f.userID, "release")
			require.NoError(t, err)
			require.Equal(t, c.visible, usable != nil, "usable")
		}
	}
}
