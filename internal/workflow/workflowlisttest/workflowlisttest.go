// Copyright (c) 2025 Reliant Labs

// Package workflowlisttest is a fake of the reads a workflow listing makes,
// counted and optionally delayed, for the tests that pin how many reads
// WorkflowService.ListWorkflows and PresetService's batch reads make. Both
// services' tests share it, so each can move to its own package
// (research/forge-migration/05-p3a-handler-map.md). Only _test.go files
// import it.
//
//forge:exclude-contract: a test double of the workflow-listing reads on *db.Repo, imported only by tests; nothing to fake behind it
package workflowlisttest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflowdrafts"
)

// Repo is the repository ListWorkflows runs against, in memory and
// counting every read workflow listing makes. Methods it does not implement
// fall through to the nil embedded *db.Repo and panic, so a listing that
// grows a new read fails here until the fake learns it. The nil *db.Repo,
// rather than any one consumer's role, is what lets it fill the Repo of every
// handler that lists workflows (handlers/workflow, handlers/preset), whatever
// each one declares.
type Repo struct {
	*db.Repo

	Project          *db.Project
	projectWorkflows *string
	drafts           []*db.WorkflowDraft
	// Latency is added to every read, standing in for the database round
	// trip that turned per-ref reads into seconds in prod.
	Latency time.Duration

	mu       sync.Mutex
	calls    map[string]int
	inFlight int
	peak     int
}

// Count records one read of name, then waits Latency. The read counts as in
// flight for that wait, so PeakInFlight sees reads that overlap.
func (r *Repo) Count(name string) {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[name]++
	r.inFlight++
	r.peak = max(r.peak, r.inFlight)
	r.mu.Unlock()
	if r.Latency > 0 {
		time.Sleep(r.Latency)
	}
	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
}

// PeakInFlight is the most reads that were in flight at once since the last
// ResetCounts. Unlike wall-clock time it does not depend on how loaded the
// host is, so a test can assert reads overlap without timing them.
func (r *Repo) PeakInFlight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// CallCount is how many times name was read.
func (r *Repo) CallCount(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[name]
}

// ResetCounts forgets every read counted so far.
func (r *Repo) ResetCounts() {
	r.mu.Lock()
	r.calls = nil
	r.peak = r.inFlight
	r.mu.Unlock()
}

// TotalReads is how many reads were made in all.
func (r *Repo) TotalReads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		n += c
	}
	return n
}

func (r *Repo) GetProjectWithUserCheck(_ context.Context, id, userID string) (*db.Project, error) {
	r.Count("GetProjectWithUserCheck")
	if r.Project == nil || r.Project.ID != id || r.Project.UserID != userID {
		return nil, fmt.Errorf("project not found")
	}
	return r.Project, nil
}

func (r *Repo) GetProject(_ context.Context, id string) (*db.Project, error) {
	r.Count("GetProject")
	if r.Project == nil || r.Project.ID != id {
		return nil, fmt.Errorf("project not found")
	}
	return r.Project, nil
}

func (r *Repo) GetProjectWorkflowsJSON(_ context.Context, projectID string) (*string, error) {
	r.Count("GetProjectWorkflowsJSON")
	if r.Project == nil || r.Project.ID != projectID {
		return nil, svcerr.NotFound("project config")
	}
	return r.projectWorkflows, nil
}

func (d *Drafts) ListWorkflowDraftsByUser(_ context.Context, userID string) ([]*db.WorkflowDraft, error) {
	d.r.Count("ListWorkflowDraftsByUser")
	var out []*db.WorkflowDraft
	for _, draft := range d.r.drafts {
		if draft.UserID == userID {
			out = append(out, draft)
		}
	}
	return out, nil
}

// Drafts is the saved-workflow reads of the fake, answering as
// workflowdrafts.Service does: it is what a handler's Drafts dependency is set
// to. Writes fall through to the nil embedded service and panic.
type Drafts struct {
	workflowdrafts.Service
	r *Repo
}

// Drafts is the fake's workflowdrafts.Service.
func (r *Repo) Drafts() *Drafts { return &Drafts{r: r} }

func (r *Repo) draftBySlug(userID, slug string) *db.WorkflowDraft {
	for _, d := range r.drafts {
		if d.UserID == userID && d.Slug == slug {
			return d
		}
	}
	return nil
}

func (d *Drafts) GetWorkflowDraftBySlug(_ context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	d.r.Count("GetWorkflowDraftBySlug")
	return d.r.draftBySlug(userID, slug), nil
}

// GetUsableWorkflowBySlug mirrors db.Repo's: a complete, visible workflow, or
// a not-runnable verdict for a visible draft.
func (d *Drafts) GetUsableWorkflowBySlug(_ context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	d.r.Count("GetUsableWorkflowBySlug")
	row := d.r.draftBySlug(userID, slug)
	if row == nil || row.IsHidden {
		return nil, nil
	}
	if row.Status != db.WorkflowDraftStatusComplete {
		return nil, &db.WorkflowDraftNotRunnableError{Slug: slug}
	}
	return row, nil
}

func (r *Repo) ListVisibilityOverrides(context.Context, string, int32) (map[string]bool, error) {
	r.Count("ListVisibilityOverrides")
	return map[string]bool{}, nil
}

func (r *Repo) ListHiddenItemDefaults(context.Context, int32) ([]string, error) {
	r.Count("ListHiddenItemDefaults")
	return nil, nil
}

// NewFixture is a project with synced workflows and a user whose own
// workflows ref each other, the project's and the builtins — the shape that
// made one listing resolve dozens of refs. It returns a context signed in as
// that user.
func NewFixture(t *testing.T, userWorkflows int) (context.Context, *Repo) {
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

	repo := &Repo{Project: project, projectWorkflows: &storedStr}
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
