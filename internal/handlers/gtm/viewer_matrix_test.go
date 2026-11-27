// Copyright (c) 2026 Reliant Labs

package gtm_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/featuregate"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/library"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The viewer matrix for GtmService: the real handler over the real domain
// service, its Postgres store and the static featuregate, with a membership
// the test controls. orgA (tenant zero) and orgB are allowlisted; orgC is
// not. alice is a member of orgA, bob of orgB, carol of orgC, dave of orgA and
// orgB.
const (
	orgA = "55949942-3ee9-4cfc-bb7c-817bdba75186"
	orgB = "6a3c1f8e-0b7d-4e5a-9c2f-1d4e8b7a6c50"
	orgC = "7b4d2a9f-1c8e-4f6b-8d3a-2e5f9c8b7d61"
	// missing is a well-formed id no row has.
	missing = "00000000-0000-4000-8000-000000000000"
)

func matrixService(t *testing.T) (*gtm.Service, gtmsvc.Service) {
	t.Helper()
	members := map[string][]string{"alice": {orgA}, "bob": {orgB}, "carol": {orgC}, "dave": {orgA, orgB}}
	membership := &featuregate.MockMembership{IsMemberFunc: func(_ context.Context, userID, orgID string) (bool, bool, error) {
		for _, o := range members[userID] {
			if o == orgID {
				return true, true, nil
			}
		}
		return false, true, nil
	}}
	gate := featuregate.NewStatic(&configv1.GtmConfig{AllowedOrgIds: []string{orgA, orgB}}, membership)
	env := storetest.New(t)
	domain := gtmsvc.New(gtmsvc.Deps{Store: gtmsvc.NewStore(env.ORM), Gate: gate})
	return gtm.NewTestGtm(t, gtm.WithGtmDeps(gtm.Deps{Service: domain})), domain
}

// rpcCall is one rpc called with a request valid by its rules, naming org
// and, where it takes one, the row id.
type rpcCall func(ctx context.Context, s *gtm.Service, org, id string) error

