// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/store"
)

// fakeBlobs is an in-memory AssetStore.
type fakeBlobs struct {
	objects map[string][]byte
	puts    []string
	deletes []string
	gets    []GetOptions
	failPut error
}

func newFakeBlobs() *fakeBlobs { return &fakeBlobs{objects: map[string][]byte{}} }

func (b *fakeBlobs) PresignPut(_ context.Context, key string, opts PutOptions) (PresignedRequest, error) {
	b.puts = append(b.puts, key)
	return PresignedRequest{
		URL:       "https://bucket.example/" + key,
		Headers:   map[string]string{"Content-Type": opts.ContentType, "Content-Length": fmt.Sprint(opts.Size)},
		ExpiresAt: t0.Add(15 * time.Minute),
	}, nil
}

func (b *fakeBlobs) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := b.objects[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *fakeBlobs) Put(_ context.Context, key string, r io.Reader, opts PutOptions) error {
	if b.failPut != nil {
		return b.failPut
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != opts.Size {
		return fmt.Errorf("size %d, not %d", len(data), opts.Size)
	}
	b.objects[key] = data
	return nil
}

func (b *fakeBlobs) Delete(_ context.Context, key string) error {
	b.deletes = append(b.deletes, key)
	delete(b.objects, key)
	return nil
}

func (b *fakeBlobs) PresignGet(_ context.Context, key string, opts GetOptions) (PresignedRequest, error) {
	b.gets = append(b.gets, opts)
	return PresignedRequest{URL: "https://bucket.example/" + key + "?get", ExpiresAt: t0.Add(5 * time.Minute)}, nil
}

type fakeFile struct {
	owner string
	file  AttachmentFile
	data  []byte
}

// fakeAttachments is an owner-scoped in-memory Attachments.
type fakeAttachments struct{ files map[string]fakeFile }

func (a *fakeAttachments) Open(_ context.Context, actor Actor, id string) (AttachmentFile, io.ReadCloser, error) {
	f, ok := a.files[id]
	if !ok || f.owner != actor.UserID {
		return AttachmentFile{}, nil, ErrNotFound
	}
	return f.file, io.NopCloser(bytes.NewReader(f.data)), nil
}

var (
	pngBytes = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{1}, 100)...)
	pdfBytes = []byte("%PDF-1.4\n%some pdf body\n")
)

func TestR2MethodsAreNotFoundWhenTheGateRefuses(t *testing.T) {
	methods := map[string]func(Service, Actor) error{
		"ListStrategies": func(s Service, a Actor) error {
			_, err := s.ListStrategies(ctx(), a, ListStrategiesInput{})
			return err
		},
		"GetStrategy": func(s Service, a Actor) error {
			_, err := s.GetStrategy(ctx(), a, GetStrategyInput{ID: "x"})
			return err
		},
		"CreateStrategy": func(s Service, a Actor) error {
			_, err := s.CreateStrategy(ctx(), a, CreateStrategyInput{Name: "n"})
			return err
		},
		"UpdateStrategy": func(s Service, a Actor) error {
			_, err := s.UpdateStrategy(ctx(), a, UpdateStrategyInput{ID: "x", ExpectedRevision: 1})
			return err
		},
		"ListChannels": func(s Service, a Actor) error { _, err := s.ListChannels(ctx(), a, ListChannelsInput{}); return err },
		"GetChannel":   func(s Service, a Actor) error { _, err := s.GetChannel(ctx(), a, GetChannelInput{ID: "x"}); return err },
		"CreateChannel": func(s Service, a Actor) error {
			_, err := s.CreateChannel(ctx(), a, CreateChannelInput{Key: "k", Name: "n", Mode: ChannelManual})
			return err
		},
		"UpdateChannel": func(s Service, a Actor) error {
			_, err := s.UpdateChannel(ctx(), a, UpdateChannelInput{ID: "x", ExpectedRevision: 1})
			return err
		},
		"ListAssets": func(s Service, a Actor) error {
			_, err := s.ListAssets(ctx(), a, ListAssetsInput{ItemID: "x"})
			return err
		},
		"GetAsset": func(s Service, a Actor) error { _, err := s.GetAsset(ctx(), a, GetAssetInput{ID: "x"}); return err },
		"CreateAssetUpload": func(s Service, a Actor) error {
			_, err := s.CreateAssetUpload(ctx(), a, CreateAssetUploadInput{ItemID: "x", Filename: "f", ContentType: "image/png", SizeBytes: 1})
			return err
		},
		"ConfirmAssetUpload": func(s Service, a Actor) error {
			_, err := s.ConfirmAssetUpload(ctx(), a, ConfirmAssetUploadInput{AssetID: "x"})
			return err
		},
		"GetAssetDownload": func(s Service, a Actor) error {
			_, err := s.GetAssetDownload(ctx(), a, GetAssetDownloadInput{AssetID: "x"})
			return err
		},
		"AttachFromAttachment": func(s Service, a Actor) error {
			_, err := s.AttachFromAttachment(ctx(), a, AttachFromAttachmentInput{AttachmentID: "x", ItemID: "y"})
			return err
		},
		"ListStrategyLibrary": func(s Service, a Actor) error {
			_, err := s.ListStrategyLibrary(ctx(), a, ListStrategyLibraryInput{})
			return err
		},
		"GetStrategyLibraryEntry": func(s Service, a Actor) error {
			_, err := s.GetStrategyLibraryEntry(ctx(), a, GetStrategyLibraryEntryInput{Key: "k"})
			return err
		},
		"ImportStrategyLibrary": func(s Service, a Actor) error {
			_, err := s.ImportStrategyLibrary(ctx(), a, ImportStrategyLibraryInput{SourceRevision: "r", Entries: []LibraryEntry{{Key: "k"}}})
			return err
		},
		"UseLibraryStrategy": func(s Service, a Actor) error {
			_, err := s.UseLibraryStrategy(ctx(), a, UseLibraryStrategyInput{LibraryKey: "k"})
			return err
		},
	}
	gateErr := errors.New("outage")
	for rname, g := range map[string]Gate{
		"not entitled": allowGate("another-org"),
		"gate error":   &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return true, gateErr }},
	} {
		for mname, call := range methods {
			t.Run(rname+"/"+mname, func(t *testing.T) {
				st, blobs, conns := &MockStore{}, newFakeBlobs(), &MockConnections{}
				svc := New(Deps{Store: st, Gate: g, Assets: blobs, Connections: conns, Attachments: &fakeAttachments{}})
				err := call(svc, person)
				require.ErrorIs(t, err, ErrNotFound)
				assert.Equal(t, "not found", err.Error())
				for _, m := range []string{"InTx", "CountChannels", "ListChannels", "GetStrategy", "ListStrategies", "GetAsset", "ListAssets", "ListLibrary", "GetLibraryEntry"} {
					assert.Zero(t, st.CallCount(m), m)
				}
				assert.Empty(t, blobs.puts)
				assert.Zero(t, conns.CallCount("State"))
			})
		}
	}
}

