// Copyright (c) 2026 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/store"
)

// Tenant zero is the allowlisted org of every GTM test and fixture.
const (
	gtmTestOrg   = "55949942-3ee9-4cfc-bb7c-817bdba75186"
	gtmOtherOrg  = "11111111-2222-3333-4444-555555555555"
	gtmTestUser  = "user-1"
	gtmTestChat  = "chat-1"
	gtmOtherChat = "chat-other-org"
)

var gtmToolNames = []string{
	ToolListGtmIdeas, ToolSaveGtmIdea, ToolListGtmStrategies, ToolSaveGtmStrategy, ToolListGtmStrategyLibrary,
	ToolListGtmCampaigns, ToolSaveGtmCampaign, ToolListGtmCalendar, ToolSaveGtmItem, ToolListGtmChannels,
	ToolSaveGtmChannel, ToolGetGtmPositioning, ToolCheckGtmClaims, ToolReadProductAnalytics, ToolAttachGtmAsset,
	ToolSendGtmEmail,
}

var gtmReadTools = map[string]bool{
	ToolListGtmIdeas: true, ToolListGtmStrategies: true, ToolListGtmStrategyLibrary: true, ToolListGtmCampaigns: true,
	ToolListGtmCalendar: true, ToolListGtmChannels: true, ToolGetGtmPositioning: true, ToolCheckGtmClaims: true,
	ToolReadProductAnalytics: true,
}

// gtmChats is the chat store: chat-1 in tenant zero, another chat in an org
// that is not allowlisted.
type gtmChats struct{}

func (gtmChats) GetChat(_ context.Context, id string) (*db.Chat, error) {
	org := map[string]string{gtmTestChat: gtmTestOrg, gtmOtherChat: gtmOtherOrg}[id]
	if org == "" {
		return nil, errors.New("chat not found")
	}
	return &db.Chat{ID: id, UserID: gtmTestUser, OrgID: &org}, nil
}

var gtmAgent = gtm.Actor{Kind: gtm.ActorAgent, UserID: gtmTestUser, OrgID: gtmTestOrg, ChatID: gtmTestChat}

// runGTM runs tool as the runtime does, with call id call-1.
func runGTM(t *testing.T, tool Tool, chatID, input string) ToolResponse {
	t.Helper()
	resp, err := tool.Run(rctx.NewToolContext(context.Background(), chatID, "thread-1", nil, nil), ToolCall{ID: "call-1", Name: tool.Name(), Input: input})
	require.NoError(t, err)
	return resp
}

// newGTMTestTool is the named GTM tool over svc (or sender), with the GTM chat
// store.
func newGTMTestTool(name string, svc GTMService, sender GTMEmailSender) Tool {
	if name == ToolSendGtmEmail {
		return NewSendGtmEmailTool(gtmChats{}, sender)
	}
	build, ok := map[string]func(runCallerStore, GTMService) Tool{
		ToolListGtmIdeas: NewListGtmIdeasTool, ToolSaveGtmIdea: NewSaveGtmIdeaTool,
		ToolListGtmStrategies: NewListGtmStrategiesTool, ToolSaveGtmStrategy: NewSaveGtmStrategyTool,
		ToolListGtmStrategyLibrary: NewListGtmStrategyLibraryTool, ToolListGtmCampaigns: NewListGtmCampaignsTool,
		ToolSaveGtmCampaign: NewSaveGtmCampaignTool, ToolListGtmCalendar: NewListGtmCalendarTool,
		ToolSaveGtmItem: NewSaveGtmItemTool, ToolListGtmChannels: NewListGtmChannelsTool,
		ToolSaveGtmChannel: NewSaveGtmChannelTool, ToolGetGtmPositioning: NewGetGtmPositioningTool,
		ToolCheckGtmClaims: NewCheckGtmClaimsTool, ToolReadProductAnalytics: NewReadProductAnalyticsTool,
		ToolAttachGtmAsset: NewAttachGtmAssetTool,
	}[name]
	if !ok {
		panic("not a GTM tool: " + name)
	}
	return build(gtmChats{}, svc)
}

func decodeGTM(t *testing.T, resp ToolResponse) map[string]any {
	t.Helper()
	require.False(t, resp.IsError, resp.Content)
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &out), resp.Content)
	return out
}

// --- registration and gating ---------------------------------------------------

func TestGTMTools_RegisteredServerPlacedAndGated(t *testing.T) {
	registry := registryIndex()
	gated := FeatureGatedTools()
	factory := NewToolsFactory(&ToolsOptions{})
	for _, name := range gtmToolNames {
		def, ok := registry[name]
		require.True(t, ok, "%s is not registered", name)
		assert.Equal(t, PlacementServer, def.Placement, name)
		assert.False(t, NeedsMachine(name), name)
		assert.Equal(t, featuregate.GTM, gated[name], "%s is not gated behind GTM", name)
		assert.Equal(t, gtmReadTools[name], hasTag(def.Tags, TagReadOnly), "%s: reads carry readonly, nothing else does", name)
		for _, tag := range def.Tags {
			assert.Equal(t, TagReadOnly, tag, "%s carries a tag beyond readonly", name)
		}
		tool := factory.GetToolByName(name, nil)
		require.NotNil(t, tool, name)
		assert.Equal(t, name, tool.Name())
		assert.Contains(t, tool.Description(), "connect", "%s says what an agent can't do", name)
	}
	assert.Len(t, gated, len(gtmToolNames), "every gated tool is a GTM tool")
	_, isApproval := factory.GetToolByName(ToolSendGtmEmail, nil).(AutomationApprovalTool)
	assert.True(t, isApproval, "send_gtm_email raises an approval card")
}

