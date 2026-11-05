// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/store"
)

// RecordFirstAppSession records userID's authenticated request and reports
// whether it is their first ever (the app_first_session product event).
func (r *Repo) RecordFirstAppSession(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, errors.New("user_id cannot be empty")
	}
	return store.RecordFirstAppSession(ctx, r.ORM(), userID)
}
