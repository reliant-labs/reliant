// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Preset. The generated preset_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: presetRepo (a *crud.Repo[Preset]) and the
//     package-level GetPresetByID / ListPreset / CreatePreset / UpdatePreset / DeletePreset
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Preset struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindPresetBy(ctx context.Context, db orm.Context, /* args */) ([]*Preset, error) {
//		var rows []*Preset
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActivePresets(ctx context.Context, db orm.Context) ([]*Preset, error) {
//		return ListPreset(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A preset is either global (project_id NULL: every project of the user) or
// scoped to one project. Every lookup by slug names its scope; a missing
// preset is sql.ErrNoRows.

// InsertPreset stores a new preset and returns it as stored.
func InsertPreset(ctx context.Context, db orm.Context, p *core.Preset) (*core.Preset, error) {
	row, err := presetFromCore(p)
	if err != nil {
		return nil, err
	}
	row.CreatedAt, row.UpdatedAt = time.Time{}, time.Time{}
	if err := CreatePreset(ctx, db, row); err != nil {
		return nil, err
	}
	return presetToCore(row), nil
}

// UpsertPreset writes the preset, replacing the name, description, tag and
// params of an existing one with the same (user, project, slug), and returns
// it as stored. A global preset (project_id NULL) never conflicts — NULL is
// never equal to NULL in the unique key — so upserting one inserts a row.
func UpsertPreset(ctx context.Context, db orm.Context, p *core.Preset) (*core.Preset, error) {
	row, err := presetFromCore(p)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	row.CreatedAt, row.UpdatedAt = now, now
	err = db.Bun().NewInsert().Model(row).
		On("CONFLICT (user_id, project_id, slug) DO UPDATE").
		Set("name = EXCLUDED.name").
		Set("description = EXCLUDED.description").
		Set("tag = EXCLUDED.tag").
		Set("params = EXCLUDED.params").
		Set("updated_at = EXCLUDED.updated_at").
		Returning("*").
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	return presetToCore(row), nil
}

// GetPreset returns the preset, or sql.ErrNoRows.
func GetPreset(ctx context.Context, db orm.Context, id string) (*core.Preset, error) {
	row, err := GetPresetByID(ctx, db, id)
	if errors.Is(err, svcerr.ErrNotFound) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	return presetToCore(row), nil
}

// GetPresetBySlug returns the user's global preset with that slug, or
// sql.ErrNoRows.
func GetPresetBySlug(ctx context.Context, db orm.Context, userID, slug string) (*core.Preset, error) {
	return firstPreset(ListPreset(ctx, db,
		orm.WhereEq(PresetFieldUserID, userID),
		orm.WhereEq(PresetFieldSlug, slug),
		orm.WhereIsNull(PresetFieldProjectID),
		orm.WithLimit(1)))
}

// GetPresetBySlugAndProject returns the user's preset with that slug scoped to
// the project, or sql.ErrNoRows. A global preset of that slug does not match.
func GetPresetBySlugAndProject(ctx context.Context, db orm.Context, userID, slug, projectID string) (*core.Preset, error) {
	return firstPreset(ListPreset(ctx, db,
		orm.WhereEq(PresetFieldUserID, userID),
		orm.WhereEq(PresetFieldSlug, slug),
		orm.WhereEq(PresetFieldProjectID, projectID),
		orm.WithLimit(1)))
}

// ListUserPresets returns every preset of the user, every scope, by name.
func ListUserPresets(ctx context.Context, db orm.Context, userID string) ([]*core.Preset, error) {
	return presetsToCore(ListPreset(ctx, db,
		orm.WhereEq(PresetFieldUserID, userID),
		orm.WithOrderBy(PresetFieldName, orm.Asc)))
}

// ListUserPresetsGlobal returns the user's global presets, by name.
func ListUserPresetsGlobal(ctx context.Context, db orm.Context, userID string) ([]*core.Preset, error) {
	return presetsToCore(ListPreset(ctx, db,
		orm.WhereEq(PresetFieldUserID, userID),
		orm.WhereIsNull(PresetFieldProjectID),
		orm.WithOrderBy(PresetFieldName, orm.Asc)))
}

// ListUserPresetsByProject returns the presets visible in the project — the
// user's global ones and the project's own — by name.
func ListUserPresetsByProject(ctx context.Context, db orm.Context, userID, projectID string) ([]*core.Preset, error) {
	return presetsToCore(ListPreset(ctx, db,
		orm.WhereEq(PresetFieldUserID, userID),
		globalOrProject(projectID),
		orm.WithOrderBy(PresetFieldName, orm.Asc)))
}

// ListPresetsByTag is ListUserPresetsByProject narrowed to one tag.
func ListPresetsByTag(ctx context.Context, db orm.Context, userID, tag, projectID string) ([]*core.Preset, error) {
	return presetsToCore(ListPreset(ctx, db,
		orm.WhereEq(PresetFieldUserID, userID),
		orm.WhereEq(PresetFieldTag, tag),
		globalOrProject(projectID),
		orm.WithOrderBy(PresetFieldName, orm.Asc)))
}

// globalOrProject matches the global presets and the project's own.
func globalOrProject(projectID string) orm.QueryOption {
	return func(q *bun.SelectQuery) {
		q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("project_id IS NULL").WhereOr("project_id = ?", projectID)
		})
	}
}