func hasTag(tags []ToolTag, want ToolTag) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// gtmEntitlement is what call_llm computes for the chat's user and org: the
// real static gate over the GTM allowlist (tenant zero), with a membership
// that says the user belongs to every org.
func gtmEntitlement(t *testing.T, orgID string) map[string]bool {
	t.Helper()
	gate := featuregate.NewStatic(&configv1.GtmConfig{AllowedOrgIds: []string{gtmTestOrg}}, &featuregate.MockMembership{
		IsMemberFunc: func(context.Context, string, string) (bool, bool, error) { return true, true, nil },
	})
	ok, err := gate.Allowed(context.Background(), featuregate.GTM, gtmTestUser, orgID)
	require.NoError(t, err)
	return map[string]bool{string(featuregate.GTM): ok}
}

// To a caller the gate does not entitle, every GTM tool is an unknown tool,
// word for word, on every path; to an entitled one it loads and is offered.
func TestGTMTools_NotEntitledReadAsUnknown(t *testing.T) {
	notEntitled := entitledCaps(gtmEntitlement(t, gtmOtherOrg), append([]string{ToolView}, gtmToolNames...))
	entitled := entitledCaps(gtmEntitlement(t, gtmTestOrg), append([]string{ToolView}, gtmToolNames...))
	ctx := toolCtxWithCaps(t, notEntitled)
	load := &loadToolTool{}
	unknown, err := load.Execute(ctx, LoadToolParams{Name: unknownTool})
	require.NoError(t, err)

	for _, name := range gtmToolNames {
		assert.False(t, notEntitled.Offers(name), name)
		assert.False(t, notEntitled.CanLoad(name), name)
		assert.False(t, notEntitled.Searchable(name), name)
		assert.True(t, notEntitled.HidesTool(name), name)
		assert.NotContains(t, notEntitled.Deferred(), name)
		assert.Equal(t, asUnknown(notEntitled.LoadRefusal(unknownTool), name), notEntitled.LoadRefusal(name), name)
		assert.Equal(t, asUnknown(notEntitled.Explain(unknownTool), name), notEntitled.Explain(name), name)

		byName, err := load.Execute(ctx, LoadToolParams{Name: name})
		require.NoError(t, err)
		assert.Equal(t, asUnknown(unknown.Content, name), byName.Content, name)

		assert.True(t, entitled.Offers(name), name)
		assert.True(t, entitled.CanLoad(name), name)
		assert.Empty(t, entitled.LoadRefusal(name), name)
	}
	for _, query := range []string{"gtm", "campaign", "analytics", "email"} {
		for _, r := range SearchTools(query, notEntitled) {
			assert.NotContains(t, gtmToolNames, r.Name, "search %q listed a GTM tool", query)
		}
	}
	byTag, err := load.Execute(ctx, LoadToolParams{Name: "tag:" + string(TagReadOnly)})
	require.NoError(t, err)
	for _, name := range gtmToolNames {
		assert.NotContains(t, byTag.Content, name, "tag:readonly neither loads nor refuses GTM tools by name")
	}
}

// --- happy paths, over the service mock ---------------------------------------

func gtmNow() time.Time { return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) }

