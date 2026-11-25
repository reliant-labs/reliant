package lint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
	"github.com/reliant-labs/forge/internal/linter/suppress"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// The conventions lane runs forgeconv-one-rpc-per-file over the handler
// tree, as a warning, and honours a file-wide forge:lint-disable for it.
func TestConventionsLaneRunsOneRPCPerFile(t *testing.T) {
	for _, tc := range []struct {
		name      string
		directive string
		want      int
	}{
		{name: "two rpcs in one file warn", want: 1},
		{name: "a file directive keeps the file whole", directive: "// forge:lint-disable-file forgeconv-one-rpc-per-file: one state machine\n\n", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{
				"proto/services/order/v1/order.proto": "syntax = \"proto3\";\n\npackage services.order.v1;\n\n" +
					"service OrderService {\n  rpc ShipOrder(ShipOrderRequest) returns (ShipOrderResponse);\n" +
					"  rpc CancelOrder(CancelOrderRequest) returns (CancelOrderResponse);\n}\n",
				"internal/handlers/order/service.go": "package order\n\ntype Service struct{}\n",
				"internal/handlers/order/order.go": tc.directive + "package order\n\n" +
					"func (s *Service) ShipOrder() error { return nil }\n\n" +
					"func (s *Service) CancelOrder() error { return nil }\n",
			}
			for name, body := range files {
				p := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(root)
			factory.SetProjectStoreLoader(func() (*projectstore.Store, error) {
				return nil, ErrProjectConfigNotFound
			})

			// The handler-tree half of collectGoTreeConventionFindings and
			// its suppression pass; the contract half execs go.
			findings, _, err := collectHandlerTreeFindings()
			if err != nil {
				t.Fatal(err)
			}
			applied := suppress.ApplyFiles("", findings, os.ReadFile)
			var got []forgeconv.Finding
			for _, f := range append(applied.Kept, applied.Violations...) {
				if f.Rule == "forgeconv-one-rpc-per-file" {
					got = append(got, f)
				}
			}
			if len(got) != tc.want {
				t.Fatalf("got %d forgeconv-one-rpc-per-file findings, want %d: %+v", len(got), tc.want, findings)
			}
			for _, f := range got {
				if f.Severity != forgeconv.SeverityWarning || f.File != "internal/handlers/order/order.go" {
					t.Errorf("finding = %+v, want a warning on internal/handlers/order/order.go", f)
				}
			}
		})
	}
}
