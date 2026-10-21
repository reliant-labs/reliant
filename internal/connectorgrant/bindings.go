// Copyright (c) 2025 Reliant Labs

package connectorgrant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
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
	row, err := store.GetLiveConnectorClientBinding(ctx, s.db, userID, clientID)
	if errors.Is(err, orm.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get client binding: %w", err)
	}
	return bindingFromRow(row), nil
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

	if err := store.PutConnectorClientBinding(ctx, s.db, &store.ConnectorClientBinding{
		ID: b.ID, UserID: b.UserID, ClientID: b.ClientID, GrantID: b.GrantID, ClientName: b.ClientName,
	}); err != nil {
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
	deleted, err := store.UnbindConnectorClient(ctx, s.db, userID, clientID)
	if err != nil {
		return false, fmt.Errorf("delete client binding: %w", err)
	}
	return deleted, nil
}

// ListBindingsByUser returns the clients a user has authorized, so the
// settings UI can show what is connected and what a revocation would cut off.
func (s *SQLStore) ListBindingsByUser(ctx context.Context, userID string) ([]*ClientBinding, error) {
	rows, err := store.ListConnectorClientBinding(ctx, s.db,
		orm.WhereEq(store.ConnectorClientBindingFieldUserID, userID),
		orm.WithOrderBy(store.ConnectorClientBindingFieldCreatedAt, orm.Desc))
	if err != nil {
		return nil, fmt.Errorf("list client bindings: %w", err)
	}
	var out []*ClientBinding
	for _, row := range rows {
		out = append(out, bindingFromRow(row))
	}
	return out, nil
}

func bindingFromRow(row *store.ConnectorClientBinding) *ClientBinding {
	return &ClientBinding{
		ID: row.ID, UserID: row.UserID, ClientID: row.ClientID, GrantID: row.GrantID,
		ClientName: row.ClientName, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
