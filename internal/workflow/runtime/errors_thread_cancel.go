// Copyright (c) 2025 Reliant Labs
package runtime

import "errors"

// ErrThreadCancelled is returned by an inline (spawned) thread that stopped
// because the user cancelled it.
//
// It is deliberately distinct from a workflow-level cancellation: only the one
// spawn stops, and the parent run — plus every sibling spawn — carries on. The
// spawn's tool result reports the cancellation so the model sees why the work
// ended rather than an unexplained gap.
var ErrThreadCancelled = errors.New("thread cancelled by user")
