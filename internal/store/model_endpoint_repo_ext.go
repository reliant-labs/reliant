// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ModelEndpoint. The generated model_endpoint_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: modelEndpointRepo (a *crud.Repo[ModelEndpoint]) and the
//     package-level GetModelEndpointByID / ListModelEndpoint / CreateModelEndpoint / UpdateModelEndpoint / DeleteModelEndpoint
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ModelEndpoint struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindModelEndpointBy(ctx context.Context, db orm.Context, /* args */) ([]*ModelEndpoint, error) {
//		var rows []*ModelEndpoint
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveModelEndpoints(ctx context.Context, db orm.Context) ([]*ModelEndpoint, error) {
//		return ListModelEndpoint(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Every model_endpoints query is scoped to the owning user: an endpoint of
// another user reads, updates and deletes exactly like a missing one
// (core.ErrModelEndpointNotFound).

// InsertModelEndpoint stores a new endpoint; created_at and updated_at are
// written back onto e. A second endpoint of the same name for the user is
// core.ErrModelEndpointNameTaken.
func InsertModelEndpoint(ctx context.Context, db orm.Context, e *core.ModelEndpoint) error {
	row := modelEndpointFromCore(e)
	if err := CreateModelEndpoint(ctx, db, row); err != nil {
		if orm.ConstraintName(err) == ModelEndpointConstraintIdxModelEndpointsUserName {
			return core.ErrModelEndpointNameTaken
		}
		return fmt.Errorf("creating model endpoint: %w", err)
	}
	e.CreatedAt, e.UpdatedAt = row.CreatedAt, row.UpdatedAt
	return nil
}

// GetModelEndpoint returns the user's endpoint, or core.ErrModelEndpointNotFound.
func GetModelEndpoint(ctx context.Context, db orm.Context, userID, id string) (*core.ModelEndpoint, error) {
	rows, err := ListModelEndpoint(ctx, db,
		orm.WhereEq(ModelEndpointFieldUserID, userID),
		orm.WhereEq(ModelEndpointFieldID, id),
		orm.WithLimit(1))
	if err != nil {
		return nil, fmt.Errorf("getting model endpoint: %w", err)
	}
	if len(rows) == 0 {
		return nil, core.ErrModelEndpointNotFound
	}
	return modelEndpointToCore(rows[0]), nil
}

// ListModelEndpoints returns every endpoint the user owns, by name; nil when
// there are none.
func ListModelEndpoints(ctx context.Context, db orm.Context, userID string) ([]*core.ModelEndpoint, error) {
	rows, err := ListModelEndpoint(ctx, db,
		orm.WhereEq(ModelEndpointFieldUserID, userID),
		orm.WithOrderBy(ModelEndpointFieldName, orm.Asc),
		orm.WithOrderBy(ModelEndpointFieldID, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("listing model endpoints: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]*core.ModelEndpoint, len(rows))
	for i, row := range rows {
		out[i] = modelEndpointToCore(row)
	}
	return out, nil
}

// ReplaceModelEndpoint rewrites every mutable column of the user's endpoint
// from e and stamps updated_at, which is written back onto e.
func ReplaceModelEndpoint(ctx context.Context, db orm.Context, e *core.ModelEndpoint) error {
	row := modelEndpointFromCore(e)
	row.UpdatedAt = time.Now().UTC()
	res, err := db.Bun().NewUpdate().Model(row).
		Column(ModelEndpointFieldName, ModelEndpointFieldBaseURL, ModelEndpointFieldRoute, ModelEndpointFieldDaemonID,
			ModelEndpointFieldCredentialConnectionID, ModelEndpointFieldHeaderNames, ModelEndpointFieldModelsJSON,
			ModelEndpointFieldProbeJSON, ModelEndpointFieldUpdatedAt).
		Where("id = ?", e.ID).
		Where("user_id = ?", e.UserID).
		Exec(ctx)
	if err != nil {
		if orm.ConstraintName(err) == ModelEndpointConstraintIdxModelEndpointsUserName {
			return core.ErrModelEndpointNameTaken
		}
		return fmt.Errorf("updating model endpoint: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrModelEndpointNotFound
	}
	e.UpdatedAt = row.UpdatedAt
	return nil
}

// SetModelEndpointProbe stores a fresh probe result without touching anything
// else, updated_at included. A missing endpoint is not an error: a periodic
// refresh can race a delete.
func SetModelEndpointProbe(ctx context.Context, db orm.Context, userID, id, probeJSON string) error {
	_, err := db.Bun().NewUpdate().Model((*ModelEndpoint)(nil)).
		Set("probe_json = ?", probeJSON).
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("storing model endpoint probe: %w", err)
	}
	return nil
}

// RemoveModelEndpoint deletes the user's endpoint, or reports
// core.ErrModelEndpointNotFound.
func RemoveModelEndpoint(ctx context.Context, db orm.Context, userID, id string) error {
	res, err := db.Bun().NewDelete().Model((*ModelEndpoint)(nil)).
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting model endpoint: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrModelEndpointNotFound
	}
	return nil
}

// modelEndpointFromCore writes an unset header list as the empty array and
// unset models as the empty JSON list, the columns' NOT NULL defaults.
func modelEndpointFromCore(e *core.ModelEndpoint) *ModelEndpoint {
	headers := e.HeaderNames
	if headers == nil {
		headers = []string{}
	}
	models := e.ModelsJSON
	if models == "" {
		models = "[]"
	}
	return &ModelEndpoint{
		ID:                     e.ID,
		UserID:                 e.UserID,
		Name:                   e.Name,
		BaseURL:                e.BaseURL,
		Route:                  e.Route,
		DaemonID:               e.DaemonID,
		CredentialConnectionID: e.CredentialConnectionID,
		HeaderNames:            headers,
		ModelsJSON:             &models,
		ProbeJSON:              e.ProbeJSON,
		CreatedAt:              e.CreatedAt,
		UpdatedAt:              e.UpdatedAt,
	}
}

func modelEndpointToCore(row *ModelEndpoint) *core.ModelEndpoint {
	e := &core.ModelEndpoint{
		ID:                     row.ID,
		UserID:                 row.UserID,
		Name:                   row.Name,
		BaseURL:                row.BaseURL,
		Route:                  row.Route,
		DaemonID:               row.DaemonID,
		CredentialConnectionID: row.CredentialConnectionID,
		HeaderNames:            row.HeaderNames,
		ProbeJSON:              row.ProbeJSON,
		CreatedAt:              row.CreatedAt,
		UpdatedAt:              row.UpdatedAt,
	}
	if row.ModelsJSON != nil {
		e.ModelsJSON = *row.ModelsJSON
	}
	return e
}

// ModelEndpointExtra carries fields on ModelEndpoint that are NOT columns — computed
// values, transient state, anything derived rather than stored. ModelEndpoint is
// embedded on ModelEndpointExtra (see model_endpoint_orm.go), so a field added here is a field on
// every ModelEndpoint value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ModelEndpointExtra struct {
}
