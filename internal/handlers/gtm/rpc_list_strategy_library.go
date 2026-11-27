package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListStrategyLibrary lists the embedded idea bank, filtered: categories,
// business types and models, the bank's lowercase stage, objective, effort
// and cost, a text query, fits_my_business (the org's positioning) and
// diverse (round-robin across categories, unpaged).
func (s *Service) ListStrategyLibrary(
	ctx context.Context,
	req *connect.Request[pb.ListStrategyLibraryRequest],
) (*connect.Response[pb.ListStrategyLibraryResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ListStrategyLibrary(ctx, a, gtmsvc.ListStrategyLibraryInput{
		Categories:     enumsIn[gtmsvc.StrategyCategory](m.GetCategories()),
		BusinessTypes:  enumsIn[gtmsvc.BusinessType](m.GetBusinessTypes()),
		BusinessModels: enumsIn[gtmsvc.BusinessModel](m.GetBusinessModels()),
		Stage:          m.GetStage(),
		Objective:      m.GetObjective(),
		Effort:         m.GetEffort(),
		Cost:           m.GetCost(),
		Query:          m.GetQuery(),
		FitsMyBusiness: m.GetFitsMyBusiness(),
		Diverse:        m.GetDiverse(),
		PageSize:       int(m.GetPageSize()),
		PageToken:      m.GetPageToken(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListStrategyLibraryResponse{
		Entries:       libraryEntriesOut(res.Entries),
		NextPageToken: res.NextPageToken,
	}), nil
}
