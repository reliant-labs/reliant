// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package gtm_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

func TestListStrategyLibrary_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListStrategyLibraryRequest, pb.ListStrategyLibraryResponse]{
		{
			Name: "maps the filters and the entries",
			Req: connect.NewRequest(&pb.ListStrategyLibraryRequest{
				OrgId:          testOrg,
				Categories:     []pb.GtmStrategyCategory{pb.GtmStrategyCategory_GTM_STRATEGY_CATEGORY_COMMUNITY_EVENTS},
				BusinessTypes:  []pb.GtmBusinessType{pb.GtmBusinessType_GTM_BUSINESS_TYPE_B2B_SAAS},
				BusinessModels: []pb.GtmBusinessModel{pb.GtmBusinessModel_GTM_BUSINESS_MODEL_SUBSCRIPTION},
				Stage:          proto.String("launch"), Objective: proto.String("awareness"),
				Effort: proto.String("low"), Cost: proto.String("free"), Query: proto.String("meetup"),
				FitsMyBusiness: proto.Bool(true), Diverse: proto.Bool(true), PageSize: 10, PageToken: "p",
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListStrategyLibraryFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListStrategyLibraryInput) (gtmsvc.ListStrategyLibraryResult, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.ListStrategyLibraryInput{
						Categories:     []gtmsvc.StrategyCategory{gtmsvc.CategoryCommunityEvents},
						BusinessTypes:  []gtmsvc.BusinessType{gtmsvc.BusinessB2BSaaS},
						BusinessModels: []gtmsvc.BusinessModel{gtmsvc.ModelSubscription},
						Stage:          "launch", Objective: "awareness", Effort: "low", Cost: "free", Query: "meetup",
						FitsMyBusiness: true, Diverse: true, PageSize: 10, PageToken: "p",
					}, in)
					return gtmsvc.ListStrategyLibraryResult{Entries: []gtmsvc.LibraryEntry{{
						Key: "local-meetup", Category: gtmsvc.CategoryCommunityEvents, Name: "Host a meetup",
						SummaryMD: "s", WhyItWorksMD: "w",
						BusinessTypes:  []gtmsvc.BusinessType{gtmsvc.BusinessDeveloperTools},
						BusinessModels: []gtmsvc.BusinessModel{gtmsvc.ModelFreeOSS},
						Stages:         []string{"launch"}, Objective: "awareness", Effort: "medium", Cost: "low", SignalDays: 30,
						TailorQuestions: []string{"Where?"}, SuggestedWorkflowMD: "wf", ChannelTypes: []string{"events"}, KPIs: []string{"rsvps"},
					}}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListStrategyLibraryResponse]) {
				assert.Empty(t, resp.Msg.GetNextPageToken())
				e := resp.Msg.GetEntries()[0]
				assert.Equal(t, pb.GtmStrategyCategory_GTM_STRATEGY_CATEGORY_COMMUNITY_EVENTS, e.GetCategory())
				assert.Equal(t, []pb.GtmBusinessType{pb.GtmBusinessType_GTM_BUSINESS_TYPE_DEVELOPER_TOOLS}, e.GetBusinessTypes())
				assert.Equal(t, []pb.GtmBusinessModel{pb.GtmBusinessModel_GTM_BUSINESS_MODEL_FREE_OSS}, e.GetBusinessModels())
				assert.Equal(t, "w", e.GetWhyItWorksMd())
				assert.Equal(t, []string{"launch"}, e.GetStages())
				assert.Equal(t, "medium", e.GetEffort())
				assert.Equal(t, int32(30), e.GetSignalDays())
				assert.Equal(t, []string{"Where?"}, e.GetTailorQuestions())
				assert.Equal(t, "wf", e.GetSuggestedWorkflowMd())
				assert.Equal(t, []string{"rsvps"}, e.GetKpis())
			},
		},
		{
			Name: "an uppercase effort breaks the rules: InvalidArgument for an admitted caller",
			Req:  connect.NewRequest(&pb.ListStrategyLibraryRequest{OrgId: testOrg, Effort: proto.String("LOW")}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) { return gtmsvc.Access{Enabled: true}, nil }
			},
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.ListStrategyLibraryRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListStrategyLibraryFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ListStrategyLibraryInput) (gtmsvc.ListStrategyLibraryResult, error) {
					return gtmsvc.ListStrategyLibraryResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListStrategyLibrary)
}
