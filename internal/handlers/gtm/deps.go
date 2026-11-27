// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them.
package gtm

import (
	"context"

	"github.com/reliant-labs/reliant/internal/gtm"
)

// ServiceService is the part of gtm.Service that this package uses through Deps.Service:
// declared here, by its consumer, so the package knows only what it uses
// (forge fix narrow-deps). Add a method when the package calls one.
type ServiceService interface {
	CheckClaims(ctx context.Context, actor gtm.Actor, in gtm.CheckClaimsInput) (gtm.CheckClaimsResult, error)
	ConfirmAssetUpload(ctx context.Context, actor gtm.Actor, in gtm.ConfirmAssetUploadInput) (gtm.Asset, error)
	CreateAssetUpload(ctx context.Context, actor gtm.Actor, in gtm.CreateAssetUploadInput) (gtm.AssetUpload, error)
	CreateCampaign(ctx context.Context, actor gtm.Actor, in gtm.CreateCampaignInput) (gtm.Campaign, error)
	CreateChannel(ctx context.Context, actor gtm.Actor, in gtm.CreateChannelInput) (gtm.Channel, error)
	CreateIdea(ctx context.Context, actor gtm.Actor, in gtm.CreateIdeaInput) (gtm.Idea, error)
	CreateItem(ctx context.Context, actor gtm.Actor, in gtm.CreateItemInput) (gtm.Item, error)
	CreateStrategy(ctx context.Context, actor gtm.Actor, in gtm.CreateStrategyInput) (gtm.Strategy, error)
	DeleteAsset(ctx context.Context, actor gtm.Actor, in gtm.DeleteAssetInput) error
	GetAccess(ctx context.Context, actor gtm.Actor) (gtm.Access, error)
	GetAsset(ctx context.Context, actor gtm.Actor, in gtm.GetAssetInput) (gtm.Asset, error)
	GetAssetDownload(ctx context.Context, actor gtm.Actor, in gtm.GetAssetDownloadInput) (gtm.AssetDownload, error)
	GetCampaign(ctx context.Context, actor gtm.Actor, in gtm.GetCampaignInput) (gtm.CampaignDetail, error)
	GetChannel(ctx context.Context, actor gtm.Actor, in gtm.GetChannelInput) (gtm.Channel, error)
	GetIdea(ctx context.Context, actor gtm.Actor, in gtm.GetIdeaInput) (gtm.Idea, error)
	GetItem(ctx context.Context, actor gtm.Actor, in gtm.GetItemInput) (gtm.Item, error)
	GetPositioning(ctx context.Context, actor gtm.Actor) (gtm.Positioning, error)
	GetStrategy(ctx context.Context, actor gtm.Actor, in gtm.GetStrategyInput) (gtm.StrategyDetail, error)
	GetStrategyLibraryEntry(ctx context.Context, actor gtm.Actor, in gtm.GetStrategyLibraryEntryInput) (gtm.LibraryEntry, error)
	ListAssets(ctx context.Context, actor gtm.Actor, in gtm.ListAssetsInput) (gtm.ListAssetsResult, error)
	ListCampaigns(ctx context.Context, actor gtm.Actor, in gtm.ListCampaignsInput) (gtm.ListCampaignsResult, error)
	ListChannels(ctx context.Context, actor gtm.Actor, in gtm.ListChannelsInput) (gtm.ListChannelsResult, error)
	ListClaims(ctx context.Context, actor gtm.Actor, in gtm.ListClaimsInput) (gtm.ListClaimsResult, error)
	ListIdeas(ctx context.Context, actor gtm.Actor, in gtm.ListIdeasInput) (gtm.ListIdeasResult, error)
	ListItems(ctx context.Context, actor gtm.Actor, in gtm.ListItemsInput) (gtm.ListItemsResult, error)
	ListStrategies(ctx context.Context, actor gtm.Actor, in gtm.ListStrategiesInput) (gtm.ListStrategiesResult, error)
	ListStrategyLibrary(ctx context.Context, actor gtm.Actor, in gtm.ListStrategyLibraryInput) (gtm.ListStrategyLibraryResult, error)
	ResolveInDoubtSend(ctx context.Context, actor gtm.Actor, in gtm.ResolveInDoubtSendInput) (gtm.Item, error)
	SetIdeaStatus(ctx context.Context, actor gtm.Actor, in gtm.SetIdeaStatusInput) (gtm.Idea, error)
	UpdateCampaign(ctx context.Context, actor gtm.Actor, in gtm.UpdateCampaignInput) (gtm.Campaign, error)
	UpdateChannel(ctx context.Context, actor gtm.Actor, in gtm.UpdateChannelInput) (gtm.Channel, error)
	UpdateIdea(ctx context.Context, actor gtm.Actor, in gtm.UpdateIdeaInput) (gtm.Idea, error)
	UpdateItem(ctx context.Context, actor gtm.Actor, in gtm.UpdateItemInput) (gtm.Item, error)
	UpdatePositioning(ctx context.Context, actor gtm.Actor, in gtm.UpdatePositioningInput) (gtm.Positioning, error)
	UpdateStrategy(ctx context.Context, actor gtm.Actor, in gtm.UpdateStrategyInput) (gtm.Strategy, error)
	UpsertClaim(ctx context.Context, actor gtm.Actor, in gtm.UpsertClaimInput) (gtm.UpsertClaimResult, error)
	UseLibraryStrategy(ctx context.Context, actor gtm.Actor, in gtm.UseLibraryStrategyInput) (gtm.UseLibraryStrategyResult, error)
}
