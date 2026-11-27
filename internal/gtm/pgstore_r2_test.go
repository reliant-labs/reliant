// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gtm/library"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The r2/r3 tables over a migrated Postgres: the channel seed is
// idempotent, the composite foreign keys come back as LinkErrors, the
// coverage aggregate and the asset quota sum see every row, and the idea
// bank's columns (positioning profile, idea fields) round-trip.
func TestPGStoreR2(t *testing.T) {
	env := storetest.New(t)
	st := NewStore(env.ORM)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	svc := New(Deps{Store: st, Gate: allowGate(orgA, orgB), Assets: newFakeBlobs()})

	t.Run("channels seed once, keys are unique per org", func(t *testing.T) {
		res, err := svc.ListChannels(ctx, person, ListChannelsInput{})
		require.NoError(t, err)
		require.Len(t, res.Channels, len(DefaultChannels()))
		again, err := svc.ListChannels(ctx, person, ListChannelsInput{})
		require.NoError(t, err)
		assert.Len(t, again.Channels, len(DefaultChannels()))
		rows := []*store.GtmChannel{seededChannelRow(orgA, DefaultChannels()[0], now)}
		require.NoError(t, st.InTx(ctx, func(ctx context.Context) error { return st.SeedChannels(ctx, rows) }), "a re-seed skips held keys")
		n, err := st.CountChannels(ctx, orgA)
		require.NoError(t, err)
		assert.Equal(t, int64(len(DefaultChannels())), n)

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
		cutoff := now.Add(-time.Hour)
		used, err := st.SumAssetBytes(ctx, orgA, cutoff)
		require.NoError(t, err)
		assert.Equal(t, int64(100), used, "PENDING counts")
		used, err = st.SumAssetBytes(ctx, orgA, now)
		require.NoError(t, err)
		assert.Zero(t, used, "an expired PENDING asset does not")
		expired, err := st.ListExpiredAssets(ctx, orgA, now, 10)
		require.NoError(t, err)
		require.Len(t, expired, 1)
		assert.Equal(t, "asset-1", expired[0].ID)
		expired, err = st.ListExpiredAssets(ctx, orgA, cutoff, 10)
		require.NoError(t, err)
		assert.Empty(t, expired, "created after the cutoff")
		require.ErrorIs(t, st.MarkAssetReady(ctx, orgA, "asset-1", "image/png", 100, sum(pngBytes), now), ErrAssetNotPending, "expired")
		require.NoError(t, st.MarkAssetReady(ctx, orgA, "asset-1", "image/png", 100, sum(pngBytes), cutoff))
		require.ErrorIs(t, st.MarkAssetReady(ctx, orgA, "asset-1", "image/png", 100, sum(pngBytes), cutoff), ErrAssetNotPending)
		got, err := st.GetAsset(ctx, orgA, "asset-1")
		require.NoError(t, err)
		assert.Equal(t, string(AssetReady), got.Status)
		used, err = st.SumAssetBytes(ctx, orgA, now)
		require.NoError(t, err)
		assert.Equal(t, int64(100), used, "READY counts, however old")
		deleted, err := st.DeletePendingAsset(ctx, orgA, "asset-1", time.Time{})
		require.NoError(t, err)
		assert.False(t, deleted, "a READY asset is not PENDING")
		bad := *asset
		bad.ID = "asset-2"
		var ve *ValidationError
		require.ErrorAs(t, st.CreateAsset(ctx, &bad), &ve, "an object key that is not its own")
		require.NoError(t, st.DeleteAsset(ctx, orgA, "asset-1"))
		used, err = st.SumAssetBytes(ctx, orgA, cutoff)
		require.NoError(t, err)
		assert.Zero(t, used)

		pending := *asset
		pending.ID, pending.ObjectKey = "asset-3", assetKey(orgA, "asset-3")
		require.NoError(t, st.CreateAsset(ctx, &pending))
		deleted, err = st.DeletePendingAsset(ctx, orgA, "asset-3", cutoff)
		require.NoError(t, err)
		assert.False(t, deleted, "created after createdBefore")
		deleted, err = st.DeletePendingAsset(ctx, orgB, "asset-3", time.Time{})
		require.NoError(t, err)
		assert.False(t, deleted, "another org's")
		deleted, err = st.DeletePendingAsset(ctx, orgA, "asset-3", now)
		require.NoError(t, err)
		assert.True(t, deleted)
		_, err = st.GetAsset(ctx, orgA, "asset-3")
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("the idea bank's columns round-trip: positioning profile, idea fields, a used strategy", func(t *testing.T) {
		pos, err := svc.UpdatePositioning(ctx, person, UpdatePositioningInput{PositioningMD: "p", BusinessType: BusinessDeveloperTools, BusinessModels: []BusinessModel{ModelFreemium, ModelUsageBased}})
		require.NoError(t, err)
		pos, err = svc.UpdatePositioning(ctx, person, UpdatePositioningInput{ExpectedRevision: pos.Revision, PositioningMD: "p2", BusinessType: BusinessDeveloperTools, BusinessModels: []BusinessModel{ModelSubscription}})
		require.NoError(t, err)
		got, err := svc.GetPositioning(ctx, person)
		require.NoError(t, err)
		assert.Equal(t, BusinessDeveloperTools, got.BusinessType)
		assert.Equal(t, []BusinessModel{ModelSubscription}, got.BusinessModels, "an update writes the profile")
		fits, err := svc.ListStrategyLibrary(ctx, person, ListStrategyLibraryInput{FitsMyBusiness: true, PageSize: MaxLibraryPageSize})
		require.NoError(t, err)
		for _, e := range fits.Entries {
			assert.True(t, len(e.BusinessModels) == 0 || slices.Contains(e.BusinessModels, ModelSubscription), e.Key)
		}

		key := library.All()[0].Key
		answers := []TailorAnswer{{Question: "Who?", Answer: "Builders"}}
		idea, err := svc.CreateIdea(ctx, agent, CreateIdeaInput{Kind: IdeaKindGTM, Title: "t", LibraryKey: key, TailorAnswers: answers, SuggestedWorkflowMD: "w"})
		require.NoError(t, err)
		stored, err := svc.GetIdea(ctx, person, GetIdeaInput{ID: idea.ID})
		require.NoError(t, err)
		assert.Equal(t, key, stored.LibraryKey)
		assert.Equal(t, answers, stored.TailorAnswers)
		assert.Equal(t, "w", stored.SuggestedWorkflowMD)
		cleared, err := svc.UpdateIdea(ctx, agent, UpdateIdeaInput{ID: idea.ID, ExpectedRevision: idea.Revision, LibraryKey: ptr(""), TailorAnswers: &[]TailorAnswer{}, SuggestedWorkflowMD: ptr("")})
		require.NoError(t, err)
		stored, err = svc.GetIdea(ctx, person, GetIdeaInput{ID: idea.ID})
		require.NoError(t, err)
		assert.Equal(t, cleared.Revision, stored.Revision)
		assert.Empty(t, stored.LibraryKey)
		assert.Empty(t, stored.TailorAnswers)
		assert.Empty(t, stored.SuggestedWorkflowMD)

		used, err := svc.UseLibraryStrategy(ctx, agent, UseLibraryStrategyInput{LibraryKey: key, CreateFirstCampaign: true})
		require.NoError(t, err)
		st2, err := svc.GetStrategy(ctx, person, GetStrategyInput{ID: used.Strategy.ID})
		require.NoError(t, err)
		assert.Equal(t, key, st2.Strategy.LibraryKey)
		assert.Equal(t, StrategyCategory("GTM_STRATEGY_CATEGORY_"+strings.ToUpper(library.All()[0].Category)), st2.Strategy.Category)
		require.Len(t, st2.Campaigns, 1)
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
