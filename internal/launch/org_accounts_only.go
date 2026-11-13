// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
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
func (l *Launcher) checkOrgAccountsOnly(ctx context.Context, spec Spec, chat *db.Chat, inputs map[string]interface{}) error {
	if !spec.OrgAccountsOnly || !spec.Unattended || chat == nil || chat.OrgID == nil || *chat.OrgID == "" {
		return nil
	}
	selector, ok := inputs["model"]
	if !ok || selector == nil || isLocalSelector(selector) {
		return nil
	}
	lookup := l.sources
	if lookup == nil {
		lookup = drivers.LookupAvailableDrivers
	}
	available, err := lookup(ctx, drivers.Scope{UserID: chat.UserID, OrgID: *chat.OrgID})
	if err != nil {
		return &InternalError{Reason: "failed to read the organization's AI accounts", Err: err}
	}
	if resolveSelector(selector, available) != nil {
		return nil
	}
	return &NoOrgAccountError{Model: selectorLabel(selector)}
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
