// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// SelectedModel is what an explicit picker choice was checked to be.
type SelectedModel struct {
	// Driver is the registry provider the choice runs on; "" for a local one.
	Driver string
	// Local is a model one of the user's machines or endpoints serves
	// ("<name>@local"), which no registry provider does.
	Local bool
	// NamesProvider is a choice that names exactly one provider itself
	// ("<model>@<driver>", or `providers: [<driver>]`). Only such a choice can
	// switch the chat's provider: a tag or bare model id says what to run,
	// not where.
	NamesProvider bool
}

// SelectionCheck is what ValidateSelectedModel checks a choice against.
type SelectionCheck struct {
	// BoundProvider is the provider the chat is bound to, "" when unbound. A
	// choice that names no provider of its own must run on it.
	BoundProvider string
	// Local resolves "<name>@local" choices against the user's machines and
	// endpoints. Nil: a local choice cannot be checked, and is refused.
	Local local.Directory
	// PreferDaemonID is the chat's own machine, tried first for a local model.
	PreferDaemonID string
}

// errLocalModelsUnreachable refuses a local choice this server cannot check.
var errLocalModelsUnreachable = errors.New("local models are not reachable from this server")

// ValidateSelectedModel checks an explicit picker choice without substituting
// anything for it: not a retired model's successor, not another provider.
//
//   - A local choice must be served by one of the user's machines or
//     endpoints right now (the same resolution the run does).
//   - A choice that names one provider resolves on that provider only. The
//     registry would otherwise treat the provider as a preference and fall
//     through to another, and the binding would follow it.
//   - Any other choice on a bound chat resolves on the bound provider only.
//   - An exact model id must resolve to that same model.
func (l *Launcher) ValidateSelectedModel(ctx context.Context, userID string, value interface{}, check SelectionCheck) (SelectedModel, error) {
	selector, ok := toSelector(value)
	if !ok {
		return SelectedModel{}, fmt.Errorf("the selected model is not a model selector")
	}
	if local.IsSelector(selector) {
		if check.Local == nil {
			return SelectedModel{}, errLocalModelsUnreachable
		}
		all, err := local.ListModels(ctx, check.Local, userID)
		if err != nil {
			return SelectedModel{}, fmt.Errorf("could not list your local models: %w", err)
		}
		if _, err := local.Resolve(all, selector, check.PreferDaemonID); err != nil {
			return SelectedModel{}, err
		}
		return SelectedModel{Local: true}, nil
	}

	available, err := drivers.LookupAvailableDrivers(ctx, userID)
	if err != nil {
		return SelectedModel{}, err
	}
	named := selectorProviders(selector)
	candidates := configuredProviders(available)
	switch {
	case len(named) == 1:
		candidates = connectedAmong(available, named)
	case check.BoundProvider != "":
		if len(named) > 0 && !containsString(named, check.BoundProvider) {
			return SelectedModel{}, fmt.Errorf("this chat runs on %s, and the selected model names only other providers; pick a model on one provider to switch the chat to it", check.BoundProvider)
		}
		candidates = connectedAmong(available, []string{check.BoundProvider})
	case len(named) > 1:
		candidates = connectedAmong(available, named)
	}
	resolved := resolveOn(selector, available, candidates)
	if resolved == nil {
		if check.BoundProvider != "" && len(named) != 1 {
			return SelectedModel{}, fmt.Errorf("the selected model is not available on %s, the provider this chat runs on; pick a model on another provider to switch the chat to it", check.BoundProvider)
		}
		return SelectedModel{}, fmt.Errorf("the selected model/provider is not available on your connected accounts")
	}
	if selector.ID != "" {
		id, _, _ := strings.Cut(selector.ID, "@")
		if id != resolved.Definition.ID {
			return SelectedModel{}, fmt.Errorf("the exact selected model/provider is unavailable; choose a replacement explicitly")
		}
	}
	return SelectedModel{Driver: resolved.Provider.Driver, NamesProvider: len(named) == 1}, nil
}

// RootProvider is the provider the chat's root runs on before an edit: the
// one it is bound to, else the one its model (presets, then its stored
// selection, then schema defaults — what its next run would be built with)
// resolves to over the user's providers. "" when that cannot be told, or the
// root runs a local model. An explicit pick on another provider than this is
// a provider switch.
func (l *Launcher) RootProvider(ctx context.Context, userID string, chat *db.Chat, workflowName string) string {
	if chat == nil {
		return ""
	}
	if sel, err := l.repo.GetChatSelection(ctx, chat.ID); err == nil && sel != nil && sel.ChatProvider != "" {
		return sel.ChatProvider
	}
	inputs, err := l.buildChatWorkflowInputs(ctx, userID, chat, workflowName, chat.SelectedPresets, nil)
	if err != nil {
		// The next run refuses to launch on it, so there is no root provider
		// to switch away from.
		return ""
	}
	selector := inputs["model"]
	if selector == nil || isLocalSelector(selector) {
		return ""
	}
	return l.primaryDriver(ctx, userID, map[string]interface{}{"model": selector})
}

// selectorProviders are the providers a selector names itself: the driver of
// a "<model>@<driver>" id, else its `providers:`.
func selectorProviders(selector models.ModelSelector) []string {
	if at := strings.LastIndex(selector.ID, "@"); at >= 0 && at < len(selector.ID)-1 {
		return []string{selector.ID[at+1:]}
	}
	return selector.Providers
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