// gtmMock answers every call the tools make with plausible rows, after
// checking that it acts as the chat's agent.
func gtmMock(t *testing.T) *gtm.MockService {
	t.Helper()
	agent := func(a gtm.Actor) { assert.Equal(t, gtmAgent, a, "the tools act as the chat's agent") }
	idea := gtm.Idea{ID: "idea-1", Kind: gtm.IdeaKindContent, Title: "Post about runs", BodyMD: strings.Repeat("b", 400), Status: gtm.IdeaProposed, Source: gtm.IdeaSourceAgent, Revision: 1, UpdatedAt: gtmNow()}
	strategy := gtm.Strategy{ID: "strat-1", Name: "Grow weekly orgs", Status: gtm.StrategyDraft, Category: gtm.CategoryOrganicContentSEO, Revision: 1}
	campaign := gtm.Campaign{ID: "camp-1", Name: "Launch", StrategyID: "strat-1", Status: gtm.CampaignDraft, PlannedChannelIDs: []string{"ch-reddit"}, PlannedFormats: []gtm.ItemFormat{gtm.FormatShortPost}, Revision: 2}
	at := gtmNow().Add(48 * time.Hour)
	item := gtm.Item{ID: "item-1", Title: "Launch post", Format: gtm.FormatShortPost, ChannelID: "ch-reddit", Status: gtm.ItemDraft, ScheduledAt: &at, BodyMD: "Hello", Revision: 1}
	channel := gtm.Channel{ID: "ch-reddit", Key: "reddit", Name: "Reddit", Mode: gtm.ChannelManual, Status: gtm.ChannelActive, Source: gtm.ChannelSourceSeed, Revision: 1}
	entry := gtm.LibraryEntry{Key: "sample-play", Name: "Sample play", Category: gtm.CategoryLaunchesPR, Depth: gtm.DepthPlay, SummaryMD: "A placeholder play.", KPIs: []string{"signups"}}
	return &gtm.MockService{
		ListIdeasFunc: func(_ context.Context, a gtm.Actor, in gtm.ListIdeasInput) (gtm.ListIdeasResult, error) {
			agent(a)
			assert.Equal(t, gtmDefaultPage, in.PageSize)
			return gtm.ListIdeasResult{Ideas: []gtm.Idea{idea}, NextPageToken: "next"}, nil
		},
		GetIdeaFunc: func(_ context.Context, a gtm.Actor, in gtm.GetIdeaInput) (gtm.Idea, error) {
			agent(a)
			return idea, nil
		},
		CreateIdeaFunc: func(_ context.Context, a gtm.Actor, in gtm.CreateIdeaInput) (gtm.Idea, error) {
			agent(a)
			return idea, nil
		},
		UpdateIdeaFunc: func(_ context.Context, a gtm.Actor, in gtm.UpdateIdeaInput) (gtm.Idea, error) {
			agent(a)
			return idea, nil
		},
		ListStrategiesFunc: func(_ context.Context, a gtm.Actor, in gtm.ListStrategiesInput) (gtm.ListStrategiesResult, error) {
			agent(a)
			return gtm.ListStrategiesResult{Strategies: []gtm.Strategy{strategy}}, nil
		},
		GetStrategyFunc: func(_ context.Context, a gtm.Actor, in gtm.GetStrategyInput) (gtm.StrategyDetail, error) {
			agent(a)
			return gtm.StrategyDetail{Strategy: strategy, Campaigns: []gtm.Campaign{campaign}}, nil
		},
		CreateStrategyFunc: func(_ context.Context, a gtm.Actor, in gtm.CreateStrategyInput) (gtm.Strategy, error) {
			agent(a)
			return strategy, nil
		},
		UpdateStrategyFunc: func(_ context.Context, a gtm.Actor, in gtm.UpdateStrategyInput) (gtm.Strategy, error) {
			agent(a)
			return strategy, nil
		},
		UseLibraryStrategyFunc: func(_ context.Context, a gtm.Actor, in gtm.UseLibraryStrategyInput) (gtm.UseLibraryStrategyResult, error) {
			agent(a)
			return gtm.UseLibraryStrategyResult{Strategy: strategy, Campaign: &campaign}, nil
		},
		ListStrategyLibraryFunc: func(_ context.Context, a gtm.Actor, in gtm.ListStrategyLibraryInput) (gtm.ListStrategyLibraryResult, error) {
			agent(a)
			return gtm.ListStrategyLibraryResult{Entries: []gtm.LibraryEntry{entry}}, nil
		},
		GetStrategyLibraryEntryFunc: func(_ context.Context, a gtm.Actor, in gtm.GetStrategyLibraryEntryInput) (gtm.LibraryEntry, error) {
			agent(a)
			return entry, nil
		},
		ListCampaignsFunc: func(_ context.Context, a gtm.Actor, in gtm.ListCampaignsInput) (gtm.ListCampaignsResult, error) {
			agent(a)
			return gtm.ListCampaignsResult{Campaigns: []gtm.Campaign{campaign}}, nil
		},
		GetCampaignFunc: func(_ context.Context, a gtm.Actor, in gtm.GetCampaignInput) (gtm.CampaignDetail, error) {
			agent(a)
			return gtm.CampaignDetail{Campaign: campaign, Ideas: []gtm.Idea{idea}, Items: []gtm.Item{item}, Coverage: []gtm.CoverageRow{
				{ChannelID: "ch-reddit", Format: gtm.FormatShortPost, Planned: true},
				{ChannelID: "ch-x", Format: gtm.FormatShortPost, Counts: []gtm.StatusCount{{Status: gtm.ItemDraft, Count: 2}}, Total: 2},
			}}, nil
		},
		CreateCampaignFunc: func(_ context.Context, a gtm.Actor, in gtm.CreateCampaignInput) (gtm.Campaign, error) {
			agent(a)
			return campaign, nil
		},
		UpdateCampaignFunc: func(_ context.Context, a gtm.Actor, in gtm.UpdateCampaignInput) (gtm.Campaign, error) {
			agent(a)
			return campaign, nil
		},
		ListItemsFunc: func(_ context.Context, a gtm.Actor, in gtm.ListItemsInput) (gtm.ListItemsResult, error) {
			agent(a)
			return gtm.ListItemsResult{Items: []gtm.Item{item}}, nil
		},
		GetItemFunc: func(_ context.Context, a gtm.Actor, in gtm.GetItemInput) (gtm.Item, error) {
			agent(a)
			return item, nil
		},
		CreateItemFunc: func(_ context.Context, a gtm.Actor, in gtm.CreateItemInput) (gtm.Item, error) {
			agent(a)
			return item, nil
		},
		UpdateItemFunc: func(_ context.Context, a gtm.Actor, in gtm.UpdateItemInput) (gtm.Item, error) {
			agent(a)
			return item, nil
		},
		ListChannelsFunc: func(_ context.Context, a gtm.Actor, in gtm.ListChannelsInput) (gtm.ListChannelsResult, error) {
			agent(a)
			return gtm.ListChannelsResult{Channels: []gtm.Channel{channel}}, nil
		},
		GetChannelFunc: func(_ context.Context, a gtm.Actor, in gtm.GetChannelInput) (gtm.Channel, error) {
			agent(a)
			return channel, nil
		},
		CreateChannelFunc: func(_ context.Context, a gtm.Actor, in gtm.CreateChannelInput) (gtm.Channel, error) {
			agent(a)
			return channel, nil
		},
		UpdateChannelFunc: func(_ context.Context, a gtm.Actor, in gtm.UpdateChannelInput) (gtm.Channel, error) {
			agent(a)
			return channel, nil
		},
		GetPositioningFunc: func(_ context.Context, a gtm.Actor) (gtm.Positioning, error) {
			agent(a)
			return gtm.Positioning{PositioningMD: "Agents for teams.", Audiences: []string{"founders"}, Revision: 4, UpdatedAt: gtmNow()}, nil
		},
		CheckClaimsFunc: func(_ context.Context, a gtm.Actor, in gtm.CheckClaimsInput) (gtm.CheckClaimsResult, error) {
			agent(a)
			return gtm.CheckClaimsResult{Blocking: true, Findings: []gtm.ClaimFinding{{Kind: gtm.FindingUnmatchedRisky, Mention: "free forever", Blocking: true}}}, nil
		},
		ReadProductAnalyticsFunc: func(_ context.Context, a gtm.Actor, q gtm.AnalyticsQuery) (gtm.AnalyticsResult, error) {
			agent(a)
			return gtm.AnalyticsResult{Template: q.Template, From: q.From, To: q.To, HiddenGroups: 2, Rows: []gtm.AnalyticsRow{{Event: "run.started", Value: "", Count: 12}}}, nil
		},
		AttachFromAttachmentFunc: func(_ context.Context, a gtm.Actor, in gtm.AttachFromAttachmentInput) (gtm.Asset, error) {
			agent(a)
			return gtm.Asset{ID: "asset-1", ItemID: in.ItemID, Filename: "hero.png", ContentType: "image/png", SizeBytes: 10, Status: gtm.AssetReady, Source: gtm.AssetSourceChat}, nil
		},
	}
}

