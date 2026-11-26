// Copyright (c) 2025 Reliant Labs
package runevents

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// The repository facade no longer carries the run-event outbox, so it cannot
// stand in for the emitter's store: a caller must hand it NewStore.
func TestFacadeIsNotTheRunEventStore(t *testing.T) {
	var facade any = (*db.Repo)(nil)
	_, ok := facade.(Store)
	require.False(t, ok, "*db.Repo must not satisfy runevents.Store")
	require.Implements(t, (*Store)(nil), NewStore(nil))
}
