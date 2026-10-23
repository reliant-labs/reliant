// Tests for `forge api curl`. The interesting logic is the pure
// buildCurlCommand function (descriptor + forge.yaml -> curl string),
// the resolver (target -> service + method), and the JSON-skeleton
// builder. We test each in isolation table-driven, and run one
// end-to-end happy-path through buildCurlCommand against a tempdir to
// pin the integration shape.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// writeProjectWithDescriptor lays down a minimal forge project on disk: a
// minimal forge.yaml plus the gen/forge_descriptor.json built from desc.
// Returns the project dir.
//
// Both pieces are required for buildCurlCommand to succeed end-to-end, and
// constructing them inline in every test would dominate the file.
func writeProjectWithDescriptor(t *testing.T, desc ForgeDescriptor) string {
	t.Helper()
	dir := t.TempDir()

	yaml := strings.Builder{}
	yaml.WriteString("name: test\n")
	yaml.WriteString("module_path: github.com/example/test\n")
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(yaml.String()), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}

	// forge derives the project kind + component inventory from real sources
	// (the gen/ descriptor below provides the services), not an authored
	// manifest. Stamp the pkg/app composition root so the project derives to
	// service kind rather than library.
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "app"), 0o755); err != nil {
		t.Fatalf("mark service project (mkdir pkg/app): %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatalf("mkdir gen: %v", err)
	}
	data, err := json.MarshalIndent(desc, "", "  ")
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor.json"), data, 0o644); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}
	return dir
}

// writeForgeDescriptor lays down a minimal gen/forge_descriptor.json whose
// services carry the given proto service NAMES (e.g. "TasksService"). The
// introspection consumers (audit / graph / api / doctor) source their service
// inventory from this descriptor via codegen.IntrospectComponents, so a test
// that needs a named server present must write one here.
// naming.ServicePackage maps "TasksService" → component "tasks".
func writeForgeDescriptor(t *testing.T, dir string, protoServiceNames ...string) {
	t.Helper()
	svcs := make([]codegen.ServiceDef, 0, len(protoServiceNames))
	for _, n := range protoServiceNames {
		svcs = append(svcs, codegen.ServiceDef{Name: n, Package: "test.v1"})
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatalf("mkdir gen: %v", err)
	}
	data, err := json.MarshalIndent(ForgeDescriptor{Services: svcs}, "", "  ")
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor.json"), data, 0o644); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}
}

// markServiceProject makes dir derive to the "service" kind by stamping a
// real service artifact — the pkg/app composition root. forge derives both
// kind and inventory from the project's real sources (proto descriptor,
// service registry, KCL tree, handler impls), so the only way to make a
// fixture read as a service is to give it one of those sources.
func markServiceProject(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "app"), 0o755); err != nil {
		t.Fatalf("mark service project (mkdir pkg/app): %v", err)
	}
}

// markDeployProject is markServiceProject plus the deploy tree. deploy derives
// from deploy/kcl existing, not from the project being a service, so a fixture
// that exercises a deploy-gated path needs both.
func markDeployProject(t *testing.T, dir string) {
	t.Helper()
	markServiceProject(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "kcl"), 0o755); err != nil {
		t.Fatalf("mark deploy project (mkdir deploy/kcl): %v", err)
	}
}

// TestZeroValueFor pins the proto-zero mapping per scalar kind. nil for
// non-scalar branches is deliberate — the body builder renders it as
// JSON null, which ProtoJSON accepts for any nullable field.
//
// `bytes` is "" rather than null, and the difference is the point. It used
// to fall through to the non-scalar arm because the switch named fourteen
// of the fifteen scalar kinds, so the skeleton for a bytes field was
// indistinguishable from the skeleton for a message field. ProtoJSON
// encodes bytes as base64 TEXT, so "" is its zero.
func TestZeroValueFor(t *testing.T) {
	cases := []struct {
		proto string
		want  any
	}{
		{"string", ""},
		{"bool", false},
		{"bytes", ""},
		{"int32", 0},
		{"int64", 0},
		{"uint32", 0},
		{"uint64", 0},
		{"sfixed64", 0},
		{"float", 0.0},
		{"double", 0.0},
		{"message", nil},
		{"enum", nil},
		{"map", nil},
		{"unknown-kind", nil},
	}
	for _, tc := range cases {
		got := zeroValueFor(tc.proto)
		if got != tc.want {
			t.Errorf("zeroValueFor(%q) = %v, want %v", tc.proto, got, tc.want)
		}
	}
}