// everyRPC is every GtmService rpc but GetGtmAccess (which never errors).
// TestGtmViewerMatrixCoversEveryRPC holds it to the proto.
func everyRPC() map[string]rpcCall {
	return map[string]rpcCall{
		"ListStrategies": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.ListStrategies(ctx, connect.NewRequest(&pb.ListStrategiesRequest{OrgId: org}))
			return err
		},
		"GetStrategy": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.GetStrategy(ctx, connect.NewRequest(&pb.GetStrategyRequest{OrgId: org, Id: id}))
			return err
		},
		"CreateStrategy": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.CreateStrategy(ctx, connect.NewRequest(&pb.CreateStrategyRequest{OrgId: org, Name: "S"}))
			return err
		},
		"UpdateStrategy": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.UpdateStrategy(ctx, connect.NewRequest(&pb.UpdateStrategyRequest{OrgId: org, Id: id, ExpectedRevision: 1, Name: proto.String("S2")}))
			return err
		},
		"ListChannels": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.ListChannels(ctx, connect.NewRequest(&pb.ListChannelsRequest{OrgId: org}))
			return err
		},
		"GetChannel": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.GetChannel(ctx, connect.NewRequest(&pb.GetChannelRequest{OrgId: org, Id: id}))
			return err
		},
		"CreateChannel": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.CreateChannel(ctx, connect.NewRequest(&pb.CreateChannelRequest{
				OrgId: org, Key: "forum", Name: "Forum", Mode: pb.GtmChannelMode_GTM_CHANNEL_MODE_MANUAL,
			}))
			return err
		},
		"UpdateChannel": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.UpdateChannel(ctx, connect.NewRequest(&pb.UpdateChannelRequest{OrgId: org, Id: id, ExpectedRevision: 1, Name: proto.String("N")}))
			return err
		},
		"ListAssets": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.ListAssets(ctx, connect.NewRequest(&pb.ListAssetsRequest{OrgId: org, ItemId: proto.String(id)}))
			return err
		},
		"GetAsset": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.GetAsset(ctx, connect.NewRequest(&pb.GetAssetRequest{OrgId: org, Id: id}))
			return err
		},
		"CreateAssetUpload": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.CreateAssetUpload(ctx, connect.NewRequest(&pb.CreateAssetUploadRequest{
				OrgId: org, Target: &pb.CreateAssetUploadRequest_ItemId{ItemId: id}, Filename: "a.png", ContentType: "image/png", SizeBytes: 1,
			}))
			return err
		},
		"ConfirmAssetUpload": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.ConfirmAssetUpload(ctx, connect.NewRequest(&pb.ConfirmAssetUploadRequest{OrgId: org, AssetId: id}))
			return err
		},
		"GetAssetDownload": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.GetAssetDownload(ctx, connect.NewRequest(&pb.GetAssetDownloadRequest{OrgId: org, AssetId: id}))
			return err
		},
		"ListStrategyLibrary": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.ListStrategyLibrary(ctx, connect.NewRequest(&pb.ListStrategyLibraryRequest{OrgId: org}))
			return err
		},
		"GetStrategyLibraryEntry": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.GetStrategyLibraryEntry(ctx, connect.NewRequest(&pb.GetStrategyLibraryEntryRequest{OrgId: org, Key: bankKey()}))
			return err
		},
		"UseLibraryStrategy": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.UseLibraryStrategy(ctx, connect.NewRequest(&pb.UseLibraryStrategyRequest{OrgId: org, LibraryKey: bankKey()}))
			return err
		},
		"ListIdeas": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.ListIdeas(ctx, connect.NewRequest(&pb.ListIdeasRequest{OrgId: org}))
			return err
		},
		"GetIdea": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.GetIdea(ctx, connect.NewRequest(&pb.GetIdeaRequest{OrgId: org, Id: id}))
			return err
		},
		"CreateIdea": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.CreateIdea(ctx, connect.NewRequest(&pb.CreateIdeaRequest{OrgId: org, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_PRODUCT, Title: "I"}))
			return err
		},
		"UpdateIdea": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.UpdateIdea(ctx, connect.NewRequest(&pb.UpdateIdeaRequest{OrgId: org, Id: id, ExpectedRevision: 1, Title: proto.String("I2")}))
			return err
		},
		"SetIdeaStatus": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.SetIdeaStatus(ctx, connect.NewRequest(&pb.SetIdeaStatusRequest{
				OrgId: org, Id: id, ExpectedRevision: 1, Status: pb.GtmIdeaStatus_GTM_IDEA_STATUS_ACCEPTED,
			}))
			return err
		},
		"ListCampaigns": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.ListCampaigns(ctx, connect.NewRequest(&pb.ListCampaignsRequest{OrgId: org}))
			return err
		},
		"GetCampaign": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.GetCampaign(ctx, connect.NewRequest(&pb.GetCampaignRequest{OrgId: org, Id: id}))
			return err
		},
		"CreateCampaign": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.CreateCampaign(ctx, connect.NewRequest(&pb.CreateCampaignRequest{OrgId: org, Name: "C", StrategyId: id}))
			return err
		},
		"UpdateCampaign": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.UpdateCampaign(ctx, connect.NewRequest(&pb.UpdateCampaignRequest{OrgId: org, Id: id, ExpectedRevision: 1, Name: proto.String("C2")}))
			return err
		},
		"ListItems": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.ListItems(ctx, connect.NewRequest(&pb.ListItemsRequest{OrgId: org}))
			return err
		},
		"GetItem": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.GetItem(ctx, connect.NewRequest(&pb.GetItemRequest{OrgId: org, Id: id}))
			return err
		},
		"CreateItem": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.CreateItem(ctx, connect.NewRequest(&pb.CreateItemRequest{OrgId: org, Format: pb.GtmItemFormat_GTM_ITEM_FORMAT_LONG_POST, Title: "T"}))
			return err
		},
		"UpdateItem": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.UpdateItem(ctx, connect.NewRequest(&pb.UpdateItemRequest{OrgId: org, Id: id, ExpectedRevision: 1, Title: proto.String("T2")}))
			return err
		},
		"ResolveItemSend": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.ResolveItemSend(ctx, connect.NewRequest(&pb.ResolveItemSendRequest{OrgId: org, Id: id, ApprovalHash: "h"}))
			return err
		},
		"GetPositioning": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.GetPositioning(ctx, connect.NewRequest(&pb.GetPositioningRequest{OrgId: org}))
			return err
		},
		"UpdatePositioning": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.UpdatePositioning(ctx, connect.NewRequest(&pb.UpdatePositioningRequest{OrgId: org, PositioningMd: "p"}))
			return err
		},
		"ListClaims": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.ListClaims(ctx, connect.NewRequest(&pb.ListClaimsRequest{OrgId: org}))
			return err
		},
		"UpsertClaim": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.UpsertClaim(ctx, connect.NewRequest(&pb.UpsertClaimRequest{
				OrgId: org, Text: proto.String("Free forever"), Category: pb.GtmClaimCategory_GTM_CLAIM_CATEGORY_PRICING.Enum(),
			}))
			return err
		},
		"CheckClaims": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.CheckClaims(ctx, connect.NewRequest(&pb.CheckClaimsRequest{OrgId: org, Text: "It is free."}))
			return err
		},
		"DeleteAsset": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.DeleteAsset(ctx, connect.NewRequest(&pb.DeleteAssetRequest{OrgId: org, AssetId: id}))
			return err
		},
	}
}