func TestStrategies(t *testing.T) {
	t.Run("an agent creates DRAFT strategies only", func(t *testing.T) {
		f := newFixture(t)
		st, err := f.svc.CreateStrategy(ctx(), agent, CreateStrategyInput{Name: "Grow", Category: CategoryLaunchesPR, PeriodStart: "2026-12-01", PeriodEnd: "2027-02-28"})
		require.NoError(t, err)
		assert.Equal(t, StrategyDraft, st.Status)
		assert.Equal(t, agent.UserID, st.CreatedBy)
		assert.Equal(t, "2027-02-28", st.PeriodEnd)
		_, err = f.svc.CreateStrategy(ctx(), agent, CreateStrategyInput{Name: "Now", Status: StrategyActive})
		require.ErrorIs(t, err, ErrPersonOnly)
		p, err := f.svc.CreateStrategy(ctx(), person, CreateStrategyInput{Name: "Now", Status: StrategyActive})
		require.NoError(t, err)
		assert.Equal(t, StrategyActive, p.Status)
	})
	t.Run("an agent edits DRAFT strategies and never moves a status", func(t *testing.T) {
		f := newFixture(t)
		st, err := f.svc.CreateStrategy(ctx(), agent, CreateStrategyInput{Name: "Grow"})
		require.NoError(t, err)
		st, err = f.svc.UpdateStrategy(ctx(), agent, UpdateStrategyInput{ID: st.ID, ExpectedRevision: st.Revision, GoalMD: ptr("More orgs"), Category: ptr(CategoryProductLedGrowth)})
		require.NoError(t, err)
		assert.Equal(t, CategoryProductLedGrowth, st.Category)
		_, err = f.svc.UpdateStrategy(ctx(), agent, UpdateStrategyInput{ID: st.ID, ExpectedRevision: st.Revision, Status: ptr(StrategyActive)})
		require.ErrorIs(t, err, ErrPersonOnly)
		st, err = f.svc.UpdateStrategy(ctx(), person, UpdateStrategyInput{ID: st.ID, ExpectedRevision: st.Revision, Status: ptr(StrategyActive)})
		require.NoError(t, err)
		assert.Equal(t, person.UserID, st.UpdatedBy)
		_, err = f.svc.UpdateStrategy(ctx(), agent, UpdateStrategyInput{ID: st.ID, ExpectedRevision: st.Revision, Name: ptr("renamed")})
		require.ErrorIs(t, err, ErrPersonOnly, "an active strategy is a person's")
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
		st, err = f.svc.UpdateStrategy(ctx(), person, UpdateStrategyInput{ID: st.ID, ExpectedRevision: st.Revision, Category: ptr(StrategyCategory(""))})
		require.NoError(t, err)
		assert.Empty(t, st.Category, "an empty category clears it")
		_, err = f.svc.CreateStrategy(ctx(), person, CreateStrategyInput{Name: "Also active", Status: StrategyActive})
		require.NoError(t, err, "several strategies may be ACTIVE")
	})
	t.Run("periods are real calendar dates in order", func(t *testing.T) {
		f := newFixture(t)
		for name, in := range map[string]CreateStrategyInput{
			"not a date":  {Name: "n", PeriodStart: "next week"},
			"no such day": {Name: "n", PeriodStart: "2026-02-30"},
			"reversed":    {Name: "n", PeriodStart: "2026-03-01", PeriodEnd: "2026-02-01"},
			"no name":     {Name: " "},
			"category":    {Name: "n", Category: "GTM_STRATEGY_CATEGORY_NOPE"},
			"long metric": {Name: "n", TargetMetric: strings.Repeat("x", 501)},
		} {
			_, err := f.svc.CreateStrategy(ctx(), person, in)
			require.ErrorIs(t, err, ErrInvalidArgument, name)
		}
		st, err := f.svc.CreateStrategy(ctx(), person, CreateStrategyInput{Name: "n", PeriodStart: "2026-03-01"})
		require.NoError(t, err)
		_, err = f.svc.UpdateStrategy(ctx(), person, UpdateStrategyInput{ID: st.ID, ExpectedRevision: st.Revision, PeriodEnd: ptr("2026-02-01")})
		require.ErrorIs(t, err, ErrInvalidArgument)
		st, err = f.svc.UpdateStrategy(ctx(), person, UpdateStrategyInput{ID: st.ID, ExpectedRevision: st.Revision, PeriodStart: ptr("")})
		require.NoError(t, err)
		assert.Empty(t, st.PeriodStart)
	})
	t.Run("GetStrategy returns its campaigns, capped and flagged", func(t *testing.T) {
		f := newFixture(t)
		sid := f.strategy(t, person)
		other := f.strategy(t, person)
		for i := range 3 {
			_, err := f.svc.CreateCampaign(ctx(), person, CreateCampaignInput{Name: fmt.Sprint("c", i), StrategyID: sid})
			require.NoError(t, err)
		}
		c, err := f.svc.CreateCampaign(ctx(), person, CreateCampaignInput{Name: "elsewhere", StrategyID: other})
		require.NoError(t, err)
		d, err := f.svc.GetStrategy(ctx(), person, GetStrategyInput{ID: sid})
		require.NoError(t, err)
		assert.Len(t, d.Campaigns, 3)
		assert.False(t, d.Truncated)

		moved, err := f.svc.UpdateCampaign(ctx(), person, UpdateCampaignInput{ID: c.ID, ExpectedRevision: c.Revision, StrategyID: &sid})
		require.NoError(t, err)
		assert.Equal(t, sid, moved.StrategyID)
		list, err := f.svc.ListCampaigns(ctx(), person, ListCampaignsInput{StrategyID: &sid})
		require.NoError(t, err)
		assert.Len(t, list.Campaigns, 4)

		var limit int
		st := &MockStore{
			GetStrategyFunc: func(_ context.Context, org, id string) (*store.GtmStrategy, error) {
				return &store.GtmStrategy{ID: id, OrgID: org}, nil
			},
			ListCampaignsFunc: func(_ context.Context, f CampaignFilter) ([]*store.GtmCampaign, error) {
				limit = f.Limit
				rows := make([]*store.GtmCampaign, f.Limit)
				for i := range rows {
					rows[i] = &store.GtmCampaign{ID: fmt.Sprint(i)}
				}
				return rows, nil
			},
		}
		big, err := New(Deps{Store: st, Gate: allowGate(orgA)}).GetStrategy(ctx(), person, GetStrategyInput{ID: "s"})
		require.NoError(t, err)
		assert.Equal(t, MaxStrategyCampaigns+1, limit)
		assert.Len(t, big.Campaigns, 200)
		assert.True(t, big.Truncated)
	})
	t.Run("a campaign needs a strategy of its own org", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.CreateCampaign(ctx(), person, CreateCampaignInput{Name: "c"})
		require.ErrorIs(t, err, ErrInvalidArgument)
		otherOrg := f.strategy(t, Actor{UserID: "u-b", OrgID: orgB, Kind: ActorPerson})
		_, err = f.svc.CreateCampaign(ctx(), person, CreateCampaignInput{Name: "c", StrategyID: otherOrg})
		var link *LinkError
		require.ErrorAs(t, err, &link)
		assert.Equal(t, "strategy_id", link.Field)
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
		c, err := f.svc.CreateCampaign(ctx(), person, CreateCampaignInput{Name: "c", StrategyID: f.strategy(t, person)})
		require.NoError(t, err)
		_, err = f.svc.UpdateCampaign(ctx(), person, UpdateCampaignInput{ID: c.ID, ExpectedRevision: c.Revision, StrategyID: &otherOrg})
		require.ErrorAs(t, err, &link)
		_, err = f.svc.UpdateCampaign(ctx(), person, UpdateCampaignInput{ID: c.ID, ExpectedRevision: c.Revision, StrategyID: ptr("")})
		require.ErrorIs(t, err, ErrInvalidArgument)
	})
}