// lastInput is the input of the most recent call to method.
func lastInput[T any](t *testing.T, m *gtm.MockService, method string) T {
	t.Helper()
	calls := m.Calls(method)
	require.NotEmpty(t, calls, "%s was not called", method)
	in, ok := calls[len(calls)-1].Args[2].(T)
	require.True(t, ok, "%s input is %T", method, calls[len(calls)-1].Args[2])
	return in
}

func TestGTMTools_HappyPaths(t *testing.T) {
	for _, tc := range []struct {
		tool, input string
		check       func(t *testing.T, m *gtm.MockService, out map[string]any)
	}{
		{ToolListGtmIdeas, `{"status":"PROPOSED","campaign_id":"camp-1"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.ListIdeasInput](t, m, "ListIdeas")
			assert.Equal(t, gtm.IdeaProposed, in.Status)
			assert.Equal(t, "camp-1", *in.CampaignID)
			idea := out["ideas"].([]any)[0].(map[string]any)
			assert.Equal(t, "PROPOSED", idea["status"])
			assert.Equal(t, true, idea["body_truncated"], "a list shows the start of a body")
			assert.Equal(t, "next", out["next_page_token"])
		}},
		{ToolListGtmIdeas, `{"id":"idea-1"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Len(t, out["body_md"], 400, "one idea is whole")
			assert.Equal(t, float64(1), out["revision"])
		}},
		{ToolSaveGtmIdea, `{"kind":"CONTENT","title":"Post about runs","evidence":[{"kind":"analytics","title":"runs up"}]}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.CreateIdeaInput](t, m, "CreateIdea")
			assert.Equal(t, gtm.IdeaKindContent, in.Kind)
			assert.Equal(t, []gtm.Evidence{{Kind: "analytics", Title: "runs up"}}, in.Evidence)
			assert.Equal(t, "idea-1", out["id"])
		}},
		{ToolSaveGtmIdea, `{"id":"idea-1","expected_revision":1,"campaign_id":""}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.UpdateIdeaInput](t, m, "UpdateIdea")
			assert.Equal(t, int32(1), in.ExpectedRevision)
			require.NotNil(t, in.CampaignID)
			assert.Empty(t, *in.CampaignID, "an empty campaign_id unlinks")
			assert.Nil(t, in.Title, "an omitted field is left alone")
		}},
		{ToolListGtmStrategies, `{"status":"DRAFT"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Equal(t, gtm.StrategyDraft, lastInput[gtm.ListStrategiesInput](t, m, "ListStrategies").Status)
			assert.Equal(t, "ORGANIC_CONTENT_SEO", out["strategies"].([]any)[0].(map[string]any)["category"])
		}},
		{ToolListGtmStrategies, `{"id":"strat-1"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Len(t, out["campaigns"], 1)
		}},
		{ToolSaveGtmStrategy, `{"name":"Grow weekly orgs","category":"ORGANIC_CONTENT_SEO","period_start":"2026-12-01"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.CreateStrategyInput](t, m, "CreateStrategy")
			assert.Equal(t, gtm.StrategyDraft, in.Status, "an agent's strategy is a draft")
			assert.Equal(t, gtm.CategoryOrganicContentSEO, in.Category)
			assert.Equal(t, "2026-12-01", in.PeriodStart)
		}},
		{ToolSaveGtmStrategy, `{"from_library_key":"sample-play","create_first_campaign":true}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.UseLibraryStrategyInput](t, m, "UseLibraryStrategy")
			assert.Equal(t, gtm.UseLibraryStrategyInput{LibraryKey: "sample-play", CreateFirstCampaign: true}, in)
			assert.NotNil(t, out["campaign"])
		}},
		{ToolSaveGtmStrategy, `{"id":"strat-1","expected_revision":1,"goal_md":"More orgs"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.UpdateStrategyInput](t, m, "UpdateStrategy")
			assert.Equal(t, "More orgs", *in.GoalMD)
			assert.Nil(t, in.Status, "an agent never moves a strategy's status")
		}},
		{ToolListGtmStrategyLibrary, `{"category":"LAUNCHES_PR","depth":"PLAY","query":"launch"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.ListStrategyLibraryInput](t, m, "ListStrategyLibrary")
			assert.Equal(t, gtm.CategoryLaunchesPR, in.Category)
			assert.Equal(t, gtm.DepthPlay, in.Depth)
			assert.Equal(t, "sample-play", out["entries"].([]any)[0].(map[string]any)["key"])
		}},
		{ToolListGtmStrategyLibrary, `{"key":"sample-play"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Equal(t, []any{"signups"}, out["kpis"])
		}},
		{ToolListGtmCampaigns, `{"strategy_id":"strat-1"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Equal(t, "strat-1", *lastInput[gtm.ListCampaignsInput](t, m, "ListCampaigns").StrategyID)
		}},
		{ToolListGtmCampaigns, `{"id":"camp-1"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			cov := out["coverage"].([]any)
			require.Len(t, cov, 2)
			assert.Equal(t, map[string]any{"channel_id": "ch-reddit", "format": "SHORT_POST", "planned": true, "total": float64(0)}, cov[0], "planned, nothing drafted")
			assert.Equal(t, map[string]any{"DRAFT": float64(2)}, cov[1].(map[string]any)["counts"])
			assert.Len(t, out["items"], 1)
		}},
		{ToolSaveGtmCampaign, `{"name":"Launch","strategy_id":"strat-1","planned_channel_ids":["ch-reddit"],"planned_formats":["SHORT_POST","email"],"period_start":"2026-12-01"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.CreateCampaignInput](t, m, "CreateCampaign")
			assert.Equal(t, "strat-1", in.StrategyID)
			assert.Equal(t, gtm.CampaignDraft, in.Status)
			assert.Equal(t, []string{"ch-reddit"}, in.PlannedChannelIDs)
			assert.Equal(t, []gtm.ItemFormat{gtm.FormatShortPost, gtm.FormatEmail}, in.PlannedFormats)
			assert.Equal(t, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), *in.PeriodStart)
		}},
		{ToolSaveGtmCampaign, `{"id":"camp-1","expected_revision":2,"planned_formats":["VIDEO"]}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.UpdateCampaignInput](t, m, "UpdateCampaign")
			require.NotNil(t, in.PlannedFormats)
			assert.Equal(t, []gtm.ItemFormat{gtm.FormatVideo}, *in.PlannedFormats, "a list replaces the planned formats")
			assert.Nil(t, in.Status)
			assert.Nil(t, in.PlannedChannelIDs)
		}},
		{ToolListGtmCalendar, `{"from":"2026-12-01","to":"2027-01-01","format":"SHORT_POST","channel_id":"ch-reddit"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.ListItemsInput](t, m, "ListItems")
			assert.Equal(t, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), *in.ScheduledFrom)
			assert.Equal(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), *in.ScheduledTo)
			assert.Equal(t, gtm.FormatShortPost, in.Format)
			assert.Equal(t, "ch-reddit", *in.ChannelID)
			assert.Equal(t, "2026-11-28T12:00:00Z", out["items"].([]any)[0].(map[string]any)["scheduled_at"])
		}},
		{ToolListGtmCalendar, `{"id":"item-1"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Equal(t, "Hello", out["body_md"])
		}},
		{ToolSaveGtmItem, `{"format":"SHORT_POST","title":"Launch post","channel_id":"ch-reddit","scheduled_at":"2026-11-28T12:00:00Z","status":"REVIEW"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.CreateItemInput](t, m, "CreateItem")
			assert.Equal(t, gtm.ItemReview, in.Status)
			assert.Equal(t, gtm.FormatShortPost, in.Format)
			assert.Equal(t, "ch-reddit", in.ChannelID)
			assert.Equal(t, gtmNow().Add(48*time.Hour), *in.ScheduledAt)
		}},
		{ToolSaveGtmItem, `{"id":"item-1","expected_revision":1,"email_to":["ann@example.com"],"clear_scheduled_at":true}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.UpdateItemInput](t, m, "UpdateItem")
			assert.Equal(t, []string{"ann@example.com"}, *in.EmailTo)
			assert.True(t, in.ClearScheduledAt)
			assert.Nil(t, in.Status)
			assert.Nil(t, in.PublishedURL, "an agent never records a published URL")
		}},
		{ToolListGtmChannels, `{"mode":"MANUAL"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Equal(t, gtm.ChannelManual, lastInput[gtm.ListChannelsInput](t, m, "ListChannels").Mode)
			assert.Equal(t, "reddit", out["channels"].([]any)[0].(map[string]any)["key"])
		}},
		{ToolListGtmChannels, `{"id":"ch-reddit"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Equal(t, "MANUAL", out["mode"])
		}},
		{ToolSaveGtmChannel, `{"key":"indie-hackers","name":"Indie Hackers","manual_instructions_md":"Post by hand.","channel_types":["forum"]}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.CreateChannelInput](t, m, "CreateChannel")
			assert.Equal(t, gtm.CreateChannelInput{Key: "indie-hackers", Name: "Indie Hackers", Mode: gtm.ChannelManual, ManualInstructionsMD: "Post by hand.", ChannelTypes: []string{"forum"}}, in,
				"an agent's channel is MANUAL, with no integration")
		}},
		{ToolSaveGtmChannel, `{"id":"ch-reddit","expected_revision":1,"status":"PAUSED"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			in := lastInput[gtm.UpdateChannelInput](t, m, "UpdateChannel")
			assert.Equal(t, gtm.ChannelPaused, *in.Status)
			assert.Nil(t, in.Mode)
			assert.Nil(t, in.IntegrationKind)
			assert.Nil(t, in.ConnectionID)
		}},
		{ToolGetGtmPositioning, `{}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Equal(t, "Agents for teams.", out["positioning_md"])
			assert.Equal(t, float64(4), out["revision"])
		}},
		{ToolCheckGtmClaims, `{"text":"Free forever."}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Equal(t, "Free forever.", lastInput[gtm.CheckClaimsInput](t, m, "CheckClaims").Text)
			assert.Equal(t, true, out["blocking"])
			assert.Equal(t, "UNMATCHED_RISKY", out["findings"].([]any)[0].(map[string]any)["kind"])
		}},
		{ToolCheckGtmClaims, `{"item_id":"item-1"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Equal(t, "Launch post\n\nHello", lastInput[gtm.CheckClaimsInput](t, m, "CheckClaims").Text)
		}},
		{ToolReadProductAnalytics, `{"template":"property_breakdown","events":["run.started"],"property":"plan","to":"2026-11-26","days":14}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			q := lastInput[gtm.AnalyticsQuery](t, m, "ReadProductAnalytics")
			assert.Equal(t, gtm.AnalyticsPropertyBreakdown, q.Template)
			assert.Equal(t, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), q.From)
			assert.Equal(t, float64(2), out["hidden_groups"])
			assert.Equal(t, map[string]any{"event": "run.started", "value": "", "count": float64(12)}, out["rows"].([]any)[0])
		}},
		{ToolAttachGtmAsset, `{"attachment_id":"att-1","item_id":"item-1"}`, func(t *testing.T, m *gtm.MockService, out map[string]any) {
			assert.Equal(t, gtm.AttachFromAttachmentInput{AttachmentID: "att-1", ItemID: "item-1"}, lastInput[gtm.AttachFromAttachmentInput](t, m, "AttachFromAttachment"))
			assert.Equal(t, "READY", out["status"])
		}},
	} {
		t.Run(tc.tool+" "+tc.input, func(t *testing.T) {
			m := gtmMock(t)
			out := decodeGTM(t, runGTM(t, newGTMTestTool(tc.tool, m, nil), gtmTestChat, tc.input))
			tc.check(t, m, out)
		})
	}
}

// What an agent may not do is not on the tools' surface at all: the schema
// refuses it before the service is asked.
func TestGTMTools_AgentLimitsOffTheSurface(t *testing.T) {
	for _, tc := range []struct{ tool, input string }{
		{ToolSaveGtmIdea, `{"id":"idea-1","expected_revision":1,"status":"ACCEPTED"}`},
		{ToolSaveGtmItem, `{"id":"item-1","expected_revision":1,"status":"APPROVED"}`},
		{ToolSaveGtmItem, `{"id":"item-1","expected_revision":1,"status":"PUBLISHED"}`},
		{ToolSaveGtmItem, `{"id":"item-1","expected_revision":1,"status":"SCHEDULED"}`},
		{ToolSaveGtmItem, `{"id":"item-1","expected_revision":1,"status":"SENT"}`},
		{ToolSaveGtmItem, `{"id":"item-1","expected_revision":1,"published_url":"https://example.com/p"}`},
		{ToolSaveGtmStrategy, `{"id":"strat-1","expected_revision":1,"status":"ACTIVE"}`},
		{ToolSaveGtmCampaign, `{"id":"camp-1","expected_revision":1,"status":"ACTIVE"}`},
		{ToolSaveGtmChannel, `{"key":"mail","name":"Mail","mode":"INTEGRATED","integration_kind":"gmail"}`},
		{ToolSaveGtmChannel, `{"id":"ch-1","expected_revision":1,"connection_id":"conn-1"}`},
		{ToolCheckGtmClaims, `{"text":"x","verify":true}`},
	} {
		t.Run(tc.tool+" "+tc.input, func(t *testing.T) {
			m := gtmMock(t)
			resp := runGTM(t, newGTMTestTool(tc.tool, m, nil), gtmTestChat, tc.input)
			assert.True(t, resp.IsError, resp.Content)
			for _, method := range []string{"UpdateIdea", "UpdateItem", "UpdateStrategy", "UpdateCampaign", "CreateChannel", "UpdateChannel", "CheckClaims"} {
				assert.Zero(t, m.CallCount(method), method)
			}
		})
	}
}

// --- the real service: agent limits and the gate --------------------------------

// gtmRealService is gtm.New over a store holding one row of each kind in
// tenant zero, behind a gate that admits tenant zero only.
func gtmRealService(rows gtmRows) (gtm.Service, *gtm.MockStore) {
	notFound := func() error { return gtm.ErrNotFound }
	st := &gtm.MockStore{
		InTxFunc: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
		GetIdeaFunc: func(_ context.Context, org, id string) (*store.GtmIdea, error) {
			if r := rows.idea; r != nil && r.OrgID == org && r.ID == id {
				c := *r
				return &c, nil
			}
			return nil, notFound()
		},
		GetItemFunc: func(_ context.Context, org, id string) (*store.GtmItem, error) {
			if r := rows.item; r != nil && r.OrgID == org && r.ID == id {
				c := *r
				return &c, nil
			}
			return nil, notFound()
		},
		GetStrategyFunc: func(_ context.Context, org, id string) (*store.GtmStrategy, error) {
			if r := rows.strategy; r != nil && r.OrgID == org && r.ID == id {
				c := *r
				return &c, nil
			}
			return nil, notFound()
		},
		GetChannelFunc: func(_ context.Context, org, id string) (*store.GtmChannel, error) {
			if r := rows.channel; r != nil && r.OrgID == org && r.ID == id {
				c := *r
				return &c, nil
			}
			return nil, notFound()
		},
		GetCampaignFunc: func(context.Context, string, string) (*store.GtmCampaign, error) { return nil, notFound() },
		GetLibraryEntryFunc: func(context.Context, string, string) (*store.GtmStrategyLibrary, error) {
			return nil, notFound()
		},
		CountChannelsFunc: func(context.Context, string) (int64, error) { return 1, nil },
	}
	svc := gtm.New(gtm.Deps{
		Store: st,
		Gate: &gtm.MockGate{AllowedFunc: func(_ context.Context, f featuregate.Feature, _, org string) (bool, error) {
			return f == featuregate.GTM && org == gtmTestOrg, nil
		}},
		Clock: &gtm.MockClock{NowFunc: gtmNow},
	})
	return svc, st
}

type gtmRows struct {
	idea     *store.GtmIdea
	item     *store.GtmItem
	strategy *store.GtmStrategy
	channel  *store.GtmChannel
}

// An agent is refused, by the service's own rules, what is a person's: it
// edits only PROPOSED ideas, DRAFT/REVIEW items, DRAFT strategies and MANUAL
// channels. The refusal says so, and nothing is written.
func TestGTMTools_AgentStatusLimitsRefused(t *testing.T) {
	gmail := gtm.GmailIntegration
	rows := gtmRows{
		idea:     &store.GtmIdea{ID: "idea-1", OrgID: gtmTestOrg, Kind: string(gtm.IdeaKindGTM), Title: "t", Status: string(gtm.IdeaAccepted), Source: string(gtm.IdeaSourceAgent), Revision: 3, Evidence: []byte("[]")},
		item:     &store.GtmItem{ID: "item-1", OrgID: gtmTestOrg, Format: string(gtm.FormatLongPost), Title: "t", Status: string(gtm.ItemApproved), Revision: 2},
		strategy: &store.GtmStrategy{ID: "strat-1", OrgID: gtmTestOrg, Name: "s", Status: string(gtm.StrategyActive), Revision: 5},
		channel:  &store.GtmChannel{ID: "ch-1", OrgID: gtmTestOrg, Key: "email", Name: "Email", Mode: string(gtm.ChannelIntegrated), IntegrationKind: &gmail, Status: string(gtm.ChannelActive), Revision: 1},
	}
	for _, tc := range []struct {
		tool, input, write string
	}{
		{ToolSaveGtmIdea, `{"id":"idea-1","expected_revision":3,"title":"new"}`, "UpdateIdea"},
		{ToolSaveGtmItem, `{"id":"item-1","expected_revision":2,"body_md":"new"}`, "UpdateItem"},
		{ToolSaveGtmItem, `{"id":"item-1","expected_revision":2,"status":"DRAFT"}`, "UpdateItem"},
		{ToolSaveGtmStrategy, `{"id":"strat-1","expected_revision":5,"name":"new"}`, "UpdateStrategy"},
		{ToolSaveGtmChannel, `{"id":"ch-1","expected_revision":1,"name":"new"}`, "UpdateChannel"},
	} {
		t.Run(tc.tool+" "+tc.input, func(t *testing.T) {
			svc, st := gtmRealService(rows)
			resp := runGTM(t, newGTMTestTool(tc.tool, svc, nil), gtmTestChat, tc.input)
			require.True(t, resp.IsError, resp.Content)
			assert.Contains(t, resp.Content, "Only a person can")
			assert.True(t, strings.HasSuffix(resp.Content, "Nothing was saved."), resp.Content)
			assert.Zero(t, st.CallCount(tc.write), "nothing was written")
		})
	}
}

// A caller the GTM gate refuses (an org that is not allowlisted, even one
// whose turn still offered the tool) reads exactly as a missing row: the
// same text, byte for byte.
func TestGTMTools_GateRefusalReadsAsMissing(t *testing.T) {
	svc, _ := gtmRealService(gtmRows{})
	for _, tc := range []struct{ tool, input string }{
		{ToolListGtmIdeas, `{"id":"idea-9"}`},
		{ToolSaveGtmIdea, `{"id":"idea-9","expected_revision":1,"title":"x"}`},
		{ToolListGtmStrategies, `{"id":"strat-9"}`},
		{ToolSaveGtmStrategy, `{"id":"strat-9","expected_revision":1,"name":"x"}`},
		{ToolListGtmStrategyLibrary, `{"key":"play-9"}`},
		{ToolListGtmCampaigns, `{"id":"camp-9"}`},
		{ToolListGtmCalendar, `{"id":"item-9"}`},
		{ToolSaveGtmItem, `{"id":"item-9","expected_revision":1,"title":"x"}`},
		{ToolListGtmChannels, `{"id":"ch-9"}`},
		{ToolSaveGtmChannel, `{"id":"ch-9","expected_revision":1,"name":"x"}`},
		{ToolCheckGtmClaims, `{"item_id":"item-9"}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			tool := newGTMTestTool(tc.tool, svc, nil)
			missing := runGTM(t, tool, gtmTestChat, tc.input)
			refused := runGTM(t, tool, gtmOtherChat, tc.input)
			assert.True(t, missing.IsError)
			assert.Contains(t, missing.Content, "never guess")
			assert.Equal(t, missing, refused)
		})
	}
}