// badTimestamp is a Timestamp no time is: protovalidate lets it through,
// and the handler's conversion refuses it.
var badTimestamp = &timestamppb.Timestamp{Seconds: 1, Nanos: -1}

// withBadTimestamp is every rpc that converts a request Timestamp before
// the domain sees it, called with one that is not valid. Each must answer a
// refused caller the bare NotFound, never InvalidArgument.
func withBadTimestamp() map[string]rpcCall {
	return map[string]rpcCall{
		"CreateCampaign": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.CreateCampaign(ctx, connect.NewRequest(&pb.CreateCampaignRequest{OrgId: org, Name: "C", StrategyId: id, PeriodStart: badTimestamp}))
			return err
		},
		"UpdateCampaign": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.UpdateCampaign(ctx, connect.NewRequest(&pb.UpdateCampaignRequest{OrgId: org, Id: id, ExpectedRevision: 1, PeriodEnd: badTimestamp}))
			return err
		},
		"CreateItem": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.CreateItem(ctx, connect.NewRequest(&pb.CreateItemRequest{
				OrgId: org, Format: pb.GtmItemFormat_GTM_ITEM_FORMAT_LONG_POST, Title: "T", ScheduledAt: badTimestamp,
			}))
			return err
		},
		"UpdateItem": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.UpdateItem(ctx, connect.NewRequest(&pb.UpdateItemRequest{OrgId: org, Id: id, ExpectedRevision: 1, ScheduledAt: badTimestamp}))
			return err
		},
		"ListItems": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.ListItems(ctx, connect.NewRequest(&pb.ListItemsRequest{OrgId: org, ScheduledFrom: badTimestamp}))
			return err
		},
		"UpsertClaim": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.UpsertClaim(ctx, connect.NewRequest(&pb.UpsertClaimRequest{OrgId: org, Id: id, ExpectedRevision: 1, ExpiresOrRecheckAt: badTimestamp}))
			return err
		},
		"CreateIdea": func(ctx context.Context, s *gtm.Service, org, _ string) error {
			_, err := s.CreateIdea(ctx, connect.NewRequest(&pb.CreateIdeaRequest{
				OrgId: org, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_PRODUCT, Title: "I", Evidence: []*pb.GtmEvidence{{ObservedAt: badTimestamp}},
			}))
			return err
		},
		"UpdateIdea": func(ctx context.Context, s *gtm.Service, org, id string) error {
			_, err := s.UpdateIdea(ctx, connect.NewRequest(&pb.UpdateIdeaRequest{
				OrgId: org, Id: id, ExpectedRevision: 1, Evidence: &pb.GtmEvidenceList{Evidence: []*pb.GtmEvidence{{ObservedAt: badTimestamp}}},
			}))
			return err
		},
	}
}

