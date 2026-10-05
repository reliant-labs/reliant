// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// Route values stored in model_endpoints.route.
const (
	ModelEndpointRouteDirect    = "direct"
	ModelEndpointRouteViaDaemon = "via_daemon"
)

// ErrModelEndpointNotFound is returned when an endpoint does not exist OR is
// owned by another user; the two are deliberately indistinguishable.
var ErrModelEndpointNotFound = errors.New("model endpoint not found")

// ErrModelEndpointNameTaken is returned when the user already has an endpoint
// with that name.
var ErrModelEndpointNameTaken = errors.New("a model endpoint with that name already exists")

// ModelEndpoint is one row of model_endpoints. It carries NO secret:
// CredentialConnectionID points at the sealed connection store.
type ModelEndpoint struct {
	ID                     string
	UserID                 string
	Name                   string
	BaseURL                string
	Route                  string
	DaemonID               *string
	CredentialConnectionID *string
	HeaderNames            []string
	// ModelsJSON is protojson of the user's per-model settings; ProbeJSON is
	// protojson of the last probe result. Both are server-owned, replaced
	// wholesale.
	ModelsJSON string
	ProbeJSON  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const modelEndpointColumns = `id, user_id, name, base_url, route, daemon_id, credential_connection_id,
	header_names, models_json, probe_json, created_at, updated_at`

func scanModelEndpoint(scan func(dest ...any) error) (*ModelEndpoint, error) {
	var e ModelEndpoint
	var daemonID, credID sql.NullString
	var headers pq.StringArray
	if err := scan(&e.ID, &e.UserID, &e.Name, &e.BaseURL, &e.Route, &daemonID, &credID,
		&headers, &e.ModelsJSON, &e.ProbeJSON, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, err
	}
	if daemonID.Valid {
		e.DaemonID = &daemonID.String
	}
	if credID.Valid {
		e.CredentialConnectionID = &credID.String
	}
	e.HeaderNames = []string(headers)
	return &e, nil
}

func isModelEndpointNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_model_endpoints_user_name"
}

// CreateModelEndpoint inserts e. e.ID, e.UserID, e.Name, e.BaseURL and e.Route
// are required.
func (r *Repo) CreateModelEndpoint(ctx context.Context, e *ModelEndpoint) error {
	if e == nil || e.ID == "" || e.UserID == "" {
		return fmt.Errorf("model endpoint id and user ID are required")
	}
	headers := e.HeaderNames
	if headers == nil {
		headers = []string{}
	}
	models := e.ModelsJSON
	if models == "" {
		models = "[]"
	}
	query := r.bindQuery(`INSERT INTO model_endpoints
		(id, user_id, name, base_url, route, daemon_id, credential_connection_id, header_names, models_json, probe_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING created_at, updated_at`)
	err := r.DB.QueryRowContext(ctx, query, e.ID, e.UserID, e.Name, e.BaseURL, e.Route,
		e.DaemonID, e.CredentialConnectionID, pq.StringArray(headers), models, e.ProbeJSON,
	).Scan(&e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if isModelEndpointNameConflict(err) {
			return ErrModelEndpointNameTaken
		}
		return fmt.Errorf("creating model endpoint: %w", err)
	}
	return nil
}

// GetModelEndpoint returns the user's endpoint, or ErrModelEndpointNotFound.
func (r *Repo) GetModelEndpoint(ctx context.Context, userID, id string) (*ModelEndpoint, error) {
	if userID == "" || id == "" {
		return nil, ErrModelEndpointNotFound
	}
	query := r.bindQuery(`SELECT ` + modelEndpointColumns + ` FROM model_endpoints WHERE user_id = ? AND id = ?`)
	e, err := scanModelEndpoint(r.DB.QueryRowContext(ctx, query, userID, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrModelEndpointNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting model endpoint: %w", err)
	}
	return e, nil
}

// ListModelEndpoints returns every endpoint the user owns, by name.
func (r *Repo) ListModelEndpoints(ctx context.Context, userID string) ([]*ModelEndpoint, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	query := r.bindQuery(`SELECT ` + modelEndpointColumns + ` FROM model_endpoints WHERE user_id = ? ORDER BY name, id`)
	rows, err := r.DB.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("listing model endpoints: %w", err)
	}
	defer rows.Close()
	var out []*ModelEndpoint
	for rows.Next() {
		e, err := scanModelEndpoint(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning model endpoint: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateModelEndpoint replaces the mutable columns of the user's endpoint.
func (r *Repo) UpdateModelEndpoint(ctx context.Context, e *ModelEndpoint) error {
	if e == nil || e.ID == "" || e.UserID == "" {
		return ErrModelEndpointNotFound
	}
	headers := e.HeaderNames
	if headers == nil {
		headers = []string{}
	}
	models := e.ModelsJSON
	if models == "" {
		models = "[]"
	}
	query := r.bindQuery(`UPDATE model_endpoints
		SET name = ?, base_url = ?, route = ?, daemon_id = ?, credential_connection_id = ?,
		    header_names = ?, models_json = ?, probe_json = ?, updated_at = now()
		WHERE user_id = ? AND id = ?
		RETURNING updated_at`)
	err := r.DB.QueryRowContext(ctx, query, e.Name, e.BaseURL, e.Route, e.DaemonID, e.CredentialConnectionID,
		pq.StringArray(headers), models, e.ProbeJSON, e.UserID, e.ID).Scan(&e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrModelEndpointNotFound
	}
	if err != nil {
		if isModelEndpointNameConflict(err) {
			return ErrModelEndpointNameTaken
		}
		return fmt.Errorf("updating model endpoint: %w", err)
	}
	return nil
}

// SetModelEndpointProbe stores a fresh probe result without touching anything
// else. A missing endpoint is not an error: a periodic refresh can race a
// delete.
func (r *Repo) SetModelEndpointProbe(ctx context.Context, userID, id, probeJSON string) error {
	query := r.bindQuery(`UPDATE model_endpoints SET probe_json = ? WHERE user_id = ? AND id = ?`)
	if _, err := r.DB.ExecContext(ctx, query, probeJSON, userID, id); err != nil {
		return fmt.Errorf("storing model endpoint probe: %w", err)
	}
	return nil
}

// DeleteModelEndpoint removes the user's endpoint.
func (r *Repo) DeleteModelEndpoint(ctx context.Context, userID, id string) error {
	query := r.bindQuery(`DELETE FROM model_endpoints WHERE user_id = ? AND id = ?`)
	res, err := r.DB.ExecContext(ctx, query, userID, id)
	if err != nil {
		return fmt.Errorf("deleting model endpoint: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrModelEndpointNotFound
	}
	return nil
}
