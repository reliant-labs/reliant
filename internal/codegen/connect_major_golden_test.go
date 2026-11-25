package codegen

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// updateConnectGoldens rewrites testdata/connect_golden from the current
// renders: `go test ./internal/codegen/ -run TestConnectMajorGoldens -update-connect-golden`.
var updateConnectGoldens = flag.Bool("update-connect-golden", false, "rewrite internal/codegen/testdata/connect_golden/*.golden from the current renders")

// TestConnectMajorGoldens snapshots every project-level emitter whose output
// names connect-go, once per major. The .v1 files pin today's output byte for
// byte (they were captured before the v2 branches existed, so a v1 diff is a
// regression, never a re-bless); the .v2 files are the connect-go v2 wiring
// the serverkit/mountkit/testkit V2 APIs expect.
func TestConnectMajorGoldens(t *testing.T) {
	for _, c := range connectGoldenCases() {
		for _, major := range []string{"v1", "v2"} {
			t.Run(c.name+"."+major, func(t *testing.T) {
				got := c.render(t, major == "v2")
				path := filepath.Join("testdata", "connect_golden", c.name+"."+major+".golden")
				if *updateConnectGoldens {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, got, 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read %s (run with -update-connect-golden to create it): %v", path, err)
				}
				if string(got) != string(want) {
					t.Errorf("%s differs from %s; rerun with -update-connect-golden if the change is intended.\n--- got ---\n%s", c.name+"."+major, path, got)
				}
			})
		}
	}
}

type connectGoldenCase struct {
	name   string
	render func(t *testing.T, v2 bool) []byte
}

func renderProjectTemplate(t *testing.T, name string, data any) []byte {
	t.Helper()
	out, err := templates.ProjectTemplates().Render(name, data)
	if err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return out
}

// goldenInventoryRows are the inventory rows generate builds for the major:
// billing declares an RPC named like the mount helper, so its helper is the
// renamed one (mountMethodFor).
func goldenInventoryRows(v2 bool) []InventoryServiceData {
	orders := ServiceDef{Methods: []Method{{Name: "GetOrder"}}}
	billing := ServiceDef{Methods: []Method{{Name: defaultMountMethod(v2)}}}
	return []InventoryServiceData{
		{
			Name: "orders", FieldName: "Orders", MountMethod: mountMethodFor(orders, v2), Package: "orders",
			ConnectPkg: "ordersv1connect", ProtoServiceName: "OrdersService",
			BaseService: "orders", Version: "v1", Audience: "user",
		},
		{
			Name: "billing", FieldName: "Billing", MountMethod: mountMethodFor(billing, v2), Package: "billing",
			ConnectPkg: "billingv1connect", ProtoServiceName: "BillingService",
			BaseService: "billing", Version: "v1", HasWebhooks: true, Audience: "daemon",
		},
	}
}

var goldenConnectImports = []string{
	"example.com/shop/gen/services/billing/v1/billingv1connect",
	"example.com/shop/gen/services/orders/v1/ordersv1connect",
}

func goldenRoutesView(v2 bool) routesGenView {
	rows := goldenInventoryRows(v2)
	orders := routesServiceView{InventoryServiceData: rows[0], Type: "*orders.Service"}
	billing := routesServiceView{InventoryServiceData: rows[1], Type: "*billing.Service"}
	return routesGenView{
		Module:         "example.com/shop",
		ConnectImports: goldenConnectImports,
		Imports: []graphImport{
			{Alias: "billing", Path: "example.com/shop/internal/handlers/billing"},
			{Alias: "orders", Path: "example.com/shop/internal/handlers/orders"},
		},
		Services: []routesServiceView{orders, billing},
		Audiences: []routesAudienceView{
			{Name: "UserRoutes", Audience: "user", Default: true, Services: []routesServiceView{orders}},
			{Name: "DaemonRoutes", Audience: "daemon", Services: []routesServiceView{billing}},
		},
		ConnectV2: v2,
	}
}

func goldenProvidersData(v2, rich bool) providersGraphData {
	if !rich {
		return providersGraphData{
			Module: "example.com/shop", ConfigFields: map[string]bool{},
			NoBrokerConfig: true, NoProjectAuthInterceptor: true,
			ConnectV2: v2,
		}
	}
	return providersGraphData{
		Module: "example.com/shop", HasDatabase: true, RESTEnabled: true,
		ConfigFields: map[string]bool{
			"RateLimitRps": true, "RateLimitBurst": true, "LogSuccessSampleWindow": true,
			"CorsOrigins": true, "Environment": true, "CorsAllowHeaders": true,
			"CorsExposeHeaders": true, "SecurityHeadersEnabled": true,
		},
		BrokerConfigType: "Broker", ClaimsEnricher: true, DevSessionCookie: true,
		ConnectV2: v2,
	}
}

