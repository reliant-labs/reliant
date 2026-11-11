package forgeconv

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// ownerFile renders a handlers_crud.go whose methods resolve their owner
// with the given statements, keyed by method name, in order.
func ownerFile(methods [][2]string) string {
	var b strings.Builder
	b.WriteString(`package channels

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"

	"example.com/app/internal/tenancy"
	"example.com/app/pkg/middleware"
)
`)
	for _, m := range methods {
		name, resolve := m[0], m[1]
		b.WriteString("\nfunc (s *Service) " + name + "(\n\tctx context.Context,\n\treq *connect.Request[pb." + name + "Request],\n) (*connect.Response[pb." + name + "Response], error) {\n")
		b.WriteString(resolve)
		if strings.HasPrefix(name, "Create") {
			b.WriteString("\tbuild := func(e *db.Row) { e.TenantID = owner }\n\t_ = build\n")
		} else {
			b.WriteString("\t_ = orm.WhereEq(\"tenant_id\", owner)\n")
		}
		b.WriteString("\treturn nil, nil\n}\n")
	}
	return b.String()
}

const (
	viaTenant       = "\towner, err := tenancy.TenantID(ctx)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n"
	viaAdmin        = "\towner, err := tenancy.RequireAdmin(ctx)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n"
	viaUser         = "\towner, err := tenancy.UserID(ctx)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n"
	viaPlaceholder  = "\tclaims, err := middleware.GetUser(ctx)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\t// FORGE_SCAFFOLD: placeholder\n\towner := claims.UserID\n"
	viaRequestField = "\towner := req.Msg.TenantId\n"
)

func lintOwnerFile(t *testing.T, src string) []Finding {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "internal/handlers/channels/handlers_crud.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return lintCRUDOwnerResolverFile("internal/handlers/channels/handlers_crud.go", fset, f, nil)
}

// TestCRUDOwnerResolver_GrowthAppendedPlaceholders is the incident: two
// appended Lists on the placeholder in a file resolved by tenancy.TenantID.
func TestCRUDOwnerResolver_GrowthAppendedPlaceholders(t *testing.T) {
	fs := lintOwnerFile(t, ownerFile([][2]string{
		{"CreateChannel", viaTenant},
		{"GetChannel", viaTenant},
		{"ListChannels", viaTenant},
		{"GetPublication", viaTenant},
		{"ListPublications", viaPlaceholder},
		{"ListTrafficSnapshots", viaPlaceholder},
	}))
	if len(fs) != 2 {
		t.Fatalf("want a finding on each appended List, got %d: %+v", len(fs), fs)
	}
	for i, method := range []string{"ListPublications", "ListTrafficSnapshots"} {
		f := fs[i]
		if f.Rule != crudOwnerResolverRule || f.Severity != SeverityError {
			t.Errorf("finding %d: rule/severity %s/%s", i, f.Rule, f.Severity)
		}
		for _, want := range []string{method, "middleware.GetUser(ctx).UserID", "tenancy.TenantID(ctx)", "placeholder"} {
			if !strings.Contains(f.Message, want) {
				t.Errorf("finding %d must mention %q: %s", i, want, f.Message)
			}
		}
	}
}

// TestCRUDOwnerResolver_StricterWritesAreNotFindings: writes behind an
// admin check beside reads by tenant are a policy, not a mismatch.
func TestCRUDOwnerResolver_StricterWritesAreNotFindings(t *testing.T) {
	if fs := lintOwnerFile(t, ownerFile([][2]string{
		{"GetMember", viaTenant},
		{"ListMembers", viaTenant},
		{"CreateMember", viaAdmin},
		{"UpdateMember", viaAdmin},
		{"DeleteMember", viaAdmin},
	})); len(fs) != 0 {
		t.Fatalf("read/write resolvers that agree within their class are fine, got %+v", fs)
	}
}

