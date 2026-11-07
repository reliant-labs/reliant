package launch

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ResolveInputResets replaces only explicit top-level nulls with the workflow's
// preset/schema baseline. The caller retains the raw request for persistence.
// A group is one override value, just as it is in the selection store: resetting
// it resets the whole group. Nested nulls are not a deletion protocol.
func (l *Launcher) ResolveInputResets(ctx context.Context, userID string, chat *db.Chat, workflowName string, presets map[string]string, update map[string]interface{}) error {
	needsBaseline := false
	schemas := l.loadWorkflowInputs(ctx, chatScope(userID, chat, workflowName), workflowName)
	for key, value := range update {
		_, object := value.(map[string]interface{})
		group := object && schemas[key] != nil && model.GetGroupInputs(schemas[key]) != nil
		if value == nil || group {
			needsBaseline = true
		}
	}
	if !needsBaseline {
		return nil
	}
	baseline, err := l.buildWorkflowInputs(ctx, chatScope(userID, chat, workflowName), l.GetEffectiveCheckout(ctx, chat), workflowName, presets, nil, nil)
	if err != nil {
		return err
	}
	if err := ResolveResetsFromBaseline(update, baseline); err != nil {
		return err
	}
	// Group requests replace the stored group whole; omitted members expose
	// that group's baseline, not the old running group's values. Do not do
	// this for object-valued model selectors: those are atomic values.
	for key, value := range update {
		provided, ok := value.(map[string]interface{})
		if !ok || schemas[key] == nil || model.GetGroupInputs(schemas[key]) == nil {
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
	return nil
}

// ResolveResetsFromBaseline resolves deletion sentinels without adding unrelated
// defaults. It rejects an unavailable baseline instead of passing nil into CEL.
func ResolveResetsFromBaseline(update, baseline map[string]interface{}) error {
	for key, value := range update {
		if value != nil {
			continue
		}
		replacement, ok := baseline[key]
		if !ok || replacement == nil {
			return svcerr.FailedPrecondition(fmt.Sprintf("Cannot reset %q: this workflow has no default value for it. Choose an explicit value instead.", key))
		}
		update[key] = replacement
	}
	return nil
}

// ChatMode resolves the chat-level mode independently of an old run's inputs.
// Only mode is authoritative here: other inputs may have been owned/changed by
// the execution itself, and absence in selection does not establish deletion.
func (l *Launcher) ChatMode(ctx context.Context, userID string, chat *db.Chat, workflowName string) (interface{}, bool, error) {
	inputs, err := l.buildChatWorkflowInputs(ctx, userID, chat, workflowName, chat.SelectedPresets, nil)
	if err != nil {
		return nil, false, err
	}
	value, ok := inputs["mode"]
	return value, ok && value != nil, nil
}