// --- send_gtm_email, over a fake sender --------------------------------------

// fakeSender stands in for internal/gtm/email's Approver: it records what it
// was asked, and answers with an opaque composite hash.
type fakeSender struct {
	prepareErr error
	sendErr    error
	prepared   []bool
	runs       []gtm.EmailRun
	actors     []gtm.Actor
	approvals  []string
}

func (f *fakeSender) PrepareSend(_ context.Context, actor gtm.Actor, itemID string, rev int32, unattended bool) (string, string, error) {
	f.actors = append(f.actors, actor)
	f.prepared = append(f.prepared, unattended)
	if f.prepareErr != nil {
		return "", "", f.prepareErr
	}
	return "Send email “Launch” to 1 recipient?", "itemhash." + itemID + "." + string(rune('0'+rev)), nil
}

func (f *fakeSender) SendApproved(ctx context.Context, actor gtm.Actor, itemID string, rev int32, run gtm.EmailRun) (gtm.Item, error) {
	f.actors = append(f.actors, actor)
	f.runs = append(f.runs, run)
	f.approvals = append(f.approvals, automationpolicy.From(ctx).Approvals[run.ToolCallID])
	if f.sendErr != nil {
		return gtm.Item{}, f.sendErr
	}
	return gtm.Item{ID: itemID, Title: "Launch", Format: gtm.FormatEmail, Status: gtm.ItemSent, Revision: rev + 1}, nil
}

