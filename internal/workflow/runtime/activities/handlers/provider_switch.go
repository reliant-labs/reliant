// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"errors"
	"fmt"
	"strings"

	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// providerSwitchHoldType is the application error type of a hold.
const providerSwitchHoldType = "ProviderSwitchHold"

// providerSwitchHold is non-retryable within an activity. The workflow holds
// just this thread at the failed model call until another chat/thread change
// or resume. It does not stop healthy siblings or run a tool twice.
//
// selector is the thread's OWN selector, the one the user can change; never
// the probe that pins it to driver.
func providerSwitchHold(driver string, selector models.ModelSelector, cause error) error {
	return temporal.NewNonRetryableApplicationError(fmt.Sprintf(
		"This thread is waiting: its model selector (id=%q, tags=%v, providers=%v) cannot run on the chat's selected provider %s. Choose a compatible model or provider to continue. %v",
		selector.ID, selector.Tags, selector.Providers, driver, cause), providerSwitchHoldType, cause)
}

func isProviderSwitchHold(err error) bool {
	var app *temporal.ApplicationError
	return errors.As(err, &app) && app.Type() == providerSwitchHoldType
}

// switchSelector preserves root exact identity. Descendants keep their tags,
// or recover the preset's original tags when an exact arg replaced them.
// A truly explicit exact-only choice may run only if that SAME model serves
// the selected provider; otherwise it visibly holds (owner-approved policy),
// unless chatModelFallback applies.
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

// chatModelFallback is the call a descendant makes when its own selector
// cannot run on the chat's bound provider: the same call on the chat's own
// model, resolved as the root resolves it (strictly on the chat's provider,
// an exact model kept exact).
//
// The hold exists for the user's own choices. A descendant's model usually
// is not one: it comes from its preset (a project preset pinning reviewers to
// gpt-6.1-sol on codex), a spawn argument, or a workflow node, and the user
// who bound the chat to another provider chose THAT provider, and the chat's
// model on it, for the whole chat. Holding such a thread parked a sub-agent
// nobody could act on; a chat bound to claude spawning the project's
// code_reviewer held it at its first model call, every time (prod,
// 2026-10-31..11-02). So the fallback applies unless the user set this
// thread's model themselves (ThreadModelChosen), which keeps the hold.
func chatModelFallback(spec llmCallSpec) (llmCallSpec, bool) {
	if spec.ChatProvider == "" || !spec.Descendant || spec.ThreadModelChosen {
		return spec, false
	}
	if spec.ChatModel.ID == "" && len(spec.ChatModel.Tags) == 0 {
		return spec, false
	}
	fallback := spec
	fallback.Selector = spec.ChatModel
	fallback.FallbackSelector = models.ModelSelector{}
	fallback.PresetModelTags = nil
	fallback.Descendant = false
	fallback.ChatModel = models.ModelSelector{}
	return fallback, true
}

// chatModelSelector is RuntimeContext.ChatModel as a selector; zero for none.
func chatModelSelector(choice *types.ModelChoice) models.ModelSelector {
	if choice == nil {
		return models.ModelSelector{}
	}
	return models.ModelSelector{ID: choice.ID, Tags: choice.Tags, Providers: choice.Providers}
}