// TestZeroValueFor_TotalOverTheScalarVocabulary derives its obligation from
// codegen.ProtoScalarKinds() — the key set of the one table forge writes
// the fifteen names down in — so a kind this projection does not name fails
// here rather than taking the non-scalar `null` answer.
//
// A scalar rendered as null is not a parse error: ProtoJSON accepts null
// for the field, so the skeleton stays valid and nothing downstream ever
// reports it. That is what hid `bytes` here.
func TestZeroValueFor_TotalOverTheScalarVocabulary(t *testing.T) {
	kinds := codegen.ProtoScalarKinds()
	if len(kinds) == 0 {
		t.Fatal("codegen.ProtoScalarKinds() is EMPTY — this obligation is derived " +
			"from it, so an empty set would loop zero times and pass while " +
			"checking nothing")
	}
	if len(kinds) < 15 {
		t.Fatalf("codegen.ProtoScalarKinds() has %d members, expected 15: %v", len(kinds), kinds)
	}
	for _, kind := range kinds {
		if got := zeroValueFor(kind); got == nil {
			t.Errorf("zeroValueFor(%q) = nil — a scalar is being rendered as JSON "+
				"null, the same skeleton a message field gets", kind)
		}
	}
}

// TestBuildZeroBody covers the three paths in the body builder:
// Empty-input method -> {}, missing-field-data method -> {}, and the
// happy path with field declaration order preserved.
func TestBuildZeroBody(t *testing.T) {
	svc := codegen.ServiceDef{
		Name: "UserService",
		Messages: map[string][]codegen.MessageFieldDef{
			"GetUserRequest": {
				{Name: "id", ProtoType: "string"},
				{Name: "include_deleted", ProtoType: "bool"},
				{Name: "page_size", ProtoType: "int32"},
			},
		},
	}
	cases := []struct {
		name   string
		method codegen.Method
		want   string
	}{
		{
			name:   "empty input renders empty body",
			method: codegen.Method{Name: "Ping", InputType: "google.protobuf.Empty"},
			want:   "{}",
		},
		{
			name:   "unknown input message renders empty body",
			method: codegen.Method{Name: "Unknown", InputType: "UnknownRequest"},
			want:   "{}",
		},
		{
			name:   "happy path preserves proto field order",
			method: codegen.Method{Name: "GetUser", InputType: "GetUserRequest"},
			want:   `{"id":"","include_deleted":false,"page_size":0}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildZeroBody(svc, tc.method)
			if got != tc.want {
				t.Errorf("buildZeroBody = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveServiceMethod_TableDriven exercises the resolver against a
// catalog that mixes uniquely- and ambiguously-named services. The
// short form must work when unambiguous and surface a disambiguation
// message when not.
func TestResolveServiceMethod_TableDriven(t *testing.T) {
	services := []codegen.ServiceDef{
		{
			Name:    "UserService",
			Package: "users.v1",
			Methods: []codegen.Method{
				{Name: "GetUser", InputType: "GetUserRequest"},
				{Name: "ListUsers", InputType: "ListUsersRequest"},
			},
		},
		{
			Name:    "AccountService",
			Package: "accounts.v1",
			Methods: []codegen.Method{
				{Name: "GetAccount", InputType: "GetAccountRequest"},
			},
		},
		{
			// Same Name as accounts.v1.AccountService — short form is
			// ambiguous and must be rejected.
			Name:    "AccountService",
			Package: "billing.v1",
			Methods: []codegen.Method{
				{Name: "Charge", InputType: "ChargeRequest"},
			},
		},
	}
	cases := []struct {
		name        string
		target      string
		wantPkg     string
		wantSvc     string
		wantMethod  string
		wantErrSubs string
	}{
		{
			name:       "fully qualified resolves uniquely",
			target:     "users.v1.UserService.GetUser",
			wantPkg:    "users.v1",
			wantSvc:    "UserService",
			wantMethod: "GetUser",
		},
		{
			name:       "short form resolves when unique",
			target:     "UserService.ListUsers",
			wantPkg:    "users.v1",
			wantSvc:    "UserService",
			wantMethod: "ListUsers",
		},
		{
			name:        "short form ambiguous surfaces disambiguation",
			target:      "AccountService.GetAccount",
			wantErrSubs: "ambiguous",
		},
		{
			name:        "unknown service",
			target:      "BogusService.DoThing",
			wantErrSubs: "no service",
		},
		{
			name:        "unknown method on known service",
			target:      "users.v1.UserService.BogusMethod",
			wantErrSubs: "method \"BogusMethod\" not found",
		},
		{
			name:        "missing method segment",
			target:      "UserService",
			wantErrSubs: "invalid target",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, method, err := resolveServiceMethod(services, tc.target)
			if tc.wantErrSubs != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (svc=%+v method=%+v)", tc.wantErrSubs, svc, method)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubs) {
					t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErrSubs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if svc.Package != tc.wantPkg || svc.Name != tc.wantSvc {
				t.Errorf("service = %s.%s, want %s.%s", svc.Package, svc.Name, tc.wantPkg, tc.wantSvc)
			}
			if method.Name != tc.wantMethod {
				t.Errorf("method = %s, want %s", method.Name, tc.wantMethod)
			}
		})
	}
}

// TestShellQuoteSingle pins the single-quote escape strategy: a string
// containing a single quote must come back as `'…'\”…'` so the shell
// re-assembles it correctly.
func TestShellQuoteSingle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`hello`, `'hello'`},
		{`{"id":""}`, `'{"id":""}'`},
		{`it's`, `'it'\''s'`},
		{`'`, `''\'''`},
	}
	for _, tc := range cases {
		got := shellQuoteSingle(tc.in)
		if got != tc.want {
			t.Errorf("shellQuoteSingle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBuildCurlCommand_HappyPath drives buildCurlCommand end-to-end
// against a tempdir-shaped project. Asserts the shell text contains the
// expected URL, port, content-type, and body.
func TestBuildCurlCommand_HappyPath(t *testing.T) {
	desc := ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name:    "UserService",
		Package: "users.v1",
		Methods: []codegen.Method{
			{Name: "GetUser", InputType: "GetUserRequest"},
		},
		Messages: map[string][]codegen.MessageFieldDef{
			"GetUserRequest": {
				{Name: "id", ProtoType: "string"},
			},
		},
	}}}
	dir := writeProjectWithDescriptor(t, desc)

	out, err := buildCurlCommand(dir, "users.v1.UserService.GetUser", curlOptions{})
	if err != nil {
		t.Fatalf("buildCurlCommand: %v", err)
	}
	// Port is the conventional default: a port is a DEPLOY fact that lives in
	// KCL, so there is nothing in the project to resolve one from. --port
	// overrides.
	wantSubs := []string{
		"curl -X POST",
		"-H 'Content-Type: application/json'",
		`-d '{"id":""}'`,
		"http://localhost:8080/users.v1.UserService/GetUser",
	}
	for _, s := range wantSubs {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\nfull output:\n%s", s, out)
		}
	}
}

