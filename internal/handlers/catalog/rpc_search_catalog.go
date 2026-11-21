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

func (s *Service) SearchCatalog(ctx context.Context, req *connect.Request[reliantv1.SearchCatalogRequest]) (*connect.Response[reliantv1.SearchCatalogResponse], error) {
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
	q := catalogindex.Query{
		Text: req.Msg.GetQuery(), Category: req.Msg.GetCategory(), Integration: req.Msg.GetIntegration(),
		ConnectedOnly: req.Msg.GetConnectedOnly(), PageSize: int(req.Msg.GetPageSize()), PageToken: req.Msg.GetPageToken(),
	}
	for _, k := range req.Msg.GetKinds() {
		kind, ok := catalogKindFromProto(k)
		if !ok {
			return nil, svcerr.InvalidArgument(fmt.Sprintf("kinds: %s is not a catalog entry kind", k))
		}
		q.Kinds = append(q.Kinds, kind)
	}
	res, err := s.deps.Search.Search(ctx, userID, q)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("catalog search failed"))
	}
	out := &reliantv1.SearchCatalogResponse{
		Entries:        make([]*reliantv1.CatalogEntrySummary, 0, len(res.Hits)),
		NextPageToken:  res.NextPageToken,
		TotalSize:      int32(res.Total),
		CategoryFacets: make([]*reliantv1.CatalogFacet, 0, len(res.CategoryFacets)),
	}
	for _, h := range res.Hits {
		out.Entries = append(out.Entries, CatalogEntrySummaryToProto(h.Entry, h.Connected))
	}
	for _, f := range res.CategoryFacets {
		out.CategoryFacets = append(out.CategoryFacets, &reliantv1.CatalogFacet{Value: f.Value, Count: int32(f.Count)})
	}
	return connect.NewResponse(out), nil
}