func gtmToolCtx(ctx context.Context) *rctx.ToolContext {
	return rctx.NewToolContext(ctx, gtmTestChat, "thread-1", nil, nil)
}

func TestSendGtmEmail_AttendedAlwaysShowsTheCard(t *testing.T) {
	sender := &fakeSender{}
	tool := newGTMTestTool(ToolSendGtmEmail, nil, sender).(AutomationApprovalTool)
	preview, needs, err := tool.PrepareAutomation(gtmToolCtx(context.Background()), ToolCall{ID: "call-1", Name: ToolSendGtmEmail, Input: `{"item_id":"item-1","expected_revision":3}`}, false)
	require.NoError(t, err)
	assert.True(t, needs, "every send needs a card")
	assert.Equal(t, ActivationPreview{Title: "Send email “Launch” to 1 recipient?", Hash: "itemhash.item-1.3"}, preview, "the card and its opaque hash pass through")
	assert.Equal(t, []bool{false}, sender.prepared)
	assert.Equal(t, []gtm.Actor{gtmAgent}, sender.actors)
}

func TestSendGtmEmail_RefusalsAreActivationRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		sender     GTMEmailSender
		unattended bool
		input      string
		reason     string
	}{
		"unattended": {&fakeSender{prepareErr: errors.New("an unattended run cannot show a card; nothing was sent")}, true,
			`{"item_id":"item-1","expected_revision":3}`, "an unattended run cannot show a card; nothing was sent"},
		"not approved": {&fakeSender{prepareErr: gtm.ErrNotApproved}, false, `{"item_id":"item-1","expected_revision":3}`, gtm.ErrNotApproved.Error()},
		"no sender":    {nil, false, `{"item_id":"item-1","expected_revision":3}`, "GTM email is unavailable on this server"},
		"a mailbox named by the model": {&fakeSender{}, false, `{"item_id":"item-1","expected_revision":3,"connection_id":"conn-x"}`,
			"tool input is not a valid send_gtm_email call"},
		"a hash named by the model": {&fakeSender{}, false, `{"item_id":"item-1","expected_revision":3,"approval_hash":"h"}`,
			"tool input is not a valid send_gtm_email call"},
		"a blank hash": {&blankHashSender{}, false, `{"item_id":"item-1","expected_revision":3}`, "the send could not be bound to an approval"},
	} {
		t.Run(name, func(t *testing.T) {
			tool := newGTMTestTool(ToolSendGtmEmail, nil, tc.sender).(AutomationApprovalTool)
			preview, needs, err := tool.PrepareAutomation(gtmToolCtx(context.Background()), ToolCall{ID: "call-1", Name: ToolSendGtmEmail, Input: tc.input}, tc.unattended)
			require.Error(t, err)
			assert.False(t, needs)
			assert.Zero(t, preview)
			assert.Equal(t, tc.reason, ActivationRefusalReason(err))
		})
	}
	t.Run("unattended reaches the sender as unattended", func(t *testing.T) {
		sender := &fakeSender{prepareErr: errors.New("unattended")}
		tool := newGTMTestTool(ToolSendGtmEmail, nil, sender).(AutomationApprovalTool)
		_, _, _ = tool.PrepareAutomation(gtmToolCtx(context.Background()), ToolCall{ID: "call-1", Input: `{"item_id":"item-1","expected_revision":3}`}, true)
		assert.Equal(t, []bool{true}, sender.prepared)
	})
}

