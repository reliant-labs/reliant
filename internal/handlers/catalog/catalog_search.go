// Copyright (c) 2025 Reliant Labs
package catalog

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
)

// Integration catalog search lives on CatalogService rather than a new
// IntegrationService: CatalogService is already the "what can I use" surface
// the builder reads (ListNodes feeds its node picker, ListTools the tool
// picker), and integration actions and triggers are more things to pick. A
// second discovery service would split one picker across two clients.

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

func catalogKindFromProto(k reliantv1.CatalogEntryKind) (catalogindex.Kind, bool) {
	switch k {
	case reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_ACTION:
		return catalogindex.KindAction, true
	case reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TRIGGER:
		return catalogindex.KindTrigger, true
	case reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TYPE:
		return catalogindex.KindType, true
	}
	return 0, false
}

func catalogKindToProto(k catalogindex.Kind) reliantv1.CatalogEntryKind {
	switch k {
	case catalogindex.KindAction:
		return reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_ACTION
	case catalogindex.KindTrigger:
		return reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TRIGGER
	case catalogindex.KindType:
		return reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_TYPE
	}
	return reliantv1.CatalogEntryKind_CATALOG_ENTRY_KIND_UNSPECIFIED
}

// CatalogEntrySummaryToProto is the wire shape of one search result.
func CatalogEntrySummaryToProto(e *catalogindex.Entry, connected bool) *reliantv1.CatalogEntrySummary {
	m := e.Manifest
	authKinds := make([]reliantv1.ConnectionAuthKind, 0, len(e.AuthKinds))
	for _, k := range e.AuthKinds {
		authKinds = append(authKinds, connectionrpc.AuthKind(k))
	}
	return &reliantv1.CatalogEntrySummary{
		Ref: e.Ref, Kind: catalogKindToProto(e.Kind), Id: e.ID,
		DisplayName: e.DisplayName, Summary: e.Summary,
		Integration:        catalogIntegrationToProto(m),
		AuthKinds:          authKinds,
		ConnectionRequired: e.ConnectionRequired,
		Connected:          connected,
		Mutates:            e.Action.GetMutates(),
		Events:             e.Trigger.GetEvents(),
	}
}

func catalogIntegrationToProto(m *reliantv1.IntegrationManifest) *reliantv1.CatalogIntegration {
	return &reliantv1.CatalogIntegration{
		Id: m.GetId(), Version: m.GetVersion(), DisplayName: m.GetDisplayName(),
		Icon: m.GetIcon(), Category: m.GetCategory(),
	}
}
