package preset

import (
	"context"
	"errors"
	"sync"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// ListPresetsForWorkflow returns presets compatible with a specific workflow.
// Priority: user (database) > project (stored config) > builtin
// Presets are matched by tag - a preset with tag "agent" will match any
// workflow/group with tag "agent".
func (s *Service) ListPresetsForWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPresetsForWorkflowRequest],
) (*connect.Response[reliantv1.ListPresetsForWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if req.Msg.WorkflowName == "" {
		return nil, svcerr.InvalidArgument("workflow_name is required")
	}

	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// The reads below are independent, so they overlap: the workflow (then the
	// user's presets for its tag), the builtin + stored project presets, and
	// the two visibility lookups.
	presetItemType := int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET)
	var (
		wf             *reliantv1.Workflow
		wfErr          error
		dbPresets      []*db.Preset
		dbPresetsErr   error
		loadResult     *preset.LoadResult
		overrides      map[string]bool
		overridesErr   error
		hiddenDefaults []string
		hiddenErr      error
		wg             sync.WaitGroup
	)
	wg.Add(4)
	go func() {
		defer wg.Done()
		wf, wfErr = s.loadWorkflow(ctx, req.Msg.WorkflowName, req.Msg.ProjectId)
		if wfErr != nil {
			return
		}
		if tag := wf.GetPresets().GetTag(); tag != "" {
			dbPresets, dbPresetsErr = s.deps.Repo.ListPresetsByTag(ctx, userID, tag, req.Msg.ProjectId)
		}
	}()
	go func() {
		defer wg.Done()
		loadResult = s.loadAllPresetsFromDB(ctx, req.Msg.ProjectId)
	}()
	go func() {
		defer wg.Done()
		overrides, overridesErr = s.deps.Repo.ListVisibilityOverrides(ctx, userID, presetItemType)
	}()
	go func() {
		defer wg.Done()
		hiddenDefaults, hiddenErr = s.deps.Repo.ListHiddenItemDefaults(ctx, presetItemType)
	}()
	wg.Wait()

	if wfErr != nil {
		// A failed read is the server's; a ref that resolves to nothing usable
		// (absent, broken, a draft) is the caller's, in the resolution's own
		// words.
		var storeErr *workflowsource.StoreError
		if errors.As(wfErr, &storeErr) {
			return nil, svcerr.WrapOr(wfErr, svcerr.Internal("failed to load workflow"))
		}
		return nil, svcerr.WithCause(svcerr.WithMessage(svcerr.ErrNotFound, "workflow not found: "+wfErr.Error()), wfErr)
	}

	// Use a map to deduplicate by slug (user presets take priority)
	presetsBySlug := make(map[string]*reliantv1.PresetInfo)
	var invalidPresets []*reliantv1.InvalidPreset

	// 1. Builtin + stored project presets, filtered by workflow compatibility
	for _, inv := range loadResult.Invalid {
		invalidPresets = append(invalidPresets, &reliantv1.InvalidPreset{
			Name:   inv.Name,
			Source: inv.Source,
			Path:   inv.Path,
			Errors: inv.Errors,
		})
	}
	for _, p := range loadResult.Valid {
		result := preset.ValidatePreset(p, wf)
		if !result.Valid {
			continue // Not compatible with this workflow
		}
		protoPreset, err := presetToProto(p)
		if err != nil {
			logging.WarnContext(ctx, "Failed to convert preset to proto", "preset", p.Name, "error", err)
			continue
		}
		presetsBySlug[p.Name] = protoPreset
	}

	// 2. User presets from the database that match this workflow's tag (highest priority)
	if workflowTag := wf.GetPresets().GetTag(); workflowTag != "" {
		if dbPresetsErr != nil {
			logging.WarnContext(ctx, "Failed to load user presets from database", "tag", workflowTag, "error", dbPresetsErr)
		} else {
			for _, dbPreset := range dbPresets {
				presetsBySlug[dbPreset.Slug] = dbPresetToProto(dbPreset)
			}
		}
	}

	if overridesErr != nil {
		logging.WarnContext(ctx, "Failed to batch-load visibility overrides for presets", "error", overridesErr)
		overrides = nil
	}
	if hiddenErr != nil {
		logging.WarnContext(ctx, "Failed to batch-load hidden defaults for presets", "error", hiddenErr)
		hiddenDefaults = nil
	}
	hiddenDefaultSet := make(map[string]bool, len(hiddenDefaults))
	for _, slug := range hiddenDefaults {
		hiddenDefaultSet[slug] = true
	}

	// Set is_hidden and filter by visibility (unless include_hidden is set)
	protoPresets := make([]*reliantv1.PresetInfo, 0, len(presetsBySlug))
	for _, p := range presetsBySlug {
		visible, ok := overrides[p.Slug]
		if !ok {
			visible = !hiddenDefaultSet[p.Slug]
		}
		p.IsHidden = !visible

		if req.Msg.IncludeHidden || visible {
			protoPresets = append(protoPresets, p)
		}
	}

	return connect.NewResponse(&reliantv1.ListPresetsForWorkflowResponse{
		Presets:        protoPresets,
		InvalidPresets: invalidPresets,
	}), nil
}
