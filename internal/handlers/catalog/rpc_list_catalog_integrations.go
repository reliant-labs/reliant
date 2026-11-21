package catalog

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
)

func (s *Service) ListCatalogIntegrations(ctx context.Context, req *connect.Request[reliantv1.ListCatalogIntegrationsRequest]) (*connect.Response[reliantv1.ListCatalogIntegrationsResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.Search == nil {
		return nil, svcerr.Unimplemented("integration catalog search is not configured")
	}
	if req.Msg.GetPageSize() < 0 {
		return nil, svcerr.InvalidArgument("page_size must not be negative")
	}
	q := catalogindex.IntegrationQuery{
		Category: req.Msg.GetCategory(), PageSize: int(req.Msg.GetPageSize()), PageToken: req.Msg.GetPageToken(),
	}
	for _, k := range req.Msg.GetKinds() {
		kind, ok := catalogKindFromProto(k)
		if !ok {
			return nil, svcerr.InvalidArgument(fmt.Sprintf("kinds: %s is not a catalog entry kind", k))
		}
		q.Kinds = append(q.Kinds, kind)
	}
	res, err := s.deps.Search.ListIntegrations(ctx, userID, q)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("catalog search failed"))
	}
	out := &reliantv1.ListCatalogIntegrationsResponse{
		Integrations:   make([]*reliantv1.CatalogIntegrationListing, 0, len(res.Integrations)),
		NextPageToken:  res.NextPageToken,
		TotalSize:      int32(res.Total),
		CategoryFacets: make([]*reliantv1.CatalogFacet, 0, len(res.CategoryFacets)),
	}
	for _, l := range res.Integrations {
		out.Integrations = append(out.Integrations, &reliantv1.CatalogIntegrationListing{
			Integration: catalogIntegrationToProto(l.Manifest), EntryCount: int32(l.EntryCount), Connected: l.Connected,
		})
	}
	for _, f := range res.CategoryFacets {
		out.CategoryFacets = append(out.CategoryFacets, &reliantv1.CatalogFacet{Value: f.Value, Count: int32(f.Count)})
	}
	return connect.NewResponse(out), nil
}
