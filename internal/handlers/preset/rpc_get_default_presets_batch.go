package preset

import (
	"context"
	"fmt"
	"sync"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/workflowcatalog"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// GetDefaultPresetsBatch resolves default presets for many workflows in one
// round trip. Behavior per workflow is identical to GetDefaultPreset — both go
// through resolveDefaultPresets — but the user's overrides are read with a
// single ListSettingsByKey rather than one GetSetting per workflow, so the
// query count stays flat as the workflow list grows.
func (s *Service) GetDefaultPresetsBatch(
	ctx context.Context,
	req *connect.Request[reliantv1.GetDefaultPresetsBatchRequest],
) (*connect.Response[reliantv1.GetDefaultPresetsBatchResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	userIDStr := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userIDStr); err != nil {
		return nil, err
	}

	// The user's `preset.defaults.*` overrides and the workflow catalog are
	// independent reads: one query each, whatever the batch size. A settings
	// failure is not fatal — no overrides apply, the same outcome as a user
	// who has never set one.
	var (
		settings    []*db.Setting
		settingsErr error
		catalog     *workflowcatalog.Catalog
		wg          sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		settings, settingsErr = s.deps.Repo.ListSettingsByKey(ctx, userIDStr, "preset.defaults.%")
	}()
	go func() {
		defer wg.Done()
		catalog = workflowcatalog.Load(ctx, s.deps.Repo, userIDStr, req.Msg.ProjectId)
	}()
	wg.Wait()

	userOverrides := make(map[string]string)
	if settingsErr == nil {
		for _, setting := range settings {
			userOverrides[setting.Key] = setting.Value
		}
	} else {
		logging.WarnContext(ctx, "GetDefaultPresetsBatch: failed to load preset default overrides", "error", settingsErr)
	}

	opts := workflowsource.Options{UserID: userIDStr, ProjectID: req.Msg.ProjectId}
	load := workflowcatalog.MemoizeRefLoader(workflowcatalog.WithBuiltinCache(func(ref string) (*reliantv1.Workflow, error) {
		resolved, err := workflowsource.Resolve(ctx, catalog, opts, ref)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: %w", ref, err)
		}
		return resolved.Workflow, nil
	}))

	presetsByWorkflow := make(map[string]*reliantv1.WorkflowDefaultPresets)
	seen := make(map[string]bool, len(req.Msg.WorkflowNames))

	for _, workflowName := range req.Msg.WorkflowNames {
		if workflowName == "" || seen[workflowName] {
			continue
		}
		seen[workflowName] = true

		defaults := s.resolveDefaultPresets(ctx, userIDStr, req.Msg.ProjectId, workflowName, userOverrides, load)
		// Omit empty results so the response mirrors the single RPC's
		// "no defaults" outcome without inventing an entry for it.
		if len(defaults) == 0 {
			continue
		}
		presetsByWorkflow[workflowName] = &reliantv1.WorkflowDefaultPresets{Presets: defaults}
	}

	return connect.NewResponse(&reliantv1.GetDefaultPresetsBatchResponse{
		PresetsByWorkflow: presetsByWorkflow,
	}), nil
}