// ReplacePreset rewrites the preset's name, description, tag and params and
// returns it as stored. A missing preset is sql.ErrNoRows.
func ReplacePreset(ctx context.Context, db orm.Context, p *core.Preset) (*core.Preset, error) {
	row, err := presetFromCore(p)
	if err != nil {
		return nil, err
	}
	err = UpdatePresetMasked(ctx, db, row, []string{PresetFieldName, PresetFieldDescription, PresetFieldTag, PresetFieldParams})
	if err != nil {
		return nil, err
	}
	return presetToCore(row), nil
}

// RemovePreset deletes the preset. A missing one is not an error.
func RemovePreset(ctx context.Context, db orm.Context, id string) error {
	if err := DeletePreset(ctx, db, id); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

// DeletePresetBySlug deletes the user's global preset with that slug, if any.
func DeletePresetBySlug(ctx context.Context, db orm.Context, userID, slug string) error {
	_, err := db.Bun().NewDelete().Model((*Preset)(nil)).
		Where("user_id = ?", userID).
		Where("slug = ?", slug).
		Where("project_id IS NULL").
		Exec(ctx)
	return err
}

// DeletePresetBySlugAndProject deletes the user's preset with that slug scoped
// to the project, if any. A global preset of that slug is kept.
func DeletePresetBySlugAndProject(ctx context.Context, db orm.Context, userID, slug, projectID string) error {
	_, err := db.Bun().NewDelete().Model((*Preset)(nil)).
		Where("user_id = ?", userID).
		Where("slug = ?", slug).
		Where("project_id = ?", projectID).
		Exec(ctx)
	return err
}

func firstPreset(rows []*Preset, err error) (*core.Preset, error) {
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return presetToCore(rows[0]), nil
}

func presetsToCore(rows []*Preset, err error) ([]*core.Preset, error) {
	if err != nil {
		return nil, err
	}
	out := make([]*core.Preset, len(rows))
	for i, row := range rows {
		out[i] = presetToCore(row)
	}
	return out, nil
}

// presetFromCore stores params as their JSON text.
func presetFromCore(p *core.Preset) (*Preset, error) {
	params, err := json.Marshal(p.Params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal preset params: %w", err)
	}
	return &Preset{
		ID:          p.ID,
		UserID:      p.UserID,
		ProjectID:   p.ProjectID,
		Name:        p.Name,
		Slug:        p.Slug,
		Description: p.Description,
		Tag:         p.Tag,
		Params:      string(params),
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}, nil
}

// presetToCore decodes params; text that is not a JSON object reads as no
// params rather than failing the whole read.
func presetToCore(row *Preset) *core.Preset {
	params := make(map[string]interface{})
	if row.Params != "" {
		_ = json.Unmarshal([]byte(row.Params), &params)
	}
	return &core.Preset{
		ID:          row.ID,
		UserID:      row.UserID,
		ProjectID:   row.ProjectID,
		Name:        row.Name,
		Slug:        row.Slug,
		Description: row.Description,
		Tag:         row.Tag,
		Params:      params,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

// PresetExtra carries fields on Preset that are NOT columns — computed
// values, transient state, anything derived rather than stored. Preset is
// embedded on PresetExtra (see preset_orm.go), so a field added here is a field on
// every Preset value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type PresetExtra struct {
}
