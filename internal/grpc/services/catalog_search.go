// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
)

// Integration catalog search lives on CatalogService rather than a new
// IntegrationService: CatalogService is already the "what can I use" surface
// the builder reads (ListNodes feeds its node picker, ListTools the tool
// picker), and integration actions and triggers are more things to pick. A
// second discovery service would split one picker across two clients.

// catalogSearcher is what the search RPCs need: the caller-aware index.
type catalogSearcher interface {
	Search(ctx context.Context, userID string, q catalogindex.Query) (*catalogindex.Result, error)
	Get(ctx context.Context, userID, ref string) (*catalogindex.Entry, bool, error)
	ListIntegrations(ctx context.Context, userID string, q catalogindex.IntegrationQuery) (*catalogindex.IntegrationResult, error)
}

// integrationMethods reports, for one integration, each declared auth method
// with whether this deployment offers it (connections.Service.ListIntegrations
// filtered to one id). nil reports every method as available with no detail.
type integrationMethods = func(integrationID string) (connections.Integration, bool)

// WithCatalogSearch enables SearchCatalog and GetCatalogEntry. Without it the
// two RPCs answer Unimplemented, which only a composition with no index (a
// test) does.
func (s *CatalogService) WithCatalogSearch(search catalogSearcher, methods integrationMethods) *CatalogService {
	s.search = search
	s.integrationMethods = methods
	return s
}

// IntegrationMethodsFrom adapts a connections service into the per-integration
// method lookup GetCatalogEntry reports.
func IntegrationMethodsFrom(svc *connections.Service) func(string) (connections.Integration, bool) {
	if svc == nil {
		return nil
	}
	return func(id string) (connections.Integration, bool) {
		for _, in := range svc.ListIntegrations() {
			if in.ID == id {
				return in, true
			}
		}
		return connections.Integration{}, false
	}
}

func (s *CatalogService) SearchCatalog(ctx context.Context, req *connect.Request[reliantv1.SearchCatalogRequest]) (*connect.Response[reliantv1.SearchCatalogResponse], error) {
	userID, err := connectionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if s.search == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("integration catalog search is not configured"))
	}
	if req.Msg.GetPageSize() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("page_size must not be negative"))
	}
	q := catalogindex.Query{
		Text: req.Msg.GetQuery(), Category: req.Msg.GetCategory(), Integration: req.Msg.GetIntegration(),
		ConnectedOnly: req.Msg.GetConnectedOnly(), PageSize: int(req.Msg.GetPageSize()), PageToken: req.Msg.GetPageToken(),
	}
	for _, k := range req.Msg.GetKinds() {
		kind, ok := catalogKindFromProto(k)
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("kinds: %s is not a catalog entry kind", k))
		}
		q.Kinds = append(q.Kinds, kind)
	}
	res, err := s.search.Search(ctx, userID, q)
	if err != nil {
		return nil, catalogSearchError(err)
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

func (s *CatalogService) GetCatalogEntry(ctx context.Context, req *connect.Request[reliantv1.GetCatalogEntryRequest]) (*connect.Response[reliantv1.GetCatalogEntryResponse], error) {
	userID, err := connectionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if s.search == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("integration catalog search is not configured"))
	}
	if req.Msg.GetRef() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("ref is required"))
	}
	e, connected, err := s.search.Get(ctx, userID, req.Msg.GetRef())
	if err != nil {
		return nil, catalogSearchError(err)
	}
	entry, err := CatalogEntryToProto(e, connected)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("catalog entry schema could not be encoded"))
	}
	var methods *connections.Integration
	if s.integrationMethods != nil {
		if in, ok := s.integrationMethods(e.IntegrationID()); ok {
			methods = &in
		}
	}
	entry.Connection = catalogConnectionToProto(e, methods)
	return connect.NewResponse(&reliantv1.GetCatalogEntryResponse{Entry: entry}), nil
}

func (s *CatalogService) ListCatalogIntegrations(ctx context.Context, req *connect.Request[reliantv1.ListCatalogIntegrationsRequest]) (*connect.Response[reliantv1.ListCatalogIntegrationsResponse], error) {
	userID, err := connectionCaller(ctx)
	if err != nil {
		return nil, err
	}
	if s.search == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("integration catalog search is not configured"))
	}
	if req.Msg.GetPageSize() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("page_size must not be negative"))
	}
	q := catalogindex.IntegrationQuery{
		Category: req.Msg.GetCategory(), PageSize: int(req.Msg.GetPageSize()), PageToken: req.Msg.GetPageToken(),
	}
	for _, k := range req.Msg.GetKinds() {
		kind, ok := catalogKindFromProto(k)
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("kinds: %s is not a catalog entry kind", k))
		}
		q.Kinds = append(q.Kinds, kind)
	}
	res, err := s.search.ListIntegrations(ctx, userID, q)
	if err != nil {
		return nil, catalogSearchError(err)
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

func catalogSearchError(err error) error {
	switch {
	case errors.Is(err, catalogindex.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, catalogindex.ErrInvalidPageToken), errors.Is(err, catalogindex.ErrQueryTooLong):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeInternal, errors.New("catalog search failed"))
}

func catalogKindFromProto(k reliantv1.CatalogEntryKind) (catalogindex.Kind, bool) {
	switch k {
	case reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_ACTION:
		return catalogindex.KindAction, true
	case reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TRIGGER:
		return catalogindex.KindTrigger, true
	}
	return 0, false
}

func catalogKindToProto(k catalogindex.Kind) reliantv1.CatalogEntryKind {
	switch k {
	case catalogindex.KindAction:
		return reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_ACTION
	case catalogindex.KindTrigger:
		return reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TRIGGER
	}
	return reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_UNSPECIFIED
}

// CatalogEntrySummaryToProto is the wire shape of one search result.
func CatalogEntrySummaryToProto(e *catalogindex.Entry, connected bool) *reliantv1.CatalogEntrySummary {
	m := e.Manifest
	authKinds := make([]reliantv1.ConnectionAuthKind, 0, len(e.AuthKinds))
	for _, k := range e.AuthKinds {
		authKinds = append(authKinds, connectionAuthKindToProto(k))
	}
	return &reliantv1.CatalogEntrySummary{
		Ref: e.Ref, Kind: catalogKindToProto(e.Kind), Id: e.ID,
		DisplayName: e.DisplayName, Summary: e.Summary,
		Integration:        catalogIntegrationToProto(m),
		AuthKinds:          authKinds,
		ConnectionRequired: e.ConnectionRequired,
		Connected:          connected,
		Mutates:            e.Action.GetMutates(),
	}
}

func catalogIntegrationToProto(m *reliantv1.IntegrationManifest) *reliantv1.CatalogIntegration {
	return &reliantv1.CatalogIntegration{
		Id: m.GetId(), Version: m.GetVersion(), DisplayName: m.GetDisplayName(),
		Icon: m.GetIcon(), Category: m.GetCategory(),
	}
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
		params, output := e.Schemas()
		var err error
		if out.ParamsSchema, err = structpb.NewStruct(params); err != nil {
			return nil, fmt.Errorf("params schema: %w", err)
		}
		if out.OutputSchema, err = structpb.NewStruct(output); err != nil {
			return nil, fmt.Errorf("output schema: %w", err)
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
				Kind: connectionAuthKindToProto(m.Kind), Available: m.Available,
				UnavailableReason: m.Reason, FieldLabels: m.FieldLabels,
			})
		}
	} else {
		for _, k := range e.AuthKinds {
			out.Methods = append(out.Methods, &reliantv1.IntegrationAuthMethod{Kind: connectionAuthKindToProto(k), Available: true})
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