func TestChannels(t *testing.T) {
	t.Run("the defaults are seeded once, on the first read", func(t *testing.T) {
		f := newFixture(t)
		res, err := f.svc.ListChannels(ctx(), person, ListChannelsInput{})
		require.NoError(t, err)
		require.Len(t, res.Channels, len(DefaultChannels))
		keys := []string{}
		for _, ch := range res.Channels {
			keys = append(keys, ch.Key)
			assert.Equal(t, ChannelSourceSeed, ch.Source)
			assert.Empty(t, ch.CreatedBy)
			assert.Equal(t, ChannelActive, ch.Status)
		}
		assert.Equal(t, []string{"blog", "email", "hacker-news", "linkedin", "reddit", "x", "youtube"}, keys)
		for _, d := range DefaultChannels {
			assert.Equal(t, d.ChannelTypes, f.channel(t, d.Key).ChannelTypes, d.Key)
		}
		assert.Equal(t, []string{"forum", "community"}, f.channel(t, "reddit").ChannelTypes)
		email := f.channel(t, "email")
		assert.Equal(t, ChannelIntegrated, email.Mode)
		assert.Equal(t, GmailIntegration, email.IntegrationKind)
		assert.Equal(t, ConnectionConnected, email.ConnectionState)
		blog := f.channel(t, "blog")
		assert.Equal(t, ChannelManual, blog.Mode)
		assert.Empty(t, blog.ConnectionState, "a manual channel has nothing to connect")

		_, err = f.svc.GetChannel(ctx(), person, GetChannelInput{ID: blog.ID})
		require.NoError(t, err)
		assert.Equal(t, 1, f.store.r2().seeds, "seeded once")
		other, err := f.svc.ListChannels(ctx(), Actor{UserID: "u-b", OrgID: orgB, Kind: ActorPerson}, ListChannelsInput{})
		require.NoError(t, err)
		assert.Len(t, other.Channels, len(DefaultChannels), "every org gets its own")
		manual, err := f.svc.ListChannels(ctx(), person, ListChannelsInput{Mode: ChannelManual})
		require.NoError(t, err)
		assert.Len(t, manual.Channels, 6)
	})
	t.Run("connection state is display only: an error or no reader is UNKNOWN", func(t *testing.T) {
		f := newFixture(t)
		f.conns.StateFunc = func(context.Context, Actor, string, string) (ConnectionState, error) { return "", errors.New("down") }
		assert.Equal(t, ConnectionUnknown, f.channel(t, "email").ConnectionState)
		svc := New(Deps{Store: f.store, Gate: allowGate(orgA)})
		res, err := svc.GetChannel(ctx(), person, GetChannelInput{ID: f.channel(t, "email").ID})
		require.NoError(t, err)
		assert.Equal(t, ConnectionUnknown, res.ConnectionState)
	})
	t.Run("an agent adds and edits manual channels only", func(t *testing.T) {
		f := newFixture(t)
		ch, err := f.svc.CreateChannel(ctx(), agent, CreateChannelInput{Key: "dev-to", Name: "dev.to", Mode: ChannelManual, ManualInstructionsMD: "Post it"})
		require.NoError(t, err)
		assert.Equal(t, ChannelSourceAgent, ch.Source)
		assert.Equal(t, agent.UserID, ch.CreatedBy)
		_, err = f.svc.CreateChannel(ctx(), agent, CreateChannelInput{Key: "mail2", Name: "Mail", Mode: ChannelIntegrated, IntegrationKind: "gmail"})
		require.ErrorIs(t, err, ErrPersonOnly)
		ch, err = f.svc.UpdateChannel(ctx(), agent, UpdateChannelInput{ID: ch.ID, ExpectedRevision: ch.Revision, Key: ptr("devto"), Name: ptr("DEV"), Status: ptr(ChannelPaused), ChannelTypes: &[]string{"forum", "blog"}})
		require.NoError(t, err)
		assert.Equal(t, "devto", ch.Key)
		assert.Equal(t, []string{"forum", "blog"}, ch.ChannelTypes)
		ch, err = f.svc.UpdateChannel(ctx(), agent, UpdateChannelInput{ID: ch.ID, ExpectedRevision: ch.Revision, ChannelTypes: &[]string{}})
		require.NoError(t, err)
		assert.Empty(t, ch.ChannelTypes, "an empty list clears them")
		for name, in := range map[string]UpdateChannelInput{
			"mode":       {Mode: ptr(ChannelIntegrated), IntegrationKind: ptr("gmail")},
			"kind":       {IntegrationKind: ptr("gmail")},
			"connection": {ConnectionID: ptr("conn-1")},
		} {
			in.ID, in.ExpectedRevision = ch.ID, ch.Revision
			_, err = f.svc.UpdateChannel(ctx(), agent, in)
			require.ErrorIs(t, err, ErrPersonOnly, name)
		}
		email := f.channel(t, "email")
		_, err = f.svc.UpdateChannel(ctx(), agent, UpdateChannelInput{ID: email.ID, ExpectedRevision: email.Revision, Name: ptr("Mail")})
		require.ErrorIs(t, err, ErrPersonOnly, "an integrated channel is a person's")
		got, err := f.svc.UpdateChannel(ctx(), person, UpdateChannelInput{ID: email.ID, ExpectedRevision: email.Revision, ConnectionID: ptr("conn-1")})
		require.NoError(t, err)
		assert.Equal(t, "conn-1", got.ConnectionID)
		got, err = f.svc.UpdateChannel(ctx(), person, UpdateChannelInput{ID: email.ID, ExpectedRevision: got.Revision, ConnectionID: ptr("")})
		require.NoError(t, err)
		assert.Empty(t, got.ConnectionID, "empty clears it")
	})
	t.Run("keys are slugs, once per org; modes keep their integration consistent", func(t *testing.T) {
		f := newFixture(t)
		f.channel(t, "blog")
		_, err := f.svc.CreateChannel(ctx(), person, CreateChannelInput{Key: "blog", Name: "Blog 2", Mode: ChannelManual})
		require.ErrorIs(t, err, ErrChannelExists)
		assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
		x := f.channel(t, "x")
		_, err = f.svc.UpdateChannel(ctx(), person, UpdateChannelInput{ID: x.ID, ExpectedRevision: x.Revision, Key: ptr("reddit")})
		require.ErrorIs(t, err, ErrChannelExists)
		for name, in := range map[string]CreateChannelInput{
			"bad key":            {Key: "Hacker News", Name: "n", Mode: ChannelManual},
			"long key":           {Key: strings.Repeat("a", 33), Name: "n", Mode: ChannelManual},
			"no mode":            {Key: "k", Name: "n"},
			"manual integration": {Key: "k", Name: "n", Mode: ChannelManual, IntegrationKind: "gmail"},
			"manual connection":  {Key: "k", Name: "n", Mode: ChannelManual, ConnectionID: "c"},
			"integrated no kind": {Key: "k", Name: "n", Mode: ChannelIntegrated},
			"long name":          {Key: "k", Name: strings.Repeat("n", 101), Mode: ChannelManual},
			"bad type":           {Key: "k", Name: "n", Mode: ChannelManual, ChannelTypes: []string{"Social Media"}},
			"empty type":         {Key: "k", Name: "n", Mode: ChannelManual, ChannelTypes: []string{""}},
			"type twice":         {Key: "k", Name: "n", Mode: ChannelManual, ChannelTypes: []string{"forum", "forum"}},
			"33 types": {Key: "k", Name: "n", Mode: ChannelManual, ChannelTypes: func() []string {
				ts := make([]string, 33)
				for i := range ts {
					ts[i] = fmt.Sprint("t", i)
				}
				return ts
			}()},
		} {
			_, err := f.svc.CreateChannel(ctx(), person, in)
			require.ErrorIs(t, err, ErrInvalidArgument, name)
		}
		blog := f.channel(t, "blog")
		_, err = f.svc.UpdateChannel(ctx(), person, UpdateChannelInput{ID: blog.ID, ExpectedRevision: blog.Revision, Mode: ptr(ChannelIntegrated)})
		require.ErrorIs(t, err, ErrInvalidArgument, "INTEGRATED needs its integration_kind")
	})
}

