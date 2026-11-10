// Copyright (c) 2025 Reliant Labs
package streaming

import (
	"context"
	"encoding/json"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// maxOrgGateEntries bounds one stream's org cache; past it the cache starts
// over (each entry is re-read once).
const maxOrgGateEntries = 4096

// orgGate decides whether a user stream may deliver an update about an org's
// resource: the chat it names, else its project, else the saved workflow it
// is about. The user log holds the caller's own updates, so the decision is
// the org half of authz (CanReadOwn): an update about an org they have left
// is not delivered, on replay or live, whether or not the stream names a
// project. Membership is asked per update (cached by the resolver,
// orgs.CacheTTL), so a removal stops delivery on an open stream within that
// bound. An update about nothing of an org (personal: a daemon signal, a
// connection) passes.
//
// The org decided on is the one the update was stored with
// (user_updates.org_id, stamped on insert), so a deleted resource's history
// keeps its org. An update with none (written before orgs, or a live update
// not read back) falls back to the resource's current org, read once per
// stream once it has one. When neither establishes an org (the resource is
// gone, or a read failed), the update is withheld (authz fails closed),
// except that a deletion still reaches the client as a bare marker, its id
// and kind and nothing else, so the client can drop the item. The cursor
// moves past a withheld update either way, as for every filtered one.
type orgGate struct {
	repo   Repository
	userID string
	orgs   map[string]string
}

func newOrgGate(repo Repository, userID string) *orgGate {
	return &orgGate{repo: repo, userID: userID, orgs: map[string]string{}}
}

// allows reports whether update may be delivered, stripping it to a
// deletion marker in place when only that may.
func (g *orgGate) allows(ctx context.Context, update *db.UserUpdate) bool {
	if g == nil {
		return true
	}
	if update.OrgID != nil && *update.OrgID != "" {
		return authz.CanReadOwn(ctx, g.userID, update.OrgID)
	}
	var key string
	var read func() (*string, error)
	switch {
	case update.ChatID != nil && *update.ChatID != "":
		id := *update.ChatID
		key = "chat:" + id
		read = func() (*string, error) {
			chat, err := g.repo.GetChatRow(ctx, id)
			if err != nil {
				return nil, err
			}
			return chat.OrgID, nil
		}
	case update.ProjectID != nil && *update.ProjectID != "":
		id := *update.ProjectID
		key = "project:" + id
		read = func() (*string, error) {
			project, err := g.repo.GetProject(ctx, id)
			if err != nil {
				return nil, err
			}
			return project.OrgID, nil
		}
	case update.EntityType == db.EntityTypeWorkflowDraft && update.EntityID != "":
		id := update.EntityID
		key = "draft:" + id
		read = func() (*string, error) {
			draft, err := g.repo.GetWorkflowDraft(ctx, id)
			if err != nil {
				return nil, err
			}
			return draft.OrgID, nil
		}
	default:
		return true
	}

	org, cached := g.orgs[key]
	if !cached {
		got, err := read()
		switch {
		case err != nil:
			if !svcerr.IsNotFound(err) {
				logging.WarnContext(ctx, LOG_PREFIX_STREAM_USER+" Could not read the org of an update's resource; withholding it",
					"userID", g.userID, "resource", key, "error", err)
			}
			return deletionMarker(update)
		case got == nil || *got == "":
			return true
		}
		if len(g.orgs) >= maxOrgGateEntries {
			g.orgs = map[string]string{}
		}
		org = *got
		g.orgs[key] = org
	}
	return authz.CanReadOwn(ctx, g.userID, &org)
}

// deletionMarker reports whether update announces a deletion, and if so
// strips it, in place, to what lets a client drop the item: its id and kind,
// and nothing of what it was.
func deletionMarker(update *db.UserUpdate) bool {
	switch update.UpdateType {
	case db.UserUpdateChatDeleted, db.UserUpdateProjectDeleted, db.UserUpdateWorktreeDeleted:
		update.Data = json.RawMessage(`{}`)
	case db.UserUpdateWorkflowDraftUpdated:
		var payload struct {
			Deleted bool `json:"deleted"`
		}
		if json.Unmarshal(update.Data, &payload) != nil || !payload.Deleted {
			return false
		}
		marker, err := json.Marshal(map[string]any{"draft_id": update.EntityID, "deleted": true})
		if err != nil {
			return false
		}
		update.Data = marker
	default:
		return false
	}
	return true
}