// bankKey is a key of the embedded idea bank.
func bankKey() string { return library.All()[0].Key }

// TestGtmViewerMatrixCoversEveryRPC fails when an rpc is added to GtmService
// without a row in everyRPC, so the matrix below covers every rpc.
func TestGtmViewerMatrixCoversEveryRPC(t *testing.T) {
	t.Parallel()
	var want []string
	methods := pb.File_reliant_v1_gtm_proto.Services().ByName("GtmService").Methods()
	for i := 0; i < methods.Len(); i++ {
		if name := string(methods.Get(i).Name()); name != "GetGtmAccess" {
			want = append(want, name)
		}
	}
	var got []string
	for name := range everyRPC() {
		got = append(got, name)
	}
	sort.Strings(want)
	sort.Strings(got)
	assert.Equal(t, want, got)
}

// requireRefused asserts err is the bare NotFound a missing row gets, byte
// for byte.
func requireRefused(t *testing.T, err error, missingRow error) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	assert.Equal(t, svcerr.ToConnect(missingRow).Message(), svcerr.ToConnect(err).Message())
}

func TestGtmViewerMatrix(t *testing.T) {
	svc, domain := matrixService(t)
	alice := sessionCtx(t, "alice")

	// A missing row, asked for by an entitled person: what every refusal
	// must be indistinguishable from.
	_, missingRow := svc.GetIdea(alice, connect.NewRequest(&pb.GetIdeaRequest{OrgId: orgA, Id: missing}))
	require.Error(t, missingRow)
	require.Equal(t, connect.CodeNotFound, svcerr.Code(missingRow))

	refused := map[string]struct {
		ctx context.Context
		org string
	}{
		"a member of an org that is not allowlisted":     {sessionCtx(t, "carol"), orgC},
		"a non-member of an allowlisted org":             {sessionCtx(t, "carol"), orgA},
		"a member of org B asking for org A":             {sessionCtx(t, "bob"), orgA},
		"a member of both with a token bound to org B":   {tokenCtx("dave", orgB), orgA},
		"an allowlisted member with a token of org C":    {tokenCtx("alice", orgC), orgA},
		"a person of an unlisted org, with no org named": {sessionCtx(t, "carol"), ""},
	}
	for name, who := range refused {
		t.Run(name+": every rpc is the bare NotFound", func(t *testing.T) {
			for rpc, call := range everyRPC() {
				err := call(who.ctx, svc, who.org, missing)
				if !assert.Error(t, err, rpc) {
					continue
				}
				assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), rpc)
				assert.Equal(t, svcerr.ToConnect(missingRow).Message(), svcerr.ToConnect(err).Message(), rpc)
			}
		})
		t.Run(name+": a timestamp that is not one is the bare NotFound too", func(t *testing.T) {
			for rpc, call := range withBadTimestamp() {
				err := call(who.ctx, svc, who.org, missing)
				if !assert.Error(t, err, rpc) {
					continue
				}
				assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), rpc)
				assert.Equal(t, svcerr.ToConnect(missingRow).Message(), svcerr.ToConnect(err).Message(), rpc)
			}
		})
		t.Run(name+": GetGtmAccess is enabled=false, not an error", func(t *testing.T) {
			resp, err := svc.GetGtmAccess(who.ctx, connect.NewRequest(&pb.GetGtmAccessRequest{OrgId: who.org}))
			require.NoError(t, err)
			assert.False(t, resp.Msg.GetEnabled())
		})
	}

	t.Run("a refused caller breaking a request's rules still gets NotFound, never the rules", func(t *testing.T) {
		_, err := svc.CreateStrategy(sessionCtx(t, "carol"), connect.NewRequest(&pb.CreateStrategyRequest{OrgId: orgC}))
		requireRefused(t, err, missingRow)
		_, err = svc.CreateStrategy(alice, connect.NewRequest(&pb.CreateStrategyRequest{OrgId: orgA}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	})

	t.Run("protovalidate runs: a rule only it enforces is InvalidArgument for an admitted caller", func(t *testing.T) {
		// size_bytes lte 64 MiB is protovalidate's alone: without it the
		// domain answers Unavailable here (no asset store is wired).
		huge := &pb.CreateAssetUploadRequest{
			OrgId: orgA, Target: &pb.CreateAssetUploadRequest_ItemId{ItemId: missing},
			Filename: "a.png", ContentType: "image/png", SizeBytes: 100 << 20,
		}
		_, err := svc.CreateAssetUpload(alice, connect.NewRequest(huge))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
		assert.Contains(t, svcerr.ToConnect(err).Message(), "size_bytes")
		huge.OrgId = orgC
		_, err = svc.CreateAssetUpload(sessionCtx(t, "carol"), connect.NewRequest(huge))
		requireRefused(t, err, missingRow)
	})

	t.Run("an admitted caller's bad timestamp is InvalidArgument naming the field", func(t *testing.T) {
		for rpc, call := range withBadTimestamp() {
			err := call(alice, svc, orgA, missing)
			assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), rpc)
		}
	})

	t.Run("GetGtmAccess is enabled for an allowlisted member", func(t *testing.T) {
		resp, err := svc.GetGtmAccess(alice, connect.NewRequest(&pb.GetGtmAccessRequest{OrgId: orgA}))
		require.NoError(t, err)
		assert.True(t, resp.Msg.GetEnabled())
	})

	// Org A's rows, made by alice.
	strategy, err := svc.CreateStrategy(alice, connect.NewRequest(&pb.CreateStrategyRequest{OrgId: orgA, Name: "Launch"}))
	require.NoError(t, err)
	idea, err := svc.CreateIdea(alice, connect.NewRequest(&pb.CreateIdeaRequest{OrgId: orgA, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_GTM, Title: "Launch week"}))
	require.NoError(t, err)
	assert.Equal(t, pb.GtmIdeaSource_GTM_IDEA_SOURCE_HUMAN, idea.Msg.GetIdea().GetSource(), "a session is a Person")

	t.Run("org B cannot read org A even when both are allowlisted", func(t *testing.T) {
		bob := sessionCtx(t, "bob")
		ideaID, strategyID := idea.Msg.GetIdea().GetId(), strategy.Msg.GetStrategy().GetId()
		for _, org := range []string{orgB, orgA} {
			_, err := svc.GetIdea(bob, connect.NewRequest(&pb.GetIdeaRequest{OrgId: org, Id: ideaID}))
			requireRefused(t, err, missingRow)
			_, err = svc.UpdateIdea(bob, connect.NewRequest(&pb.UpdateIdeaRequest{OrgId: org, Id: ideaID, ExpectedRevision: 1, Title: proto.String("x")}))
			requireRefused(t, err, missingRow)
			_, err = svc.GetStrategy(bob, connect.NewRequest(&pb.GetStrategyRequest{OrgId: org, Id: strategyID}))
			requireRefused(t, err, missingRow)
		}
		list, err := svc.ListIdeas(bob, connect.NewRequest(&pb.ListIdeasRequest{OrgId: orgB}))
		require.NoError(t, err)
		assert.Empty(t, list.Msg.GetIdeas())
		// dave, a member of both, sees it in org A only.
		dave := sessionCtx(t, "dave")
		_, err = svc.GetIdea(dave, connect.NewRequest(&pb.GetIdeaRequest{OrgId: orgB, Id: ideaID}))
		requireRefused(t, err, missingRow)
		got, err := svc.GetIdea(dave, connect.NewRequest(&pb.GetIdeaRequest{OrgId: orgA, Id: ideaID}))
		require.NoError(t, err)
		assert.Equal(t, "Launch week", got.Msg.GetIdea().GetTitle())
	})

	t.Run("a revision conflict carries GtmRevisionConflict", func(t *testing.T) {
		ideaID := idea.Msg.GetIdea().GetId()
		_, err := svc.UpdateIdea(alice, connect.NewRequest(&pb.UpdateIdeaRequest{OrgId: orgA, Id: ideaID, ExpectedRevision: 1, Title: proto.String("v2")}))
		require.NoError(t, err)
		_, err = svc.UpdateIdea(alice, connect.NewRequest(&pb.UpdateIdeaRequest{OrgId: orgA, Id: ideaID, ExpectedRevision: 1, Title: proto.String("v3")}))
		var ce *connect.Error
		require.True(t, errors.As(err, &ce))
		assert.Equal(t, connect.CodeAborted, ce.Code())
		require.Len(t, ce.Details(), 1)
		v, derr := ce.Details()[0].Value()
		require.NoError(t, derr)
		assert.True(t, proto.Equal(&pb.GtmRevisionConflict{Id: ideaID, ExpectedRevision: 1, CurrentRevision: 2}, v), "%v", v)
	})

	t.Run("an access token is an Agent; a session is a Person", func(t *testing.T) {
		token := tokenCtx("alice", orgA)
		item, err := svc.CreateItem(alice, connect.NewRequest(&pb.CreateItemRequest{OrgId: orgA, Format: pb.GtmItemFormat_GTM_ITEM_FORMAT_LONG_POST, Title: "Post"}))
		require.NoError(t, err)
		itemID := item.Msg.GetItem().GetId()
		approve := &pb.UpdateItemRequest{OrgId: orgA, Id: itemID, ExpectedRevision: 1, Status: pb.GtmItemStatus_GTM_ITEM_STATUS_APPROVED.Enum()}

		_, err = svc.UpdateItem(token, connect.NewRequest(approve))
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "a token may not approve")
		assert.Equal(t, "only a person can approve an item", svcerr.ToConnect(err).Message())

		claim, err := svc.UpsertClaim(alice, connect.NewRequest(&pb.UpsertClaimRequest{
			OrgId: orgA, Text: proto.String("Free forever"), Category: pb.GtmClaimCategory_GTM_CLAIM_CATEGORY_PRICING.Enum(),
		}))
		require.NoError(t, err)
		verify := &pb.UpsertClaimRequest{
			OrgId: orgA, Id: claim.Msg.GetClaim().GetId(), ExpectedRevision: 1, Status: pb.GtmClaimStatus_GTM_CLAIM_STATUS_VERIFIED.Enum(),
			SourceUrl: proto.String("https://example.com/pricing"), SourceKind: pb.GtmClaimSourceKind_GTM_CLAIM_SOURCE_KIND_PRICING_CATALOG.Enum(),
		}
		_, err = svc.UpsertClaim(token, connect.NewRequest(verify))
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "a token may not verify")

		_, err = svc.ResolveItemSend(token, connect.NewRequest(&pb.ResolveItemSendRequest{OrgId: orgA, Id: itemID, ApprovalHash: "h"}))
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "a token may not resolve a send")

		// The same acts in a session: a Person.
		approved, err := svc.UpdateItem(alice, connect.NewRequest(approve))
		require.NoError(t, err)
		assert.Equal(t, pb.GtmItemStatus_GTM_ITEM_STATUS_APPROVED, approved.Msg.GetItem().GetStatus())
		verified, err := svc.UpsertClaim(alice, connect.NewRequest(verify))
		require.NoError(t, err)
		assert.Equal(t, pb.GtmClaimStatus_GTM_CLAIM_STATUS_VERIFIED, verified.Msg.GetClaim().GetStatus())
		assert.Equal(t, "alice", verified.Msg.GetClaim().GetVerifiedBy())
		_, err = svc.ResolveItemSend(alice, connect.NewRequest(&pb.ResolveItemSendRequest{OrgId: orgA, Id: itemID, ApprovalHash: "h"}))
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "a person gets past the person check; the item is not held")
	})

	t.Run("the idea bank, positioning and an idea from it, mapped both ways", func(t *testing.T) {
		pos, err := svc.UpdatePositioning(alice, connect.NewRequest(&pb.UpdatePositioningRequest{
			OrgId: orgA, PositioningMd: "We help teams ship.",
			BusinessType:   pb.GtmBusinessType_GTM_BUSINESS_TYPE_DEVELOPER_TOOLS,
			BusinessModels: []pb.GtmBusinessModel{pb.GtmBusinessModel_GTM_BUSINESS_MODEL_FREEMIUM},
		}))
		require.NoError(t, err)
		assert.Equal(t, pb.GtmBusinessType_GTM_BUSINESS_TYPE_DEVELOPER_TOOLS, pos.Msg.GetPositioning().GetBusinessType())
		assert.Equal(t, []pb.GtmBusinessModel{pb.GtmBusinessModel_GTM_BUSINESS_MODEL_FREEMIUM}, pos.Msg.GetPositioning().GetBusinessModels())

		diverse, err := svc.ListStrategyLibrary(alice, connect.NewRequest(&pb.ListStrategyLibraryRequest{
			OrgId: orgA, Diverse: proto.Bool(true), FitsMyBusiness: proto.Bool(true), PageSize: 12,
		}))
		require.NoError(t, err)
		require.NotEmpty(t, diverse.Msg.GetEntries())
		assert.LessOrEqual(t, len(diverse.Msg.GetEntries()), 12)
		assert.Empty(t, diverse.Msg.GetNextPageToken(), "diverse is unpaged")
		categories := map[pb.GtmStrategyCategory]bool{}
		for _, e := range diverse.Msg.GetEntries() {
			categories[e.GetCategory()] = true
			assert.NotEqual(t, pb.GtmStrategyCategory_GTM_STRATEGY_CATEGORY_UNSPECIFIED, e.GetCategory(), e.GetKey())
		}
		assert.Greater(t, len(categories), 1, "diverse spans categories")

		filtered, err := svc.ListStrategyLibrary(alice, connect.NewRequest(&pb.ListStrategyLibraryRequest{
			OrgId:      orgA,
			Categories: []pb.GtmStrategyCategory{pb.GtmStrategyCategory_GTM_STRATEGY_CATEGORY_COMMUNITY_EVENTS},
		}))
		require.NoError(t, err)
		require.NotEmpty(t, filtered.Msg.GetEntries())
		for _, e := range filtered.Msg.GetEntries() {
			assert.Equal(t, pb.GtmStrategyCategory_GTM_STRATEGY_CATEGORY_COMMUNITY_EVENTS, e.GetCategory())
		}

		_, err = svc.ListStrategyLibrary(alice, connect.NewRequest(&pb.ListStrategyLibraryRequest{OrgId: orgA, Effort: proto.String("LOW")}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "the bank's vocabularies are lowercase")

		key := bankKey()
		entry, err := svc.GetStrategyLibraryEntry(alice, connect.NewRequest(&pb.GetStrategyLibraryEntryRequest{OrgId: orgA, Key: key}))
		require.NoError(t, err)
		require.NotEmpty(t, entry.Msg.GetEntry().GetTailorQuestions())
		question := entry.Msg.GetEntry().GetTailorQuestions()[0]

		idea, err := svc.CreateIdea(alice, connect.NewRequest(&pb.CreateIdeaRequest{
			OrgId: orgA, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_GTM, Title: "From the bank", LibraryKey: key,
			TailorAnswers:       []*pb.GtmTailorAnswer{{Question: question, Answer: "Developers at small teams."}},
			SuggestedWorkflowMd: "Weekly digest.",
		}))
		require.NoError(t, err)
		got := idea.Msg.GetIdea()
		assert.Equal(t, key, got.GetLibraryKey())
		assert.Equal(t, "Developers at small teams.", got.GetTailorAnswers()[0].GetAnswer())
		assert.Equal(t, "Weekly digest.", got.GetSuggestedWorkflowMd())

		cleared, err := svc.UpdateIdea(alice, connect.NewRequest(&pb.UpdateIdeaRequest{
			OrgId: orgA, Id: got.GetId(), ExpectedRevision: got.GetRevision(),
			TailorAnswers: &pb.GtmTailorAnswerList{}, LibraryKey: proto.String(""),
		}))
		require.NoError(t, err)
		assert.Empty(t, cleared.Msg.GetIdea().GetTailorAnswers())
		assert.Empty(t, cleared.Msg.GetIdea().GetLibraryKey())
		assert.Equal(t, "Weekly digest.", cleared.Msg.GetIdea().GetSuggestedWorkflowMd(), "unset is left alone")

		_, err = svc.CreateIdea(alice, connect.NewRequest(&pb.CreateIdeaRequest{
			OrgId: orgA, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_GTM, Title: "x", LibraryKey: "no-such-entry",
		}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "a library key must be in the bank")
	})

	t.Run("an access token is an Agent on every rpc, with no exception left", func(t *testing.T) {
		token := tokenCtx("alice", orgA)
		_, err := svc.DeleteAsset(token, connect.NewRequest(&pb.DeleteAssetRequest{OrgId: orgA, AssetId: missing}))
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
		_, err = svc.UpdateStrategy(token, connect.NewRequest(&pb.UpdateStrategyRequest{
			OrgId: orgA, Id: strategy.Msg.GetStrategy().GetId(), ExpectedRevision: 1,
			Status: pb.GtmStrategyStatus_GTM_STRATEGY_STATUS_ACTIVE.Enum(),
		}))
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "a token may not activate a strategy")
		for _, c := range []auth.Credential{auth.CredentialOAuthClient, auth.CredentialIdentityToken, auth.CredentialUnknown} {
			_, err = svc.DeleteAsset(credentialCtx("alice", c), connect.NewRequest(&pb.DeleteAssetRequest{OrgId: orgA, AssetId: missing}))
			assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), c.String())
		}
	})

	t.Run("over a Connect client: the refusal, and the conflict detail on the wire", func(t *testing.T) {
		_, carol := gtm.NewTestGtmServer(t, gtm.WithGtmDeps(gtm.Deps{Service: domain}), gtm.WithPrincipal(testkit.WithUserID("carol")))
		_, err := carol.ListIdeas(context.Background(), connect.NewRequest(&pb.ListIdeasRequest{OrgId: orgC}))
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		resp, err := carol.GetGtmAccess(context.Background(), connect.NewRequest(&pb.GetGtmAccessRequest{OrgId: orgC}))
		require.NoError(t, err)
		assert.False(t, resp.Msg.GetEnabled())

		_, aliceClient := gtm.NewTestGtmServer(t, gtm.WithGtmDeps(gtm.Deps{Service: domain}), gtm.WithPrincipal(testkit.WithUserID("alice")))
		_, err = aliceClient.UpdateIdea(context.Background(), connect.NewRequest(&pb.UpdateIdeaRequest{
			OrgId: orgA, Id: idea.Msg.GetIdea().GetId(), ExpectedRevision: 1, Title: proto.String("stale"),
		}))
		var ce *connect.Error
		require.True(t, errors.As(err, &ce))
		assert.Equal(t, connect.CodeAborted, ce.Code())
		require.Len(t, ce.Details(), 1)
		v, derr := ce.Details()[0].Value()
		require.NoError(t, derr)
		assert.Equal(t, int32(1), v.(*pb.GtmRevisionConflict).GetExpectedRevision())
	})
}
