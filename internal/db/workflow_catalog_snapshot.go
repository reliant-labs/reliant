package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/store"
)

// CatalogPinScope is what a run's catalog is pinned for: the project and the
// machine whose synced workflows and presets it resolves against, and the
// owner whose account workflows a ref may resolve to before the project's.
// DraftRoot is the slug of the workflow a builder test runs as a draft (its
// unfinished definition, not its usable one); empty for every other run.
type CatalogPinScope struct {
	ProjectID string
	DaemonID  string
	UserID    string
	DraftRoot string
}

// PinnedCatalog is the catalog a run started with.
type PinnedCatalog struct {
	// Workflows and Presets are the project's synced catalog, as stored.
	Workflows, Presets *string
	// UserWorkflows is how each of the owner's account workflows resolved
	// at the pin, by slug. A slug absent here resolved to nothing.
	UserWorkflows map[string]PinnedUserWorkflow
	// DaemonID is the machine whose catalog was pinned, "" for the union of
	// every machine's (no one machine's copy can be named).
	DaemonID string
	// RepoStates is the git state DaemonID's files were synced from, read in
	// the same row read as Workflows; nil when none was reported.
	RepoStates []config.SyncedRepoState
}

// PinnedUserWorkflow is one account workflow as a ref resolved it at the pin.
type PinnedUserWorkflow struct {
	// Definition is the YAML a ref ran: the complete workflow, or, for the
	// draft root, its draft.
	Definition string `json:"definition,omitempty"`
	// NotRunnable marks an unfinished, visible workflow: a ref to it is an
	// error rather than a fall-through to a project workflow of the name.
	NotRunnable bool `json:"not_runnable,omitempty"`
}

// PinProjectCatalog stores the catalog a run resolves against right now and
// returns its pin: the id every later load of the run reads it back by
// (GetPinnedProjectCatalog). Identical catalogs share one pin.
//
// The catalog is everything a workflow ref or preset name can resolve to:
// the project's workflows and presets as scope.DaemonID synced them (or the
// union of every machine's when it is empty or has never synced the
// project), and the owner's account workflows. A run that pins it executes
// one definition tree from start to finish: a child workflow or preset loaded
// an hour in is the one that existed when the run started, not whatever a
// machine pushed, or the owner saved, since.
func (r *Repo) PinProjectCatalog(ctx context.Context, scope CatalogPinScope) (string, error) {
	if scope.ProjectID == "" {
		return "", fmt.Errorf("project ID cannot be empty")
	}
	var workflows, presets, repoStates *string
	var machine *string
	synced := false
	if scope.DaemonID != "" {
		// One row read: the machine's catalog and the git state it was
		// synced from, so the pin's provenance is the catalog's.
		row, err := store.GetProjectMachineCatalog(ctx, r.ORM(), scope.ProjectID, scope.DaemonID)
		if err != nil {
			return "", err
		}
		if row != nil {
			workflows, presets, repoStates, synced = row.WorkflowsJSON, row.PresetsJSON, row.RepoStatesJSON, true
			daemonID := scope.DaemonID
			machine = &daemonID
		}
	}
	if !synced {
		// One statement: the union's workflows and presets from one sync.
		var err error
		if workflows, presets, err = store.GetProjectCatalogJSON(ctx, r.ORM(), scope.ProjectID); err != nil {
			return "", err
		}
	}
	userWorkflows, err := r.pinnableUserWorkflows(ctx, scope)
	if err != nil {
		return "", err
	}
	// The machine and its git state are part of the content address: two
	// machines (or two commits) holding the same bytes are two snapshots,
	// each naming its own provenance.
	pin := catalogPin(scope.ProjectID, workflows, presets, userWorkflows, machine, repoStates)
	now := time.Now().UTC()
	inTx := r.DB.inTx(ctx)
	if _, touchDue, ok := r.pinnedCatalogs().get(pin, now); ok && !touchDue && !inTx {
		// Content is cached, but another process may have pruned the row.
		stored, err := store.HasWorkflowCatalogSnapshot(ctx, r.ORM(), pin)
		if err != nil {
			return "", err
		}
		if stored {
			return pin, nil
		}
		r.pinnedCatalogs().remove(pin)
	}
	row := &store.WorkflowCatalogSnapshot{
		ID: pin, ProjectID: scope.ProjectID, DaemonID: machine,
		WorkflowsJSON: workflows, PresetsJSON: presets, UserWorkflowsJSON: userWorkflows, RepoStatesJSON: repoStates,
	}
	if err := store.PutWorkflowCatalogSnapshot(ctx, r.ORM(), row, now); err != nil {
		return "", err
	}
	// The run reads it back next. Kept only when committed: a pin stored in a
	// transaction that rolls back was never stored.
	if !inTx {
		if catalog, err := pinnedCatalogOf(row); err == nil {
			r.pinnedCatalogs().add(pin, catalog, now)
		}
	}
	return pin, nil
}

