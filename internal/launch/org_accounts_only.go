// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/modelprefs"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// orgSources reads the accounts a scope may draw on. drivers.LookupAvailableDrivers
// in production; a test substitutes the org's sources.
type orgSources func(ctx context.Context, scope drivers.Scope) (models.AvailableDrivers, error)

// checkOrgAccountsOnly refuses a Spec.OrgAccountsOnly launch whose model none
// of its org's own sources serves (control-plane
// docs/design/org-provider-accounts.md §11.4, I5): the run would spend its
// first call finding out, and an unattended run is never launched just to
// wait, nor moved onto its owner's personal accounts.
//
// The model is resolved exactly as the run's first call resolves it, through
// the one scoped loader (§4.1) in the unattended scope: the org's keys
// granted to the owner and the org's Reliant credits, never a personal
// account or a seat. A run with no model input, or one on a local model
// (the owner's machine or endpoint, which no account pays for), is not
// refused here; the run decides. A failed read is retryable, never a verdict.
//
// Every model input the workflow declares is checked, whatever its name
// (autopilot-pr-review's is review_model), not only one called "model": a
// check keyed on the name let an org_only review launch and wait at its first
// call while an issue worker in the same org was refused here (control-plane#3176
// TL-35). workflowName and pin name the definition the run was built from, as
// buildInputs read it.
func (l *Launcher) checkOrgAccountsOnly(ctx context.Context, spec Spec, userID string, chat *db.Chat, workflowName, pin string, inputs map[string]interface{}) error {
	if !spec.OrgAccountsOnly || !spec.Unattended || chat == nil || chat.OrgID == nil || *chat.OrgID == "" {
		return nil
	}
	resolved, err := workflowsource.Resolve(ctx, l.repo, chatScopeAt(userID, chat, workflowName, pin).sourceOptions(), workflowName)
	if err != nil {
		return &InternalError{Reason: "failed to look up workflow", Err: err}
	}
	return l.refuseUnservedModels(ctx, chat, resolved.Workflow.GetInputs(), inputs)
}

// refuseUnservedModels is checkOrgAccountsOnly's verdict over the run's
// resolved inputs and their schema: the first model input, in path order,
// that no source of the chat's org serves is refused and named. An input
// called "model" that the schema does not declare as a model is still
// checked, as it always was.
func (l *Launcher) refuseUnservedModels(ctx context.Context, chat *db.Chat, schema map[string]*reliantv1.Input, inputs map[string]interface{}) error {
	selectors := ExtractModelSelectors(inputs, schema, "")
	if _, declared := selectors["model"]; !declared && inputs["model"] != nil {
		selectors["model"] = inputs["model"]
	}
	paths := make([]string, 0, len(selectors))
	for path, selector := range selectors {
		if !isLocalSelector(selector) {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	lookup := l.sources
	if lookup == nil {
		lookup = drivers.LookupAvailableDrivers
	}
	available, err := lookup(ctx, drivers.Scope{UserID: chat.UserID, OrgID: *chat.OrgID})
	if err != nil {
		return &InternalError{Reason: "failed to read the organization's AI accounts", Err: err}
	}
	for _, path := range paths {
		if unservedIn(selectors[path], available) {
			return &NoOrgAccountError{Model: selectorLabel(selectors[path])}
		}
	}
	return nil
}

// UnservedModel reports whether no source of scope serves selector, a model
// input value, resolved exactly as a run's first call resolves it, and names
// the model for a person ("claude-opus-4-6", "the flagship model"). No
// selector, and a local model (the owner's machine or endpoint, which no
// account pays for), are never unserved, and scope's sources are not read
// for them. lookup reads scope's sources: drivers.LookupAvailableDrivers in
// production. Its error is returned as is.
func UnservedModel(ctx context.Context, lookup func(context.Context, drivers.Scope) (models.AvailableDrivers, error), scope drivers.Scope, selector interface{}) (string, bool, error) {
	if selector == nil || isLocalSelector(selector) {
		return "", false, nil
	}
	available, err := lookup(ctx, scope)
	if err != nil {
		return "", false, err
	}
	if !unservedIn(selector, available) {
		return "", false, nil
	}
	return selectorLabel(selector), true, nil
}

// unservedIn reports whether selector resolves to no model of available.
// Whether it resolves at all: the default provider only ranks, so it is left
// out.
func unservedIn(selector interface{}, available models.AvailableDrivers) bool {
	return resolveSelector(selector, available, modelprefs.ProviderPreference{}) == nil
}

// selectorLabel names a model selector for a person: its model id, else its
// tags, else "this automation's model".
func selectorLabel(selector interface{}) string {
	ms, ok := toSelector(selector)
	switch {
	case !ok:
	case ms.ID != "":
		id, _, _ := strings.Cut(ms.ID, "@")
		return id
	case len(ms.Tags) > 0:
		return "the " + strings.Join(ms.Tags, ", ") + " model"
	}
	return "this automation's model"
}
