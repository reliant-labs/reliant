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
	assert.Equal(t, assetstore.SweepResult{Deleted: 3}, res, "both orgs' stale PENDING assets, the one exactly 24h old too")

	for _, row := range []*store.GtmAsset{staleA, staleB, edge} {
		r, own, staging := e.exists(t, row)
		assert.False(t, r || own || staging, "%s: row and both objects deleted", row.ID)
	}
	for _, row := range []*store.GtmAsset{fresh, ready} {
		r, own, staging := e.exists(t, row)
		assert.True(t, r && own && staging, "%s: kept whole", row.ID)
	}

	res, err = sw.SweepStalePending(ctx, e.now)
	require.NoError(t, err)
	assert.Zero(t, res.Deleted, "nothing left to sweep")
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
	go func() { sw.Run(ctx, time.Hour); close(done) }()
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
