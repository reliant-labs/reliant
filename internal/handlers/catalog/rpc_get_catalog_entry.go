package catalog

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
)

func (s *Service) GetCatalogEntry(ctx context.Context, req *connect.Request[reliantv1.GetCatalogEntryRequest]) (*connect.Response[reliantv1.GetCatalogEntryResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.Search == nil {
		return nil, svcerr.Unimplemented("integration catalog search is not configured")
	}
	if req.Msg.GetRef() == "" {
		return nil, svcerr.InvalidArgument("ref is required")
	}
	e, connected, err := s.deps.Search.Get(ctx, userID, req.Msg.GetRef())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("catalog search failed"))
	}
	entry, err := CatalogEntryToProto(e, connected)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("catalog entry schema could not be encoded"))
	}
	var methods *connections.Integration
	if s.deps.IntegrationMethods != nil {
		if in, ok := s.deps.IntegrationMethods(e.IntegrationID()); ok {
			methods = &in
		}
	}
	entry.Connection = catalogConnectionToProto(e, methods)
	return connect.NewResponse(&reliantv1.GetCatalogEntryResponse{Entry: entry}), nil
}

// CatalogEntryToProto is the full wire shape of one entry, without the
// deployment-dependent connection requirement (the handler adds that).
func CatalogEntryToProto(e *catalogindex.Entry, connected bool) (*reliantv1.CatalogEntry, error) {
	out := &reliantv1.CatalogEntry{
		Summary:     CatalogEntrySummaryToProto(e, connected),
		Description: e.Description,
		ToolName:    e.ToolName(),
	}
	if e.Kind == catalogindex.KindAction {
		out.ParamOrder = e.Action.GetParamOrder()
		params, output := e.Schemas()
		var err error
		if out.ParamsSchema, err = structpb.NewStruct(params); err != nil {
			return nil, fmt.Errorf("params schema: %w", err)
		}
		if out.OutputSchema, err = structpb.NewStruct(output); err != nil {
			return nil, fmt.Errorf("output schema: %w", err)
		}
	}
	if value := e.ValueSchema(); value != nil {
		var err error
		if out.ValueSchema, err = structpb.NewStruct(value); err != nil {
			return nil, fmt.Errorf("value schema: %w", err)
		}
	}
	if payload := e.PayloadSchema(); payload != nil {
		var err error
		if out.PayloadSchema, err = structpb.NewStruct(payload); err != nil {
			return nil, fmt.Errorf("payload schema: %w", err)
		}
	}
	return out, nil
}

// catalogConnectionToProto describes what a connection to the entry's
// integration needs. methods, when known, carries each method's availability
// on this deployment; otherwise every declared method is listed as available.
func catalogConnectionToProto(e *catalogindex.Entry, methods *connections.Integration) *reliantv1.CatalogConnectionRequirement {
	out := &reliantv1.CatalogConnectionRequirement{Required: e.ConnectionRequired}
	if methods != nil {
		for _, m := range methods.Methods {
			out.Methods = append(out.Methods, &reliantv1.IntegrationAuthMethod{
				Kind: connectionrpc.AuthKind(m.Kind), Available: m.Available,
				UnavailableReason: m.Reason, FieldLabels: m.FieldLabels,
			})
		}
	} else {
		for _, k := range e.AuthKinds {
			out.Methods = append(out.Methods, &reliantv1.IntegrationAuthMethod{Kind: connectionrpc.AuthKind(k), Available: true})
		}
	}
	for _, p := range e.ConnectionSpec().GetConnectionParams() {
		out.ConnectionParams = append(out.ConnectionParams, &reliantv1.IntegrationConnectionParam{
			Name: p.GetName(), DisplayName: p.GetDisplayName(), Description: p.GetDescription(),
			Pattern: p.GetPattern(), DefaultValue: p.GetDefaultValue(), Required: p.GetDefaultValue() == "",
		})
	}
	return out
}