// TestCRUDOwnerResolver_ReadOnAnotherPrincipal: a read on a user id among
// reads by tenant is the same bug without the placeholder.
func TestCRUDOwnerResolver_ReadOnAnotherPrincipal(t *testing.T) {
	fs := lintOwnerFile(t, ownerFile([][2]string{
		{"GetChannel", viaTenant},
		{"ListChannels", viaTenant},
		{"GetPublication", viaUser},
		{"CreateChannel", viaTenant},
	}))
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "GetPublication scopes tenant_id by tenancy.UserID(ctx)") ||
		!strings.Contains(fs[0].Message, "other read wrappers") {
		t.Fatalf("want one finding on GetPublication, got %+v", fs)
	}
}

// TestCRUDOwnerResolver_QuietCases: a newborn file (every wrapper on the
// placeholder), untraceable owners, and siblings that already disagree are
// not judged.
func TestCRUDOwnerResolver_QuietCases(t *testing.T) {
	cases := map[string][][2]string{
		"newborn": {
			{"CreateChannel", viaPlaceholder}, {"GetChannel", viaPlaceholder}, {"ListChannels", viaPlaceholder},
		},
		"untraceable": {
			{"GetChannel", viaTenant}, {"ListChannels", viaRequestField},
		},
		"siblings disagree": {
			{"GetChannel", viaTenant}, {"ListChannels", viaUser}, {"GetPublication", viaAdmin},
		},
	}
	for name, methods := range cases {
		t.Run(name, func(t *testing.T) {
			if fs := lintOwnerFile(t, ownerFile(methods)); len(fs) != 0 {
				t.Fatalf("want no findings, got %+v", fs)
			}
		})
	}
}

// TestCRUDOwnerResolver_PlaceholderWriteBesideResolvedReads: with no other
// write to compare to, a placeholder write is measured against the reads.
func TestCRUDOwnerResolver_PlaceholderWriteBesideResolvedReads(t *testing.T) {
	fs := lintOwnerFile(t, ownerFile([][2]string{
		{"GetChannel", viaTenant},
		{"CreateChannel", viaPlaceholder},
	}))
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "CreateChannel") || !strings.Contains(fs[0].Message, "this file's other wrappers") {
		t.Fatalf("want one finding on CreateChannel, got %+v", fs)
	}
}

// TestCRUDOwnerResolver_SameColumnNameOnDifferentTables: tenants.id is a
// tenant and users.id a user; growth's workspace file scopes each by its
// own resolver, and that is not a mismatch.
func TestCRUDOwnerResolver_SameColumnNameOnDifferentTables(t *testing.T) {
	src := strings.ReplaceAll(ownerFile([][2]string{
		{"GetTenant", viaTenant},
		{"ListTenants", viaTenant},
		{"UpdateTenant", viaAdmin},
		{"DeleteTenant", viaAdmin},
		{"GetUser", viaUser},
		{"UpdateUser", viaUser},
		{"DeleteUser", viaUser},
	}), `"tenant_id"`, `"id"`)
	if fs := lintOwnerFile(t, src); len(fs) != 0 {
		t.Fatalf("each table's id is its own principal, got %+v", fs)
	}
}

// userIDOwnerFile is ownerFile scoped on user_id instead of tenant_id.
func userIDOwnerFile(methods [][2]string) string {
	return strings.NewReplacer(`"tenant_id"`, `"user_id"`, "e.TenantID = owner", "e.UserID = owner").Replace(ownerFile(methods))
}

