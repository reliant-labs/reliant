// Copyright (c) 2025 Reliant Labs
package db

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Only the trigger name key becomes ErrTriggerNameTaken, and it no longer
// carries the PgError, so the retry ladder does not re-run the conflict.
func TestMapTriggerNameConflict(t *testing.T) {
	t.Parallel()
	dup := &pgconn.PgError{Code: "23505", ConstraintName: store.TriggerConstraintUserIDProjectIDNameKey}
	got := mapTriggerNameConflict(dup, "tick")
	assert.ErrorIs(t, got, core.ErrTriggerNameTaken)
	var pgErr *pgconn.PgError
	assert.False(t, errors.As(got, &pgErr), "the PgError must not reach the retry ladder")

	other := &pgconn.PgError{Code: "23505", ConstraintName: "triggers_pkey"}
	assert.Same(t, error(other), mapTriggerNameConflict(other, "tick"))
}
