// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The r2/r3 tables over a migrated Postgres: the channel seed is
// idempotent, the composite foreign keys come back as LinkErrors, the
// coverage aggregate and the asset quota sum see every row, and the library
// lists, upserts and prunes by key.
func TestPGStoreR2(t *testing.T) {
	env := storetest.New(t)
	st := NewStore(env.ORM)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	svc := New(Deps{Store: st, Gate: allowGate(orgA, orgB), Assets: newFakeBlobs()})

	t.Run("channels seed once, keys are unique per org", func(t *testing.T) {
		res, err := svc.ListChannels(ctx, person, ListChannelsInput{})
		require.NoError(t, err)
		require.Len(t, res.Channels, len(DefaultChannels))
		again, err := svc.ListChannels(ctx, person, ListChannelsInput{})
		require.NoError(t, err)
		assert.Len(t, again.Channels, len(DefaultChannels))
		rows := []*store.GtmChannel{seededChannelRow(orgA, DefaultChannels[0], now)}
		require.NoError(t, st.InTx(ctx, func(ctx context.Context) error { return st.SeedChannels(ctx, rows) }), "a re-seed skips held keys")
		n, err := st.CountChannels(ctx, orgA)
		require.NoError(t, err)
		assert.Equal(t, int64(len(DefaultChannels)), n)

		_, err = svc.CreateChannel(ctx, person, CreateChannelInput{Key: "blog", Name: "Blog", Mode: ChannelManual})
		require.ErrorIs(t, err, ErrChannelExists)
		ch, err := svc.CreateChannel(ctx, agent, CreateChannelInput{Key: "dev-to", Name: "dev.to", Mode: ChannelManual})
		require.NoError(t, err)
		assert.Equal(t, ChannelSourceAgent, ch.Source)
		_, err = svc.UpdateChannel(ctx, agent, UpdateChannelInput{ID: ch.ID, ExpectedRevision: ch.Revision, Key: ptr("reddit")})
		require.ErrorIs(t, err, ErrChannelExists, "a renamed key the org holds")
		email := channelByKey(t, res.Channels, "email")
		assert.Equal(t, GmailIntegration, email.IntegrationKind)
		assert.Empty(t, email.ConnectionID, "NULL is the user's default")
		assert.Equal(t, []string{"email", "newsletter"}, email.ChannelTypes, "types round-trip")
	})

	t.Run("links, coverage and the asset quota", func(t *testing.T) {
		chs, err := svc.ListChannels(ctx, person, ListChannelsInput{})
		require.NoError(t, err)
		reddit := channelByKey(t, chs.Channels, "reddit")
		strat, err := svc.CreateStrategy(ctx, person, CreateStrategyInput{Name: "Grow"})
		require.NoError(t, err)
		c, err := svc.CreateCampaign(ctx, person, CreateCampaignInput{Name: "c", StrategyID: strat.ID,
			PlannedChannelIDs: []string{reddit.ID}, PlannedFormats: []ItemFormat{FormatShortPost, FormatVideo}})
		require.NoError(t, err)
		assert.Equal(t, []ItemFormat{FormatShortPost, FormatVideo}, c.PlannedFormats)
		for _, f := range []ItemFormat{FormatShortPost, FormatShortPost, FormatLongPost} {
			_, err := svc.CreateItem(ctx, agent, CreateItemInput{Format: f, Title: "t", ChannelID: reddit.ID, CampaignID: c.ID})
			require.NoError(t, err)
		}
		counts, err := st.CampaignItemCounts(ctx, orgA, c.ID)
		require.NoError(t, err)
		assert.ElementsMatch(t, []ItemCount{
			{ChannelID: reddit.ID, Format: FormatLongPost, Status: ItemDraft, Count: 1},
			{ChannelID: reddit.ID, Format: FormatShortPost, Status: ItemDraft, Count: 2},
		}, counts)
		d, err := svc.GetCampaign(ctx, person, GetCampaignInput{ID: c.ID})
		require.NoError(t, err)
		require.Len(t, d.Coverage, 3)
		assert.Equal(t, 2, d.Coverage[0].Total)

		// The store's foreign keys hold when the service's checks are skipped.
		err = st.CreateItem(ctx, &store.GtmItem{OrgID: orgB, Format: string(FormatShortPost), Title: "x", ChannelID: &reddit.ID,
			Status: string(ItemDraft), Revision: 1, CreatedAt: now, UpdatedAt: now})
		var link *LinkError
		require.ErrorAs(t, err, &link)
		assert.Equal(t, "channel_id", link.Field)
		err = st.CreateCampaign(ctx, &store.GtmCampaign{OrgID: orgB, Name: "x", StrategyID: strat.ID, Status: string(CampaignDraft), Revision: 1, CreatedAt: now, UpdatedAt: now})
		require.ErrorAs(t, err, &link)
		assert.Equal(t, "strategy_id", link.Field)

		size := int64(100)
		asset := &store.GtmAsset{ID: "asset-1", OrgID: orgA, CampaignID: &c.ID, ObjectKey: assetKey(orgA, "asset-1"), ContentType: "image/png",
			SizeBytes: &size, Status: string(AssetPending), UploadedBy: "u", Source: string(AssetSourcePerson), CreatedAt: now}
		require.NoError(t, st.CreateAsset(ctx, asset))
		used, err := st.SumAssetBytes(ctx, orgA)
		require.NoError(t, err)
		assert.Equal(t, int64(100), used, "PENDING counts")
		require.NoError(t, st.MarkAssetReady(ctx, orgA, "asset-1", "image/png", 100, sum(pngBytes)))
		require.ErrorIs(t, st.MarkAssetReady(ctx, orgA, "asset-1", "image/png", 100, sum(pngBytes)), ErrAssetNotPending)
		got, err := st.GetAsset(ctx, orgA, "asset-1")
		require.NoError(t, err)
		assert.Equal(t, string(AssetReady), got.Status)
		bad := *asset
		bad.ID = "asset-2"
		var ve *ValidationError
		require.ErrorAs(t, st.CreateAsset(ctx, &bad), &ve, "an object key that is not its own")
		require.NoError(t, st.DeleteAsset(ctx, orgA, "asset-1"))
		used, err = st.SumAssetBytes(ctx, orgA)
		require.NoError(t, err)
		assert.Zero(t, used)
	})

	t.Run("the library upserts, filters and prunes", func(t *testing.T) {
		a, b := libEntry("alpha", CategoryLaunchesPR), libEntry("beta", CategoryPaidAcquisition)
		b.Fits, b.Name = []string{"b2c"}, "Paid ads"
		res, err := svc.ImportStrategyLibrary(ctx, person, ImportStrategyLibraryInput{SourceRevision: "r1", Entries: []LibraryEntry{a, b}})
		require.NoError(t, err)
		assert.Equal(t, ImportStrategyLibraryResult{Created: 2}, res)
		res, err = svc.ImportStrategyLibrary(ctx, person, ImportStrategyLibraryInput{SourceRevision: "r2", Entries: []LibraryEntry{a, b}})
		require.NoError(t, err)
		assert.Equal(t, ImportStrategyLibraryResult{Unchanged: 2}, res, "equal after a Postgres round trip")

		for name, tc := range map[string]struct {
			f    LibraryFilter
			want []string
		}{
			"fit":   {LibraryFilter{Fit: "b2c"}, []string{"beta"}},
			"query": {LibraryFilter{Query: "ADS"}, []string{"beta"}},
			"keys":  {LibraryFilter{Keys: []string{"alpha"}}, []string{"alpha"}},
			"none":  {LibraryFilter{Keys: []string{}}, nil},
			"cat":   {LibraryFilter{Category: CategoryLaunchesPR, Stage: "launch"}, []string{"alpha"}},
		} {
			tc.f.OrgID = orgA
			rows, err := st.ListLibrary(ctx, tc.f)
			require.NoError(t, err, name)
			var got []string
			for _, r := range rows {
				got = append(got, r.Key)
			}
			assert.Equal(t, tc.want, got, name)
		}
		used, err := svc.UseLibraryStrategy(ctx, agent, UseLibraryStrategyInput{LibraryKey: "alpha", CreateFirstCampaign: true})
		require.NoError(t, err)
		assert.Equal(t, "alpha", used.Strategy.LibraryKey)
		require.NotNil(t, used.Campaign)
		assert.Len(t, used.Campaign.PlannedChannelIDs, 2, "reddit and blog, by key")

		res, err = svc.ImportStrategyLibrary(ctx, person, ImportStrategyLibraryInput{SourceRevision: "r3", Entries: []LibraryEntry{a}, Prune: true, AllKeys: []string{"alpha"}})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Pruned)
		_, err = st.GetLibraryEntry(ctx, orgA, "beta")
		require.ErrorIs(t, err, ErrNotFound)
		kept, err := svc.GetStrategy(ctx, person, GetStrategyInput{ID: used.Strategy.ID})
		require.NoError(t, err)
		assert.Equal(t, "alpha", kept.Strategy.LibraryKey)
	})
}

func channelByKey(t *testing.T, chs []Channel, key string) Channel {
	t.Helper()
	for _, ch := range chs {
		if ch.Key == key {
			return ch
		}
	}
	t.Fatalf("no channel %q", key)
	return Channel{}
}
