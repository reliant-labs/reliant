// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// presetReadRepo is workflowListRepo plus the reads the preset RPCs make,
// every one counted (and delayed by latency).
type presetReadRepo struct {
	*workflowListRepo
}

func (r *presetReadRepo) ListSettingsByKey(context.Context, string, string) ([]*db.Setting, error) {
	r.count("ListSettingsByKey")
	return nil, nil
}

func (r *presetReadRepo) GetSetting(context.Context, string, *string, string) (*db.Setting, error) {
	r.count("GetSetting")
	return nil, fmt.Errorf("not found")
}

func (r *presetReadRepo) GetProjectPresetsJSON(context.Context, string) (*string, error) {
	r.count("GetProjectPresetsJSON")
	return nil, nil
}

func (r *presetReadRepo) ListPresetsByTag(context.Context, string, string, string) ([]*db.Preset, error) {
	r.count("ListPresetsByTag")
	return nil, nil
}

// GetDefaultPresetsBatch used to resolve every workflow name with its own
// project-workflows read and user-slug lookup: 2 + 2N queries. The catalog
// answers every name from one snapshot.
func TestGetDefaultPresetsBatch_ReadsDoNotScaleWithWorkflowNames(t *testing.T) {
	ctx, wr := newWorkflowListFixture(t, 12)
	repo := &presetReadRepo{wr}
	svc := NewPresetService(repo)

	names := []string{"builtin://agent", "team-flow-0", "team-flow-1", "no-such-flow"}
	for i := 0; i < 12; i++ {
		names = append(names, fmt.Sprintf("mine-%d", i))
	}
	_, err := svc.GetDefaultPresetsBatch(ctx, connect.NewRequest(&reliantv1.GetDefaultPresetsBatchRequest{
		ProjectId: repo.project.ID, WorkflowNames: names,
	}))
	require.NoError(t, err)

	assert.Equal(t, 1, repo.callCount("GetProjectWithUserCheck"))
	assert.Equal(t, 1, repo.callCount("ListSettingsByKey"))
	assert.Equal(t, 1, repo.callCount("ListWorkflowDraftsByUser"))
	assert.Equal(t, 1, repo.callCount("GetProjectWorkflowsJSON"))
	assert.Zero(t, repo.callCount("GetUsableWorkflowBySlug"), "slugs are answered from the catalog")
	assert.Zero(t, repo.callCount("GetSetting"), "overrides come from the one batched read")
	assert.Equal(t, 4, repo.totalReads(), "ownership, settings, user workflows, project workflows — for any batch size")
}

// ListPresetsForWorkflow's reads are independent; with a round trip per read
// they must overlap rather than add up.
func TestListPresetsForWorkflow_ReadsRunConcurrently(t *testing.T) {
	ctx, wr := newWorkflowListFixture(t, 1)
	repo := &presetReadRepo{wr}
	svc := NewPresetService(repo)
	req := connect.NewRequest(&reliantv1.ListPresetsForWorkflowRequest{
		ProjectId: repo.project.ID, WorkflowName: "builtin://agent", IncludeHidden: true,
	})
	_, err := svc.ListPresetsForWorkflow(ctx, req) // warm process caches
	require.NoError(t, err)

	repo.mu.Lock()
	repo.calls = nil
	repo.mu.Unlock()
	const latency = 50 * time.Millisecond
	repo.latency = latency

	start := time.Now()
	_, err = svc.ListPresetsForWorkflow(ctx, req)
	elapsed := time.Since(start)
	require.NoError(t, err)

	reads := repo.totalReads()
	require.GreaterOrEqual(t, reads, 5, "ownership, presets, visibility x2 and the workflow's own reads")
	// Serial: reads*latency. Concurrent: ownership, then the longest chain
	// (workflow, then presets by tag) — three round trips.
	assert.Less(t, elapsed, time.Duration(reads-2)*latency,
		"%d reads at %v took %v: they are running one after another", reads, latency, elapsed)
}

// The embedded presets are compiled into the binary: parsed once per process,
// not on every request.
func TestBuiltinPresets_ParsedOncePerProcess(t *testing.T) {
	first := builtinPresets()
	require.NotEmpty(t, first)
	second := builtinPresets()
	require.Equal(t, len(first), len(second))
	assert.Same(t, &first[0], &second[0], "the second call must reuse the first call's parse")

	svc := NewPresetService(nil)
	a := svc.loadAllPresetsFromDB(context.Background(), "")
	b := svc.loadAllPresetsFromDB(context.Background(), "")
	require.NotEmpty(t, a.Valid)
	for _, p := range a.Valid {
		for _, q := range b.Valid {
			if p.Name == q.Name {
				assert.NotSame(t, p, q, "callers get their own preset; the cache's copy is never handed out")
			}
		}
	}
}
