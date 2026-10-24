// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"fmt"
	"strings"

	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// providerSwitchHold is non-retryable within an activity. The workflow holds
// just this thread at the failed model call until another chat/thread change
// or resume. It does not stop healthy siblings or run a tool twice.
func providerSwitchHold(driver string, selector models.ModelSelector, cause error) error {
	return temporal.NewNonRetryableApplicationError(fmt.Sprintf(
		"This thread is waiting: its original model selector (id=%q, tags=%v) cannot run on the chat's selected provider %s. Choose a compatible model or provider to continue. %v",
		selector.ID, selector.Tags, driver, cause), "ProviderSwitchHold", cause)
}

// switchSelector preserves root exact identity. Descendants keep their tags,
// or recover the preset's original tags when an exact arg replaced them.
// A truly explicit exact-only choice may run only if that SAME model serves
// the selected provider; otherwise it visibly holds (owner-approved policy).
func switchSelector(spec llmCallSpec) models.ModelSelector {
	selector := spec.Selector
	if spec.ChatProvider == "" {
		return selector
	}
	if spec.Descendant {
		if len(selector.Tags) == 0 && len(spec.PresetModelTags) > 0 {
			selector.Tags = append([]string(nil), spec.PresetModelTags...)
		}
		if len(selector.Tags) > 0 {
			selector.ID = ""
		} else if at := strings.LastIndex(selector.ID, "@"); at >= 0 {
			selector.ID = selector.ID[:at]
		}
	}
	// Keep exact IDs pinned while resolving. The registry's normal unpinned
	// retirement fallback must not substitute a different model for an
	// explicit-only descendant or an exact root pick.
	if selector.ID != "" && !strings.Contains(selector.ID, "@") {
		selector.ID += "@" + spec.ChatProvider
	}
	selector.Providers = []string{spec.ChatProvider}
	return selector
}
