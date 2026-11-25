package codegen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// The tests in this file pin every place forge spells an identifier that a
// GENERATOR declares — protoc-gen-go's pb types and fields,
// protoc-gen-connect-go's handler/client/procedure names, protoc-gen-es's
// TS properties — for proto names those generators re-case. naming's
// TestGeneratedNames_MatchRealPlugins proves the helpers against the real
// generators; these prove the emitters USE them.
//
// The fixture is the shape that broke a 146-service scaffold: an entity
// whose name has a lowercase letter after a digit (protoc-gen-go:
// Oauth2token → Oauth2Token) and fields protoc-gen-go and forge's ORM spell
// differently (sha256sum → pb Sha256Sum / db Sha256sum; address_line_2 →
// pb AddressLine_2 / db AddressLine2).

func digitEntityService() (ServiceDef, []EntityDef) {
	entity := func(n string) MessageFieldDef {
		return MessageFieldDef{Name: n, ProtoType: "message", MessageType: "Oauth2token"}
	}
	svc := ServiceDef{
		Name:       "Oauth2Service",
		Package:    "services.oauth2.v1",
		GoPackage:  "example.com/test/gen/services/oauth2/v1",
		PkgName:    "oauth2v1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreateOauth2token", InputType: "CreateOauth2tokenRequest", OutputType: "CreateOauth2tokenResponse"},
			{Name: "GetOauth2token", InputType: "GetOauth2tokenRequest", OutputType: "GetOauth2tokenResponse"},
			{Name: "ListOauth2tokens", InputType: "ListOauth2tokensRequest", OutputType: "ListOauth2tokensResponse"},
			{Name: "UpdateOauth2token", InputType: "UpdateOauth2tokenRequest", OutputType: "UpdateOauth2tokenResponse"},
			{Name: "DeleteOauth2token", InputType: "DeleteOauth2tokenRequest", OutputType: "DeleteOauth2tokenResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"CreateOauth2tokenRequest":  {{Name: "name", ProtoType: "string"}, {Name: "sha256sum", ProtoType: "string"}, {Name: "address_line_2", ProtoType: "string"}},
			"CreateOauth2tokenResponse": {entity("oauth2token")},
			"GetOauth2tokenRequest":     {{Name: "id", ProtoType: "string"}},
			"GetOauth2tokenResponse":    {entity("oauth2token")},
			"ListOauth2tokensRequest": {
				{Name: "page_size", ProtoType: "int32"},
				{Name: "page_token", ProtoType: "string"},
				{Name: "sha256sum", ProtoType: "string", IsOptional: true},
			},
			"ListOauth2tokensResponse": {
				{Name: "oauth2tokens", ProtoType: "[]message", MessageType: "Oauth2token"},
				{Name: "next_page_token", ProtoType: "string"},
			},
			"UpdateOauth2tokenRequest": {
				entity("oauth2token"),
				{Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask"},
			},
			"UpdateOauth2tokenResponse": {entity("oauth2token")},
			"DeleteOauth2tokenRequest":  {{Name: "id", ProtoType: "string"}},
			"DeleteOauth2tokenResponse": {{Name: "id", ProtoType: "string"}},
			"Oauth2token": {
				{Name: "id", ProtoType: "string"},
				{Name: "name", ProtoType: "string"},
				{Name: "sha256sum", ProtoType: "string"},
				{Name: "address_line_2", ProtoType: "string"},
			},
		},
	}
	entities := []EntityDef{{
		Name: "Oauth2token", TableName: "oauth2tokens", PkField: "id", PkGoType: "string",
		Fields: WireEntityFields(svc, "Oauth2token"),
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true},
			{Name: "name", Type: "string", NotNull: true},
			{Name: "sha256sum", Type: "string", NotNull: true},
			{Name: "address_line_2", Type: "string", NotNull: true},
		},
		SearchColumns: []string{"name", "sha256sum", "address_line_2"},
	}}
	return svc, entities
}