func goldenHelperData(v2 bool) componentTestHelperData {
	return componentTestHelperData{
		Module: "example.com/shop", Package: "orders", Name: "orders", FieldName: "Orders",
		IsService: true, ConstructorName: "New", Fallible: true, HasDB: true, HasLogger: true,
		HasMigrationsFS: true,
		ConfigImport:    "example.com/shop/pkg/config", ConfigType: "Config", ConfigDefaults: "Defaults",
		ConnectServiceGoName:   "OrdersService",
		ProtoConnectImportPath: "example.com/shop/gen/services/orders/v1/ordersv1connect",
		ProtoConnectPkg:        "ordersv1connect",
		MountMethod:            defaultMountMethod(v2),
		UnfilledDeps:           []UnresolvedAutoStub{{FieldName: "Billing", TypeExpr: "billing.Client"}},
		ConnectV2:              v2,
	}
}

func connectGoldenCases() []connectGoldenCase {
	return []connectGoldenCase{
		{"routes_gen", func(t *testing.T, v2 bool) []byte {
			out, err := renderGoTemplate("routes_gen.go.tmpl", goldenRoutesView(v2))
			if err != nil {
				t.Fatal(err)
			}
			return out
		}},
		{"routes_gen_empty", func(t *testing.T, v2 bool) []byte {
			out, err := renderGoTemplate("routes_gen.go.tmpl", routesGenView{
				Module:    "example.com/shop",
				Audiences: []routesAudienceView{{Name: "UserRoutes", Audience: "user", Default: true}},
				ConnectV2: v2,
			})
			if err != nil {
				t.Fatal(err)
			}
			return out
		}},
		{"mounts_services_gen", func(t *testing.T, v2 bool) []byte {
			return renderProjectTemplate(t, "mounts_services_gen.go.tmpl", InventoryGenData{
				Module: "example.com/shop", Services: goldenInventoryRows(v2), ConnectImports: goldenConnectImports, ConnectV2: v2,
			})
		}},
		{"mounts_services_gen_empty", func(t *testing.T, v2 bool) []byte {
			return renderProjectTemplate(t, "mounts_services_gen.go.tmpl", InventoryGenData{Module: "example.com/shop", ConnectV2: v2})
		}},
		{"providers_graph", func(t *testing.T, v2 bool) []byte {
			return renderProjectTemplate(t, "providers-graph.go.tmpl", goldenProvidersData(v2, true))
		}},
		{"providers_graph_minimal", func(t *testing.T, v2 bool) []byte {
			return renderProjectTemplate(t, "providers-graph.go.tmpl", goldenProvidersData(v2, false))
		}},
		{"cmd_svc_mount_gen", func(t *testing.T, v2 bool) []byte {
			return renderProjectTemplate(t, "cmd-svc-mount-gen.go.tmpl", CmdGroupItem{
				Module: "example.com/shop", Bin: "shop", Name: "orders", FieldName: "Orders", MountFieldName: "Orders", ConnectV2: v2,
			})
		}},
		{"cmd_svc_mount_gen_missing", func(t *testing.T, v2 bool) []byte {
			return renderProjectTemplate(t, "cmd-svc-mount-gen.go.tmpl", CmdGroupItem{
				Module: "example.com/shop", Bin: "shop", Name: "orders", FieldName: "Orders", MountFieldName: "Orders", MountMissing: true, ConnectV2: v2,
			})
		}},
		{"cmd_tree_serve", func(t *testing.T, v2 bool) []byte {
			return renderProjectTemplate(t, "cmd-tree-serve.go.tmpl", CmdServerTemplateData{
				Module: "example.com/shop", Name: "shop", ConfigFields: DefaultConfigFieldNames(), DevSessionCookie: true, ConnectV2: v2,
			})
		}},
		{"component_test_helpers", func(t *testing.T, v2 bool) []byte {
			return renderProjectTemplate(t, "component_test_helpers.go.tmpl", goldenHelperData(v2))
		}},
	}
}
