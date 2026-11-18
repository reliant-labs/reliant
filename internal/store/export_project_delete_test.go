// Copyright (c) 2025 Reliant Labs
package store

import "context"

// SetProjectDeleteBeforeFinal installs f as the hook DeleteUserProject runs
// between its batch loops and the project row's transaction, for the external
// tests, and returns the restore. f gets every delete's project id; a test
// acts only on its own.
func SetProjectDeleteBeforeFinal(f func(ctx context.Context, projectID string)) (restore func()) {
	prev := projectDeleteBeforeFinal.Swap(&f)
	return func() { projectDeleteBeforeFinal.Store(prev) }
}