func TestItemsOnChannels(t *testing.T) {
	t.Run("an item's channel is one of its org's", func(t *testing.T) {
		f := newFixture(t)
		b := Actor{UserID: "u-b", OrgID: orgB, Kind: ActorPerson}
		res, err := f.svc.ListChannels(ctx(), b, ListChannelsInput{})
		require.NoError(t, err)
		_, err = f.svc.CreateItem(ctx(), person, CreateItemInput{Format: FormatShortPost, Title: "t", ChannelID: res.Channels[0].ID})
		var link *LinkError
		require.ErrorAs(t, err, &link)
		assert.Equal(t, "channel_id", link.Field)
		reddit := f.channel(t, "reddit")
		it, err := f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatShortPost, Title: "t", ChannelID: reddit.ID})
		require.NoError(t, err)
		_, err = f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatVideo, Title: "v"})
		require.NoError(t, err)
		list, err := f.svc.ListItems(ctx(), person, ListItemsInput{ChannelID: &reddit.ID})
		require.NoError(t, err)
		require.Len(t, list.Items, 1)
		assert.Equal(t, it.ID, list.Items[0].ID)
		list, err = f.svc.ListItems(ctx(), person, ListItemsInput{Format: FormatVideo})
		require.NoError(t, err)
		require.Len(t, list.Items, 1)
		none := ""
		list, err = f.svc.ListItems(ctx(), person, ListItemsInput{ChannelID: &none})
		require.NoError(t, err)
		assert.Len(t, list.Items, 1, "items with no channel")
	})
	t.Run("PUBLISHED on a manual channel needs the URL, which only a person records", func(t *testing.T) {
		f := newFixture(t)
		reddit := f.channel(t, "reddit")
		it, err := f.svc.CreateItem(ctx(), person, CreateItemInput{Format: FormatShortPost, Title: "t", ChannelID: reddit.ID})
		require.NoError(t, err)
		it, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemApproved)})
		require.NoError(t, err)
		_, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemPublished)})
		var te *TransitionError
		require.ErrorAs(t, err, &te)
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		_, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, PublishedURL: ptr("ftp://x")})
		require.ErrorIs(t, err, ErrInvalidArgument)

		got, err := f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, PublishedURL: ptr("https://reddit.com/r/x/1")})
		require.NoError(t, err)
		assert.Equal(t, ItemApproved, got.Status, "recording a URL is not a content edit")
		got, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: got.Revision, Status: ptr(ItemPublished)})
		require.NoError(t, err)
		assert.Equal(t, ItemPublished, got.Status)
		_, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: got.Revision, PublishedURL: ptr("")})
		require.ErrorAs(t, err, &te, "a published manual item keeps its URL")

		// In one call, as a person who posted by hand does it.
		it2, err := f.svc.CreateItem(ctx(), person, CreateItemInput{Format: FormatShortPost, Title: "t2", ChannelID: reddit.ID})
		require.NoError(t, err)
		it2, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it2.ID, ExpectedRevision: it2.Revision, Status: ptr(ItemApproved)})
		require.NoError(t, err)
		got, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it2.ID, ExpectedRevision: it2.Revision, Status: ptr(ItemPublished), PublishedURL: ptr("https://reddit.com/r/x/2")})
		require.NoError(t, err)
		assert.Equal(t, "https://reddit.com/r/x/2", got.PublishedURL)

		draft, err := f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: FormatShortPost, Title: "d"})
		require.NoError(t, err)
		_, err = f.svc.UpdateItem(ctx(), agent, UpdateItemInput{ID: draft.ID, ExpectedRevision: draft.Revision, PublishedURL: ptr("https://x.com/1")})
		require.ErrorIs(t, err, ErrPersonOnly)
	})
	t.Run("an email-type item sends only on an active, integrated gmail channel", func(t *testing.T) {
		f := newFixture(t)
		email, blog := f.channel(t, "email"), f.channel(t, "blog")
		mk := func(format ItemFormat, channelID string) Item {
			it, err := f.svc.CreateItem(ctx(), person, CreateItemInput{Format: format, Title: "m", BodyMD: "b", EmailTo: []string{"a@example.com"}, EmailSubject: "s", ChannelID: channelID})
			require.NoError(t, err)
			it, err = f.svc.UpdateItem(ctx(), person, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemApproved)})
			require.NoError(t, err)
			return it
		}
		bad := mk(FormatEmail, blog.ID)
		_, err := f.svc.SendItemEmail(ctx(), agent, f.send(bad))
		require.ErrorIs(t, err, ErrChannelNotSendable)
		got, err := f.svc.SendItemEmail(ctx(), agent, f.send(mk(FormatColdOutreach, email.ID)))
		require.NoError(t, err, "cold outreach is email-type")
		assert.Equal(t, ItemSent, got.Status)

		paused := mk(FormatEmail, email.ID)
		_, err = f.svc.UpdateChannel(ctx(), person, UpdateChannelInput{ID: email.ID, ExpectedRevision: email.Revision, Status: ptr(ChannelPaused)})
		require.NoError(t, err)
		_, err = f.svc.SendItemEmail(ctx(), agent, f.send(paused))
		require.ErrorIs(t, err, ErrChannelNotSendable)
		assert.Equal(t, 1, f.email.CallCount("Send"))
	})
}

