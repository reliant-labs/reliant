// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Setting. The generated setting_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: settingRepo (a *crud.Repo[Setting]) and the
//     package-level GetSettingByID / ListSetting / CreateSetting / UpdateSetting / DeleteSetting
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Setting struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindSettingBy(ctx context.Context, db orm.Context, /* args */) ([]*Setting, error) {
//		var rows []*Setting
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveSettings(ctx context.Context, db orm.Context) ([]*Setting, error) {
//		return ListSetting(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// UpsertSetting writes a setting, replacing the value (and value_type and
// updated_at) of an existing one with the same (user, project, key).
//
// The conflict target depends on the scope. A user-level setting has
// project_id NULL, which the table's UNIQUE (user_id, project_id, key) never
// matches — NULL is never equal to NULL — so it resolves against the partial
// index settings_user_key_unique (user_id, key) WHERE project_id IS NULL. A
// project-scoped setting uses the table constraint. Postgres requires the ON
// CONFLICT target to name an index that covers the row, so one statement
// cannot serve both.
func UpsertSetting(ctx context.Context, db orm.Context, s *core.Setting) error {
	q := db.Bun().NewInsert().Model(&Setting{
		ID:        s.ID,
		UserID:    s.UserID,
		ProjectID: s.ProjectID,
		Key:       s.Key,
		Value:     s.Value,
		ValueType: s.ValueType,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	})
	if s.ProjectID == nil {
		q = q.On("CONFLICT (user_id, key) WHERE project_id IS NULL DO UPDATE")
	} else {
		q = q.On("CONFLICT (user_id, project_id, key) DO UPDATE")
	}
	_, err := q.
		Set("value = EXCLUDED.value").
		Set("value_type = EXCLUDED.value_type").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	return err
}

// settingScope selects one scope: the user-level settings when projectID is
// nil, else the project's.
func settingScope(userID string, projectID *string) []orm.QueryOption {
	opts := []orm.QueryOption{orm.WhereEq(SettingFieldUserID, userID)}
	if projectID == nil {
		return append(opts, orm.WhereIsNull(SettingFieldProjectID))
	}
	return append(opts, orm.WhereEq(SettingFieldProjectID, *projectID))
}

// GetSetting returns the setting of one scope (see settingScope), or an error
// whose text says "setting not found" when there is none.
func GetSetting(ctx context.Context, db orm.Context, userID string, projectID *string, key string) (*core.Setting, error) {
	opts := append(settingScope(userID, projectID), orm.WhereEq(SettingFieldKey, key), orm.WithLimit(1))
	rows, err := ListSetting(ctx, db, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to get setting: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("setting not found: %s", key)
	}
	return settingToCore(rows[0]), nil
}

// ListSettings returns every setting of one scope by key. updated_at breaks a
// tie, so if a duplicate ever survives, the newest comes last and wins on a
// client that loads the rows in order.
func ListSettings(ctx context.Context, db orm.Context, userID string, projectID *string) ([]*core.Setting, error) {
	opts := append(settingScope(userID, projectID),
		orm.WithOrderBy(SettingFieldKey, orm.Asc),
		orm.WithOrderBy(SettingFieldUpdatedAt, orm.Asc))
	rows, err := ListSetting(ctx, db, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to list settings: %w", err)
	}
	return settingsToCore(rows), nil
}

// ListSettingsByKey returns the user's settings, every scope, whose key is
// LIKE keyPattern, by key.
func ListSettingsByKey(ctx context.Context, db orm.Context, userID, keyPattern string) ([]*core.Setting, error) {
	rows, err := ListSetting(ctx, db,
		orm.WhereEq(SettingFieldUserID, userID),
		orm.WhereLike(SettingFieldKey, keyPattern),
		orm.WithOrderBy(SettingFieldKey, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list settings by key: %w", err)
	}
	return settingsToCore(rows), nil
}

// ReplaceSettingValue rewrites the setting's value and value_type and stamps
// updated_at. A missing setting is not an error: the write simply lands
// nowhere, as a concurrent delete wins.
func ReplaceSettingValue(ctx context.Context, db orm.Context, s *core.Setting) error {
	err := UpdateSettingMasked(ctx, db, &Setting{ID: s.ID, Value: s.Value, ValueType: s.ValueType},
		[]string{SettingFieldValue, SettingFieldValueType})
	if err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

// RemoveSetting deletes the setting. A missing one is not an error.
func RemoveSetting(ctx context.Context, db orm.Context, id string) error {
	if err := DeleteSetting(ctx, db, id); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

func settingToCore(row *Setting) *core.Setting {
	return &core.Setting{
		ID:        row.ID,
		UserID:    row.UserID,
		ProjectID: row.ProjectID,
		Key:       row.Key,
		Value:     row.Value,
		ValueType: row.ValueType,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func settingsToCore(rows []*Setting) []*core.Setting {
	out := make([]*core.Setting, len(rows))
	for i, row := range rows {
		out[i] = settingToCore(row)
	}
	return out
}

// SettingExtra carries fields on Setting that are NOT columns — computed
// values, transient state, anything derived rather than stored. Setting is
// embedded on SettingExtra (see setting_orm.go), so a field added here is a field on
// every Setting value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type SettingExtra struct {
}
