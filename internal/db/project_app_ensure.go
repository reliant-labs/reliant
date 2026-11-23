package db

import "github.com/reliant-labs/reliant/internal/store"

// EnsureProjectForAppInput is a deployed forge app control-plane wants a project for
// (store.EnsureProjectForApp).
type EnsureProjectForAppInput = store.EnsureProjectForAppInput

// EnsureProjectForAppResult is the project that now links the app, and whether this call created it.
type EnsureProjectForAppResult = store.EnsureProjectForAppResult
