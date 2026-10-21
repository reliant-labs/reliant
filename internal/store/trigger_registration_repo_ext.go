// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for TriggerRegistration. The generated trigger_registration_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: triggerRegistrationRepo (a *crud.Repo[TriggerRegistration]) and the
//     package-level GetTriggerRegistrationByID / ListTriggerRegistration / CreateTriggerRegistration / UpdateTriggerRegistration / DeleteTriggerRegistration
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     TriggerRegistration struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindTriggerRegistrationBy(ctx context.Context, db orm.Context, /* args */) ([]*TriggerRegistration, error) {
//		var rows []*TriggerRegistration
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("provider = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveTriggerRegistrations(ctx context.Context, db orm.Context) ([]*TriggerRegistration, error) {
//		return ListTriggerRegistration(ctx, db, orm.WhereEq("provider", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// GetTriggerRegistration returns a trigger's provider state, or
// core.ErrTriggerRegistrationNotFound.
func GetTriggerRegistration(ctx context.Context, db orm.Context, triggerID string) (*core.TriggerRegistration, error) {
	row, err := GetTriggerRegistrationByID(ctx, db, triggerID)
	if errors.Is(err, svcerr.ErrNotFound) {
		return nil, core.ErrTriggerRegistrationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get trigger registration: %w", err)
	}
	return triggerRegistrationToCore(row), nil
}

// ListTriggerRegistrationsForUser returns the named triggers' registrations,
// keyed by trigger id, in ONE query, for health. Joined through triggers so
// only the caller's own appear.
func ListTriggerRegistrationsForUser(ctx context.Context, db orm.Context, userID string, triggerIDs []string) (map[string]*core.TriggerRegistration, error) {
	out := map[string]*core.TriggerRegistration{}
	if len(triggerIDs) == 0 {
		return out, nil
	}
	var rows []*TriggerRegistration
	err := db.Bun().NewSelect().Model(&rows).
		Join("JOIN triggers AS t ON t.id = trigger_registrations.trigger_id").
		Where("t.user_id = ?", userID).
		Where("trigger_registrations.trigger_id IN (?)", bun.List(triggerIDs)).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list trigger registrations: %w", err)
	}
	for _, row := range rows {
		out[row.TriggerID] = triggerRegistrationToCore(row)
	}
	return out, nil
}

// upsertTriggerRegistrationSQL writes a trigger's provider state. status_since
// moves only when status changes, so it is the start of the current status
// episode however often the source is polled.
const upsertTriggerRegistrationSQL = `INSERT INTO trigger_registrations (
	trigger_id, provider, registration_id, cursor, last_polled_at, status, status_detail,
	last_gap_at, last_gap_detail, status_since, created_at, updated_at
) VALUES (
	?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, NOW(), NOW(), NOW()
)
ON CONFLICT (trigger_id) DO UPDATE SET
	provider = EXCLUDED.provider,
	registration_id = EXCLUDED.registration_id,
	cursor = EXCLUDED.cursor,
	last_polled_at = EXCLUDED.last_polled_at,
	status_since = CASE WHEN trigger_registrations.status = EXCLUDED.status
	                    THEN trigger_registrations.status_since ELSE NOW() END,
	status = EXCLUDED.status,
	status_detail = EXCLUDED.status_detail,
	last_gap_at = EXCLUDED.last_gap_at,
	last_gap_detail = EXCLUDED.last_gap_detail,
	updated_at = NOW()`

// UpsertTriggerRegistration records a trigger's provider state. An empty
// Status is core.TriggerRegistrationActive.
func UpsertTriggerRegistration(ctx context.Context, db orm.Context, reg *core.TriggerRegistration) error {
	status := reg.Status
	if status == "" {
		status = core.TriggerRegistrationActive
	}
	_, err := db.Bun().NewRaw(upsertTriggerRegistrationSQL,
		reg.TriggerID, reg.Provider, reg.RegistrationID, reg.Cursor, reg.LastPolledAt, status,
		reg.StatusDetail, reg.LastGapAt, reg.LastGapDetail).Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to upsert trigger registration: %w", err)
	}
	return nil
}

func triggerRegistrationToCore(row *TriggerRegistration) *core.TriggerRegistration {
	return &core.TriggerRegistration{
		TriggerID:      row.TriggerID,
		Provider:       row.Provider,
		RegistrationID: row.RegistrationID,
		Cursor:         row.Cursor,
		LastPolledAt:   row.LastPolledAt,
		Status:         row.Status,
		StatusDetail:   row.StatusDetail,
		StatusSince:    row.StatusSince,
		LastGapAt:      row.LastGapAt,
		LastGapDetail:  row.LastGapDetail,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

// TriggerRegistrationExtra carries fields on TriggerRegistration that are NOT columns — computed
// values, transient state, anything derived rather than stored. TriggerRegistration is
// embedded on TriggerRegistrationExtra (see trigger_registration_orm.go), so a field added here is a field on
// every TriggerRegistration value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type TriggerRegistrationExtra struct {
}
