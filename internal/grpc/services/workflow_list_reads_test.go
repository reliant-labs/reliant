// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// workflowListRepo is the repository ListWorkflows runs against, in memory and
// counting every read workflow listing makes. Methods it does not implement
// fall through to the nil embedded Repository and panic, so a listing that
// grows a new read fails here until the fake learns it.
type workflowListRepo struct {
	db.Repository

	project          *db.Project
	projectWorkflows *string
	drafts           []*db.WorkflowDraft
	// latency is added to every read, standing in for the database round
	// trip that turned per-ref reads into seconds in prod.
	latency time.Duration

	mu    sync.Mutex
	calls map[string]int
}

func (r *workflowListRepo) count(name string) {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[name]++
	r.mu.Unlock()
	if r.latency > 0 {
		time.Sleep(r.latency)
	}
}

func (r *workflowListRepo) callCount(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[name]
}

func (r *workflowListRepo) totalReads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		n += c
	}
	return n
}

func (r *workflowListRepo) GetProjectWithUserCheck(_ context.Context, id, userID string) (*db.Project, error) {
	r.count("GetProjectWithUserCheck")
	if r.project == nil || r.project.ID != id || r.project.UserID != userID {
		return nil, fmt.Errorf("project not found")
	}
	return r.project, nil
}

func (r *workflowListRepo) GetProject(_ context.Context, id string) (*db.Project, error) {
	r.count("GetProject")
	if r.project == nil || r.project.ID != id {
		return nil, fmt.Errorf("project not found")
	}
	return r.project, nil
}

func (r *workflowListRepo) GetProjectWorkflowsJSON(_ context.Context, projectID string) (*string, error) {
	r.count("GetProjectWorkflowsJSON")
	if r.project == nil || r.project.ID != projectID {
		return nil, sql.ErrNoRows
	}
	return r.projectWorkflows, nil
}

