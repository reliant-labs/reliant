// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A worker given a database but no org accounts store (the restart rig, the
// replay harness) ran the rejected-key sweep over a nil store and panicked on
// its first tick. Each sweep is gated on its own store.
func TestNoticeSweepsRunOnlyOverTheStoresTheWorkerHas(t *testing.T) {
	q := orgKeyNoticeQueue{starter: &pauseStarter{}, taskQueue: "q"}

	assert.Empty(t, noticeSweeps(&Config{Database: &db.Repo{}, OrgAccounts: &MockOrgKeyNoticeStore{}}, orgKeyNoticeQueue{}),
		"no Temporal: nothing to start notices on")
	assert.Len(t, noticeSweeps(&Config{Database: &db.Repo{}}, q), 1,
		"a database without the org accounts store runs only the pause-notice sweep")

	claimed := 0
	keys := &MockOrgKeyNoticeStore{ClaimDueOrgKeyNoticesFunc: func(context.Context, int, time.Duration) ([]core.ProviderAccount, error) {
		claimed++
		return nil, nil
	}}
	sweeps := noticeSweeps(&Config{OrgAccounts: keys}, q)
	require.Len(t, sweeps, 1, "the org accounts store without a database runs only the key sweep")
	sweeps[0](context.Background())
	assert.Equal(t, 1, claimed)
	assert.Len(t, noticeSweeps(&Config{Database: &db.Repo{}, OrgAccounts: keys}, q), 2)
}
