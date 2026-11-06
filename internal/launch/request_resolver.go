// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"fmt"
	"sync"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// NewRequestResolver wraps repo for one request that resolves many workflows:
// each distinct read is made once and its answer, error included, reused for
// the rest of the request.
//
// A list of activations resolves every one of them, then each one's children
// and presets, and each resolution reads the same machine catalog row (often
// hundreds of KB) and the owner's same account drafts again. Prod ListTriggers
// for a user with 20 activations made 41 catalog reads and 70 draft reads, p50
// 4.9s (trace d16638c2…, 2026-11-01). Through this, a list reads each catalog
// and each draft once.
//
// Use it for one request only: it never sees a write made after its first
// read, which is the consistent snapshot one response should render, and the
// wrong thing for anything that lives longer.
func NewRequestResolver(repo WorkflowResolver) WorkflowResolver {
	return &requestResolver{inner: repo}
}

type requestResolver struct {
	inner WorkflowResolver

	usable, drafts     memo[[2]string, *db.WorkflowDraft]
	presets            memo[[2]string, *db.Preset]
	projectPresets     memo[[3]string, *db.Preset]
	projectWorkflows   memo[string, *string]
	projectPresetsJSON memo[string, *string]
	machineWorkflows   memo[[2]string, syncedJSON]
	machinePresets     memo[[2]string, syncedJSON]
	diagnostics        memo[[3]string, string]
	pinnedCatalogs     memo[string, pinnedResult]
	provenance         memo[string, []config.WorkflowProvenance]
	machineNames       memo[string, string]
}

type syncedJSON struct {
	json   *string
	synced bool
}

type pinnedResult struct {
	catalog *db.PinnedCatalog
	found   bool
}

func (r *requestResolver) GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	return r.drafts.get([2]string{userID, slug}, func() (*db.WorkflowDraft, error) {
		return r.inner.GetWorkflowDraftBySlug(ctx, userID, slug)
	})
}

func (r *requestResolver) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	return r.usable.get([2]string{userID, slug}, func() (*db.WorkflowDraft, error) {
		return r.inner.GetUsableWorkflowBySlug(ctx, userID, slug)
	})
}

func (r *requestResolver) GetPresetBySlug(ctx context.Context, userID, slug string) (*db.Preset, error) {
	return r.presets.get([2]string{userID, slug}, func() (*db.Preset, error) {
		return r.inner.GetPresetBySlug(ctx, userID, slug)
	})
}

func (r *requestResolver) GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*db.Preset, error) {
	return r.projectPresets.get([3]string{userID, slug, projectID}, func() (*db.Preset, error) {
		return r.inner.GetPresetBySlugAndProject(ctx, userID, slug, projectID)
	})
}

func (r *requestResolver) GetProjectWorkflowsJSON(ctx context.Context, projectID string) (*string, error) {
	return r.projectWorkflows.get(projectID, func() (*string, error) {
		return r.inner.GetProjectWorkflowsJSON(ctx, projectID)
	})
}

func (r *requestResolver) GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error) {
	return r.projectPresetsJSON.get(projectID, func() (*string, error) {
		return r.inner.GetProjectPresetsJSON(ctx, projectID)
	})
}

func (r *requestResolver) GetProjectMachineWorkflowsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	got, err := r.machineWorkflows.get([2]string{projectID, daemonID}, func() (syncedJSON, error) {
		j, synced, err := r.inner.GetProjectMachineWorkflowsJSON(ctx, projectID, daemonID)
		return syncedJSON{j, synced}, err
	})
	return got.json, got.synced, err
}

func (r *requestResolver) GetProjectMachinePresetsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	got, err := r.machinePresets.get([2]string{projectID, daemonID}, func() (syncedJSON, error) {
		j, synced, err := r.inner.GetProjectMachinePresetsJSON(ctx, projectID, daemonID)
		return syncedJSON{j, synced}, err
	})
	return got.json, got.synced, err
}

func (r *requestResolver) WorkflowSourceDiagnostic(ctx context.Context, projectID, daemonID, ref string) (string, error) {
	return r.diagnostics.get([3]string{projectID, daemonID, ref}, func() (string, error) {
		return r.inner.WorkflowSourceDiagnostic(ctx, projectID, daemonID, ref)
	})
}

// pinnedStore is workflowsource.PinnedStore, which resolution asks its store
// for by assertion when a scope carries a catalog pin.
type pinnedStore interface {
	GetPinnedProjectCatalog(ctx context.Context, pin string) (*db.PinnedCatalog, bool, error)
}

// GetPinnedProjectCatalog serves a pinned scope when the wrapped store can;
// otherwise it fails as resolution fails a store that cannot read pins.
func (r *requestResolver) GetPinnedProjectCatalog(ctx context.Context, pin string) (*db.PinnedCatalog, bool, error) {
	pinned, ok := r.inner.(pinnedStore)
	if !ok {
		return nil, false, fmt.Errorf("this store cannot read pinned workflow catalogs")
	}
	got, err := r.pinnedCatalogs.get(pin, func() (pinnedResult, error) {
		c, found, err := pinned.GetPinnedProjectCatalog(ctx, pin)
		return pinnedResult{c, found}, err
	})
	return got.catalog, got.found, err
}

// ProjectWorkflowProvenance serves the wrapped store's, once per project. A
// store without it has no provenance: nil, as attribution reads a store that
// cannot say.
func (r *requestResolver) ProjectWorkflowProvenance(ctx context.Context, projectID string) ([]config.WorkflowProvenance, error) {
	p, ok := r.inner.(WorkflowProvenanceReader)
	if !ok {
		return nil, nil
	}
	return r.provenance.get(projectID, func() ([]config.WorkflowProvenance, error) {
		return p.ProjectWorkflowProvenance(ctx, projectID)
	})
}

// MachineDisplayName serves the wrapped store's, once per machine; "" (no
// name) from a store without it.
func (r *requestResolver) MachineDisplayName(ctx context.Context, daemonID string) string {
	n, ok := r.inner.(MachineNamer)
	if !ok {
		return ""
	}
	name, _ := r.machineNames.get(daemonID, func() (string, error) {
		return n.MachineDisplayName(ctx, daemonID), nil
	})
	return name
}

// memo is one read's answers by argument. The zero value is ready to use.
type memo[K comparable, V any] struct {
	mu   sync.Mutex
	seen map[K]memoAnswer[V]
}

type memoAnswer[V any] struct {
	v   V
	err error
}

// get returns key's answer, reading it with load the first time. The lock is
// held across load so concurrent askers of one key read it once.
func (m *memo[K, V]) get(key K, load func() (V, error)) (V, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.seen[key]; ok {
		return a.v, a.err
	}
	v, err := load()
	if m.seen == nil {
		m.seen = map[K]memoAnswer[V]{}
	}
	m.seen[key] = memoAnswer[V]{v, err}
	return v, err
}