// pinnableUserWorkflows is the owner's account workflows as a ref resolves
// them (workflowsource's user lookup), encoded: every complete, visible one;
// every visible unfinished one as not runnable; and the draft root's draft.
// nil when the owner has none.
func (r *Repo) pinnableUserWorkflows(ctx context.Context, scope CatalogPinScope) (*string, error) {
	if scope.UserID == "" {
		return nil, nil
	}
	drafts, err := store.ListUserWorkflowDrafts(ctx, r.ORM(), scope.UserID)
	if err != nil {
		return nil, fmt.Errorf("read the account workflows to pin: %w", err)
	}
	pinned := map[string]PinnedUserWorkflow{}
	for _, d := range drafts {
		switch {
		case d.IsHidden:
		case d.Slug == scope.DraftRoot, d.Status == WorkflowDraftStatusComplete:
			pinned[d.Slug] = PinnedUserWorkflow{Definition: d.Definition}
		default:
			pinned[d.Slug] = PinnedUserWorkflow{NotRunnable: true}
		}
	}
	if len(pinned) == 0 {
		return nil, nil
	}
	// json.Marshal orders map keys, so equal sets encode, and pin, equally.
	encoded, err := json.Marshal(pinned)
	if err != nil {
		return nil, err
	}
	s := string(encoded)
	return &s, nil
}

// GetPinnedProjectCatalog reads a pinned catalog; found is false when the pin
// is unknown (never stored, or pruned).
//
// A pin names its content (catalogPin), so a catalog read once is served from
// this Repo's cache after (pinnedCatalogCache), with existence checked on every
// read. Its last_used_at, which only the retention prune reads, is bumped at
// most hourly outside transactions. Transactional reads use storage directly.
// The returned
// catalog's strings, map and slice are shared: read them, never write them.
func (r *Repo) GetPinnedProjectCatalog(ctx context.Context, pin string) (*PinnedCatalog, bool, error) {
	if pin == "" {
		return nil, false, fmt.Errorf("catalog pin cannot be empty")
	}
	now := time.Now().UTC()
	cache := r.pinnedCatalogs()
	// A transaction must read its own storage view, including local deletes,
	// and must not publish a last-used bump that can still roll back.
	inTx := r.DB.inTx(ctx)
	if catalog, touchDue, ok := cache.get(pin, now); ok && !inTx {
		var stored bool
		var err error
		if touchDue {
			stored, err = store.TouchWorkflowCatalogSnapshot(ctx, r.ORM(), pin, now)
		} else {
			stored, err = store.HasWorkflowCatalogSnapshot(ctx, r.ORM(), pin)
		}
		if err != nil {
			return nil, false, err
		}
		if !stored {
			cache.remove(pin)
			countPinnedRead(ctx, "miss")
			return nil, false, nil
		}
		if touchDue {
			cache.touched(pin, now)
		}
		countPinnedRead(ctx, "hit")
		return sharedPinnedCatalog(catalog), true, nil
	}
	countPinnedRead(ctx, "miss")
	row, err := store.UseWorkflowCatalogSnapshot(ctx, r.ORM(), pin, now)
	if err != nil || row == nil {
		return nil, false, err
	}
	catalog, err := pinnedCatalogOf(row)
	if err != nil {
		return nil, false, err
	}
	// Kept only when read outside a transaction: one could be reading a pin
	// it stored itself and may yet roll back.
	if !inTx {
		cache.add(pin, catalog, now)
		return sharedPinnedCatalog(catalog), true, nil
	}
	return catalog, true, nil
}

// pinnedCatalogOf decodes a stored pinned catalog.
func pinnedCatalogOf(row *store.WorkflowCatalogSnapshot) (*PinnedCatalog, error) {
	catalog := &PinnedCatalog{Workflows: row.WorkflowsJSON, Presets: row.PresetsJSON}
	if row.DaemonID != nil {
		catalog.DaemonID = *row.DaemonID
	}
	var err error
	if catalog.RepoStates, err = config.ParseSyncedRepoStates(row.RepoStatesJSON); err != nil {
		return nil, fmt.Errorf("read the git state of catalog %s: %w", row.ID, err)
	}
	if row.UserWorkflowsJSON != nil {
		if err := json.Unmarshal([]byte(*row.UserWorkflowsJSON), &catalog.UserWorkflows); err != nil {
			return nil, fmt.Errorf("read the account workflows of catalog %s: %w", row.ID, err)
		}
	}
	return catalog, nil
}

// catalogPin is a catalog's content address within its project.
func catalogPin(projectID string, parts ...*string) string {
	h := sha256.New()
	for _, part := range append([]*string{&projectID}, parts...) {
		if part != nil {
			h.Write([]byte(*part))
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
