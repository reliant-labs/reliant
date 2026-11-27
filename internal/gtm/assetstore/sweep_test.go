// Copyright (c) 2026 Reliant Labs

package assetstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/blobstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/assetstore"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

const (
	orgA = "55949942-3ee9-4cfc-bb7c-817bdba75186"
	orgB = "org-b"
)

const sha = "0000000000000000000000000000000000000000000000000000000000000000"

// sweepEnv is a migrated database with one campaign per org, and a bucket.
type sweepEnv struct {
	env       *storetest.Env
	st        gtm.Store
	mem       *blobstore.Memory
	campaigns map[string]string
	now       time.Time
}

func newSweepEnv(t *testing.T) *sweepEnv {
	t.Helper()
	env := storetest.New(t)
	e := &sweepEnv{env: env, st: gtm.NewStore(env.ORM), mem: blobstore.NewMemory(), campaigns: map[string]string{},
		now: time.Now().UTC().Truncate(time.Microsecond)}
	ctx := context.Background()
	for _, org := range []string{orgA, orgB} {
		s := &store.GtmStrategy{OrgID: org, Name: "S", Status: string(gtm.StrategyDraft), Revision: 1, CreatedBy: "u", UpdatedBy: "u", CreatedAt: e.now, UpdatedAt: e.now}
		require.NoError(t, e.st.CreateStrategy(ctx, s))
		c := &store.GtmCampaign{OrgID: org, Name: "C", Status: string(gtm.CampaignDraft), StrategyID: s.ID, Revision: 1, CreatedAt: e.now, UpdatedAt: e.now}
		require.NoError(t, e.st.CreateCampaign(ctx, c))
		e.campaigns[org] = c.ID
	}
	return e
}

// asset records an asset of org created age ago, with both its objects in
// the bucket.
func (e *sweepEnv) asset(t *testing.T, org, id string, status gtm.AssetStatus, age time.Duration) *store.GtmAsset {
	t.Helper()
	size := int64(len(pngBytes))
	campaign := e.campaigns[org]
	row := &store.GtmAsset{ID: id, OrgID: org, CampaignID: &campaign, ObjectKey: "gtm/" + org + "/" + id,
		Filename: "f.png", ContentType: "image/png", SizeBytes: &size, Status: string(status),
		UploadedBy: "u", Source: string(gtm.AssetSourcePerson), CreatedAt: e.now.Add(-age)}
	if status == gtm.AssetReady {
		s := sha
		row.Sha256 = &s
	}
	require.NoError(t, e.st.CreateAsset(context.Background(), row))
	require.NoError(t, e.mem.Put(row.ObjectKey, pngBytes, "image/png"))
	require.NoError(t, e.mem.Put(row.ObjectKey+".upload", pngBytes, "image/png"))
	return row
}

func (e *sweepEnv) exists(t *testing.T, row *store.GtmAsset) (rowThere, ownThere, stagingThere bool) {
	t.Helper()
	_, err := e.st.GetAsset(context.Background(), row.OrgID, row.ID)
	if err != nil {
		require.ErrorIs(t, err, gtm.ErrNotFound)
	}
	_, own := e.mem.Get(row.ObjectKey)
	_, staging := e.mem.Get(row.ObjectKey + ".upload")
	return err == nil, own, staging
}

func TestSweepStalePending(t *testing.T) {
	e := newSweepEnv(t)
	ctx := context.Background()
	staleA := e.asset(t, orgA, "stale-a", gtm.AssetPending, 25*time.Hour)
	staleB := e.asset(t, orgB, "stale-b", gtm.AssetPending, 30*time.Hour)
	edge := e.asset(t, orgA, "edge", gtm.AssetPending, assetstore.StalePendingAge)
	fresh := e.asset(t, orgA, "fresh", gtm.AssetPending, time.Hour)
	ready := e.asset(t, orgA, "ready", gtm.AssetReady, 48*time.Hour)

	sw := assetstore.NewSweeper(assetstore.SweeperDeps{DB: e.env.ORM, Assets: assetstore.New(e.mem)})
	res, err := sw.SweepStalePending(ctx, e.now)
	require.NoError(t, err)
	assert.Equal(t, assetstore.SweepResult{Deleted: 3, StagingDeleted: 1}, res,
		"both orgs' stale PENDING assets, the one exactly 24h old too; and the READY one's leftover staging object")

	for _, row := range []*store.GtmAsset{staleA, staleB, edge} {
		r, own, staging := e.exists(t, row)
		assert.False(t, r || own || staging, "%s: row and both objects deleted", row.ID)
	}
	r, own, staging := e.exists(t, fresh)
	assert.True(t, r && own && staging, "a fresh PENDING asset is kept whole")
	r, own, staging = e.exists(t, ready)
	assert.True(t, r && own, "a READY asset and its own key are kept")
	assert.False(t, staging, "its leftover staging object goes")

	res, err = sw.SweepStalePending(ctx, e.now)
	require.NoError(t, err)
	assert.Zero(t, res, "nothing left to sweep")
}