// profilesOps is the generated ops file of a package whose profiles table
// is keyed by user_id and whose orders table by id.
const profilesOps = `// Code generated by forge. DO NOT EDIT.
package channels

func (s *Service) crudGetProfileOp() crud.GetOp[pb.GetProfileRequest, pb.GetProfileResponse, *db.Profile] {
	return crud.GetOp[pb.GetProfileRequest, pb.GetProfileResponse, *db.Profile]{
		Fetch: func(ctx context.Context, id string, opts ...orm.QueryOption) (*db.Profile, error) {
			return crud.ScopedGet(ctx, "user_id", id, opts, nil, nil)
		},
	}
}

func (s *Service) crudListProfilesOp() crud.ListOp[pb.ListProfilesRequest, pb.ListProfilesResponse, *db.Profile] {
	return crud.ListOp[pb.ListProfilesRequest, pb.ListProfilesResponse, *db.Profile]{
		PkColumnName: "user_id",
	}
}

func (s *Service) crudGetOrderOp() crud.GetOp[pb.GetOrderRequest, pb.GetOrderResponse, *db.Order] {
	return crud.GetOp[pb.GetOrderRequest, pb.GetOrderResponse, *db.Order]{
		Fetch: func(ctx context.Context, id string, opts ...orm.QueryOption) (*db.Order, error) {
			return crud.ScopedGet(ctx, "id", id, opts, nil, nil)
		},
	}
}

func (s *Service) crudListOrdersOp() crud.ListOp[pb.ListOrdersRequest, pb.ListOrdersResponse, *db.Order] {
	return crud.ListOp[pb.ListOrdersRequest, pb.ListOrdersResponse, *db.Order]{
		PkColumnName: "id",
	}
}
`

// profilesOpKeys reads profilesOps the way LintHandlerErrorMapping reads a
// package's ops file.
func profilesOpKeys(t *testing.T) func() map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "handlers_crud_ops_gen.go", profilesOps, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse ops: %v", err)
	}
	keys := codegen.ScanCRUDOpKeyColumns(f)
	want := map[string]string{"GetProfile": "user_id", "ListProfiles": "user_id", "GetOrder": "id", "ListOrders": "id"}
	if len(keys) != len(want) {
		t.Fatalf("ScanCRUDOpKeyColumns = %v, want %v", keys, want)
	}
	for m, pk := range want {
		if keys[m] != pk {
			t.Fatalf("ScanCRUDOpKeyColumns = %v, want %v", keys, want)
		}
	}
	return func() map[string]string { return keys }
}

func lintOwnerFileWithKeys(t *testing.T, src string, keys func() map[string]string) []Finding {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "internal/handlers/channels/handlers_crud.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return lintCRUDOwnerResolverFile("internal/handlers/channels/handlers_crud.go", fset, f, keys)
}

// TestCRUDOwnerResolver_NonIDPrimaryKeyOwner: profiles is keyed by
// user_id, so profiles.user_id is the profile's own principal, not the
// orders.user_id the order wrappers scope. Neither table's wrappers are
// evidence for the other's, whatever the key column is called.
func TestCRUDOwnerResolver_NonIDPrimaryKeyOwner(t *testing.T) {
	cases := map[string][][2]string{
		// The key owner judged against the other table's wrappers.
		"key owner judged": {
			{"GetOrder", viaUser}, {"ListOrders", viaUser}, {"CreateOrder", viaUser},
			{"GetProfile", viaAdmin},
		},
		// The other table's key-owner wrappers taken as evidence.
		"key owner as evidence": {
			{"GetProfile", viaUser}, {"ListProfiles", viaUser},
			{"GetOrder", viaAdmin},
		},
	}
	for name, methods := range cases {
		t.Run(name, func(t *testing.T) {
			src := userIDOwnerFile(methods)
			// Without the ops file's key metadata, user_id reads as one
			// column across both tables: the false finding this guards.
			if fs := lintOwnerFile(t, src); len(fs) != 1 {
				t.Fatalf("want the cross-table finding without key metadata, got %+v", fs)
			}
			if fs := lintOwnerFileWithKeys(t, src, profilesOpKeys(t)); len(fs) != 0 {
				t.Fatalf("profiles.user_id is the key: no cross-table evidence, got %+v", fs)
			}
		})
	}
}