func (r *workflowListRepo) ListWorkflowDraftsByUser(_ context.Context, userID string) ([]*db.WorkflowDraft, error) {
	r.count("ListWorkflowDraftsByUser")
	var out []*db.WorkflowDraft
	for _, d := range r.drafts {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (r *workflowListRepo) draftBySlug(userID, slug string) *db.WorkflowDraft {
	for _, d := range r.drafts {
		if d.UserID == userID && d.Slug == slug {
			return d
		}
	}
	return nil
}

func (r *workflowListRepo) GetWorkflowDraftBySlug(_ context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	r.count("GetWorkflowDraftBySlug")
	return r.draftBySlug(userID, slug), nil
}

// GetUsableWorkflowBySlug mirrors db.Repo's: a complete, visible workflow, or
// a not-runnable verdict for a visible draft.
func (r *workflowListRepo) GetUsableWorkflowBySlug(_ context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	r.count("GetUsableWorkflowBySlug")
	d := r.draftBySlug(userID, slug)
	if d == nil || d.IsHidden {
		return nil, nil
	}
	if d.Status != db.WorkflowDraftStatusComplete {
		return nil, &db.WorkflowDraftNotRunnableError{Slug: slug}
	}
	return d, nil
}

func (r *workflowListRepo) ListVisibilityOverrides(context.Context, string, int32) (map[string]bool, error) {
	r.count("ListVisibilityOverrides")
	return map[string]bool{}, nil
}

func (r *workflowListRepo) ListHiddenItemDefaults(context.Context, int32) ([]string, error) {
	r.count("ListHiddenItemDefaults")
	return nil, nil
}

// newWorkflowListFixture is a project with synced workflows and a user whose
// own workflows ref each other, the project's and the builtins — the shape
// that made one listing resolve dozens of refs.
func newWorkflowListFixture(t *testing.T, userWorkflows int) (context.Context, *workflowListRepo) {
	t.Helper()
	const userID = "list-user"
	project := &db.Project{ID: "list-project", UserID: userID, Name: "p", Path: "/tmp/p"}

	stored := []config.StoredWorkflow{}
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("team-flow-%d", i)
		stored = append(stored, config.StoredWorkflow{
			Slug:         name,
			Name:         name,
			RelativePath: ".reliant/workflows/" + name + ".yaml",
			YAMLContent: fmt.Sprintf(`
name: %s
entry: [run]
nodes:
  - id: run
    type: workflow
    args:
      ref: builtin://agent
`, name),
		})
	}
	storedJSON, err := json.Marshal(stored)
	require.NoError(t, err)
	storedStr := string(storedJSON)

	repo := &workflowListRepo{project: project, projectWorkflows: &storedStr}
	now := time.Now().UTC()
	for i := 0; i < userWorkflows; i++ {
		slug := fmt.Sprintf("mine-%d", i)
		repo.drafts = append(repo.drafts, &db.WorkflowDraft{
			ID: slug, UserID: userID, Name: slug, Slug: slug,
			Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now, Version: 1,
			Definition: fmt.Sprintf(`
name: %s
entry: [helper, team, agent]
nodes:
  - id: helper
    type: workflow
    args:
      ref: mine-%d
  - id: team
    type: workflow
    args:
      ref: team-flow-%d
  - id: agent
    type: workflow
    args:
      ref: builtin://agent
`, slug, (i+1)%userWorkflows, i%3),
		})
	}
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	return ctx, repo
}

// A listing resolves every ref of every workflow it lists, twice over (once to
// validate each of the user's workflows, once to say what each needs a machine
// for). Each resolution used to read the project's synced workflows and look
// the ref up among the user's own, so a listing's reads grew with the number
// of refs: in prod each project read was an 18 MB row and ListWorkflows took
// 5–10s at p90. Every distinct read is now made once per listing.
func TestListWorkflows_ReadsEachSourceOncePerListing(t *testing.T) {
	ctx, repo := newWorkflowListFixture(t, 12)
	svc := NewWorkflowService(repo, nil)

	resp, err := svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{
		ProjectId: repo.project.ID, IncludeHidden: true,
	}))
	require.NoError(t, err)

	listed := map[string]bool{}
	for _, wf := range resp.Msg.GetWorkflows() {
		listed[wf.GetName()] = true
	}
	require.True(t, listed["mine-0"], "the user's workflows are listed")
	require.True(t, listed["team-flow-0"], "the project's workflows are listed")
	require.True(t, listed["builtin://agent"], "the builtins are listed")

	assert.Equal(t, 1, repo.callCount("GetProjectWorkflowsJSON"), "the project's synced workflows are read once")
	assert.Equal(t, 1, repo.callCount("GetProjectWithUserCheck"), "the project is read once")
	assert.Zero(t, repo.callCount("GetProject"), "the ownership check already returned the project")
	// One lookup per distinct user slug a ref names: mine-0..mine-11 and the
	// three team-flow refs that miss among the user's own.
	assert.LessOrEqual(t, repo.callCount("GetUsableWorkflowBySlug"), 12+3,
		"each slug is looked up among the user's workflows at most once")
	assert.LessOrEqual(t, repo.totalReads(), 6+12+3,
		"a listing's reads do not grow with the number of refs it resolves")
}

// The read count is what made the latency: with a realistic round trip per
// read, a listing must cost a handful of round trips, not one per ref.
func TestListWorkflows_LatencyDoesNotScaleWithRefs(t *testing.T) {
	ctx, repo := newWorkflowListFixture(t, 12)
	svc := NewWorkflowService(repo, nil)
	req := connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: repo.project.ID, IncludeHidden: true})
	// Warm the process-wide caches (parsed builtins, tool registry) so what
	// is measured is the listing's round trips, not first-use CPU.
	_, err := svc.ListWorkflows(ctx, req)
	require.NoError(t, err)

	repo.mu.Lock()
	repo.calls = nil
	repo.mu.Unlock()
	repo.latency = 20 * time.Millisecond
	start := time.Now()
	_, err = svc.ListWorkflows(ctx, req)
	elapsed := time.Since(start)
	require.NoError(t, err)
	// Before: 522 reads in a row, ~10s at 20ms each. Now a handful, the
	// independent ones concurrent.
	assert.Less(t, elapsed, time.Second,
		"ListWorkflows made %d reads at 20ms each", repo.totalReads())
}