func writeHandlerServiceGo(t *testing.T, projectDir, pkg string) string {
	t.Helper()
	dir := filepath.Join(projectDir, "internal", "handlers", pkg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package " + pkg + "\n\nimport \"github.com/reliant-labs/forge/pkg/orm\"\n\ntype Deps struct {\n\tDB orm.Context\n}\n\ntype Service struct {\n\tdeps Deps\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// assertSpellings fails for each want missing from content and each
// stale (the pre-fix spelling) still present. Whitespace is normalised
// (inside composite literals, and gofmt's key alignment) so gofmt's
// choices do not matter.
func assertSpellings(t *testing.T, file, content string, want, stale []string) {
	t.Helper()
	norm := regexp.MustCompile(`[ \t]+`).ReplaceAllString(content, " ")
	norm = regexp.MustCompile(`\{\s+`).ReplaceAllString(norm, "{")
	norm = regexp.MustCompile(`\s+\}`).ReplaceAllString(norm, "}")
	for _, w := range want {
		if !strings.Contains(norm, w) {
			t.Errorf("%s: missing %q", file, w)
		}
	}
	for _, s := range stale {
		if strings.Contains(norm, s) {
			t.Errorf("%s: still spells %q, which the generators do not declare", file, s)
		}
	}
	if t.Failed() {
		t.Logf("%s:\n%s", file, content)
	}
}

func TestGenerateCRUD_DigitNamesUseTheGeneratorsCasing(t *testing.T) {
	svc, entities := digitEntityService()
	projectDir := t.TempDir()
	dir := writeHandlerServiceGo(t, projectDir, "oauth2")

	crud := MatchCRUDMethods(svc, entities)
	if len(crud) != 5 {
		t.Fatalf("matched %d CRUD methods, want 5", len(crud))
	}
	if _, err := GenerateCRUDHandlers(svc, crud, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers: %v", err)
	}
	if _, err := GenerateCRUDTests(svc, crud, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDTests: %v", err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}

	assertSpellings(t, "handlers_crud_ops_gen.go", read("handlers_crud_ops_gen.go"),
		[]string{
			// pb message types and the RPC's Go method: protoc-gen-go's casing.
			"func oauth2tokenToProto(e *db.Oauth2token) (*pb.Oauth2Token, error)",
			"m := &pb.Oauth2Token{}",
			"func (s *Service) crudUpdateOauth2TokenOp() crud.UpdateOp[pb.UpdateOauth2TokenRequest, pb.UpdateOauth2TokenResponse, *db.Oauth2token]",
			// The update request's entity field — the reported `req.Base64item` bug.
			"if req.Oauth2Token == nil",
			"return oauth2tokenFromProto(req.Oauth2Token)",
			// Response fields are read off the response messages.
			"&pb.CreateOauth2TokenResponse{Oauth2Token: m}",
			"Oauth2Tokens: out,",
			// One conversion, two rules: pb field left, db field right.
			"m.Sha256Sum = e.Sha256sum",
			"m.AddressLine_2 = e.AddressLine2",
			"e.Sha256sum = m.Sha256Sum",
			"e.AddressLine2 = m.AddressLine_2",
			// Create assigns the request's pb fields onto the db row.
			"e.Sha256sum = req.Sha256Sum",
			// A list filter dereferences the pb request field.
			"req.Sha256Sum",
			// db side keeps forge's ORM names.
			"db.CreateOauth2token(ctx, s.deps.DB, entity)",
		},
		[]string{"req.Oauth2token", "pb.UpdateOauth2tokenRequest", "pb.Oauth2token{", "m.Sha256sum", "m.AddressLine2", "Oauth2tokens: out"},
	)
	assertSpellings(t, "handlers_crud.go", read("handlers_crud.go"),
		[]string{
			"func (s *Service) CreateOauth2Token(",
			"req *connect.Request[pb.CreateOauth2TokenRequest]",
			"s.crudCreateOauth2TokenOp()",
		},
		[]string{"func (s *Service) CreateOauth2token(", "pb.CreateOauth2tokenRequest"},
	)
	assertSpellings(t, "handlers_crud_test.go", read("handlers_crud_test.go"),
		[]string{
			"svc.CreateOauth2Token(ctx",
			"NewCreateOauth2TokenRequest(t, db, 0)",
			"first.Msg.GetOauth2Token().GetId()",
			"got.Msg.GetOauth2Token().GetId()",
			"listed.Msg.GetOauth2Tokens()",
			"&pb.UpdateOauth2TokenRequest{Oauth2Token: row}",
		},
		[]string{"GetOauth2token()", "svc.CreateOauth2token(", "GetOauth2tokens()"},
	)
}

// The service's handler, mock and test client embed and construct what
// protoc-gen-connect-go declares — spelled from the service's GoName — while
// the fully-qualified-name constant is spelled from the RAW proto name.
func TestConnectIdentifiers_DigitServiceName(t *testing.T) {
	svc := ServiceDef{
		Name:       "Alphav1connectService",
		Package:    "services.alphav1connect.v1",
		GoPackage:  "example.com/test/gen/services/alphav1connect/v1",
		PkgName:    "alphav1connectv1",
		ModulePath: "example.com/test",
		ProtoFile:  "proto/services/alphav1connect/v1/alphav1connect.proto",
		Methods: []Method{
			{Name: "GetOauth2tokenStats", InputType: "GetOauth2tokenStatsRequest", OutputType: "GetOauth2tokenStatsResponse"},
		},
	}

	service, err := templates.ServiceTemplates().Render("service.go.tmpl", mapServiceDefToTemplateData(svc))
	if err != nil {
		t.Fatal(err)
	}
	assertSpellings(t, "service.go", string(service),
		[]string{"alphav1connectv1connect.UnimplementedAlphav1ConnectServiceHandler", "alphav1connectv1connect.NewAlphav1ConnectServiceHandler(s, opts...)"},
		[]string{"Alphav1connectServiceHandler"},
	)

	// A custom RPC's stub declares the method the handler interface has.
	projectDir := t.TempDir()
	dir := writeHandlerServiceGo(t, projectDir, "alphav1connect")
	res, err := GenerateMissingHandlerStubs(svc, projectDir, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NewMethods) != 1 {
		t.Fatalf("stubbed %v, want the one RPC", res.NewMethods)
	}
	stub, err := os.ReadFile(filepath.Join(dir, RPCHandlerFileName("GetOauth2tokenStats")))
	if err != nil {
		t.Fatal(err)
	}
	assertSpellings(t, "rpc stub", string(stub),
		[]string{"func (s *Service) GetOauth2TokenStats(", "req *connect.Request[pb.GetOauth2TokenStatsRequest]", "connect.Response[pb.GetOauth2TokenStatsResponse]"},
		[]string{"GetOauth2tokenStats("},
	)
	// ...and the next generate sees it as implemented instead of stubbing
	// it again (a second declaration is a compile error).
	again, err := GenerateMissingHandlerStubs(svc, projectDir, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !again.AllUpToDate {
		t.Errorf("second pass re-stubbed %v: the scan compares Go method names against proto RPC names", again.NewMethods)
	}

	mock := renderMockForTest(t, svc)
	assertSpellings(t, "mock", mock,
		[]string{"alphav1connectv1connect.UnimplementedAlphav1ConnectServiceHandler", "NewAlphav1ConnectServiceHandler(m, opts...)", "func (m *Alphav1ConnectServiceMock) GetOauth2TokenStats(", "*connect.Request[pb.GetOauth2TokenStatsRequest]"},
		[]string{"Alphav1connectServiceHandler", "GetOauth2tokenStats("},
	)

	// Procedure constants are GoName+GoName.
	svc.Methods[0].AuthRequired = false
	open := BuildOpenProcedures([]ServiceDef{svc}, "example.com/test")
	if len(open.Open) != 1 || open.Open[0].Const != "alphav1connectv1connect.Alphav1ConnectServiceGetOauth2TokenStatsProcedure" {
		t.Errorf("open procedures = %+v, want alphav1connectv1connect.Alphav1ConnectServiceGetOauth2TokenStatsProcedure", open.Open)
	}
}

func renderMockForTest(t *testing.T, svc ServiceDef) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := GenerateMock(svc, "", dir, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "alphav1connect_mock_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// protoc-gen-es names a field by protoCamelCase and an RPC's client method
// by lowering ONLY its first character; the generated TS must reference
// those, not a forge camel-casing of the entity or method name.
func TestFrontendIdentifiers_UseProtobufEsLocalNames(t *testing.T) {
	svc, _ := digitEntityService()
	svc.ProtoFile = "proto/services/oauth2/v1/oauth2.proto"
	svc.Methods = append(svc.Methods, Method{Name: "LLMChat", InputType: "LLMChatRequest", OutputType: "LLMChatResponse"})

	hooks := ServiceDefToHookData(svc)
	clientMethod := map[string]string{}
	for _, m := range hooks.Methods {
		clientMethod[m.Name] = m.ClientMethod
	}
	for rpc, want := range map[string]string{"LLMChat": "lLMChat", "GetOauth2token": "getOauth2token", "ListOauth2tokens": "listOauth2tokens"} {
		if got := clientMethod[rpc]; got != want {
			t.Errorf("client method for %s = %q, connect-es generates %q", rpc, got, want)
		}
	}

	// The detail and edit pages read the record off the Get response. For
	// entity Base64Item the scaffolder writes `Base64Item base64item = 1;`,
	// which protoc-gen-es names base64item — the pages used to spell
	// `{{.EntityName | camelCase}}`, i.e. data?.base64Item, which tsc
	// rejects.
	b64 := ServiceDef{
		Name:      "Base64Service",
		Package:   "services.base64.v1",
		ProtoFile: "proto/services/base64/v1/base64.proto",
		Methods: []Method{
			{Name: "GetBase64Item", InputType: "GetBase64ItemRequest", OutputType: "GetBase64ItemResponse"},
			{Name: "ListBase64Items", InputType: "ListBase64ItemsRequest", OutputType: "ListBase64ItemsResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"GetBase64ItemResponse":   {{Name: "base64item", ProtoType: "message", MessageType: "Base64Item"}},
			"ListBase64ItemsResponse": {{Name: "base64items", ProtoType: "[]message", MessageType: "Base64Item"}},
		},
	}
	pages := ExtractCRUDEntities(b64)
	if len(pages) != 1 {
		t.Fatalf("extracted %d CRUD pages, want 1", len(pages))
	}
	if p := pages[0]; p.GetEntityFieldCamel != "base64item" || p.ItemsField != "base64items" {
		t.Errorf("GetEntityFieldCamel, ItemsField = %q, %q; protoc-gen-es generates base64item, base64items", p.GetEntityFieldCamel, p.ItemsField)
	}
	for in, want := range map[string]string{"Base64Item": "base64item", "LLMKey": "llmKey", "Order": "order"} {
		if got := entityFieldCamel(in); got != want {
			t.Errorf("entityFieldCamel(%q) = %q, protoc-gen-es names the scaffolded field %q", in, got, want)
		}
	}
	// The descriptor-less fallbacks follow the same rule.
	if got := listItemsField(ServiceDef{}, "", "Base64Item"); got != "base64items" {
		t.Errorf("listItemsField fallback = %q, want base64items", got)
	}
	if got := responseEntityField(ServiceDef{}, "", "Base64Item"); got != "base64item" {
		t.Errorf("responseEntityField fallback = %q, want base64item", got)
	}
}