func TestCampaignPlanAndCoverage(t *testing.T) {
	f := newFixture(t)
	reddit, x := f.channel(t, "reddit"), f.channel(t, "x")
	sid := f.strategy(t, person)
	bChannels, err := f.svc.ListChannels(ctx(), Actor{UserID: "u-b", OrgID: orgB, Kind: ActorPerson}, ListChannelsInput{})
	require.NoError(t, err)
	for name, in := range map[string]CreateCampaignInput{
		"another org's channel": {PlannedChannelIDs: []string{bChannels.Channels[0].ID}},
		"a channel twice":       {PlannedChannelIDs: []string{reddit.ID, reddit.ID}},
		"a format twice":        {PlannedFormats: []ItemFormat{FormatAd, FormatAd}},
		"an unknown format":     {PlannedFormats: []ItemFormat{"GTM_ITEM_FORMAT_BILLBOARD"}},
	} {
		in.Name, in.StrategyID = "c", sid
		_, err := f.svc.CreateCampaign(ctx(), person, in)
		require.Error(t, err, name)
	}
	c, err := f.svc.CreateCampaign(ctx(), person, CreateCampaignInput{
		Name: "Launch", StrategyID: sid,
		PlannedChannelIDs: []string{reddit.ID, x.ID}, PlannedFormats: []ItemFormat{FormatShortPost},
	})
	require.NoError(t, err)
	mk := func(format ItemFormat, channelID string, status ItemStatus) {
		_, err := f.svc.CreateItem(ctx(), agent, CreateItemInput{Format: format, Title: "t", ChannelID: channelID, CampaignID: c.ID, Status: status})
		require.NoError(t, err)
	}
	mk(FormatShortPost, reddit.ID, ItemDraft)
	mk(FormatShortPost, reddit.ID, ItemReview)
	mk(FormatVideo, x.ID, ItemDraft)
	mk(FormatLongPost, "", ItemDraft)

	d, err := f.svc.GetCampaign(ctx(), person, GetCampaignInput{ID: c.ID})
	require.NoError(t, err)
	require.Len(t, d.Coverage, 4)
	assert.Equal(t, CoverageRow{ChannelID: reddit.ID, Format: FormatShortPost, Planned: true, Total: 2,
		Counts: []StatusCount{{ItemDraft, 1}, {ItemReview, 1}}}, d.Coverage[0])
	assert.Equal(t, CoverageRow{ChannelID: x.ID, Format: FormatShortPost, Planned: true}, d.Coverage[1], "planned, nothing drafted")
	assert.False(t, d.Coverage[2].Planned)
	assert.False(t, d.Coverage[3].Planned)
	unplanned := map[string]ItemFormat{d.Coverage[2].ChannelID: d.Coverage[2].Format, d.Coverage[3].ChannelID: d.Coverage[3].Format}
	assert.Equal(t, map[string]ItemFormat{"": FormatLongPost, x.ID: FormatVideo}, unplanned)

	t.Run("a one-sided plan leaves the other dimension unset", func(t *testing.T) {
		rows := coverage([]string{"ch-1"}, nil, []ItemCount{
			{ChannelID: "ch-1", Format: FormatAd, Status: ItemDraft, Count: 2},
			{ChannelID: "ch-2", Format: FormatAd, Status: ItemDraft, Count: 1},
		})
		require.Len(t, rows, 2)
		assert.Equal(t, CoverageRow{ChannelID: "ch-1", Planned: true, Total: 2, Counts: []StatusCount{{ItemDraft, 2}}}, rows[0])
		assert.Equal(t, CoverageRow{ChannelID: "ch-2", Format: FormatAd, Total: 1, Counts: []StatusCount{{ItemDraft, 1}}}, rows[1])
		rows = coverage(nil, []ItemFormat{FormatAd}, []ItemCount{{ChannelID: "ch-9", Format: FormatAd, Status: ItemApproved, Count: 1}})
		assert.Equal(t, []CoverageRow{{Format: FormatAd, Planned: true, Total: 1, Counts: []StatusCount{{ItemApproved, 1}}}}, rows)
	})

	upd, err := f.svc.UpdateCampaign(ctx(), person, UpdateCampaignInput{ID: c.ID, ExpectedRevision: c.Revision, PlannedFormats: &[]ItemFormat{}})
	require.NoError(t, err)
	assert.Empty(t, upd.PlannedFormats)
	assert.Equal(t, []string{reddit.ID, x.ID}, upd.PlannedChannelIDs)
}

func libEntry(key string, cat StrategyCategory) LibraryEntry {
	return LibraryEntry{
		Key: key, Category: cat, Depth: DepthPlay, Name: "Play " + key, SummaryMD: "A placeholder play about " + key,
		Stage: "launch", Cost: "low", Difficulty: "easy", NorthStar: "More signups", Objective: "Reach builders",
		KPIs: []string{"signups", "activation"}, Fits: []string{"devtools"}, ChannelTypes: []string{"reddit", "blog"},
		Hours: 4, Steps: []LibraryStep{{Who: "agent", Title: "Draft the post"}}, Extra: map[string]any{"note": "placeholder"},
	}
}