// TestBuildCurlCommand_PortOverride pins the --port flag: a non-zero
// opts.port wins over forge.yaml's port.
func TestBuildCurlCommand_PortOverride(t *testing.T) {
	desc := ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name:    "UserService",
		Package: "users.v1",
		Methods: []codegen.Method{
			{Name: "Ping", InputType: "google.protobuf.Empty"},
		},
	}}}
	dir := writeProjectWithDescriptor(t, desc)

	out, err := buildCurlCommand(dir, "users.v1.UserService.Ping", curlOptions{port: 9090})
	if err != nil {
		t.Fatalf("buildCurlCommand: %v", err)
	}
	if !strings.Contains(out, ":9090/") {
		t.Errorf("--port override not applied; output:\n%s", out)
	}
	if strings.Contains(out, ":8080/") {
		t.Errorf("--port override was supposed to replace the 8080 default; output:\n%s", out)
	}
}

// TestBuildCurlCommand_Auth pins the three auth shapes. The descriptor
// already carries AuthRequired (it mirrors the proto's auth_required, which
// is fail-closed), so a printed command that omits the header on a protected
// RPC is one the user can only discover is wrong by running it and reading a
// 401 — which is exactly what this command exists to save them.
func TestBuildCurlCommand_Auth(t *testing.T) {
	desc := ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name:    "UserService",
		Package: "users.v1",
		Methods: []codegen.Method{
			{Name: "GetUser", InputType: "google.protobuf.Empty", AuthRequired: true},
			{Name: "Health", InputType: "google.protobuf.Empty", AuthRequired: false},
		},
	}}}
	dir := writeProjectWithDescriptor(t, desc)

	t.Run("protected rpc gets a token placeholder and a note", func(t *testing.T) {
		out, err := buildCurlCommand(dir, "users.v1.UserService.GetUser", curlOptions{})
		if err != nil {
			t.Fatalf("buildCurlCommand: %v", err)
		}
		if !strings.Contains(out, `-H "Authorization: Bearer $TOKEN"`) {
			t.Errorf("protected RPC is missing the Authorization header; output:\n%s", out)
		}
		if !strings.Contains(out, "auth_required") {
			t.Errorf("protected RPC should explain why the header is there; output:\n%s", out)
		}
	})

	t.Run("public rpc gets no auth header", func(t *testing.T) {
		out, err := buildCurlCommand(dir, "users.v1.UserService.Health", curlOptions{})
		if err != nil {
			t.Fatalf("buildCurlCommand: %v", err)
		}
		if strings.Contains(out, "Authorization") {
			t.Errorf("public RPC must not carry an Authorization header; output:\n%s", out)
		}
	})

	t.Run("default port warns that the dev loop is ephemeral", func(t *testing.T) {
		out, err := buildCurlCommand(dir, "users.v1.UserService.Health", curlOptions{})
		if err != nil {
			t.Fatalf("buildCurlCommand: %v", err)
		}
		if !strings.Contains(out, "ephemeral port") {
			t.Errorf("default port should flag the `forge env up` mismatch; output:\n%s", out)
		}
	})

	t.Run("--port silences the ephemeral-port note", func(t *testing.T) {
		out, err := buildCurlCommand(dir, "users.v1.UserService.Health", curlOptions{port: 51141})
		if err != nil {
			t.Fatalf("buildCurlCommand: %v", err)
		}
		if strings.Contains(out, "ephemeral port") {
			t.Errorf("an explicit --port needs no warning; output:\n%s", out)
		}
	})

	t.Run("--auth-token inlines the real value and drops the note", func(t *testing.T) {
		out, err := buildCurlCommand(dir, "users.v1.UserService.GetUser", curlOptions{authToken: "tok-123"})
		if err != nil {
			t.Fatalf("buildCurlCommand: %v", err)
		}
		if !strings.Contains(out, "-H 'Authorization: Bearer tok-123'") {
			t.Errorf("--auth-token not inlined; output:\n%s", out)
		}
		if strings.Contains(out, "$TOKEN") {
			t.Errorf("--auth-token should replace the placeholder, not sit beside it; output:\n%s", out)
		}
	})
}

