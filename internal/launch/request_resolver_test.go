// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// Each distinct read reaches the store once per request resolver, its error
// included; a different argument is a different read.
func TestRequestResolverReadsEachThingOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	catalog := `[]`
	var catalogReads, draftReads int
	boom := errors.New("store down")
	inner := &MockWorkflowResolver{
		GetProjectMachineWorkflowsJSONFunc: func(_ context.Context, projectID, _ string) (*string, bool, error) {
			catalogReads++
			if projectID == "broken" {
				return nil, false, boom
			}
			return &catalog, true, nil
		},
		GetUsableWorkflowBySlugFunc: func(_ context.Context, _, slug string) (*db.WorkflowDraft, error) {
			draftReads++
			if slug == "missing" {
				return nil, nil
			}
			return &db.WorkflowDraft{Slug: slug}, nil
		},
	}
	r := NewRequestResolver(inner)

	for i := 0; i < 3; i++ {
		got, synced, err := r.GetProjectMachineWorkflowsJSON(ctx, "p", "d")
		require.NoError(t, err)
		require.True(t, synced)
		require.Same(t, &catalog, got)
		_, _, err = r.GetProjectMachineWorkflowsJSON(ctx, "broken", "d")
		require.ErrorIs(t, err, boom)
		d, err := r.GetUsableWorkflowBySlug(ctx, "u", "triage")
		require.NoError(t, err)
		require.Equal(t, "triage", d.Slug)
		d, err = r.GetUsableWorkflowBySlug(ctx, "u", "missing")
		require.NoError(t, err)
		require.Nil(t, d, "an absent workflow stays absent")
	}
	require.Equal(t, 2, catalogReads, "one read per (project, machine), failures included")
	require.Equal(t, 2, draftReads, "one read per (user, slug)")
}

// A store that cannot read pinned catalogs is refused as resolution refuses
// it, rather than answered with a guess.
func TestRequestResolverRefusesPinsItsStoreCannotRead(t *testing.T) {
	t.Parallel()
	r := NewRequestResolver(&MockWorkflowResolver{}).(interface {
		GetPinnedProjectCatalog(context.Context, string) (*db.PinnedCatalog, bool, error)
	})
	_, found, err := r.GetPinnedProjectCatalog(context.Background(), "pin-1")
	require.Error(t, err)
	require.False(t, found)
}