func TestStrategyLibrary(t *testing.T) {
	t.Run("import is a person's, idempotent, counted, and prunes by all_keys", func(t *testing.T) {
		f := newFixture(t)
		in := ImportStrategyLibraryInput{SourceRevision: "abc123", Entries: []LibraryEntry{libEntry("alpha", CategoryLaunchesPR), libEntry("beta", CategoryPaidAcquisition)}}
		_, err := f.svc.ImportStrategyLibrary(ctx(), agent, in)
		require.ErrorIs(t, err, ErrPersonOnly)
		res, err := f.svc.ImportStrategyLibrary(ctx(), person, in)
		require.NoError(t, err)
		assert.Equal(t, ImportStrategyLibraryResult{Created: 2}, res)
		res, err = f.svc.ImportStrategyLibrary(ctx(), person, in)
		require.NoError(t, err)
		assert.Equal(t, ImportStrategyLibraryResult{Unchanged: 2}, res)
		in.Entries[1].SummaryMD = "changed"
		in.SourceRevision = "def456"
		res, err = f.svc.ImportStrategyLibrary(ctx(), person, in)
		require.NoError(t, err)
		assert.Equal(t, ImportStrategyLibraryResult{Unchanged: 1, Updated: 1}, res)
		got, err := f.svc.GetStrategyLibraryEntry(ctx(), agent, GetStrategyLibraryEntryInput{Key: "beta"})
		require.NoError(t, err)
		assert.Equal(t, "def456", got.SourceRevision)
		assert.Equal(t, person.UserID, got.ImportedBy)
		assert.Equal(t, []LibraryStep{{Who: "agent", Title: "Draft the post"}}, got.Steps)
		assert.Equal(t, map[string]any{"note": "placeholder"}, got.Extra)

		prune := ImportStrategyLibraryInput{SourceRevision: "g", Entries: []LibraryEntry{libEntry("alpha", CategoryLaunchesPR)}, Prune: true}
		_, err = f.svc.ImportStrategyLibrary(ctx(), person, prune)
		require.ErrorIs(t, err, ErrInvalidArgument, "prune needs all_keys")
		prune.AllKeys = []string{"gamma"}
		_, err = f.svc.ImportStrategyLibrary(ctx(), person, prune)
		require.ErrorIs(t, err, ErrInvalidArgument, "every entry's key is in all_keys")
		prune.AllKeys = []string{"alpha", "gamma"}
		res, err = f.svc.ImportStrategyLibrary(ctx(), person, prune)
		require.NoError(t, err)
		assert.Equal(t, 1, res.Pruned, "beta goes")
		_, err = f.svc.GetStrategyLibraryEntry(ctx(), person, GetStrategyLibraryEntryInput{Key: "beta"})
		require.ErrorIs(t, err, ErrNotFound)
	})
	t.Run("every entry validates itself", func(t *testing.T) {
		f := newFixture(t)
		big := make([]LibraryEntry, MaxLibraryImport+1)
		for i := range big {
			big[i] = libEntry(fmt.Sprintf("k%d", i), CategoryLaunchesPR)
		}
		for name, in := range map[string]ImportStrategyLibraryInput{
			"empty":         {SourceRevision: "r"},
			"too many":      {SourceRevision: "r", Entries: big},
			"no revision":   {Entries: []LibraryEntry{libEntry("a", CategoryLaunchesPR)}},
			"duplicate key": {SourceRevision: "r", Entries: []LibraryEntry{libEntry("a", CategoryLaunchesPR), libEntry("a", CategoryLaunchesPR)}},
			"bad key":       {SourceRevision: "r", Entries: []LibraryEntry{libEntry("Not A Slug", CategoryLaunchesPR)}},
			"no category":   {SourceRevision: "r", Entries: []LibraryEntry{libEntry("a", "")}},
			"bad all_keys":  {SourceRevision: "r", Entries: []LibraryEntry{libEntry("a", CategoryLaunchesPR)}, AllKeys: []string{"a", "a"}},
			"negative hours": {SourceRevision: "r", Entries: []LibraryEntry{func() LibraryEntry {
				e := libEntry("a", CategoryLaunchesPR)
				e.Hours = -1
				return e
			}()}},
			"blank kpi": {SourceRevision: "r", Entries: []LibraryEntry{func() LibraryEntry {
				e := libEntry("a", CategoryLaunchesPR)
				e.KPIs = []string{" "}
				return e
			}()}},
		} {
			_, err := f.svc.ImportStrategyLibrary(ctx(), person, in)
			require.ErrorIs(t, err, ErrInvalidArgument, name)
		}
		keys := make([]string, MaxLibraryKeys+1)
		for i := range keys {
			keys[i] = fmt.Sprintf("k%d", i)
		}
		_, err := f.svc.ImportStrategyLibrary(ctx(), person, ImportStrategyLibraryInput{SourceRevision: "r", Entries: []LibraryEntry{libEntry("k1", CategoryLaunchesPR)}, Prune: true, AllKeys: keys})
		require.ErrorIs(t, err, ErrInvalidArgument, "all_keys is capped")
	})
	t.Run("list filters and searches", func(t *testing.T) {
		f := newFixture(t)
		a, b := libEntry("alpha", CategoryLaunchesPR), libEntry("beta", CategoryPaidAcquisition)
		b.Fits, b.Name = []string{"b2c"}, "Paid ads"
		_, err := f.svc.ImportStrategyLibrary(ctx(), person, ImportStrategyLibraryInput{SourceRevision: "r", Entries: []LibraryEntry{a, b}})
		require.NoError(t, err)
		for name, tc := range map[string]struct {
			in   ListStrategyLibraryInput
			want []string
		}{
			"all":      {ListStrategyLibraryInput{}, []string{"alpha", "beta"}},
			"category": {ListStrategyLibraryInput{Category: CategoryPaidAcquisition}, []string{"beta"}},
			"fit":      {ListStrategyLibraryInput{Fit: "devtools"}, []string{"alpha"}},
			"query":    {ListStrategyLibraryInput{Query: "PAID"}, []string{"beta"}},
			"depth":    {ListStrategyLibraryInput{Depth: DepthTactic}, nil},
		} {
			res, err := f.svc.ListStrategyLibrary(ctx(), agent, tc.in)
			require.NoError(t, err, name)
			var got []string
			for _, e := range res.Entries {
				got = append(got, e.Key)
			}
			assert.Equal(t, tc.want, got, name)
		}
		_, err = f.svc.ListStrategyLibrary(ctx(), agent, ListStrategyLibraryInput{Query: strings.Repeat("q", 201)})
		require.ErrorIs(t, err, ErrInvalidArgument)
		other, err := f.svc.ListStrategyLibrary(ctx(), Actor{UserID: "u-b", OrgID: orgB, Kind: ActorAgent}, ListStrategyLibraryInput{})
		require.NoError(t, err)
		assert.Empty(t, other.Entries, "each org's library is its own")
	})
	t.Run("use makes a DRAFT strategy and a first campaign planning the matching channels", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.ImportStrategyLibrary(ctx(), person, ImportStrategyLibraryInput{SourceRevision: "r", Entries: []LibraryEntry{libEntry("alpha", CategoryLaunchesPR)}})
		require.NoError(t, err)
		res, err := f.svc.UseLibraryStrategy(ctx(), agent, UseLibraryStrategyInput{LibraryKey: "alpha", CreateFirstCampaign: true})
		require.NoError(t, err)
		st := res.Strategy
		assert.Equal(t, StrategyDraft, st.Status)
		assert.Equal(t, "Play alpha", st.Name)
		assert.Equal(t, "More signups\n\nReach builders", st.GoalMD)
		assert.Equal(t, "signups, activation", st.TargetMetric)
		assert.Equal(t, CategoryLaunchesPR, st.Category)
		assert.Equal(t, "alpha", st.LibraryKey)
		require.NotNil(t, res.Campaign)
		assert.Equal(t, CampaignDraft, res.Campaign.Status)
		assert.Equal(t, st.ID, res.Campaign.StrategyID)
		assert.Equal(t, "Play alpha", res.Campaign.Name)
		assert.Equal(t, []string{f.channel(t, "reddit").ID, f.channel(t, "blog").ID}, res.Campaign.PlannedChannelIDs, "by key, in channel_types order")

		bare, err := f.svc.UseLibraryStrategy(ctx(), person, UseLibraryStrategyInput{LibraryKey: "alpha"})
		require.NoError(t, err)
		assert.Nil(t, bare.Campaign)
		named, err := f.svc.UseLibraryStrategy(ctx(), person, UseLibraryStrategyInput{LibraryKey: "alpha", CreateFirstCampaign: true, CampaignName: ptr("Q1 push")})
		require.NoError(t, err)
		assert.Equal(t, "Q1 push", named.Campaign.Name)
		_, err = f.svc.UseLibraryStrategy(ctx(), person, UseLibraryStrategyInput{LibraryKey: "alpha", CampaignName: ptr("x")})
		require.ErrorIs(t, err, ErrInvalidArgument, "a campaign name needs create_first_campaign")
		_, err = f.svc.UseLibraryStrategy(ctx(), person, UseLibraryStrategyInput{LibraryKey: "nope"})
		require.ErrorIs(t, err, ErrNotFound)
	})
	t.Run("plannedChannels matches by key or by a shared channel type", func(t *testing.T) {
		chs := []*store.GtmChannel{{ID: "1", Key: "reddit", ChannelTypes: []string{"forum"}}, {ID: "2", Key: "hacker-news", ChannelTypes: []string{"forum"}}, {ID: "3", Key: "blog"}}
		assert.Equal(t, []string{"3", "1"}, plannedChannels([]string{"blog", "reddit", "blog"}, chs))
		assert.Equal(t, []string{"1", "2"}, plannedChannels([]string{"forum"}, chs))
		assert.Empty(t, plannedChannels([]string{"podcast"}, chs))
	})
	t.Run("use plans the org's ACTIVE channels whose types fit", func(t *testing.T) {
		f := newFixture(t)
		e := libEntry("forums", CategoryCommunityEvents)
		e.ChannelTypes = []string{"forum", "video"}
		_, err := f.svc.ImportStrategyLibrary(ctx(), person, ImportStrategyLibraryInput{SourceRevision: "r", Entries: []LibraryEntry{e}})
		require.NoError(t, err)
		hn := f.channel(t, "hacker-news")
		_, err = f.svc.UpdateChannel(ctx(), person, UpdateChannelInput{ID: hn.ID, ExpectedRevision: hn.Revision, Status: ptr(ChannelArchived)})
		require.NoError(t, err)
		res, err := f.svc.UseLibraryStrategy(ctx(), agent, UseLibraryStrategyInput{LibraryKey: "forums", CreateFirstCampaign: true})
		require.NoError(t, err)
		assert.Equal(t, []string{f.channel(t, "reddit").ID, f.channel(t, "youtube").ID}, res.Campaign.PlannedChannelIDs,
			"reddit is a forum, youtube a video; the archived hacker-news is left out")
	})
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestAssets(t *testing.T) {
	upload := func(t *testing.T, f *fixture, itemID string, data []byte, ct string) AssetUpload {
		t.Helper()
		up, err := f.svc.CreateAssetUpload(ctx(), person, CreateAssetUploadInput{ItemID: itemID, Filename: "shot.png", ContentType: ct, SizeBytes: int64(len(data))})
		require.NoError(t, err)
		f.blobs.objects[up.Asset.ObjectKey] = data
		return up
	}
	t.Run("upload, confirm, download", func(t *testing.T) {
		f := newFixture(t)
		it := f.itemAt(t, FormatShortPost, ItemDraft)
		up, err := f.svc.CreateAssetUpload(ctx(), person, CreateAssetUploadInput{ItemID: it.ID, Filename: "../../etc/shot.png", ContentType: "image/png; charset=binary", SizeBytes: int64(len(pngBytes))})
		require.NoError(t, err)
		assert.Equal(t, AssetPending, up.Asset.Status)
		assert.Equal(t, "gtm/"+orgA+"/"+up.Asset.ID, up.Asset.ObjectKey)
		assert.Equal(t, "shot.png", up.Asset.Filename)
		assert.Equal(t, AssetSourcePerson, up.Asset.Source)
		assert.Equal(t, "image/png", up.RequiredHeaders["Content-Type"])
		assert.Equal(t, []string{up.Asset.ObjectKey}, f.blobs.puts)

		_, err = f.svc.ConfirmAssetUpload(ctx(), person, ConfirmAssetUploadInput{AssetID: up.Asset.ID})
		require.ErrorIs(t, err, ErrAssetNotUploaded, "not PUT yet: it stays PENDING")
		_, err = f.svc.GetAssetDownload(ctx(), person, GetAssetDownloadInput{AssetID: up.Asset.ID})
		require.ErrorIs(t, err, ErrAssetNotReady)

		f.blobs.objects[up.Asset.ObjectKey] = pngBytes
		a, err := f.svc.ConfirmAssetUpload(ctx(), person, ConfirmAssetUploadInput{AssetID: up.Asset.ID})
		require.NoError(t, err)
		assert.Equal(t, AssetReady, a.Status)
		assert.Equal(t, sum(pngBytes), a.SHA256)
		assert.Equal(t, "image/png", a.ContentType)
		_, err = f.svc.ConfirmAssetUpload(ctx(), person, ConfirmAssetUploadInput{AssetID: up.Asset.ID})
		require.ErrorIs(t, err, ErrAssetNotPending)

		dl, err := f.svc.GetAssetDownload(ctx(), agent, GetAssetDownloadInput{AssetID: a.ID})
		require.NoError(t, err)
		assert.Contains(t, dl.URL, a.ObjectKey)
		assert.Equal(t, GetOptions{Inline: true, ContentType: "image/png", Filename: "shot.png"}, f.blobs.gets[0])

		pdf := upload(t, f, it.ID, pdfBytes, "application/pdf")
		_, err = f.svc.ConfirmAssetUpload(ctx(), person, ConfirmAssetUploadInput{AssetID: pdf.Asset.ID})
		require.NoError(t, err)
		_, err = f.svc.GetAssetDownload(ctx(), person, GetAssetDownloadInput{AssetID: pdf.Asset.ID})
		require.NoError(t, err)
		assert.False(t, f.blobs.gets[1].Inline, "a pdf downloads as an attachment")

		list, err := f.svc.ListAssets(ctx(), person, ListAssetsInput{ItemID: it.ID})
		require.NoError(t, err)
		assert.Len(t, list.Assets, 2)
		_, err = f.svc.ListAssets(ctx(), person, ListAssetsInput{ItemID: it.ID, CampaignID: "c"})
		require.ErrorIs(t, err, ErrInvalidArgument, "exactly one target")
		_, err = f.svc.ListAssets(ctx(), person, ListAssetsInput{})
		require.ErrorIs(t, err, ErrInvalidArgument)
	})
	t.Run("a check that fails deletes the object and the row", func(t *testing.T) {
		for name, tc := range map[string]struct {
			declared string
			data     []byte
			size     int
		}{
			"size": {"image/png", pngBytes, len(pngBytes) + 1},
			"svg":  {"text/plain", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0},
			"xml":  {"text/plain", []byte(`<?xml version="1.0"?><svg/>`), 0},
			"html": {"text/plain", []byte(`<!DOCTYPE html><html><script>x</script></html>`), 0},
			"zip":  {"application/pdf", []byte("PK\x03\x04 not a pdf at all"), 0},
		} {
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				it := f.itemAt(t, FormatShortPost, ItemDraft)
				size := tc.size
				if size == 0 {
					size = len(tc.data)
				}
				up, err := f.svc.CreateAssetUpload(ctx(), person, CreateAssetUploadInput{ItemID: it.ID, Filename: "f", ContentType: tc.declared, SizeBytes: int64(size)})
				require.NoError(t, err)
				f.blobs.objects[up.Asset.ObjectKey] = tc.data
				_, err = f.svc.ConfirmAssetUpload(ctx(), person, ConfirmAssetUploadInput{AssetID: up.Asset.ID})
				var rej *AssetRejectedError
				require.ErrorAs(t, err, &rej)
				assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
				assert.Contains(t, f.blobs.deletes, up.Asset.ObjectKey)
				_, gone := f.blobs.objects[up.Asset.ObjectKey]
				assert.False(t, gone)
				_, err = f.svc.GetAsset(ctx(), person, GetAssetInput{ID: up.Asset.ID})
				require.ErrorIs(t, err, ErrNotFound, "the row is gone")
			})
		}
		f := newFixture(t)
		it := f.itemAt(t, FormatShortPost, ItemDraft)
		plain := upload(t, f, it.ID, []byte("just some notes\nfor the post\n"), "text/plain")
		a, err := f.svc.ConfirmAssetUpload(ctx(), person, ConfirmAssetUploadInput{AssetID: plain.Asset.ID})
		require.NoError(t, err, "plain text is allowed")
		assert.Equal(t, "text/plain", a.ContentType)
		mislabelled := upload(t, f, it.ID, []byte("plain words, no png here"), "image/png")
		a, err = f.svc.ConfirmAssetUpload(ctx(), person, ConfirmAssetUploadInput{AssetID: mislabelled.Asset.ID})
		require.NoError(t, err)
		assert.Equal(t, "text/plain", a.ContentType, "the sniffed type wins over the claim")
	})
	t.Run("limits and the allowlist on the claim too", func(t *testing.T) {
		f := newFixture(t)
		it := f.itemAt(t, FormatShortPost, ItemDraft)
		_, err := f.svc.CreateAssetUpload(ctx(), person, CreateAssetUploadInput{ItemID: it.ID, Filename: "f", ContentType: "image/png", SizeBytes: MaxAssetBytes + 1})
		require.ErrorIs(t, err, ErrAssetTooLarge)
		assert.Equal(t, connect.CodeResourceExhausted, svcerr.Code(err))
		_, err = f.svc.CreateAssetUpload(ctx(), person, CreateAssetUploadInput{ItemID: it.ID, Filename: "f", ContentType: "image/svg+xml", SizeBytes: 10})
		require.ErrorIs(t, err, ErrInvalidArgument)
		_, err = f.svc.CreateAssetUpload(ctx(), person, CreateAssetUploadInput{ItemID: it.ID, Filename: "f", ContentType: "image/png"})
		require.ErrorIs(t, err, ErrInvalidArgument, "a size is required")
		_, err = f.svc.CreateAssetUpload(ctx(), person, CreateAssetUploadInput{ItemID: "nope", Filename: "f", ContentType: "image/png", SizeBytes: 10})
		require.ErrorIs(t, err, ErrLinkNotFound)

		// The quota counts READY and PENDING bytes together.
		used := MaxOrgAssetBytes - 10
		f.store.r2().assets["big"] = &store.GtmAsset{ID: "big", OrgID: orgA, SizeBytes: &used, Status: string(AssetPending)}
		_, err = f.svc.CreateAssetUpload(ctx(), person, CreateAssetUploadInput{ItemID: it.ID, Filename: "f", ContentType: "image/png", SizeBytes: 11})
		require.ErrorIs(t, err, ErrAssetQuota)
		_, err = f.svc.CreateAssetUpload(ctx(), person, CreateAssetUploadInput{ItemID: it.ID, Filename: "f", ContentType: "image/png", SizeBytes: 10})
		require.NoError(t, err, "exactly at the quota")
		_, err = f.svc.CreateAssetUpload(ctx(), Actor{UserID: "u-b", OrgID: orgB, Kind: ActorPerson}, CreateAssetUploadInput{
			CampaignID: "missing", Filename: "f", ContentType: "image/png", SizeBytes: 10})
		require.ErrorIs(t, err, ErrLinkNotFound, "another org's quota is its own, and its targets too")
	})
	t.Run("no asset store fails closed", func(t *testing.T) {
		f := newFixture(t)
		svc := New(Deps{Store: f.store, Gate: allowGate(orgA), Attachments: f.files})
		_, err := svc.CreateAssetUpload(ctx(), person, CreateAssetUploadInput{ItemID: "x", Filename: "f", ContentType: "image/png", SizeBytes: 1})
		require.ErrorIs(t, err, ErrAssetsUnavailable)
		_, err = svc.ConfirmAssetUpload(ctx(), person, ConfirmAssetUploadInput{AssetID: "x"})
		require.ErrorIs(t, err, ErrAssetsUnavailable)
		_, err = svc.GetAssetDownload(ctx(), person, GetAssetDownloadInput{AssetID: "x"})
		require.ErrorIs(t, err, ErrAssetsUnavailable)
		_, err = svc.AttachFromAttachment(ctx(), person, AttachFromAttachmentInput{AttachmentID: "a", ItemID: "x"})
		require.ErrorIs(t, err, ErrAssetsUnavailable)
		_, err = New(Deps{Store: f.store, Gate: allowGate(orgA), Assets: f.blobs}).AttachFromAttachment(ctx(), person, AttachFromAttachmentInput{AttachmentID: "a", ItemID: "x"})
		require.ErrorIs(t, err, ErrAttachmentsUnavailable)
	})
	t.Run("attach a chat attachment of the actor's own", func(t *testing.T) {
		f := newFixture(t)
		it := f.itemAt(t, FormatShortPost, ItemDraft)
		f.files.files["att-1"] = fakeFile{owner: agent.UserID, file: AttachmentFile{Filename: "chart.png", ContentType: "image/png", Size: int64(len(pngBytes))}, data: pngBytes}
		f.files.files["att-2"] = fakeFile{owner: "someone-else", file: AttachmentFile{Filename: "x.png"}, data: pngBytes}
		f.files.files["att-svg"] = fakeFile{owner: agent.UserID, file: AttachmentFile{Filename: "x.svg", Size: 20}, data: []byte("<svg onload=alert(1)>")}
		f.files.files["att-big"] = fakeFile{owner: agent.UserID, file: AttachmentFile{Filename: "x.mp4", Size: MaxCopiedAssetBytes + 1}}

		a, err := f.svc.AttachFromAttachment(ctx(), agent, AttachFromAttachmentInput{AttachmentID: "att-1", ItemID: it.ID})
		require.NoError(t, err)
		assert.Equal(t, AssetReady, a.Status)
		assert.Equal(t, AssetSourceChat, a.Source)
		assert.Equal(t, sum(pngBytes), a.SHA256)
		assert.Equal(t, pngBytes, f.blobs.objects[a.ObjectKey])

		_, err = f.svc.AttachFromAttachment(ctx(), agent, AttachFromAttachmentInput{AttachmentID: "att-2", ItemID: it.ID})
		require.ErrorIs(t, err, ErrNotFound, "another user's attachment does not exist")
		assert.Equal(t, "not found", err.Error())
		_, err = f.svc.AttachFromAttachment(ctx(), agent, AttachFromAttachmentInput{AttachmentID: "att-svg", ItemID: it.ID})
		require.ErrorIs(t, err, ErrAssetRejected)
		_, err = f.svc.AttachFromAttachment(ctx(), agent, AttachFromAttachmentInput{AttachmentID: "att-big", ItemID: it.ID})
		require.ErrorIs(t, err, ErrAssetTooLarge)

		f.blobs.failPut = errors.New("bucket 10.0.0.1 down")
		_, err = f.svc.AttachFromAttachment(ctx(), agent, AttachFromAttachmentInput{AttachmentID: "att-1", ItemID: it.ID})
		require.ErrorIs(t, err, ErrAssetStorageFailed)
		assert.NotContains(t, err.Error(), "10.0.0.1")
		list, err := f.svc.ListAssets(ctx(), person, ListAssetsInput{ItemID: it.ID})
		require.NoError(t, err)
		assert.Len(t, list.Assets, 1, "a failed write leaves no row")
	})
}