// TestBuildCurlCommand_BodyOverride pins the --body flag: a non-empty
// opts.body wins over the zero-skeleton render.
func TestBuildCurlCommand_BodyOverride(t *testing.T) {
	desc := ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name:    "UserService",
		Package: "users.v1",
		Methods: []codegen.Method{
			{Name: "CreateUser", InputType: "CreateUserRequest"},
		},
		Messages: map[string][]codegen.MessageFieldDef{
			"CreateUserRequest": {
				{Name: "name", ProtoType: "string"},
				{Name: "age", ProtoType: "int32"},
			},
		},
	}}}
	dir := writeProjectWithDescriptor(t, desc)

	custom := `{"name":"alice","age":30}`
	out, err := buildCurlCommand(dir, "users.v1.UserService.CreateUser", curlOptions{body: custom})
	if err != nil {
		t.Fatalf("buildCurlCommand: %v", err)
	}
	if !strings.Contains(out, "-d '"+custom+"'") {
		t.Errorf("--body override not applied; output:\n%s", out)
	}
	// Default skeleton must not appear when overridden.
	if strings.Contains(out, `"age":0`) {
		t.Errorf("expected user-supplied body, not zero skeleton; output:\n%s", out)
	}
}

// TestBuildCurlCommand_StreamingContentType pins that a server- or
// client-streaming method emits Content-Type: application/connect+json
// and a streaming note instead of the default application/json.
func TestBuildCurlCommand_StreamingContentType(t *testing.T) {
	desc := ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name:    "EchoService",
		Package: "echo.v1",
		Methods: []codegen.Method{
			{Name: "Stream", InputType: "StreamRequest", ServerStreaming: true},
		},
		Messages: map[string][]codegen.MessageFieldDef{
			"StreamRequest": {{Name: "id", ProtoType: "string"}},
		},
	}}}
	dir := writeProjectWithDescriptor(t, desc)

	out, err := buildCurlCommand(dir, "echo.v1.EchoService.Stream", curlOptions{})
	if err != nil {
		t.Fatalf("buildCurlCommand: %v", err)
	}
	if !strings.Contains(out, "application/connect+json") {
		t.Errorf("expected streaming content-type; output:\n%s", out)
	}
	if !strings.Contains(out, "streaming") {
		t.Errorf("expected streaming note; output:\n%s", out)
	}
}

