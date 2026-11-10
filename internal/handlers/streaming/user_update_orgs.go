// Copyright (c) 2025 Reliant Labs
package streaming

import (
	"context"

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
// the org half of authz (CanReadOwn): a row of an org they have left is not
// delivered, on replay or live, whether or not the stream names a project.
// Membership is asked per update (cached by the resolver, orgs.CacheTTL), so
// a removal stops delivery on an open stream within that bound. An update
// about nothing of an org (personal: a daemon signal, a connection) passes.
//
// A resource's org is read once per stream once it has one (it never
// changes); a resource with none yet is re-read, since a backfill may stamp
// it. A resource that no longer exists passes: its removal must reach the
// client, and the update names nothing it could still read. A failed read
// withholds the update (authz fails closed); the cursor moves past it either
// way, as for every filtered update.
type orgGate struct {
	repo   Repository
	userID string
	orgs   map[string]string
}

func newOrgGate(repo Repository, userID string) *orgGate {
	return &orgGate{repo: repo, userID: userID, orgs: map[string]string{}}
}

func (g *orgGate) allows(ctx context.Context, update *db.UserUpdate) bool {
	if g == nil {
		return true
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
		case svcerr.IsNotFound(err):
			return true
		case err != nil:
			logging.WarnContext(ctx, LOG_PREFIX_STREAM_USER+" Could not read the org of an update's resource; withholding it",
				"userID", g.userID, "resource", key, "error", err)
			return false
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