func TestAnalyticsErrorsAndHiddenGroups(t *testing.T) {
	t.Run("the domain's fixed-message errors pass through; others are hidden", func(t *testing.T) {
		for name, tc := range map[string]struct {
			err  error
			want error
		}{
			"invalid":     {&ValidationError{Field: "property", Reason: "an identifier property is not allowed"}, ErrInvalidArgument},
			"unavailable": {ErrAnalyticsUnavailable, ErrAnalyticsUnavailable},
			"failed":      {ErrAnalyticsFailed, ErrAnalyticsFailed},
			"other":       {errors.New("cp 10.0.0.9: boom"), ErrAnalyticsFailed},
		} {
			f := newFixture(t)
			f.analytics.QueryFunc = func(context.Context, string, AnalyticsQuery) (AnalyticsResult, error) {
				return AnalyticsResult{}, tc.err
			}
			_, err := f.svc.ReadProductAnalytics(ctx(), agent, AnalyticsQuery{Template: AnalyticsEventCounts})
			require.ErrorIs(t, err, tc.want, name)
			assert.NotContains(t, err.Error(), "10.0.0.9", name)
			if name == "invalid" {
				assert.Contains(t, err.Error(), "identifier property", "the caller learns what to fix")
			}
		}
	})
	t.Run("a funnel names each step once", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.ReadProductAnalytics(ctx(), agent, AnalyticsQuery{Template: AnalyticsFunnel, Events: []string{"a", "b", "a"}})
		require.ErrorIs(t, err, ErrInvalidArgument)
		assert.Zero(t, f.analytics.CallCount("Query"))
	})
	t.Run("the re-cap keeps HiddenGroups", func(t *testing.T) {
		f := newFixture(t)
		f.analytics.QueryFunc = func(_ context.Context, _ string, q AnalyticsQuery) (AnalyticsResult, error) {
			return AnalyticsResult{Rows: make([]AnalyticsRow, 20), HiddenGroups: 4}, nil
		}
		res, err := f.svc.ReadProductAnalytics(ctx(), agent, AnalyticsQuery{Template: AnalyticsPropertyBreakdown, Events: []string{"signup"}, Property: "plan", Limit: 5})
		require.NoError(t, err)
		assert.Len(t, res.Rows, 5)
		assert.True(t, res.Truncated)
		assert.Equal(t, 4, res.HiddenGroups)
	})
}
