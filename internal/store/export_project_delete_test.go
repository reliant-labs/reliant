// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
)

// DeleteUserProjectInSlices is DeleteUserProject with its thinning slices
// sized messages and rows, so a test crosses slice boundaries with a few
// rows.
func DeleteUserProjectInSlices(ctx context.Context, db orm.Context, id, userID string, messages, rows int) error {
	sizes := defaultProjectDeleteSizes
	sizes.messages, sizes.rows = messages, rows
	return deleteUserProject(ctx, db, id, userID, sizes)
}

// SetProjectDeleteAfterThin installs f as the hook DeleteUserProject runs
// between its thinning slices and its chat batches, and returns the restore.
// f gets every delete's project id; a test acts only on its own.
func SetProjectDeleteAfterThin(f func(ctx context.Context, projectID string)) (restore func()) {
	prev := projectDeleteAfterThin.Swap(&f)
	return func() { projectDeleteAfterThin.Store(prev) }
}

// SetProjectDeleteBeforeFinal installs f as the hook DeleteUserProject runs
// between its batch loops and the project row's transaction, for the external
// tests, and returns the restore. f gets every delete's project id; a test
// acts only on its own.
func SetProjectDeleteBeforeFinal(f func(ctx context.Context, projectID string)) (restore func()) {
	prev := projectDeleteBeforeFinal.Swap(&f)
	return func() { projectDeleteBeforeFinal.Store(prev) }
}