// raceStore is a gtm.Store whose DeletePendingAsset first runs race: what
// another process does to the row between the sweep's list and its delete.
type raceStore struct {
	gtm.Store
	race func()
}

func (s raceStore) DeletePendingAsset(ctx context.Context, orgID, id string, createdBefore time.Time) (bool, error) {
	s.race()
	return s.Store.DeletePendingAsset(ctx, orgID, id, createdBefore)
}

// The sweep's delete is conditional: a row that a confirm made READY, or
// that is no longer that old, between the list and the delete keeps its
// row and both objects.
func TestSweepStalePendingConditionalDelete(t *testing.T) {
	for name, race := range map[string]string{
		"made READY": `UPDATE gtm_assets SET status = 'GTM_ASSET_STATUS_READY', sha256 = '` + sha + `' WHERE id = $1`,
		"newer":      `UPDATE gtm_assets SET created_at = now() WHERE id = $1`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newSweepEnv(t)
			row := e.asset(t, orgA, "racing", gtm.AssetPending, 25*time.Hour)
			raced := 0
			st := raceStore{Store: e.st, race: func() {
				raced++
				_, err := e.env.SQL.Exec(race, row.ID)
				require.NoError(t, err)
			}}
			// Leftover staging is not this test's subject: a store whose
			// Stat finds nothing keeps the READY half of the pass out of it.
			sw := assetstore.NewSweeper(assetstore.SweeperDeps{DB: e.env.ORM, Store: st, Assets: noStat{assetstore.New(e.mem)}})
			res, err := sw.SweepStalePending(context.Background(), e.now)
			require.NoError(t, err)
			assert.Equal(t, 1, raced, "the row was listed, and the race ran before its delete")
			assert.Zero(t, res.Deleted)
			r, own, staging := e.exists(t, row)
			assert.True(t, r && own && staging, "the row and both objects survive")
		})
	}
}

// noStat is an AssetStore whose Stat finds nothing.
type noStat struct{ gtm.AssetStore }

func (noStat) Stat(context.Context, string) (int64, error) { return 0, gtm.ErrObjectNotFound }

// listStale reads PENDING rows only: a batch's worth of older READY rows
// does not crowd out the stale PENDING one.
func TestSweepStalePendingSkipsReadyRows(t *testing.T) {
	e := newSweepEnv(t)
	var ready []*store.GtmAsset
	for i := range 120 {
		ready = append(ready, e.asset(t, orgA, fmt.Sprintf("ready-%03d", i), gtm.AssetReady, 30*time.Hour+time.Duration(i)*time.Second))
	}
	pending := e.asset(t, orgB, "pending", gtm.AssetPending, 25*time.Hour)
	sw := assetstore.NewSweeper(assetstore.SweeperDeps{DB: e.env.ORM, Assets: assetstore.New(e.mem)})
	res, err := sw.SweepStalePending(context.Background(), e.now)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Deleted)
	r, _, _ := e.exists(t, pending)
	assert.False(t, r, "the stale PENDING row is swept")
	for _, row := range ready {
		r, own, _ := e.exists(t, row)
		require.True(t, r && own, "%s: a READY asset is kept", row.ID)
	}
}

// A READY asset's leftover staging object is deleted once the asset is
// StalePendingAge old, while it is in ReadyStagingWindow; nothing else of
// it is touched.
func TestSweepReadyStaging(t *testing.T) {
	e := newSweepEnv(t)
	old := e.asset(t, orgA, "old", gtm.AssetReady, 25*time.Hour)
	clean := e.asset(t, orgB, "clean", gtm.AssetReady, 26*time.Hour)
	require.NoError(t, e.mem.Delete(context.Background(), clean.ObjectKey+".upload"))
	young := e.asset(t, orgA, "young", gtm.AssetReady, time.Hour)
	aged := e.asset(t, orgA, "aged", gtm.AssetReady, assetstore.StalePendingAge+assetstore.ReadyStagingWindow+time.Hour)

	sw := assetstore.NewSweeper(assetstore.SweeperDeps{DB: e.env.ORM, Assets: assetstore.New(e.mem)})
	res, err := sw.SweepStalePending(context.Background(), e.now)
	require.NoError(t, err)
	assert.Equal(t, assetstore.SweepResult{StagingDeleted: 1}, res)

	r, own, staging := e.exists(t, old)
	assert.True(t, r && own, "the asset and its own key stay")
	assert.False(t, staging, "its leftover staging object goes")
	r, own, _ = e.exists(t, clean)
	assert.True(t, r && own)
	for _, row := range []*store.GtmAsset{young, aged} {
		r, own, staging := e.exists(t, row)
		assert.True(t, r && own && staging, "%s: outside the window, untouched", row.ID)
	}
}

