// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// A pause's notice reads its trigger through the repository facade and writes
// claims, confirmations and refusals through the generated notice store. The
// facade no longer carries the three writes, so it cannot stand in for the
// store, and the joined store refuses to be built without its trigger read.
func TestPauseStoresNeedBothHalves(t *testing.T) {
	var facade any = (*db.Repo)(nil)
	_, store := facade.(PauseNoticeStore)
	_, confirmer := facade.(pauseNoticeConfirmer)
	assert.False(t, store || confirmer, "*db.Repo must not satisfy the notice store on its own")

	require.PanicsWithValue(t, "workersetup: newPauseStores requires the trigger read", func() { newPauseStores(nil, nil) })
}