type blankHashSender struct{ fakeSender }

func (*blankHashSender) PrepareSend(context.Context, gtm.Actor, string, int32, bool) (string, string, error) {
	return "title", "", nil
}

// The call id that keys the approval is the runtime's tool call id, never
// a model value, and the run is the thread's.
func TestSendGtmEmail_ExecuteUsesTheRuntimeCallID(t *testing.T) {
	sender := &fakeSender{}
	tool := newGTMTestTool(ToolSendGtmEmail, nil, sender)
	ctx := automationpolicy.WithExecution(context.Background(), automationpolicy.Execution{Approvals: map[string]string{"call-1": "itemhash.item-1.3"}})
	resp, err := tool.Run(gtmToolCtx(ctx), ToolCall{ID: "call-1", Name: ToolSendGtmEmail, Input: `{"item_id":"item-1","expected_revision":3}`})
	require.NoError(t, err)
	out := decodeGTM(t, resp)
	assert.Equal(t, true, out["sent"])
	assert.Equal(t, "SENT", out["status"])
	require.Len(t, sender.runs, 1)
	assert.Equal(t, gtm.EmailRun{RunID: "thread-1", ToolCallID: "call-1"}, sender.runs[0], "no mailbox is named: the item's channel or the owner's default is")
	assert.Equal(t, []string{"itemhash.item-1.3"}, sender.approvals, "the sender reads the approval for this call from the runtime")

	resp, err = tool.Run(gtmToolCtx(ctx), ToolCall{ID: "call-2", Name: ToolSendGtmEmail, Input: `{"item_id":"item-1","expected_revision":3,"tool_call_id":"call-1"}`})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "a model cannot name the call id")
	assert.Len(t, sender.runs, 1)

	sender.sendErr = errors.New("sending a GTM email needs the owner's approval card for exactly this email; nothing was sent")
	resp, err = tool.Run(gtmToolCtx(ctx), ToolCall{ID: "call-2", Name: ToolSendGtmEmail, Input: `{"item_id":"item-1","expected_revision":3}`})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Equal(t, "call-2", sender.runs[1].ToolCallID)
	assert.Empty(t, sender.approvals[1], "call-2 has no approval of its own")
}

func TestSendGtmEmail_NoSenderSendsNothing(t *testing.T) {
	resp := runGTM(t, newGTMTestTool(ToolSendGtmEmail, nil, nil), gtmTestChat, `{"item_id":"item-1","expected_revision":3}`)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "Nothing was sent")
}

func TestGTMTools_NoServiceIsUnavailable(t *testing.T) {
	resp := runGTM(t, NewToolsFactory(&ToolsOptions{}).ListGtmIdeas(), gtmTestChat, `{}`)
	assert.True(t, resp.IsError)
}
