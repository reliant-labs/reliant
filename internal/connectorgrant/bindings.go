// Copyright (c) 2025 Reliant Labs

package connectorgrant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ClientBinding records which connector an OAuth client acts through.
//
// An OAuth token identifies a user; a grant says what may be touched. This is
// the user's explicit answer to "which workspace may this application use?",
// captured at consent so later requests resolve without asking again.
type ClientBinding struct {
	ID         string
	UserID     string
	ClientID   string
	GrantID    string
	ClientName string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// GetBinding returns the connector a client was authorized to use.
//
// Only live grants resolve: a binding to a revoked or expired connector is
// reported as absent, so the caller falls back to consent rather than acting
// on a choice that no longer exists.
func (s *SQLStore) GetBinding(ctx context.Context, userID, clientID string) (*ClientBinding, error) {
	const query = `
		SELECT b.id, b.user_id, b.client_id, b.grant_id, b.client_name, b.created_at, b.updated_at
		FROM connector_client_bindings b
		JOIN connector_grants g ON g.id = b.grant_id
		WHERE b.user_id = $1
		  AND b.client_id = $2
		  AND g.revoked_at IS NULL
		  AND (g.expires_at IS NULL OR g.expires_at > now())`

	var b ClientBinding
	err := s.db.QueryRowContext(ctx, query, userID, clientID).Scan(
		&b.ID, &b.UserID, &b.ClientID, &b.GrantID, &b.ClientName, &b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get client binding: %w", err)
	}
	return &b, nil
}

// PutBinding records the user's choice.
//
// Upsert rather than insert: re-consenting must MOVE a client to a different
// connector, not add a second authorization. A client that could accumulate
// access to several connectors invisibly is exactly what consent exists to
// prevent.
func (s *SQLStore) PutBinding(ctx context.Context, b *ClientBinding) error {
	switch {
	case b == nil:
		return errors.New("binding is required")
	case b.ID == "":
		return errors.New("binding id is required")
	case b.UserID == "":
		return errors.New("binding user id is required")
	case b.ClientID == "":
		return errors.New("binding client id is required")
	case b.GrantID == "":
		return errors.New("binding grant id is required")
	}

	const query = `
		INSERT INTO connector_client_bindings (id, user_id, client_id, grant_id, client_name)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (user_id, client_id)
		DO UPDATE SET grant_id = EXCLUDED.grant_id,
		              client_name = EXCLUDED.client_name,
		              updated_at = now()`

	if _, err := s.db.ExecContext(ctx, query,
		b.ID, b.UserID, b.ClientID, b.GrantID, b.ClientName,
	); err != nil {
		return fmt.Errorf("put client binding: %w", err)
	}
	return nil
}

// DeleteBinding removes a client's authorization.
//
// Scoped by user so one user cannot disconnect another's client, and it leaves
// the grant alone: the same connector may serve several clients, and
// disconnecting one should not revoke the others.
func (s *SQLStore) DeleteBinding(ctx context.Context, userID, clientID string) (bool, error) {
	const query = `DELETE FROM connector_client_bindings WHERE user_id = $1 AND client_id = $2`

	res, err := s.db.ExecContext(ctx, query, userID, clientID)
	if err != nil {
		return false, fmt.Errorf("delete client binding: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, nil
	}
	return affected > 0, nil
}

// ListBindingsByUser returns the clients a user has authorized, so the
// settings UI can show what is connected and what a revocation would cut off.
func (s *SQLStore) ListBindingsByUser(ctx context.Context, userID string) ([]*ClientBinding, error) {
	const query = `
		SELECT id, user_id, client_id, grant_id, client_name, created_at, updated_at
		FROM connector_client_bindings
		WHERE user_id = $1
		ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("list client bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*ClientBinding
	for rows.Next() {
		var b ClientBinding
		if err := rows.Scan(
			&b.ID, &b.UserID, &b.ClientID, &b.GrantID, &b.ClientName, &b.CreatedAt, &b.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan client binding: %w", err)
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}
