// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ShareEvent. The generated share_event_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: shareEventRepo (a *crud.Repo[ShareEvent]) and the
//     package-level GetShareEventByID / ListShareEvent / CreateShareEvent / UpdateShareEvent / DeleteShareEvent
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ShareEvent struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindShareEventBy(ctx context.Context, db orm.Context, /* args */) ([]*ShareEvent, error) {
//		var rows []*ShareEvent
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("org_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveShareEvents(ctx context.Context, db orm.Context) ([]*ShareEvent, error) {
//		return ListShareEvent(ctx, db, orm.WhereEq("org_id", true))
//	}
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Read-only sharing (control-plane docs/design/org-sharing.md §2, §9). The
// visibility columns of projects, chats and workflow_drafts are read and
// written only here and decided on only in internal/authz.

// shareTables names the table each shareable kind lives in. The set is
// closed: a kind not here is refused, never interpolated.
var shareTables = map[core.ShareKind]string{
	core.ShareKindProject:  "projects",
	core.ShareKindChat:     "chats",
	core.ShareKindWorkflow: "workflow_drafts",
}

// ApplyShareChange sets one resource's visibility and records the change in
// share_events, as one unit: run it inside a transaction (db.RunTx) so the
// two land together.
//
// The UPDATE applies only while the row still has c.OwnerUserID as its owner
// and c.OrgID as its org, and only when it changes something: a change
// decided on a stale read, a missing row, and a no-op (sharing what is
// shared, clearing what is private) write nothing and record nothing. It
// reports whether the visibility changed. shared_at is stamped with c.At on
// a share and cleared otherwise; nothing else on the row (updated_at,
// last_active, unread) moves, so changing who may read a resource is not
// activity on it.
func ApplyShareChange(ctx context.Context, db orm.Context, c core.ShareChange) (bool, error) {
	table, ok := shareTables[c.Kind]
	if !ok {
		return false, fmt.Errorf("share: unknown resource kind %q", c.Kind)
	}
	switch c.Action {
	case core.ShareActionShare, core.ShareActionUnshare, core.ShareActionAdminUnshare:
	default:
		return false, fmt.Errorf("share: unknown action %q", c.Action)
	}
	if c.ResourceID == "" || c.OrgID == "" || c.OwnerUserID == "" || c.ActorUserID == "" {
		return false, fmt.Errorf("share: resource, org, owner and actor are required")
	}
	at := c.At
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()

	q := db.Bun().NewUpdate().TableExpr(table).
		Where("id = ?", c.ResourceID).
		Where("user_id = ?", c.OwnerUserID).
		Where("org_id = ?", c.OrgID)
	if c.To() == core.VisibilityOrg {
		q = q.Set("visibility = ?", string(core.VisibilityOrg)).
			Set("shared_at = ?", at).
			Where("visibility IS NULL")
	} else {
		q = q.Set("visibility = NULL").
			Set("shared_at = NULL").
			Where("visibility IS NOT NULL")
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("share: setting %s visibility: %w", c.Kind, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := CreateShareEvent(ctx, db, &ShareEvent{
		ID:           uuid.NewString(),
		OrgID:        c.OrgID,
		ResourceKind: string(c.Kind),
		ResourceID:   c.ResourceID,
		OwnerUserID:  c.OwnerUserID,
		ActorUserID:  c.ActorUserID,
		Action:       string(c.Action),
		CreatedAt:    at,
	}); err != nil {
		return false, fmt.Errorf("share: recording the change: %w", err)
	}
	return true, nil
}

// ListShareEvents lists the visibility changes recorded for one resource,
// newest first: its share history.
func ListShareEvents(ctx context.Context, db orm.Context, kind core.ShareKind, resourceID string) ([]*core.ShareEvent, error) {
	rows, err := ListShareEvent(ctx, db,
		orm.WhereEq(ShareEventFieldResourceKind, string(kind)),
		orm.WhereEq(ShareEventFieldResourceID, resourceID),
		orm.WithOrderBy(ShareEventFieldCreatedAt, orm.Desc))
	if err != nil {
		return nil, fmt.Errorf("listing share events: %w", err)
	}
	out := make([]*core.ShareEvent, len(rows))
	for i, r := range rows {
		out[i] = &core.ShareEvent{
			ID: r.ID, OrgID: r.OrgID, Kind: core.ShareKind(r.ResourceKind), ResourceID: r.ResourceID,
			OwnerUserID: r.OwnerUserID, ActorUserID: r.ActorUserID, Action: core.ShareAction(r.Action), CreatedAt: r.CreatedAt,
		}
	}
	return out, nil
}

// ListSharedChats lists the chats of f.OrgID its owners shared with the org,
// most recently active first, leaving out f.ViewerUserID's own (those are in
// their own list) and archived chats. It is a visibility predicate, not a
// decision: the caller still passes each row through authz (a viewer must be
// a live member of the org). An empty org or viewer lists nothing.
func ListSharedChats(ctx context.Context, db orm.Context, f core.SharedChatFilters) ([]*core.Chat, error) {
	if f.OrgID == "" || f.ViewerUserID == "" {
		return []*core.Chat{}, nil
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	opts := []orm.QueryOption{
		orm.WhereEq(ChatsWithActivityFieldOrgID, f.OrgID),
		orm.WhereEq(ChatsWithActivityFieldVisibility, string(core.VisibilityOrg)),
		orm.WhereNotEq(ChatsWithActivityFieldUserID, f.ViewerUserID),
		orm.WhereNotEq(ChatsWithActivityFieldState, core.ChatStateArchived),
	}
	if f.ProjectID != nil {
		opts = append(opts, orm.WhereEq(ChatsWithActivityFieldProjectID, *f.ProjectID))
	}
	opts = append(opts,
		orm.WithOrderBy(ChatsWithActivityFieldLastActive, orm.Desc),
		orm.WithOrderBy(ChatsWithActivityFieldID, orm.Asc),
		orm.WithLimit(limit))
	if f.Offset > 0 {
		opts = append(opts, orm.WithOffset(f.Offset))
	}
	rows, err := ListChatsWithActivity(ctx, db, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to list shared chats: %w", err)
	}
	return chatsFromView(rows), nil
}

// ListSharedProjects lists the projects of f.OrgID their owners shared with
// the org, most recently active first, leaving out f.ViewerUserID's own. Like
// ListSharedChats it is a visibility predicate; the caller decides each row
// through authz. An empty org or viewer lists nothing.
func ListSharedProjects(ctx context.Context, db orm.Context, f core.SharedProjectFilters) ([]*core.Project, error) {
	if f.OrgID == "" || f.ViewerUserID == "" {
		return []*core.Project{}, nil
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := ListProject(ctx, db,
		orm.WhereEq(ProjectFieldOrgID, f.OrgID),
		orm.WhereEq(ProjectFieldVisibility, string(core.VisibilityOrg)),
		orm.WhereNotEq(ProjectFieldUserID, f.ViewerUserID),
		orm.WithOrderBy(ProjectFieldLastActive, orm.Desc),
		orm.WithOrderBy(ProjectFieldID, orm.Asc),
		orm.WithLimit(limit),
		orm.WithOffset(f.Offset))
	if err != nil {
		return nil, fmt.Errorf("failed to list shared projects: %w", err)
	}
	out := make([]*core.Project, len(rows))
	for i, row := range rows {
		out[i] = projectToCore(row)
	}
	return out, nil
}

// visibilityOf maps a nullable visibility column onto core.Visibility (NULL
// is private).
func visibilityOf(col *string) core.Visibility {
	if col == nil {
		return core.VisibilityPrivate
	}
	return core.Visibility(*col)
}

// ShareEventExtra carries fields on ShareEvent that are NOT columns — computed
// values, transient state, anything derived rather than stored. ShareEvent is
// embedded on ShareEventExtra (see share_event_orm.go), so a field added here is a field on
// every ShareEvent value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ShareEventExtra struct {
}
