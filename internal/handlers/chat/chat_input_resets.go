package chat

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/types/known/structpb"
)

func (s *Service) resolveThreadResetParams(ctx context.Context, userID string, chat *db.Chat, workflowName, rootID, threadID string, params map[string]*structpb.Value) (map[string]*structpb.Value, error) {
	update := make(map[string]interface{})
	hasExplicitReset := false
	schemas := s.launcher().LoadWorkflowInputsForBuild(ctx, userID, workflowName, chat.ProjectID)
	for key, value := range params {
		plain := value.AsInterface()
		if plain == nil {
			hasExplicitReset = true
			update[key] = nil
		} else if schemas[key] != nil && model.GetGroupInputs(schemas[key]) != nil {
			update[key] = plain
		}
	}
	if len(update) == 0 {
		return params, nil
	}
	unavailable := func() error {
		return svcerr.FailedPrecondition("This thread's default settings are unavailable in its current run. Choose an explicit value, or retry after starting a new run.")
	}
	// Inline children have database workflow IDs, not Temporal executions.
	// The root's shared ChildWorkflowTracker owns their reset baselines:
	// DynamicWorkflow registers this query; InlineWorkflowExecutor captures
	// each baseline on that tracker under threadID before user overrides.
	response, err := rtemporal.QueryOpenRun(ctx, s.deps.Temporal, rootID, "get_thread_reset_inputs", threadID)
	if err != nil {
		if !hasExplicitReset {
			return params, nil
		}
		return nil, unavailable()
	}
	var baseline map[string]interface{}
	if err := response.Get(&baseline); err != nil {
		if !hasExplicitReset {
			return params, nil
		}
		return nil, unavailable()
	}
	if err := launch.ResolveResetsFromBaseline(update, baseline); err != nil {
		return nil, err
	}
	for key, value := range update {
		provided, group := value.(map[string]interface{})
		if !group {
			continue
		}
		merged := map[string]interface{}{}
		if defaults, ok := baseline[key].(map[string]interface{}); ok {
			for field, value := range defaults {
				merged[field] = value
			}
		}
		for field, value := range provided {
			if value != nil {
				merged[field] = value
			}
		}
		update[key] = merged
	}
	resolved := make(map[string]*structpb.Value, len(params))
	for key, value := range params {
		resolved[key] = value
	}
	for key, value := range update {
		converted, err := structpb.NewValue(value)
		if err != nil {
			return nil, unavailable()
		}
		resolved[key] = converted
	}
	return resolved, nil
}

// reconcileResumeMode repairs the one chat-owned input whose absent selection
// means Default. Do not overlay all defaults: model/tools/skills and internal
// inputs can belong to the resumed execution. A mode explicitly sent now wins.
func (s *Service) reconcileResumeMode(ctx context.Context, userID string, chat *db.Chat, workflowName string, update map[string]interface{}, sent map[string]interface{}) error {
	if _, explicit := sent["mode"]; explicit {
		return nil
	}
	value, ok, err := s.launcher().ChatMode(ctx, userID, chat, workflowName)
	if err != nil {
		return err
	}
	if ok {
		update["mode"] = value
	}
	return nil
}