// TestBuildCurlCommand_DefaultPortFallback pins the no-flag case: with no
// --port the command addresses the conventional local listener. A port is a
// deploy fact that lives in KCL, so there is nothing in the project to
// resolve one from and the command must stay useful without one.
func TestBuildCurlCommand_DefaultPortFallback(t *testing.T) {
	desc := ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name:    "UserService",
		Package: "users.v1",
		Methods: []codegen.Method{
			{Name: "Ping", InputType: "google.protobuf.Empty"},
		},
	}}}
	dir := writeProjectWithDescriptor(t, desc)

	out, err := buildCurlCommand(dir, "users.v1.UserService.Ping", curlOptions{})
	if err != nil {
		t.Fatalf("buildCurlCommand: %v", err)
	}
	if !strings.Contains(out, ":8080/") {
		t.Errorf("expected fallback port 8080; output:\n%s", out)
	}
}

// TestBuildCurlCommand_NoDescriptor pins the user-facing error when the
// project has never been generated. The error must name the missing
// file and point at `forge generate`.
func TestBuildCurlCommand_NoDescriptor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"),
		[]byte("name: t\nmodule_path: github.com/example/t\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	_, err := buildCurlCommand(dir, "users.v1.UserService.GetUser", curlOptions{})
	if err == nil {
		t.Fatal("expected error for missing descriptor")
	}
	if !strings.Contains(err.Error(), "no services found") && !strings.Contains(err.Error(), "forge_descriptor.json") {
		t.Errorf("error %q should mention the missing descriptor / no services", err.Error())
	}
	if !strings.Contains(err.Error(), "forge generate") {
		t.Errorf("error %q should point at the fix (`forge generate`)", err.Error())
	}
}
