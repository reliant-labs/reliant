// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/store"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/projectaccess"
	"github.com/reliant-labs/reliant/internal/threads"
)

// fireReadCountingRepo is the real store, counting the reads a fire repeats when it
// resolves one workflow several times: the pinned catalog row (hundreds of
// KB in prod, and an UPDATE … RETURNING that bumps last_used_at) and the
// owner's preset lookups.
type fireReadCountingRepo struct {
	*db.Repo
	mu      sync.Mutex
	pinned  map[string]int
	presets int
}

func (r *fireReadCountingRepo) GetPinnedProjectCatalog(ctx context.Context, pin string) (*db.PinnedCatalog, bool, error) {
	r.mu.Lock()
	r.pinned[pin]++
	r.mu.Unlock()
	return r.Repo.GetPinnedProjectCatalog(ctx, pin)
}

func (r *fireReadCountingRepo) GetPresetBySlug(ctx context.Context, userID, slug string) (*db.Preset, error) {
	r.mu.Lock()
	r.presets++
	r.mu.Unlock()
	return r.Repo.GetPresetBySlug(ctx, userID, slug)
}

func (r *fireReadCountingRepo) GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*db.Preset, error) {
	r.mu.Lock()
	r.presets++
	r.mu.Unlock()
	return r.Repo.GetPresetBySlugAndProject(ctx, userID, slug, projectID)
}

func (r *fireReadCountingRepo) counts() (map[string]int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pinned := make(map[string]int, len(r.pinned))
	for k, v := range r.pinned {
		pinned[k] = v
	}
	return pinned, r.presets
}

// fanOutRelease is release as prod's automations are: a project child and an
// agent with a preset, each of which a resolution reads and validates.
const fanOutRelease = `name: release
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: release
entry: [check]
nodes:
  - id: check
    type: workflow
    ref: child
  - id: plan
    type: workflow
    ref: builtin://agent
    presets:
      default: planner
edges:
  - from: check
    default: plan
`

const provenanceChild = `name: child
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// pushCatalog syncs workflows (slug → content) from cicd's control-plane
// checkout.
func (p *provenanceProject) pushCatalog(t *testing.T, at int64, workflows map[string]string) {
	t.Helper()
	stored := make([]config.StoredWorkflow, 0, len(workflows))
	for slug, content := range workflows {
		stored = append(stored, config.StoredWorkflow{Slug: slug, Name: slug, RelativePath: "control-plane/.reliant/workflows/" + slug + ".yaml", YAMLContent: content})
	}
	wj, err := config.EncodeCatalogJSON(stored)
	require.NoError(t, err)
	_, err = p.repo.SyncProjectConfigSnapshot(p.ctx, &db.ProjectConfigRecord{
		ProjectID: p.projectID, DaemonID: p.daemonID, ProjectWorkflowsJSON: wj, PushedAt: time.UnixMilli(at).UTC(),
	}, false, nil)
	require.NoError(t, err)
}

// A fire resolves its workflow again and again — the definition it records
// and validates, its declaration, its check, its capacity policy — and the
// launch resolves it again to admit the tree, build and validate the inputs
// and record the selection, every time in the one catalog snapshot the fire
// pinned. Each resolution re-read that snapshot row (an UPDATE … RETURNING
// of the whole catalog) and the owner's presets: prod's FireTriggerEvent made
// 40 snapshot reads and 50 preset lookups in one 12.9s activity (trace
// 7f55dfed…, 2026-11-20); this workflow made 16 and 8. Each side reads
// through a memo of its own store now: the fire's decision reads the snapshot
// once and the launch's admission once, and each looks each preset up once.
func TestFireReadsItsPinnedCatalogOncePerSide(t *testing.T) {
	for _, inbound := range []bool{false, true} {
		t.Run(map[bool]string{false: "schedule", true: "inbound"}[inbound], func(t *testing.T) {
			p := newProvenanceProject(t)
			p.pushCatalog(t, 2000, map[string]string{"release": fanOutRelease, "child": provenanceChild})
			// A fixed instant on the schedule's own slot (cron "0 9 * * *"):
			// the worktree's timestamps, the event's occurrence and the fire's
			// scheduled time read no clock, so no boundary can move them.
			now := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
			require.NoError(t, p.repo.CreateWorktree(p.ctx, &db.Worktree{ID: uuid.NewString(), Name: "main", Path: t.TempDir(), Branch: "main", BaseBranch: "main", ProjectID: p.projectID, Status: int32(pb.WorktreeStatus_WORKTREE_STATUS_ACTIVE), IsMain: true, CreatedAt: now, UpdatedAt: now, LastActive: now}))
			repo := &fireReadCountingRepo{Repo: p.repo, pinned: map[string]int{}}
			starter := &pinRecordingStarter{}
			launcher := launch.New(launch.Deps{Store: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), Projects: projectaccess.NewFromORM(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Threads: threads.New(threads.Deps{Repo: p.repo, Chats: chataccess.NewFromORM(p.repo.ORM()), Messages: store.NewMessageExtStore(p.repo.ORM()), Senders: store.NewThreadExtStore(p.repo.ORM())}), Temporal: starter, Runs: noopRunRecorder{}, TaskQueue: "test-q"})
			workflows := LaunchWorkflows{Repo: repo}
			key := "reads-" + uuid.NewString()
			kind := core.TriggerEventKindSchedule
			var (
				out *FireOutput
				err error
			)
			if inbound {
				kind = core.TriggerEventKindWebhook
				created, cerr := p.repo.CreateTriggerEvent(p.ctx, &core.TriggerEvent{ID: uuid.NewString(), UserID: p.userID, TriggerID: &p.webhook.ID, Kind: kind, DedupeKey: key, OccurredAt: now, CreatedAt: now, Payload: map[string]any{}, Outcome: core.TriggerEventPending})
				require.NoError(t, cerr)
				require.True(t, created)
				out, err = NewEventFirer(NewStores(repo.Repo), launcher).WithWorkflows(workflows).Fire(p.ctx, EventFireInput{TriggerID: p.webhook.ID, Kind: kind, DedupeKey: key, Manual: true})
			} else {
				out, err = NewFirer(NewStores(repo.Repo), launcher).WithWorkflows(workflows).Fire(p.ctx, FireRequest{TriggerID: p.schedule.ID, FireWorkflowID: key, ScheduledAt: now})
			}
			require.NoError(t, err)
			require.Equal(t, string(core.TriggerEventLaunched), out.Outcome, out.Reason)

			recorded, err := p.repo.GetTriggerEventByDedupe(p.ctx, kind, key)
			require.NoError(t, err)
			require.NotEmpty(t, recorded.Definition.CatalogPin, "the fire pinned the catalog it resolved")
			require.Len(t, starter.inputs, 1)
			require.Equal(t, recorded.Definition.CatalogPin, starter.inputs[0].ExecContext.CatalogPin, "the run launches on the pin the fire recorded")

			pinned, presets := repo.counts()
			require.Equal(t, map[string]int{recorded.Definition.CatalogPin: 2}, pinned,
				"the fire's resolutions read its pinned snapshot once, and the launch's admission once")
			// planner: the owner's project preset and own preset, looked up
			// once by the fire's validation and once by the launch's.
			require.Equal(t, 4, presets, "each side looks each preset up once")
		})
	}
}
