package codegen

import "testing"

func TestToCamelCaseFromPascal(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"GetUser", "getUser"},
		{"CreateUser", "createUser"},
		{"RPCMethod", "rpcMethod"},
		{"ID", "id"},
		{"A", "a"},
		{"", ""},
		{"alreadyCamel", "alreadyCamel"},
		{"ListUsers", "listUsers"},
	}
	for _, tt := range tests {
		got := toCamelCaseFromPascal(tt.input)
		if got != tt.want {
			t.Errorf("toCamelCaseFromPascal(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsQueryMethod(t *testing.T) {
	queries := []string{
		"GetUser", "ListUsers", "SearchProducts", "FindItem",
		"CheckHealth", "HasFeature", "IsReady", "CountOrders", "ExistsUser",
		// The verb standing alone is still a read, and so is one followed
		// by a digit — the boundary rule must not lose these.
		"List", "Get2FACodes",
	}
	mutations := []string{
		"CreateUser", "UpdateUser", "DeleteUser", "SetConfig",
		"AddItem", "RemoveItem", "SendNotification",
		// Writes whose names merely BEGIN WITH the characters of a read
		// verb. Every one of these was classified as a query while the
		// match was on characters instead of whole words, and a query hook
		// for a write replays it on every component remount.
		// IssuePrescription is the one that shipped.
		"IssuePrescription", "IssueRefund", // "Is"
		"CheckoutCart",         // "Check"
		"CountersignAgreement", // "Count"
		"ListenForEvents",      // "List"
		"HashPassword",         // "Has"
		"FinalizeInvoice",      // "Fin"…, near-miss on "Find"
	}

	for _, name := range queries {
		if !isQueryMethod(name) {
			t.Errorf("isQueryMethod(%q) = false, want true", name)
		}
	}
	for _, name := range mutations {
		if isQueryMethod(name) {
			t.Errorf("isQueryMethod(%q) = true, want false", name)
		}
	}
}

func TestProtoFileToTSImportPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"proto/services/users/v1/users.proto", "services/users/v1/users_pb"},
		{"proto/services/echo/v1/echo.proto", "services/echo/v1/echo_pb"},
		{"services/billing/v1/billing.proto", "services/billing/v1/billing_pb"},
	}
	for _, tt := range tests {
		got := ProtoFileToTSImportPath(tt.input)
		if got != tt.want {
			t.Errorf("ProtoFileToTSImportPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestServiceDefToHookData(t *testing.T) {
	svc := ServiceDef{
		Name:      "UserService",
		ProtoFile: "proto/services/users/v1/users.proto",
		Methods: []Method{
			{Name: "GetUser", InputType: "GetUserRequest", OutputType: "GetUserResponse"},
			{Name: "CreateUser", InputType: "CreateUserRequest", OutputType: "CreateUserResponse"},
			{Name: "ListUsers", InputType: "ListUsersRequest", OutputType: "ListUsersResponse"},
			{Name: "StreamUpdates", InputType: "StreamRequest", OutputType: "StreamResponse", ServerStreaming: true},
		},
	}

	data := ServiceDefToHookData(svc)

	if data.ServiceName != "UserService" {
		t.Errorf("ServiceName = %q, want %q", data.ServiceName, "UserService")
	}
	if data.ServiceNameCamel != "userService" {
		t.Errorf("ServiceNameCamel = %q, want %q", data.ServiceNameCamel, "userService")
	}
	if data.ImportPath != "services/users/v1/users_pb" {
		t.Errorf("ImportPath = %q, want %q", data.ImportPath, "services/users/v1/users_pb")
	}

	// Should have 3 methods (streaming one skipped)
	if len(data.Methods) != 3 {
		t.Fatalf("got %d methods, want 3", len(data.Methods))
	}

	// GetUser should be a query
	if !data.Methods[0].IsQuery {
		t.Error("GetUser should be IsQuery=true")
	}
	if data.Methods[0].NameCamel != "getUser" {
		t.Errorf("GetUser.NameCamel = %q, want %q", data.Methods[0].NameCamel, "getUser")
	}

	// CreateUser should be a mutation
	if data.Methods[1].IsQuery {
		t.Error("CreateUser should be IsQuery=false")
	}

	// ListUsers should be a query
	if !data.Methods[2].IsQuery {
		t.Error("ListUsers should be IsQuery=true")
	}
}

// TestServiceDefToHookData_CrossProtoFileImports asserts that an RPC whose
// input/output messages live in a DIFFERENT proto file from the service
// produces a separate import group keyed on the message's declaring file.
// Regression test for the v3-migration failure where admin-web's user
// service returned a shared/v1/types.proto Page and the rendered hooks.ts
// imported Page from users_pb (where it did not exist).
func TestServiceDefToHookData_CrossProtoFileImports(t *testing.T) {
	svc := ServiceDef{
		Name:      "UserService",
		ProtoFile: "proto/services/users/v1/users.proto",
		Methods: []Method{
			// Same-file refs: ListUsersRequest/Response live in users.proto.
			{
				Name:            "ListUsers",
				InputType:       "ListUsersRequest",
				OutputType:      "ListUsersResponse",
				InputProtoFile:  "proto/services/users/v1/users.proto",
				OutputProtoFile: "proto/services/users/v1/users.proto",
			},
			// Cross-file output ref: returns a shared/v1/types.proto Page.
			{
				Name:            "GetUserPage",
				InputType:       "GetUserPageRequest",
				OutputType:      "Page",
				InputProtoFile:  "proto/services/users/v1/users.proto",
				OutputProtoFile: "proto/shared/v1/types.proto",
			},
		},
	}

	data := ServiceDefToHookData(svc)

	// All input schemas come from users.proto — one schema group.
	if got, want := len(data.SchemaImports), 1; got != want {
		t.Fatalf("len(SchemaImports) = %d, want %d", got, want)
	}
	if data.SchemaImports[0].ImportPath != "services/users/v1/users_pb" {
		t.Errorf("SchemaImports[0].ImportPath = %q, want users_pb", data.SchemaImports[0].ImportPath)
	}

	// Outputs come from TWO files — expect two type-import groups,
	// sorted alphabetically by import path so shared/* comes first.
	if got, want := len(data.TypeImports), 2; got != want {
		t.Fatalf("len(TypeImports) = %d, want %d", got, want)
	}
	if data.TypeImports[0].ImportPath != "services/users/v1/users_pb" {
		t.Errorf("TypeImports[0].ImportPath = %q, want users_pb", data.TypeImports[0].ImportPath)
	}
	if data.TypeImports[1].ImportPath != "shared/v1/types_pb" {
		t.Errorf("TypeImports[1].ImportPath = %q, want types_pb", data.TypeImports[1].ImportPath)
	}
	if got, want := data.TypeImports[1].Symbols, []string{"Page"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("TypeImports[1].Symbols = %v, want %v", got, want)
	}
}

// TestServiceDefToHookData_FallsBackToServiceProtoFile asserts that when
// InputProtoFile/OutputProtoFile are unset (legacy descriptor.json
// produced by an older forge binary), the import group falls back to the
// service's own proto file. This keeps the existing flat-import behavior
// intact for projects regenerated against an older descriptor.
func TestServiceDefToHookData_FallsBackToServiceProtoFile(t *testing.T) {
	svc := ServiceDef{
		Name:      "EchoService",
		ProtoFile: "proto/services/echo/v1/echo.proto",
		Methods: []Method{
			{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"},
		},
	}

	data := ServiceDefToHookData(svc)

	if len(data.SchemaImports) != 1 || data.SchemaImports[0].ImportPath != "services/echo/v1/echo_pb" {
		t.Errorf("SchemaImports = %+v, want one entry at echo_pb", data.SchemaImports)
	}
	if len(data.TypeImports) != 1 || data.TypeImports[0].ImportPath != "services/echo/v1/echo_pb" {
		t.Errorf("TypeImports = %+v, want one entry at echo_pb", data.TypeImports)
	}
}