// The READY half pages by (created_at, id): past one batch, and through
// rows that share a created_at.
func TestSweepReadyStagingPages(t *testing.T) {
	e := newSweepEnv(t)
	var rows []*store.GtmAsset
	// Rows share a created_at in groups of 7, which does not divide the
	// batch of 100: the first page ends inside a group (rows 98-104), so only
	// the id half of the (created_at, id) keyset finds the rest of it.
	for i := range 130 {
		rows = append(rows, e.asset(t, orgA, fmt.Sprintf("r-%03d", i), gtm.AssetReady, 25*time.Hour+time.Duration(i/7)*time.Second))
	}
	sw := assetstore.NewSweeper(assetstore.SweeperDeps{DB: e.env.ORM, Assets: assetstore.New(e.mem)})
	res, err := sw.SweepStalePending(context.Background(), e.now)
	require.NoError(t, err)
	assert.Equal(t, 130, res.StagingDeleted)
	for _, row := range rows {
		_, _, staging := e.exists(t, row)
		require.False(t, staging, row.ID)
	}
}

func TestSweepStalePendingPages(t *testing.T) {
	e := newSweepEnv(t)
	var rows []*store.GtmAsset
	for i := range 130 {
		rows = append(rows, e.asset(t, orgB, fmt.Sprintf("old-%03d", i), gtm.AssetPending, 25*time.Hour+time.Duration(i)*time.Second))
	}
	sw := assetstore.NewSweeper(assetstore.SweeperDeps{DB: e.env.ORM, Assets: assetstore.New(e.mem)})
	res, err := sw.SweepStalePending(context.Background(), e.now)
	require.NoError(t, err)
	assert.Equal(t, 130, res.Deleted, "past one batch")
	for _, row := range rows {
		r, _, _ := e.exists(t, row)
		require.False(t, r, row.ID)
	}
}

// failingDeletes is an AssetStore whose Delete fails.
type failingDeletes struct{ gtm.AssetStore }

func (failingDeletes) Delete(context.Context, string) error { return errors.New("bucket unreachable") }

func TestSweepStalePendingOrphans(t *testing.T) {
	e := newSweepEnv(t)
	row := e.asset(t, orgA, "stale", gtm.AssetPending, 25*time.Hour)
	sw := assetstore.NewSweeper(assetstore.SweeperDeps{DB: e.env.ORM, Assets: failingDeletes{assetstore.New(e.mem)}})
	res, err := sw.SweepStalePending(context.Background(), e.now)
	require.NoError(t, err)
	assert.Equal(t, assetstore.SweepResult{Deleted: 1, Orphaned: 1}, res)
	r, own, staging := e.exists(t, row)
	assert.False(t, r, "the row goes first, whatever the bucket does")
	assert.True(t, own && staging, "the objects stay behind, logged")
}

func TestSweepStalePendingWithoutAssets(t *testing.T) {
	e := newSweepEnv(t)
	row := e.asset(t, orgA, "stale", gtm.AssetPending, 25*time.Hour)
	sw := assetstore.NewSweeper(assetstore.SweeperDeps{DB: e.env.ORM, Assets: assetstore.New(nil)})
	res, err := sw.SweepStalePending(context.Background(), e.now)
	require.NoError(t, err)
	assert.Zero(t, res)
	r, _, _ := e.exists(t, row)
	assert.True(t, r, "no bucket: nothing is deleted, so no row loses track of its objects")
}

func TestSweepRunStopsWithItsContext(t *testing.T) {
	e := newSweepEnv(t)
	e.asset(t, orgA, "stale", gtm.AssetPending, 25*time.Hour)
	sw := assetstore.NewSweeper(assetstore.SweeperDeps{DB: e.env.ORM, Assets: assetstore.New(e.mem)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// A non-positive interval is DefaultSweepInterval, not a ticker panic.
	go func() { sw.Run(ctx, 0); close(done) }()
	require.Eventually(t, func() bool {
		page, err := e.mem.List(context.Background(), "gtm/", blobstore.ListOptions{})
		return err == nil && !strings.Contains(fmt.Sprint(page.Objects), "stale")
	}, 30*time.Second, 50*time.Millisecond, "the first pass runs at start")
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}
