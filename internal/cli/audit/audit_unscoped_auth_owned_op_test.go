package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
)

// These pin the table-level owner declaration (`COMMENT ON TABLE t IS
// 'forge:owner=…'`): it arms the gate exactly as a `forge:owner` column does,
// and a handler delegating to the generated op — which resolves the caller
// through s.deps.Owners — answers it.

// writeTableOwnerMigration births customers with a table-level owner.
func writeTableOwnerMigration(t *testing.T, projectDir string, extra ...string) {
	t.Helper()
	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sql := "CREATE TABLE customers (\n  id TEXT PRIMARY KEY,\n  user_id TEXT NOT NULL\n);\n\n" +
		"COMMENT ON TABLE customers IS 'forge:owner=user_id';\n"
	if err := os.WriteFile(filepath.Join(migDir, "20240101000000_init.up.sql"), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, s := range extra {
		name := filepath.Join(migDir, "2024010100000"+string(rune('1'+i))+"_more.up.sql")
		if err := os.WriteFile(name, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writeOps writes the generated ops file beside the handlers; scoped decides
// whether the op goes through s.deps.Owners (an owned table's op) or not (an
// op generated before the declaration).
func writeOps(t *testing.T, projectDir string, scoped bool, methods ...string) {
	t.Helper()
	src := "package shop\n\n"
	for _, m := range methods {
		body := "\treturn crud.GetOp[pb." + m + "Request, pb." + m + "Response, *db.Customer]{}\n"
		if scoped {
			body = "\treturn crud.GetOp[pb." + m + "Request, pb." + m + "Response, *db.Customer]{\n" +
				"\t\tFetch: func(ctx context.Context, id string) (*db.Customer, error) {\n" +
				"\t\t\treturn crud.OwnedGet(ctx, s.deps.Owners, customerOwnership, \"id\", id, nil, s.deps.DB.GetCustomer, s.deps.DB.ListCustomers)\n" +
				"\t\t},\n\t}\n"
		}
		src += "func (s *Service) crud" + m + "Op() crud.GetOp[pb." + m + "Request, pb." + m + "Response, *db.Customer] {\n" + body + "}\n\n"
	}
	if err := os.WriteFile(filepath.Join(projectDir, "internal", "handlers", "shop", "handlers_crud_ops_gen.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gatingRPCs(t *testing.T, cat audittype.Category) []unscopedRPC {
	t.Helper()
	g, _ := cat.Details["owner_scoped_unscoped_rpcs"].([]unscopedRPC)
	return g
}

func TestUnscopedAuth_DelegatingToAnOwnedOpReadsTheCaller(t *testing.T) {
	src := handlerHeader + delegationBody("GetCustomer")
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeTableOwnerMigration(t, dir)
	writeOps(t, dir, true, "GetCustomer")

	cat := auditUnscopedAuth(nil, dir)

	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok — GetCustomer delegates to an op that resolves the caller through s.deps.Owners\nsummary: %s", cat.Status, cat.Summary)
	}
	if got, _ := cat.Details["owned_op_rpcs"].([]string); len(got) != 1 || got[0] != "ShopService.GetCustomer" {
		t.Errorf("owned_op_rpcs = %v, want [ShopService.GetCustomer]", cat.Details["owned_op_rpcs"])
	}
}

// The op reached through a package helper counts too: the same fixed point
// the seam check uses.
func TestUnscopedAuth_OwnedOpReachedThroughAHelper(t *testing.T) {
	src := handlerHeader +
		"func (s *Service) GetCustomer(ctx context.Context, req *connect.Request[pb.GetCustomerRequest]) (*connect.Response[pb.GetCustomerResponse], error) {\n" +
		"\treturn s.get(ctx, req)\n}\n\n" +
		"func (s *Service) get(ctx context.Context, req *connect.Request[pb.GetCustomerRequest]) (*connect.Response[pb.GetCustomerResponse], error) {\n" +
		"\treturn crud.HandleGet(s.crudGetCustomerOp())(ctx, req)\n}\n"
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeTableOwnerMigration(t, dir)
	writeOps(t, dir, true, "GetCustomer")

	if cat := auditUnscopedAuth(nil, dir); cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok\nsummary: %s", cat.Status, cat.Summary)
	}
}

func TestUnscopedAuth_TableOwnerArmsTheGate(t *testing.T) {
	for name, tc := range map[string]struct {
		handler string
		ops     func(t *testing.T, dir string)
	}{
		// The declaration postdates the last generate: the op scopes nothing.
		"op that does not resolve the caller": {
			handler: delegationBody("GetCustomer"),
			ops:     func(t *testing.T, dir string) { writeOps(t, dir, false, "GetCustomer") },
		},
		"handler that neither delegates nor reads the caller": {
			handler: "func (s *Service) GetCustomer(ctx context.Context, req *connect.Request[pb.GetCustomerRequest]) (*connect.Response[pb.GetCustomerResponse], error) {\n\treturn nil, nil\n}\n",
			ops:     func(t *testing.T, dir string) { writeOps(t, dir, true, "GetCustomer") },
		},
		// Reaching ANOTHER rpc's scoped op is not this rpc's scope.
		"handler that delegates to another rpc's op": {
			handler: "func (s *Service) GetCustomer(ctx context.Context, req *connect.Request[pb.GetCustomerRequest]) (*connect.Response[pb.GetCustomerResponse], error) {\n\t_ = s.crudListCustomersOp()\n\treturn nil, nil\n}\n",
			ops:     func(t *testing.T, dir string) { writeOps(t, dir, true, "GetCustomer", "ListCustomers") },
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, handlerHeader+tc.handler)
			writeTableOwnerMigration(t, dir)
			tc.ops(t, dir)

			cat := auditUnscopedAuth(nil, dir)

			if cat.Status != audittype.StatusError {
				t.Fatalf("status = %q, want error — customers declares forge:owner= and GetCustomer is not scoped\nsummary: %s", cat.Status, cat.Summary)
			}
			g := gatingRPCs(t, cat)
			if len(g) != 1 || g[0].Table != "customers" || g[0].OwnerSpec != "forge:owner=user:user_id" {
				t.Fatalf("gating = %+v, want GetCustomer over customers armed by forge:owner=user:user_id", g)
			}
			if !strings.Contains(g[0].Remediation, "crud.HandleGet(s.crudGetCustomerOp())(ctx, req)") {
				t.Errorf("remediation is not the delegation to the owned op:\n%s", g[0].Remediation)
			}
			if hint, _ := cat.Details["owner_scoping_hint"].(string); !strings.Contains(hint, "s.deps.Owners") {
				t.Errorf("hint does not explain the owned op: %s", hint)
			}
		})
	}
}

// COMMENT ON replaces the whole comment: a later one without the marker
// withdraws the declaration, and the finding is advisory again.
func TestUnscopedAuth_WithdrawnTableOwnerDisarms(t *testing.T) {
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, handlerHeader+delegationBody("GetCustomer"))
	writeTableOwnerMigration(t, dir, "COMMENT ON TABLE customers IS 'customers of the shop';\n")
	writeOps(t, dir, false, "GetCustomer")

	if cat := auditUnscopedAuth(nil, dir); cat.Status != audittype.StatusWarn {
		t.Fatalf("status = %q, want warn\nsummary: %s", cat.Status, cat.Summary)
	}
}

// Hand-written SQL over a table-owned table is classified as over a
// column-owned one: the first owner column must appear.
func TestUnscopedAuth_TableOwnerArmsTheRawSQLCheck(t *testing.T) {
	src := handlerHeader + "func (s *Service) GetCustomer(ctx context.Context, req *connect.Request[pb.GetCustomerRequest]) (*connect.Response[pb.GetCustomerResponse], error) {\n" +
		"\tclaims, _ := " + "middleware.GetUser(ctx)\n\t_ = claims\n" +
		"\t_ = s.db.Raw(\"SELECT id FROM customers WHERE id = ?\")\n\treturn nil, nil\n}\n"
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeTableOwnerMigration(t, dir)

	cat := auditUnscopedAuth(nil, dir)
	got, _ := cat.Details["raw_sql_unscoped_rpcs"].([]rawSQLRPC)
	if cat.Status != audittype.StatusError || len(got) != 1 || got[0].OwnerColumns[0] != "user_id" {
		t.Fatalf("status = %q, raw_sql_unscoped_rpcs = %+v, want the query reported for not naming user_id\nsummary: %s", cat.Status, got, cat.Summary)
	}
}

func TestOwnerTableSpecsIn(t *testing.T) {
	specs := map[string]string{"stale": "forge:owner=user:x"}
	ownerTableSpecsIn(`
COMMENT ON TABLE public.plans IS 'forge:owner=thread_id>chat_id>user_id';
COMMENT ON TABLE accounts IS 'Accounts. forge:owner=user:user_id,org:org_id';
COMMENT ON TABLE catalog IS 'forge:public: listed on the pricing page';
COMMENT ON TABLE stale IS 'no longer owned';
COMMENT ON COLUMN notes.user_id IS 'forge:owner';
`, specs)
	want := map[string]string{
		"plans":    "forge:owner=user:thread_id>chat_id>user_id",
		"accounts": "forge:owner=user:user_id,org:org_id",
	}
	if len(specs) != len(want) {
		t.Fatalf("specs = %v, want %v", specs, want)
	}
	for k, v := range want {
		if specs[k] != v {
			t.Errorf("specs[%s] = %q, want %q", k, specs[k], v)
		}
	}
}
